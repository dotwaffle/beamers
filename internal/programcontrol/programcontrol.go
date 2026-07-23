// Package programcontrol owns volatile Program Channel control and durable Takes.
package programcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
)

var (
	// ErrOperatorRequired means the actor lacks live-control authority.
	ErrOperatorRequired = errors.New("operator authority required")
	// ErrControlOwned means another Crew Member owns the Program Channel.
	ErrControlOwned = errors.New("program Channel already has a Control Owner")
	// ErrControlOwnerRequired means the actor does not own the Program Channel.
	ErrControlOwnerRequired = errors.New("program Channel Control Owner required")
	// ErrHandoverUnavailable means the requested ownership transition is invalid.
	ErrHandoverUnavailable = errors.New("program Channel handover is unavailable")
	// ErrTakeoverConfirmation means an involuntary takeover was not confirmed.
	ErrTakeoverConfirmation = errors.New("program Channel takeover requires confirmation")
	// ErrPreviewItem means Preview selected no current Program Item.
	ErrPreviewItem = errors.New("invalid Program Item Preview")
	// ErrProgramRevision means Program Output changed after observation.
	ErrProgramRevision = store.ErrProgramRevision
	// ErrControlRevision means ownership or Preview changed after observation.
	ErrControlRevision = errors.New("program Channel control revision conflict")
	// ErrProgramItem means Preview is not in the current catalog.
	ErrProgramItem = store.ErrProgramItem
	// ErrEntryRevision means Defer observed stale Entry state.
	ErrEntryRevision = store.ErrCompetitionEntryRevision
	// ErrEntryDefer means the Entry is not the current canonical Next item.
	ErrEntryDefer = store.ErrCompetitionEntryDefer
	// ErrCommandConflict means a Command ID was reused with another payload.
	ErrCommandConflict = store.ErrCommandConflict
)

// ControlAction selects one explicit ownership transition.
type ControlAction string

const (
	// ControlClaim acquires an unowned Program Channel.
	ControlClaim ControlAction = "Claim"
	// ControlRequestHandover asks the current owner for control.
	ControlRequestHandover ControlAction = "RequestHandover"
	// ControlHandover transfers control to the pending requester.
	ControlHandover ControlAction = "Handover"
	// ControlTakeover replaces an owner after explicit confirmation.
	ControlTakeover ControlAction = "Takeover"
	// ControlDisconnect marks the owner offline without releasing control.
	ControlDisconnect ControlAction = "Disconnect"
)

// Owner is one process-local Program Channel controller.
type Owner struct {
	AccountID int    `json:"account_id"`
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
}

// State is the complete control projection for one Program Channel.
type State struct {
	Channel           store.ProgramChannelState
	ControlRevision   int
	Owner             *Owner
	HandoverRequester *Owner
	Preview           store.ProgramItem
}

// ControlInput changes volatile Program Channel ownership.
type ControlInput struct {
	EventID, SessionID int
	Action             ControlAction
	Confirmed          bool
	CommandID          string
	ExpectedRevision   int
}

// SelectPreviewInput changes only the current owner's process-local Preview.
type SelectPreviewInput struct {
	EventID, SessionID int
	Item               store.ProgramItem
	CommandID          string
	ExpectedRevision   int
}

// TakeInput durably commits one exact Preview as Program Output.
type TakeInput struct {
	EventID, SessionID         int
	CommandID                  string
	ExpectedRevision           int
	ExpectedControlRevision    int
	Item                       store.ProgramItem
	ExpectedEntryOrderRevision int
	EntryOrderFingerprint      string
}

// DeferEntryInput advances past one exact unpresented canonical Entry.
type DeferEntryInput struct {
	EventID, SessionID, EntryID int
	CommandID                   string
	ExpectedEntryRevision       int
	ExpectedProgramRevision     int
	ExpectedControlRevision     int
}

// TakeResult distinguishes a new durable commit from an exact receipt replay.
type TakeResult struct {
	State     State
	Committed bool
}

type controlState struct {
	revision   int
	owner      Owner
	hasOwner   bool
	requester  Owner
	hasRequest bool
	preview    store.ProgramItem
}

type channelControl struct {
	mu          sync.Mutex
	state       controlState
	connections map[int]int
}

type rejectionCode string

const (
	rejectionControlOwned         rejectionCode = "program_control_owned"
	rejectionOperatorRequired     rejectionCode = "program_operator_required"
	rejectionControlOwnerRequired rejectionCode = "program_control_owner_required"
	rejectionTakeoverConfirmation rejectionCode = "program_takeover_confirmation_required"
	rejectionHandoverUnavailable  rejectionCode = "program_handover_unavailable"
	rejectionPreviewItemInvalid   rejectionCode = "program_preview_item_invalid"
	rejectionProgramRevision      rejectionCode = "program_revision_conflict"
	rejectionControlRevision      rejectionCode = "program_control_revision_conflict"
	rejectionProgramItemInvalid   rejectionCode = "program_item_invalid"
	rejectionEntryOrderRevision   rejectionCode = "competition_entry_order_revision_conflict"
	rejectionEntryOrderStale      rejectionCode = "competition_entry_order_preview_stale"
	rejectionEntryRevision        rejectionCode = "competition_entry_revision_conflict"
	rejectionEntryDefer           rejectionCode = "competition_entry_defer_invalid"
)

type controlReceipt struct {
	Revision  int               `json:"revision"`
	Owner     *Owner            `json:"owner,omitempty"`
	Requester *Owner            `json:"requester,omitempty"`
	Preview   store.ProgramItem `json:"preview"`
}

type takeReceipt struct {
	Channel store.ProgramChannelState `json:"channel"`
	Control controlReceipt            `json:"control"`
}

// Service serializes process-local ownership around durable Program Output.
type Service struct {
	storage  *store.SQLite
	now      func() time.Time
	mu       sync.Mutex
	controls map[int]*channelControl
}

// New creates a Program control service. Its empty control map deliberately
// clears ownership and unsent Preview after every process restart.
func New(storage *store.SQLite, now func() time.Time) (*Service, error) {
	if storage == nil {
		return nil, errors.New("program control storage is required")
	}
	if now == nil {
		return nil, errors.New("program control clock is required")
	}
	return &Service{storage: storage, now: now, controls: make(map[int]*channelControl)}, nil
}

func (service *Service) controlFor(sessionID int) *channelControl {
	service.mu.Lock()
	defer service.mu.Unlock()
	if found := service.controls[sessionID]; found != nil {
		return found
	}
	created := &channelControl{connections: make(map[int]int)}
	service.controls[sessionID] = created
	return created
}

// OpenConnection tracks one live control View and reports presence transitions.
func (service *Service) OpenConnection(
	ctx context.Context,
	actor auth.Account,
	eventID, sessionID int,
) (State, bool, func() bool, error) {
	if !actor.CanOperateEvent(eventID) {
		return State{}, false, nil, ErrOperatorRequired
	}
	if _, err := service.storage.LoadProgramChannel(
		actor.Context(ctx), eventID, sessionID,
	); err != nil {
		return State{}, false, nil, err
	}
	owned := service.controlFor(sessionID)
	owned.mu.Lock()
	channel, err := service.storage.LoadProgramChannel(
		actor.Context(ctx), eventID, sessionID,
	)
	if err != nil {
		owned.mu.Unlock()
		return State{}, false, nil, err
	}
	owned.connections[actor.ID]++
	changed := false
	if owned.state.hasOwner &&
		owned.state.owner.AccountID == actor.ID &&
		!owned.state.owner.Connected {
		owned.state.owner.Connected = true
		owned.state.revision++
		changed = true
	}
	if owned.state.hasRequest &&
		owned.state.requester.AccountID == actor.ID &&
		!owned.state.requester.Connected {
		owned.state.requester.Connected = true
		if !changed {
			owned.state.revision++
		}
		changed = true
	}
	state := service.state(channel, owned.state)
	owned.mu.Unlock()

	var closeOnce sync.Once
	release := func() bool {
		disconnected := false
		closeOnce.Do(func() {
			owned.mu.Lock()
			defer owned.mu.Unlock()
			owned.connections[actor.ID]--
			if owned.connections[actor.ID] <= 0 {
				delete(owned.connections, actor.ID)
				if owned.state.hasOwner &&
					owned.state.owner.AccountID == actor.ID &&
					owned.state.owner.Connected {
					owned.state.owner.Connected = false
					owned.state.revision++
					disconnected = true
				}
				if owned.state.hasRequest &&
					owned.state.requester.AccountID == actor.ID &&
					owned.state.requester.Connected {
					owned.state.requester.Connected = false
					if !disconnected {
						owned.state.revision++
					}
					disconnected = true
				}
			}
		})
		return disconnected
	}
	return state, changed, release, nil
}

func (service *Service) controlIdentity(actor auth.Account, input ControlInput) store.CommandIdentity {
	return store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(
			strconv.Itoa(input.EventID), strconv.Itoa(input.SessionID),
			string(input.Action), strconv.FormatBool(input.Confirmed),
			strconv.Itoa(input.ExpectedRevision),
		),
		Action:     "ChangeProgramControl" + string(input.Action),
		TargetType: "ProgramChannel", TargetID: strconv.Itoa(input.SessionID),
		Now: service.now().UTC(),
	}
}

func (service *Service) previewIdentity(actor auth.Account, input SelectPreviewInput) store.CommandIdentity {
	payload := []string{
		strconv.Itoa(input.EventID), strconv.Itoa(input.SessionID),
		strconv.Itoa(input.ExpectedRevision), string(input.Item.Kind),
		strconv.Itoa(input.Item.EntryID),
	}
	if input.Item.Retry {
		payload = append(payload, "retry")
	}
	return store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(payload...),
		Action:      "SelectProgramPreview", TargetType: "ProgramChannel",
		TargetID: strconv.Itoa(input.SessionID), Now: service.now().UTC(),
	}
}

func (service *Service) takeIdentity(actor auth.Account, input TakeInput) store.CommandIdentity {
	payload := []string{
		strconv.Itoa(input.EventID), strconv.Itoa(input.SessionID),
		strconv.Itoa(input.ExpectedRevision), string(input.Item.Kind),
		strconv.Itoa(input.Item.EntryID),
	}
	if input.Item.Retry {
		payload = append(payload, "retry")
	}
	payload = append(
		payload,
		strconv.Itoa(input.ExpectedControlRevision),
		strconv.Itoa(input.ExpectedEntryOrderRevision),
		input.EntryOrderFingerprint,
	)
	return store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(payload...),
		Action:      "TakeProgramOutput", TargetType: "ProgramChannel",
		TargetID: strconv.Itoa(input.SessionID), Now: service.now().UTC(),
	}
}

func (service *Service) deferIdentity(actor auth.Account, input DeferEntryInput) store.CommandIdentity {
	return store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(
			strconv.Itoa(input.EventID), strconv.Itoa(input.SessionID),
			strconv.Itoa(input.EntryID), strconv.Itoa(input.ExpectedEntryRevision),
			strconv.Itoa(input.ExpectedProgramRevision),
			strconv.Itoa(input.ExpectedControlRevision),
		),
		Action: "DeferCompetitionEntry", TargetType: "CompetitionEntry",
		TargetID: strconv.Itoa(input.EntryID), Now: service.now().UTC(),
	}
}

func (service *Service) auditOperatorRejection(
	ctx context.Context,
	identity store.CommandIdentity,
) error {
	_, err := command.Execute(ctx, command.Plan[struct{}]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (struct{}, error) {
			var receipt struct{}
			err := store.DecodeCommandReceipt(outcome, &receipt)
			return receipt, err
		},
		Apply: func(_ *store.CommandTx) (command.Execution[struct{}], error) {
			return command.Reject(struct{}{}, store.CommandRejection{
				Code: string(rejectionOperatorRequired), Message: ErrOperatorRequired.Error(),
			}, ErrOperatorRequired), nil
		},
	})
	if err != nil {
		var rejected *store.RejectedCommandError
		if errors.As(err, &rejected) {
			return rejectionError(rejected.Rejection.Code, "program command rejected")
		}
	}
	return err
}

// Current returns durable output with process-local owner and Preview.
func (service *Service) Current(
	ctx context.Context,
	actor auth.Account,
	eventID, sessionID int,
) (State, error) {
	if !actor.CanOperateEvent(eventID) {
		return State{}, ErrOperatorRequired
	}
	if _, err := service.storage.LoadProgramChannel(actor.Context(ctx), eventID, sessionID); err != nil {
		return State{}, err
	}
	owned := service.controlFor(sessionID)
	owned.mu.Lock()
	defer owned.mu.Unlock()
	channel, err := service.storage.LoadProgramChannel(actor.Context(ctx), eventID, sessionID)
	if err != nil {
		return State{}, err
	}
	return service.state(channel, owned.state), nil
}

// Control applies one explicit process-local ownership transition.
func (service *Service) Control(
	ctx context.Context,
	actor auth.Account,
	input ControlInput,
) (State, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return State{}, err
	}
	identity := service.controlIdentity(actor, input)
	if !actor.CanOperateEvent(input.EventID) {
		return State{}, service.auditOperatorRejection(actor.Context(ctx), identity)
	}
	if _, err := service.storage.LoadProgramChannel(
		actor.Context(ctx), input.EventID, input.SessionID,
	); err != nil {
		return State{}, err
	}
	owned := service.controlFor(input.SessionID)
	owned.mu.Lock()
	defer owned.mu.Unlock()
	channel, err := service.storage.LoadProgramChannel(
		actor.Context(ctx), input.EventID, input.SessionID,
	)
	if err != nil {
		return State{}, err
	}
	current := owned.state
	applied := false
	next, err := command.Execute(actor.Context(ctx), command.Plan[controlState]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (controlState, error) {
			var receipt controlReceipt
			if decodeErr := store.DecodeCommandReceipt(outcome, &receipt); decodeErr != nil {
				return controlState{}, decodeErr
			}
			return receipt.control(), nil
		},
		Apply: func(_ *store.CommandTx) (command.Execution[controlState], error) {
			if current.revision != input.ExpectedRevision {
				return controlRejection(current, rejectionControlRevision, ErrControlRevision), nil
			}
			transitioned, transitionErr := transitionControl(current, actor, input, channel)
			if transitionErr != nil {
				rejection := store.CommandRejection{
					Code: controlErrorCode(transitionErr), Message: transitionErr.Error(),
				}
				return command.Reject(current, rejection, transitionErr), nil
			}
			transitioned.revision++
			encoded, encodeErr := json.Marshal(controlReceiptFrom(transitioned))
			if encodeErr != nil {
				return command.Execution[controlState]{}, errors.New("encode Program control outcome")
			}
			applied = true
			return command.Success(transitioned, string(encoded)), nil
		},
	})
	if err != nil {
		var rejected *store.RejectedCommandError
		if errors.As(err, &rejected) {
			err = controlError(rejected.Rejection.Code)
		}
		return service.state(channel, current), err
	}
	if applied {
		owned.state = next
	}
	return service.state(channel, next), nil
}

func transitionControl(
	control controlState,
	actor auth.Account,
	input ControlInput,
	channel store.ProgramChannelState,
) (controlState, error) {
	switch input.Action {
	case ControlClaim:
		if control.hasOwner && control.owner.AccountID != actor.ID {
			return control, ErrControlOwned
		}
		control.owner = owner(actor, true)
		control.hasOwner = true
		if control.preview.Kind == "" {
			control.preview = channel.Next
		}
	case ControlRequestHandover:
		if !control.hasOwner || control.owner.AccountID == actor.ID {
			return control, ErrHandoverUnavailable
		}
		control.requester = owner(actor, true)
		control.hasRequest = true
	case ControlHandover:
		if !control.hasOwner || control.owner.AccountID != actor.ID || !control.hasRequest {
			return control, ErrHandoverUnavailable
		}
		control.owner = control.requester
		control.hasRequest = false
	case ControlTakeover:
		if !input.Confirmed {
			return control, ErrTakeoverConfirmation
		}
		if control.hasOwner && control.owner.AccountID == actor.ID {
			control.owner.Connected = true
		} else {
			control.owner = owner(actor, true)
			control.hasOwner = true
		}
		control.hasRequest = false
		if control.preview.Kind == "" {
			control.preview = channel.Next
		}
	case ControlDisconnect:
		if !control.hasOwner || control.owner.AccountID != actor.ID {
			return control, ErrControlOwnerRequired
		}
		control.owner.Connected = false
	default:
		return control, ErrHandoverUnavailable
	}
	return control, nil
}

// SelectPreview changes no durable state.
func (service *Service) SelectPreview(
	ctx context.Context,
	actor auth.Account,
	input SelectPreviewInput,
) (State, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return State{}, err
	}
	identity := service.previewIdentity(actor, input)
	if !actor.CanOperateEvent(input.EventID) {
		return State{}, service.auditOperatorRejection(actor.Context(ctx), identity)
	}
	if _, err := service.storage.LoadProgramChannel(
		actor.Context(ctx), input.EventID, input.SessionID,
	); err != nil {
		return State{}, err
	}
	owned := service.controlFor(input.SessionID)
	owned.mu.Lock()
	defer owned.mu.Unlock()
	channel, err := service.storage.LoadProgramChannel(
		actor.Context(ctx), input.EventID, input.SessionID,
	)
	if err != nil {
		return State{}, err
	}
	control := owned.state
	applied := false
	selected, err := command.Execute(actor.Context(ctx), command.Plan[controlState]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (controlState, error) {
			var receipt controlReceipt
			if decodeErr := store.DecodeCommandReceipt(outcome, &receipt); decodeErr != nil {
				return controlState{}, decodeErr
			}
			return receipt.control(), nil
		},
		Apply: func(_ *store.CommandTx) (command.Execution[controlState], error) {
			if control.revision != input.ExpectedRevision {
				return controlRejection(control, rejectionControlRevision, ErrControlRevision), nil
			}
			if !control.hasOwner || control.owner.AccountID != actor.ID {
				return controlRejection(
					control, rejectionControlOwnerRequired, ErrControlOwnerRequired,
				), nil
			}
			item, ok := selectItem(channel.Items, input.Item)
			if !ok {
				return controlRejection(control, rejectionPreviewItemInvalid, ErrPreviewItem), nil
			}
			next := control
			next.preview = item
			next.revision++
			encoded, encodeErr := json.Marshal(controlReceiptFrom(next))
			if encodeErr != nil {
				return command.Execution[controlState]{}, errors.New("encode Program Preview outcome")
			}
			applied = true
			return command.Success(next, string(encoded)), nil
		},
	})
	if err != nil {
		var rejected *store.RejectedCommandError
		if errors.As(err, &rejected) {
			err = rejectionError(rejected.Rejection.Code, "program Preview command rejected")
		}
		return service.state(channel, control), err
	}
	if applied {
		owned.state = selected
	}
	return service.state(channel, selected), nil
}

// Take commits Program Output before a caller notifies Displays.
func (service *Service) Take(
	ctx context.Context,
	actor auth.Account,
	input TakeInput,
) (TakeResult, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return TakeResult{}, err
	}
	identity := service.takeIdentity(actor, input)
	if !actor.CanOperateEvent(input.EventID) {
		return TakeResult{}, service.auditOperatorRejection(actor.Context(ctx), identity)
	}
	if _, err := service.storage.LoadProgramChannel(
		actor.Context(ctx), input.EventID, input.SessionID,
	); err != nil {
		return TakeResult{}, err
	}
	owned := service.controlFor(input.SessionID)
	owned.mu.Lock()
	defer owned.mu.Unlock()
	control := owned.state
	committed := false
	outcome, err := command.Execute(actor.Context(ctx), command.Plan[takeReceipt]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (takeReceipt, error) {
			var replayed takeReceipt
			if err := store.DecodeCommandReceipt(outcome, &replayed); err != nil {
				return takeReceipt{}, err
			}
			return replayed, nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[takeReceipt], error) {
			if control.revision != input.ExpectedControlRevision {
				return takeRejection(
					store.ProgramChannelState{},
					control,
					rejectionControlRevision,
					ErrControlRevision,
				), nil
			}
			if !control.hasOwner || control.owner.AccountID != actor.ID {
				return takeRejection(
					store.ProgramChannelState{},
					control,
					rejectionControlOwnerRequired,
					ErrControlOwnerRequired,
				), nil
			}
			if !programItemEqual(control.preview, input.Item) {
				return takeRejection(
					store.ProgramChannelState{},
					control,
					rejectionPreviewItemInvalid,
					ErrPreviewItem,
				), nil
			}
			current, loadErr := transaction.LoadProgramChannel(
				actor.Context(ctx), input.EventID, input.SessionID,
			)
			if loadErr != nil {
				return command.Execution[takeReceipt]{}, loadErr
			}
			if current.Revision != input.ExpectedRevision {
				return takeRejection(
					current, control, rejectionProgramRevision, ErrProgramRevision,
				), nil
			}
			if _, valid := selectItem(current.Items, input.Item); !valid {
				return takeRejection(
					current, control, rejectionProgramItemInvalid, ErrProgramItem,
				), nil
			}
			taken, takeErr := transaction.TakeProgramItem(actor.Context(ctx), store.TakeProgramItemParams{
				EventID: input.EventID, SessionID: input.SessionID,
				ExpectedRevision: input.ExpectedRevision, Item: input.Item,
				ExpectedEntryOrderRevision: input.ExpectedEntryOrderRevision,
				EntryOrderFingerprint:      input.EntryOrderFingerprint,
				Now:                        identity.Now,
			})
			if takeErr != nil {
				switch {
				case errors.Is(takeErr, store.ErrEntryOrderRevision):
					return takeRejection(
						current,
						control,
						rejectionEntryOrderRevision,
						store.ErrEntryOrderRevision,
					), nil
				case errors.Is(takeErr, store.ErrEntryOrderPreviewStale):
					return takeRejection(
						current,
						control,
						rejectionEntryOrderStale,
						store.ErrEntryOrderPreviewStale,
					), nil
				}
				return command.Execution[takeReceipt]{}, takeErr
			}
			nextControl := control
			nextControl.preview = taken.Next
			nextControl.revision++
			result := takeReceipt{
				Channel: taken,
				Control: controlReceiptFrom(nextControl),
			}
			committed = true
			encoded, encodeErr := json.Marshal(result)
			if encodeErr != nil {
				return command.Execution[takeReceipt]{}, errors.New("encode Program Output outcome")
			}
			return command.Success(result, string(encoded)), nil
		},
	})
	if err != nil {
		var rejected *store.RejectedCommandError
		if errors.As(err, &rejected) {
			err = takeError(rejected.Rejection.Code)
		}
		return TakeResult{}, err
	}
	if committed {
		owned.state = outcome.Control.control()
	}
	return TakeResult{
		State:     service.state(outcome.Channel, outcome.Control.control()),
		Committed: committed,
	}, nil
}

// DeferEntry advances the cursor while serializing the change through Control Owner.
func (service *Service) DeferEntry(
	ctx context.Context,
	actor auth.Account,
	input DeferEntryInput,
) (TakeResult, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return TakeResult{}, err
	}
	identity := service.deferIdentity(actor, input)
	if !actor.CanOperateEvent(input.EventID) {
		return TakeResult{}, service.auditOperatorRejection(actor.Context(ctx), identity)
	}
	if _, err := service.storage.LoadProgramChannel(
		actor.Context(ctx), input.EventID, input.SessionID,
	); err != nil {
		return TakeResult{}, err
	}
	owned := service.controlFor(input.SessionID)
	owned.mu.Lock()
	defer owned.mu.Unlock()
	control := owned.state
	committed := false
	outcome, err := command.Execute(actor.Context(ctx), command.Plan[takeReceipt]{
		Storage:  service.storage,
		Identity: identity,
		Replay: func(outcome string) (takeReceipt, error) {
			var replayed takeReceipt
			if err := store.DecodeCommandReceipt(outcome, &replayed); err != nil {
				return takeReceipt{}, err
			}
			return replayed, nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[takeReceipt], error) {
			current, loadErr := transaction.LoadProgramChannel(
				actor.Context(ctx), input.EventID, input.SessionID,
			)
			if loadErr != nil {
				return command.Execution[takeReceipt]{}, loadErr
			}
			if control.revision != input.ExpectedControlRevision {
				return takeRejection(
					current, control, rejectionControlRevision, ErrControlRevision,
				), nil
			}
			if !control.hasOwner || control.owner.AccountID != actor.ID {
				return takeRejection(
					current, control, rejectionControlOwnerRequired, ErrControlOwnerRequired,
				), nil
			}
			if current.Revision != input.ExpectedProgramRevision {
				return takeRejection(
					current, control, rejectionProgramRevision, ErrProgramRevision,
				), nil
			}
			if _, deferErr := transaction.DeferCompetitionEntry(
				actor.Context(ctx),
				store.DeferCompetitionEntryParams{
					EventID: input.EventID, SessionID: input.SessionID, EntryID: input.EntryID,
					ExpectedEntryRevision:   input.ExpectedEntryRevision,
					ExpectedProgramRevision: input.ExpectedProgramRevision,
					Now:                     identity.Now,
				},
			); deferErr != nil {
				switch {
				case errors.Is(deferErr, store.ErrCompetitionEntryRevision):
					return takeRejection(
						current, control, rejectionEntryRevision, ErrEntryRevision,
					), nil
				case errors.Is(deferErr, store.ErrCompetitionEntryDefer):
					return takeRejection(
						current, control, rejectionEntryDefer, ErrEntryDefer,
					), nil
				}
				return command.Execution[takeReceipt]{}, deferErr
			}
			deferred, loadErr := transaction.LoadProgramChannel(
				actor.Context(ctx), input.EventID, input.SessionID,
			)
			if loadErr != nil {
				return command.Execution[takeReceipt]{}, loadErr
			}
			nextControl := control
			nextControl.preview = deferred.Next
			nextControl.revision++
			result := takeReceipt{
				Channel: deferred,
				Control: controlReceiptFrom(nextControl),
			}
			encoded, encodeErr := json.Marshal(result)
			if encodeErr != nil {
				return command.Execution[takeReceipt]{}, errors.New("encode Program Defer outcome")
			}
			committed = true
			return command.Success(result, string(encoded)), nil
		},
	})
	if err != nil {
		var rejected *store.RejectedCommandError
		if errors.As(err, &rejected) {
			err = takeError(rejected.Rejection.Code)
		}
		return TakeResult{}, err
	}
	if committed {
		owned.state = outcome.Control.control()
	}
	return TakeResult{
		State:     service.state(outcome.Channel, outcome.Control.control()),
		Committed: committed,
	}, nil
}

func (service *Service) state(channel store.ProgramChannelState, control controlState) State {
	result := State{Channel: channel, ControlRevision: control.revision, Preview: control.preview}
	if result.Preview.Kind == "" {
		result.Preview = channel.Next
	}
	if control.hasOwner {
		copied := control.owner
		result.Owner = &copied
	}
	if control.hasRequest {
		copied := control.requester
		result.HandoverRequester = &copied
	}
	return result
}

func owner(actor auth.Account, connected bool) Owner {
	return Owner{AccountID: actor.ID, Name: actor.Name, Connected: connected}
}

func selectItem(items []store.ProgramItem, wanted store.ProgramItem) (store.ProgramItem, bool) {
	for _, item := range items {
		if programItemEqual(item, wanted) {
			return item, true
		}
	}
	return store.ProgramItem{}, false
}

func programItemEqual(left, right store.ProgramItem) bool {
	return left.Kind == right.Kind && left.EntryID == right.EntryID && left.Retry == right.Retry
}

func controlReceiptFrom(control controlState) controlReceipt {
	receipt := controlReceipt{Revision: control.revision, Preview: control.preview}
	if control.hasOwner {
		copied := control.owner
		receipt.Owner = &copied
	}
	if control.hasRequest {
		copied := control.requester
		receipt.Requester = &copied
	}
	return receipt
}

func (receipt controlReceipt) control() controlState {
	control := controlState{revision: receipt.Revision, preview: receipt.Preview}
	if receipt.Owner != nil {
		control.owner = *receipt.Owner
		control.hasOwner = true
	}
	if receipt.Requester != nil {
		control.requester = *receipt.Requester
		control.hasRequest = true
	}
	return control
}

func controlErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrControlOwned):
		return string(rejectionControlOwned)
	case errors.Is(err, ErrControlOwnerRequired):
		return string(rejectionControlOwnerRequired)
	case errors.Is(err, ErrTakeoverConfirmation):
		return string(rejectionTakeoverConfirmation)
	default:
		return string(rejectionHandoverUnavailable)
	}
}

func controlError(code string) error {
	return rejectionError(code, "program control command rejected")
}

func controlRejection(
	current controlState,
	code rejectionCode,
	err error,
) command.Execution[controlState] {
	return command.Reject(current, store.CommandRejection{
		Code: string(code), Message: err.Error(),
	}, err)
}

func takeRejection(
	current store.ProgramChannelState,
	control controlState,
	code rejectionCode,
	err error,
) command.Execution[takeReceipt] {
	return command.Reject(takeReceipt{
		Channel: current,
		Control: controlReceiptFrom(control),
	}, store.CommandRejection{
		Code: string(code), Message: err.Error(),
	}, err)
}

func takeError(code string) error {
	return rejectionError(code, "program Take rejected")
}

func rejectionError(code, fallback string) error {
	switch rejectionCode(code) {
	case rejectionOperatorRequired:
		return ErrOperatorRequired
	case rejectionControlOwned:
		return ErrControlOwned
	case rejectionControlOwnerRequired:
		return ErrControlOwnerRequired
	case rejectionTakeoverConfirmation:
		return ErrTakeoverConfirmation
	case rejectionHandoverUnavailable:
		return ErrHandoverUnavailable
	case rejectionPreviewItemInvalid:
		return ErrPreviewItem
	case rejectionProgramRevision:
		return ErrProgramRevision
	case rejectionControlRevision:
		return ErrControlRevision
	case rejectionProgramItemInvalid:
		return ErrProgramItem
	case rejectionEntryOrderRevision:
		return store.ErrEntryOrderRevision
	case rejectionEntryOrderStale:
		return store.ErrEntryOrderPreviewStale
	case rejectionEntryRevision:
		return ErrEntryRevision
	case rejectionEntryDefer:
		return ErrEntryDefer
	default:
		return errors.New(fallback)
	}
}
