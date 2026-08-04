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
	"github.com/dotwaffle/beamers/internal/authz"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/prizegivingvalue"
	"github.com/dotwaffle/beamers/internal/results"
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
	// ErrEntryOrderRevision means Take observed a stale Locked Entry Order.
	ErrEntryOrderRevision = store.ErrEntryOrderRevision
	// ErrEntryOrderPreviewStale means Take observed a stale Entry Order preview.
	ErrEntryOrderPreviewStale = store.ErrEntryOrderPreviewStale
	// ErrCommandConflict means a Command ID was reused with another payload.
	ErrCommandConflict = store.ErrCommandConflict
	// ErrResultTransition means the requested Result action is not valid now.
	ErrResultTransition = results.ErrResultItemTransition
	// ErrResultRevealRunning means Reveal completion was requested too early.
	ErrResultRevealRunning = results.ErrResultRevealRunning
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
	Channel           Channel
	ControlRevision   int
	Owner             *Owner
	HandoverRequester *Owner
	Preview           Item
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
	Item               Item
	CommandID          string
	ExpectedRevision   int
}

// TakeInput durably commits one exact Preview as Program Output.
type TakeInput struct {
	EventID, SessionID         int
	CommandID                  string
	ExpectedRevision           int
	ExpectedControlRevision    int
	Item                       Item
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

// ResultAction selects one explicit Prizegiving Result transition.
type ResultAction string

const (
	// ResultReveal starts the locked Result presentation.
	ResultReveal ResultAction = "Reveal"
	// ResultReplayReveal reruns presentation without changing final truth.
	ResultReplayReveal ResultAction = "ReplayReveal"
	// ResultSkipToFinal completes the current Result immediately.
	ResultSkipToFinal ResultAction = "SkipToFinal"
	// ResultSkipFromStage omits an unpresented Result and advances Preview.
	ResultSkipFromStage ResultAction = "SkipFromStage"
)

// ResultActionInput applies one Result transition through the Control Owner.
type ResultActionInput struct {
	EventID, SessionID      int
	CommandID               string
	Action                  ResultAction
	Item                    Item
	ExpectedProgramRevision int
	ExpectedControlRevision int
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
	rejectionResultTransition     rejectionCode = "prizegiving_result_transition_invalid"
	rejectionResultRevealRunning  rejectionCode = "prizegiving_result_reveal_running"
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
	storage        *store.SQLite
	publications   *results.Service
	now            func() time.Time
	notifyDisplays func()
	notifyProgram  func()
	notifyVoting   func()
	mu             sync.Mutex
	controls       map[int]*channelControl
}

// New creates a Program control service. Its empty control map deliberately
// clears ownership and unsent Preview after every process restart.
func New(
	storage *store.SQLite,
	publications *results.Service,
	now func() time.Time,
	notifyDisplays func(),
	notifyProgram func(),
	notifyVoting func(),
) (*Service, error) {
	if storage == nil {
		return nil, errors.New("program control storage is required")
	}
	if publications == nil {
		return nil, errors.New("results publication service is required")
	}
	if now == nil {
		return nil, errors.New("program control clock is required")
	}
	return &Service{
		storage: storage, publications: publications, now: now,
		notifyDisplays: notifyDisplays, notifyProgram: notifyProgram, notifyVoting: notifyVoting,
		controls: make(map[int]*channelControl),
	}, nil
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

// OpenConnection tracks one live control View and publishes presence transitions.
func (service *Service) OpenConnection(
	ctx context.Context,
	actor auth.Account,
	eventID, sessionID int,
) (State, func(), error) {
	if !actor.CanOperateEvent(eventID) {
		return State{}, nil, ErrOperatorRequired
	}
	if _, err := service.storage.LoadProgramChannelAt(
		actor.Context(ctx), eventID, sessionID, service.now().UTC(),
	); err != nil {
		return State{}, nil, err
	}
	owned := service.controlFor(sessionID)
	owned.mu.Lock()
	channel, err := service.storage.LoadProgramChannelAt(
		actor.Context(ctx), eventID, sessionID, service.now().UTC(),
	)
	if err != nil {
		owned.mu.Unlock()
		return State{}, nil, err
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
	if changed && service.notifyProgram != nil {
		service.notifyProgram()
	}

	var closeOnce sync.Once
	release := func() {
		closeOnce.Do(func() {
			owned.mu.Lock()
			disconnected := false
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
			owned.mu.Unlock()
			if disconnected && service.notifyProgram != nil {
				service.notifyProgram()
			}
		})
	}
	return state, release, nil
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
		strconv.Itoa(input.ExpectedRevision),
	}
	payload = append(payload, programItemIdentity(input.Item)...)
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
		strconv.Itoa(input.ExpectedRevision),
	}
	payload = append(payload, programItemIdentity(input.Item)...)
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

func (service *Service) resultIdentity(
	actor auth.Account,
	input ResultActionInput,
) store.CommandIdentity {
	payload := []string{
		strconv.Itoa(input.EventID),
		strconv.Itoa(input.SessionID),
		string(input.Action),
	}
	payload = append(payload, programItemIdentity(input.Item)...)
	payload = append(
		payload,
		strconv.Itoa(input.ExpectedProgramRevision),
		strconv.Itoa(input.ExpectedControlRevision),
	)
	return store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(payload...),
		Action:      "ActOnPrizegivingResult" + string(input.Action),
		TargetType:  "ProgramChannel",
		TargetID:    strconv.Itoa(input.SessionID),
		Now:         service.now().UTC(),
	}
}

func programItemIdentity(item Item) []string {
	parts := []string{
		string(item.Kind),
		strconv.Itoa(item.EntryID),
		strconv.FormatBool(item.Retry),
	}
	if item.Result == nil {
		return parts
	}
	return append(
		parts,
		string(item.Result.Ref.Kind),
		strconv.Itoa(item.Result.Ref.CompetitionSessionID),
		item.Result.Ref.AwardKey,
		strconv.Itoa(item.Result.Ref.DisplayOrder),
	)
}

func (service *Service) auditOperatorRejection(
	ctx context.Context,
	eventID int,
	identity store.CommandIdentity,
) error {
	_, err := command.Execute(ctx, command.Plan[struct{}]{
		Storage: service.storage, Identity: identity,
		Authorization: command.Authorization{
			Facts:    authz.Event(eventID),
			Refusals: programRejections,
		},
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
	return service.currentAt(ctx, actor, eventID, sessionID, service.now().UTC())
}

// ReconcileAndCurrent commits elapsed publication before returning current state.
func (service *Service) ReconcileAndCurrent(
	ctx context.Context,
	actor auth.Account,
	eventID, sessionID int,
) (State, error) {
	if !actor.CanOperateEvent(eventID) {
		return State{}, ErrOperatorRequired
	}
	now := service.now().UTC()
	if err := service.reconcileProgressivePublication(
		ctx, actor, eventID, sessionID, now,
	); err != nil {
		return State{}, err
	}
	return service.currentAt(ctx, actor, eventID, sessionID, now)
}

func (service *Service) currentAt(
	ctx context.Context,
	actor auth.Account,
	eventID, sessionID int,
	now time.Time,
) (State, error) {
	owned := service.controlFor(sessionID)
	owned.mu.Lock()
	defer owned.mu.Unlock()
	channel, err := service.storage.LoadProgramChannelAt(
		actor.Context(ctx), eventID, sessionID, now,
	)
	if err != nil {
		return State{}, err
	}
	return service.state(channel, owned.state), nil
}

func (service *Service) reconcileProgressivePublication(
	ctx context.Context,
	actor auth.Account,
	eventID, sessionID int,
	now time.Time,
) error {
	for range 2 {
		channel, err := service.storage.LoadProgramChannelAt(
			actor.Context(ctx), eventID, sessionID, now,
		)
		if err != nil {
			return err
		}
		states := results.PrizegivingPublicationStates(channel.Items)
		if len(states) == 0 {
			return nil
		}
		plan, err := service.storage.LoadPrizegivingPlan(
			actor.Context(ctx),
			eventID,
			sessionID,
		)
		if err != nil {
			return err
		}
		if !plan.Locked ||
			plan.ReleasePolicy != prizegivingvalue.ReleaseProgressiveOnReveal {
			return nil
		}
		ready := false
		for _, state := range states {
			if state.Release == results.ResultReleaseReady {
				ready = true
				break
			}
		}
		if !ready {
			return nil
		}
		payload, err := progressiveReconciliationPayload(
			eventID,
			sessionID,
			actor.ID,
			states,
		)
		if err != nil {
			return errors.New("encode Progressive Results reconciliation")
		}
		digest := command.PayloadHash(payload)
		identity := store.CommandIdentity{
			ActorAccountID: actor.ID,
			CommandID:      "reconcile-progressive-results-" + digest,
			PayloadHash:    digest,
			Action:         "ReconcileProgressiveResultsPublication",
			TargetType:     "Session",
			TargetID:       strconv.Itoa(sessionID),
			Now:            now,
		}
		_, err = command.Execute(
			actor.Context(ctx),
			command.Plan[results.Publication]{
				Storage: service.storage, Identity: identity,
				Authorization: command.Authorization{
					Facts:    authz.Event(eventID),
					Refusals: programRejections,
				},
				Replay: func(outcome string) (results.Publication, error) {
					var publication results.Publication
					if decodeErr := store.DecodeCommandReceipt(
						outcome,
						&publication,
					); decodeErr != nil {
						return results.Publication{}, decodeErr
					}
					return publication, nil
				},
				Apply: func(
					transaction *store.CommandTx,
				) (command.Execution[results.Publication], error) {
					updated, _, advanceErr :=
						service.publications.ReconcilePrizegivingPublication(
							actor.Context(ctx),
							actor,
							transaction,
							results.ReconcilePrizegivingPublicationInput{
								EventID: eventID, CeremonySessionID: sessionID, Now: now,
							},
						)
					if advanceErr != nil {
						return command.Execution[results.Publication]{}, advanceErr
					}
					encoded, encodeErr := json.Marshal(updated)
					if encodeErr != nil {
						return command.Execution[results.Publication]{}, errors.New(
							"encode Progressive Results reconciliation outcome",
						)
					}
					return command.Success(updated, string(encoded)), nil
				},
			},
		)
		if !errors.Is(err, store.ErrResultsPublicationRevision) {
			return err
		}
	}
	return store.ErrResultsPublicationRevision
}

func progressiveReconciliationPayload(
	eventID, sessionID, actorAccountID int,
	states []results.ResultItemStageState,
) (string, error) {
	payload, err := json.Marshal(progressiveReconciliationIdentity{
		EventID: eventID, SessionID: sessionID,
		ActorAccountID: actorAccountID, States: states,
	})
	return string(payload), err
}

type progressiveReconciliationIdentity struct {
	EventID        int                            `json:"event_id"`
	SessionID      int                            `json:"session_id"`
	ActorAccountID int                            `json:"actor_account_id"`
	States         []results.ResultItemStageState `json:"states"`
}

// controlCommand is the varying work of one process-local ownership command.
type controlCommand struct {
	identity  store.CommandIdentity
	eventID   int
	sessionID int
	// unavailable is the failure a replayed rejection restores to when this
	// package no longer recognizes its recorded code.
	unavailable string
	// apply decides the command's outcome against the ownership state held for
	// the Program Channel and its current durable output.
	apply func(controlState, store.ProgramChannelState) (command.Execution[controlState], error)
}

// runControlCommand owns the authority check, the Program Channel lock, and
// the receipt handling that every process-local ownership command shares.
// Ownership lives in this process only, so the durable command exists to record
// the decision and to keep a retried command from changing it twice.
func (service *Service) runControlCommand(
	ctx context.Context,
	actor auth.Account,
	channelCommand controlCommand,
) (State, error) {
	if err := command.ValidateID(channelCommand.identity.CommandID); err != nil {
		return State{}, err
	}
	if !actor.CanOperateEvent(channelCommand.eventID) {
		return State{}, service.auditOperatorRejection(
			actor.Context(ctx), channelCommand.eventID, channelCommand.identity,
		)
	}
	if _, err := service.storage.LoadProgramChannelAt(
		actor.Context(ctx), channelCommand.eventID, channelCommand.sessionID,
		service.now().UTC(),
	); err != nil {
		return State{}, err
	}
	owned := service.controlFor(channelCommand.sessionID)
	owned.mu.Lock()
	defer owned.mu.Unlock()
	channel, err := service.storage.LoadProgramChannelAt(
		actor.Context(ctx), channelCommand.eventID, channelCommand.sessionID,
		service.now().UTC(),
	)
	if err != nil {
		return State{}, err
	}
	current := owned.state
	var executionState controlState
	next, err := command.Execute(actor.Context(ctx), command.Plan[controlState]{
		Storage: service.storage, Identity: channelCommand.identity,
		Authorization: command.Authorization{
			Facts:    authz.Event(channelCommand.eventID),
			Refusals: programRejections,
		},
		Applied: func() { owned.state = executionState },
		Notify: func() {
			if service.notifyProgram != nil {
				service.notifyProgram()
			}
		},
		Replay: func(outcome string) (controlState, error) {
			var receipt controlReceipt
			if decodeErr := store.DecodeCommandReceipt(outcome, &receipt); decodeErr != nil {
				return controlState{}, decodeErr
			}
			executionState = receipt.control()
			return executionState, nil
		},
		Apply: func(_ *store.CommandTx) (command.Execution[controlState], error) {
			execution, applyErr := channelCommand.apply(current, channel)
			if applyErr != nil {
				return command.Execution[controlState]{}, applyErr
			}
			executionState = execution.Value()
			return execution, nil
		},
	})
	if err != nil {
		var rejected *store.RejectedCommandError
		if errors.As(err, &rejected) {
			err = rejectionError(rejected.Rejection.Code, channelCommand.unavailable)
		}
		return service.state(channel, current), err
	}
	return service.state(channel, next), nil
}

// Control applies one explicit process-local ownership transition.
func (service *Service) Control(
	ctx context.Context,
	actor auth.Account,
	input ControlInput,
) (State, error) {
	return service.runControlCommand(ctx, actor, controlCommand{
		identity:    service.controlIdentity(actor, input),
		eventID:     input.EventID,
		sessionID:   input.SessionID,
		unavailable: "program control command rejected",
		apply: func(
			current controlState,
			channel store.ProgramChannelState,
		) (command.Execution[controlState], error) {
			return applyControl(controlTransitionInput{
				control: current, actor: actor, input: input, channel: channel,
			})
		},
	})
}

func applyControl(
	transition controlTransitionInput,
) (command.Execution[controlState], error) {
	current := transition.control
	if current.revision != transition.input.ExpectedRevision {
		return controlRejection(current, rejectionControlRevision, ErrControlRevision), nil
	}
	transitioned, transitionErr := transitionControl(transition)
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
	return command.Success(transitioned, string(encoded)), nil
}

// controlTransitionInput is the complete context of one ownership action.
type controlTransitionInput struct {
	control controlState
	actor   auth.Account
	input   ControlInput
	channel store.ProgramChannelState
}

// owned reports whether the acting Crew Member is the current Control Owner.
func (input controlTransitionInput) owned() bool {
	return input.control.hasOwner && input.control.owner.AccountID == input.actor.ID
}

// controlTransition is one ownership action's availability guard and the
// change it makes once that guard admits it.
type controlTransition struct {
	guard func(controlTransitionInput) error
	apply func(controlTransitionInput) controlState
}

// controlTransitions is the single source for which ownership actions are
// available in which state. Every action states its own precondition, so an
// unavailable transition can never fall through into a state change.
var controlTransitions = map[ControlAction]controlTransition{
	ControlClaim: {
		guard: func(input controlTransitionInput) error {
			if input.control.hasOwner && !input.owned() {
				return ErrControlOwned
			}
			return nil
		},
		apply: func(input controlTransitionInput) controlState {
			control := input.control
			control.owner = owner(input.actor, true)
			control.hasOwner = true
			return withDefaultedPreview(control, input.channel)
		},
	},
	ControlRequestHandover: {
		guard: func(input controlTransitionInput) error {
			if !input.control.hasOwner || input.owned() {
				return ErrHandoverUnavailable
			}
			return nil
		},
		apply: func(input controlTransitionInput) controlState {
			control := input.control
			control.requester = owner(input.actor, true)
			control.hasRequest = true
			return control
		},
	},
	ControlHandover: {
		guard: func(input controlTransitionInput) error {
			if !input.owned() || !input.control.hasRequest {
				return ErrHandoverUnavailable
			}
			return nil
		},
		apply: func(input controlTransitionInput) controlState {
			control := input.control
			control.owner = control.requester
			control.hasRequest = false
			return control
		},
	},
	ControlTakeover: {
		guard: func(input controlTransitionInput) error {
			if !input.input.Confirmed {
				return ErrTakeoverConfirmation
			}
			return nil
		},
		apply: func(input controlTransitionInput) controlState {
			control := input.control
			if input.owned() {
				control.owner.Connected = true
			} else {
				control.owner = owner(input.actor, true)
				control.hasOwner = true
			}
			control.hasRequest = false
			return withDefaultedPreview(control, input.channel)
		},
	},
	ControlDisconnect: {
		guard: func(input controlTransitionInput) error {
			if !input.owned() {
				return ErrControlOwnerRequired
			}
			return nil
		},
		apply: func(input controlTransitionInput) controlState {
			control := input.control
			control.owner.Connected = false
			return control
		},
	},
}

// withDefaultedPreview adopts the canonical Next item for a Crew Member who has
// not selected a Preview yet.
func withDefaultedPreview(
	control controlState,
	channel store.ProgramChannelState,
) controlState {
	if control.preview.Kind == "" {
		control.preview = channel.Next
	}
	return control
}

func transitionControl(input controlTransitionInput) (controlState, error) {
	transition, available := controlTransitions[input.input.Action]
	if !available {
		return input.control, ErrHandoverUnavailable
	}
	if err := transition.guard(input); err != nil {
		return input.control, err
	}
	return transition.apply(input), nil
}

// SelectPreview changes no durable state.
func (service *Service) SelectPreview(
	ctx context.Context,
	actor auth.Account,
	input SelectPreviewInput,
) (State, error) {
	return service.runControlCommand(ctx, actor, controlCommand{
		identity:    service.previewIdentity(actor, input),
		eventID:     input.EventID,
		sessionID:   input.SessionID,
		unavailable: "program Preview command rejected",
		apply: func(
			control controlState,
			channel store.ProgramChannelState,
		) (command.Execution[controlState], error) {
			return applySelectPreview(previewSelection{
				control: control, actor: actor, input: input, channel: channel,
			})
		},
	})
}

// previewSelection is the complete context of one Preview selection.
type previewSelection struct {
	control controlState
	actor   auth.Account
	input   SelectPreviewInput
	channel store.ProgramChannelState
}

func applySelectPreview(
	selection previewSelection,
) (command.Execution[controlState], error) {
	control := selection.control
	if control.revision != selection.input.ExpectedRevision {
		return controlRejection(control, rejectionControlRevision, ErrControlRevision), nil
	}
	if !control.hasOwner || control.owner.AccountID != selection.actor.ID {
		return controlRejection(
			control, rejectionControlOwnerRequired, ErrControlOwnerRequired,
		), nil
	}
	item, selectable := selectItem(selection.channel.Items, storedItem(selection.input.Item))
	if !selectable {
		return controlRejection(control, rejectionPreviewItemInvalid, ErrPreviewItem), nil
	}
	next := control
	next.preview = item
	next.revision++
	encoded, encodeErr := json.Marshal(controlReceiptFrom(next))
	if encodeErr != nil {
		return command.Execution[controlState]{}, errors.New("encode Program Preview outcome")
	}
	return command.Success(next, string(encoded)), nil
}

// channelCommand is the varying work of one durable Program Channel command.
type channelCommand struct {
	identity  store.CommandIdentity
	eventID   int
	sessionID int
	// notify publishes freshness hints for the projections this command changed.
	notify func(takeReceipt)
	// apply runs the command's guards and durable work with the ownership state
	// held for the Program Channel.
	apply func(*store.CommandTx, controlState) (command.Execution[takeReceipt], error)
}

// runChannelCommand owns the authority check, the Program Channel lock, and the
// receipt handling that every durable Program Channel command shares. Adopting
// the new Preview belongs to a fresh application only: a replayed command must
// return its original outcome without moving live control again.
func (service *Service) runChannelCommand(
	ctx context.Context,
	actor auth.Account,
	durableCommand channelCommand,
) (TakeResult, error) {
	if err := command.ValidateID(durableCommand.identity.CommandID); err != nil {
		return TakeResult{}, err
	}
	if !actor.CanOperateEvent(durableCommand.eventID) {
		return TakeResult{}, service.auditOperatorRejection(
			actor.Context(ctx), durableCommand.eventID, durableCommand.identity,
		)
	}
	if _, err := service.storage.LoadProgramChannelAt(
		actor.Context(ctx), durableCommand.eventID, durableCommand.sessionID,
		service.now().UTC(),
	); err != nil {
		return TakeResult{}, err
	}
	owned := service.controlFor(durableCommand.sessionID)
	owned.mu.Lock()
	defer owned.mu.Unlock()
	control := owned.state
	committed := false
	var receipt takeReceipt
	outcome, err := command.Execute(actor.Context(ctx), command.Plan[takeReceipt]{
		Storage:  service.storage,
		Identity: durableCommand.identity,
		Authorization: command.Authorization{
			Facts:    authz.Event(durableCommand.eventID),
			Refusals: programRejections,
		},
		Applied: func() {
			owned.state = receipt.Control.control()
			committed = true
		},
		Notify: func() { durableCommand.notify(receipt) },
		Replay: func(outcome string) (takeReceipt, error) {
			var replayed takeReceipt
			if decodeErr := store.DecodeCommandReceipt(outcome, &replayed); decodeErr != nil {
				return takeReceipt{}, decodeErr
			}
			receipt = replayed
			return replayed, nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[takeReceipt], error) {
			execution, applyErr := durableCommand.apply(transaction, control)
			if applyErr != nil {
				return command.Execution[takeReceipt]{}, applyErr
			}
			receipt = execution.Value()
			return execution, nil
		},
	})
	if err != nil {
		var rejected *store.RejectedCommandError
		if errors.As(err, &rejected) {
			err = takeError(rejected.Rejection.Code)
		}
		return TakeResult{}, err
	}
	return TakeResult{
		State:     service.state(outcome.Channel, outcome.Control.control()),
		Committed: committed,
	}, nil
}

// Take commits Program Output and refreshes its affected live projections.
func (service *Service) Take(
	ctx context.Context,
	actor auth.Account,
	input TakeInput,
) (TakeResult, error) {
	identity := service.takeIdentity(actor, input)
	return service.runChannelCommand(ctx, actor, channelCommand{
		identity: identity, eventID: input.EventID, sessionID: input.SessionID,
		notify: func(receipt takeReceipt) {
			service.notifyOutput(receipt.Channel.Output.Kind == store.ProgramItemEntry)
		},
		apply: func(
			transaction *store.CommandTx,
			control controlState,
		) (command.Execution[takeReceipt], error) {
			return applyTake(ctx, actor, takeApplication{
				input: input, now: identity.Now, control: control, transaction: transaction,
			})
		},
	})
}

// takeApplication is the complete context of one durable Take.
type takeApplication struct {
	input       TakeInput
	now         time.Time
	control     controlState
	transaction *store.CommandTx
}

func applyTake(
	ctx context.Context,
	actor auth.Account,
	application takeApplication,
) (command.Execution[takeReceipt], error) {
	input, control := application.input, application.control
	if control.revision != input.ExpectedControlRevision {
		return takeRejection(
			store.ProgramChannelState{}, control, rejectionControlRevision, ErrControlRevision,
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
	if !control.preview.SameIdentity(storedItem(input.Item)) {
		return takeRejection(
			store.ProgramChannelState{}, control, rejectionPreviewItemInvalid, ErrPreviewItem,
		), nil
	}
	current, loadErr := application.transaction.LoadProgramChannelAt(
		actor.Context(ctx), input.EventID, input.SessionID, application.now,
	)
	if loadErr != nil {
		return command.Execution[takeReceipt]{}, loadErr
	}
	if current.Revision != input.ExpectedRevision {
		return takeRejection(
			current, control, rejectionProgramRevision, ErrProgramRevision,
		), nil
	}
	if unresolvedResultInOutput(current.Output) {
		return takeRejection(
			current, control, rejectionResultRevealRunning, ErrResultRevealRunning,
		), nil
	}
	selected, valid := selectItem(current.Items, storedItem(input.Item))
	if !valid {
		return takeRejection(
			current, control, rejectionProgramItemInvalid, ErrProgramItem,
		), nil
	}
	resultState, takeable := takenResultState(selected, application.now)
	if !takeable {
		return takeRejection(
			current, control, rejectionProgramItemInvalid, ErrProgramItem,
		), nil
	}
	taken, takeErr := application.transaction.TakeProgramItem(
		actor.Context(ctx),
		store.TakeProgramItemParams{
			EventID: input.EventID, SessionID: input.SessionID,
			ExpectedRevision: input.ExpectedRevision, Item: selected,
			ExpectedEntryOrderRevision: input.ExpectedEntryOrderRevision,
			EntryOrderFingerprint:      input.EntryOrderFingerprint,
			Now:                        application.now,
			ResultState:                resultState,
		},
	)
	if takeErr != nil {
		switch {
		case errors.Is(takeErr, store.ErrEntryOrderRevision):
			return takeRejection(
				current, control, rejectionEntryOrderRevision, store.ErrEntryOrderRevision,
			), nil
		case errors.Is(takeErr, store.ErrEntryOrderPreviewStale):
			return takeRejection(
				current, control, rejectionEntryOrderStale, store.ErrEntryOrderPreviewStale,
			), nil
		}
		return command.Execution[takeReceipt]{}, takeErr
	}
	return channelExecution(taken, control, "encode Program Output outcome")
}

// takenResultState returns the durable presentation state a Result Item enters
// when it is taken, and reports whether the Item may be taken at all.
func takenResultState(
	selected store.ProgramItem,
	now time.Time,
) (*store.PrizegivingStageState, bool) {
	if selected.Kind != store.ProgramItemResult {
		return nil, true
	}
	next, transitionErr := results.TakePrizegivingResultItem(
		lockedResultItem(selected), resultItemStageState(selected), now,
	)
	if transitionErr != nil {
		return nil, false
	}
	converted := prizegivingStageState(next)
	return &converted, true
}

// DeferEntry advances the cursor while serializing the change through Control Owner.
func (service *Service) DeferEntry(
	ctx context.Context,
	actor auth.Account,
	input DeferEntryInput,
) (TakeResult, error) {
	identity := service.deferIdentity(actor, input)
	return service.runChannelCommand(ctx, actor, channelCommand{
		identity: identity, eventID: input.EventID, sessionID: input.SessionID,
		notify: func(takeReceipt) {
			if service.notifyProgram != nil {
				service.notifyProgram()
			}
		},
		apply: func(
			transaction *store.CommandTx,
			control controlState,
		) (command.Execution[takeReceipt], error) {
			return applyDeferEntry(ctx, actor, deferApplication{
				input: input, now: identity.Now, control: control, transaction: transaction,
			})
		},
	})
}

// deferApplication is the complete context of one durable Defer Entry.
type deferApplication struct {
	input       DeferEntryInput
	now         time.Time
	control     controlState
	transaction *store.CommandTx
}

func applyDeferEntry(
	ctx context.Context,
	actor auth.Account,
	application deferApplication,
) (command.Execution[takeReceipt], error) {
	input, control := application.input, application.control
	current, loadErr := application.transaction.LoadProgramChannelAt(
		actor.Context(ctx), input.EventID, input.SessionID, application.now,
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
	if _, deferErr := application.transaction.DeferCompetitionEntry(
		actor.Context(ctx),
		store.DeferCompetitionEntryParams{
			EventID: input.EventID, SessionID: input.SessionID, EntryID: input.EntryID,
			ExpectedEntryRevision:   input.ExpectedEntryRevision,
			ExpectedProgramRevision: input.ExpectedProgramRevision,
			Now:                     application.now,
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
	deferred, loadErr := application.transaction.LoadProgramChannelAt(
		actor.Context(ctx), input.EventID, input.SessionID, application.now,
	)
	if loadErr != nil {
		return command.Execution[takeReceipt]{}, loadErr
	}
	return channelExecution(deferred, control, "encode Program Defer outcome")
}

// ActOnResult applies a pure Prizegiving Result transition and refreshes its
// affected live projections.
func (service *Service) ActOnResult(
	ctx context.Context,
	actor auth.Account,
	input ResultActionInput,
) (TakeResult, error) {
	identity := service.resultIdentity(actor, input)
	return service.runChannelCommand(ctx, actor, channelCommand{
		identity: identity, eventID: input.EventID, sessionID: input.SessionID,
		notify: func(takeReceipt) { service.notifyOutput(false) },
		apply: func(
			transaction *store.CommandTx,
			control controlState,
		) (command.Execution[takeReceipt], error) {
			return service.applyResultAction(ctx, actor, resultApplication{
				input: input, now: identity.Now, control: control, transaction: transaction,
			})
		},
	})
}

// channelExecution commits one advanced Program Channel with the Preview its
// Control Owner inherits from the new canonical Next item.
func channelExecution(
	channel store.ProgramChannelState,
	control controlState,
	encodeFailure string,
) (command.Execution[takeReceipt], error) {
	next := control
	next.preview = channel.Next
	next.revision++
	result := takeReceipt{Channel: channel, Control: controlReceiptFrom(next)}
	encoded, encodeErr := json.Marshal(result)
	if encodeErr != nil {
		return command.Execution[takeReceipt]{}, errors.New(encodeFailure)
	}
	return command.Success(result, string(encoded)), nil
}

func (service *Service) notifyOutput(voting bool) {
	if service.notifyDisplays != nil {
		service.notifyDisplays()
	}
	if service.notifyProgram != nil {
		service.notifyProgram()
	}
	if voting && service.notifyVoting != nil {
		service.notifyVoting()
	}
}

// resultApplication is the complete context of one durable Result action.
type resultApplication struct {
	input       ResultActionInput
	now         time.Time
	control     controlState
	transaction *store.CommandTx
}

func (service *Service) applyResultAction(
	ctx context.Context,
	actor auth.Account,
	application resultApplication,
) (command.Execution[takeReceipt], error) {
	input, control := application.input, application.control
	current, err := application.transaction.LoadProgramChannelAt(
		actor.Context(ctx), input.EventID, input.SessionID, application.now,
	)
	if err != nil {
		return command.Execution[takeReceipt]{}, err
	}
	selected, code, validationErr := validateResultAction(
		actor, input, current, control,
	)
	if validationErr != nil {
		return takeRejection(current, control, code, validationErr), nil
	}
	nextState, presentation, transitionErr := transitionResult(resultTransitionInput{
		action: input.Action, selected: selected, channel: current, now: application.now,
	})
	if transitionErr != nil {
		code = rejectionResultTransition
		if errors.Is(transitionErr, results.ErrResultRevealRunning) {
			code = rejectionResultRevealRunning
		}
		return takeRejection(current, control, code, transitionErr), nil
	}
	updated, err := persistResultAction(ctx, actor, resultPersistence{
		input: input, now: application.now, transaction: application.transaction,
		selected: selected, state: nextState, presentation: presentation,
	})
	if err != nil {
		return command.Execution[takeReceipt]{}, err
	}
	_, _, publicationErr := service.publications.ReconcilePrizegivingPublication(
		actor.Context(ctx),
		actor,
		application.transaction,
		results.ReconcilePrizegivingPublicationInput{
			EventID: input.EventID, CeremonySessionID: input.SessionID, Now: application.now,
		},
	)
	if publicationErr != nil {
		return command.Execution[takeReceipt]{}, publicationErr
	}
	return channelExecution(updated, control, "encode Prizegiving Result outcome")
}

func validateResultAction(
	actor auth.Account,
	input ResultActionInput,
	current store.ProgramChannelState,
	control controlState,
) (store.ProgramItem, rejectionCode, error) {
	if control.revision != input.ExpectedControlRevision {
		return store.ProgramItem{}, rejectionControlRevision, ErrControlRevision
	}
	if !control.hasOwner || control.owner.AccountID != actor.ID {
		return store.ProgramItem{}, rejectionControlOwnerRequired, ErrControlOwnerRequired
	}
	if current.Revision != input.ExpectedProgramRevision {
		return store.ProgramItem{}, rejectionProgramRevision, ErrProgramRevision
	}
	selected, valid := selectItem(current.Items, storedItem(input.Item))
	if !valid || selected.Kind != store.ProgramItemResult {
		return store.ProgramItem{}, rejectionProgramItemInvalid, ErrProgramItem
	}
	if input.Action == ResultSkipFromStage &&
		!control.preview.SameIdentity(selected) {
		return store.ProgramItem{}, rejectionPreviewItemInvalid, ErrPreviewItem
	}
	return selected, "", nil
}

// resultPersistence is one decided Result transition awaiting its durable write.
type resultPersistence struct {
	input        ResultActionInput
	now          time.Time
	transaction  *store.CommandTx
	selected     store.ProgramItem
	state        results.ResultItemStageState
	presentation store.PrizegivingPresentationRun
}

func persistResultAction(
	ctx context.Context,
	actor auth.Account,
	persistence resultPersistence,
) (store.ProgramChannelState, error) {
	input := persistence.input
	if input.Action == ResultSkipFromStage {
		return persistence.transaction.SkipPrizegivingResultFromStage(
			actor.Context(ctx),
			store.SkipPrizegivingResultFromStageParams{
				EventID: input.EventID, SessionID: input.SessionID,
				ExpectedRevision: input.ExpectedProgramRevision,
				Item:             persistence.selected,
				State:            prizegivingStageState(persistence.state),
			},
		)
	}
	return persistence.transaction.ApplyPrizegivingResultAction(
		actor.Context(ctx),
		store.PrizegivingResultActionParams{
			EventID: input.EventID, SessionID: input.SessionID,
			ExpectedRevision: input.ExpectedProgramRevision,
			Item:             persistence.selected,
			State:            prizegivingStageState(persistence.state),
			Presentation:     persistence.presentation, ObservedAt: persistence.now,
		},
	)
}

func unresolvedResultInOutput(item store.ProgramItem) bool {
	return item.Kind == store.ProgramItemResult &&
		item.Result != nil &&
		item.Result.Status != prizegivingvalue.StageRevealed &&
		item.Result.Status != prizegivingvalue.StageSkipped
}

// resultTransitionInput is the complete context of one Result stage action.
type resultTransitionInput struct {
	action   ResultAction
	selected store.ProgramItem
	channel  store.ProgramChannelState
	now      time.Time
}

// locked returns the immutable Result truth the action presents.
func (input resultTransitionInput) locked() results.LockedResultItem {
	return lockedResultItem(input.selected)
}

// stage returns the acted item's current presentation state.
func (input resultTransitionInput) stage() results.ResultItemStageState {
	return resultItemStageState(input.selected)
}

// resultTransition is one Result action's stage guard and the presentation
// change it makes once that guard admits it.
type resultTransition struct {
	staged func(resultTransitionInput) bool
	apply  func(resultTransitionInput) (
		results.ResultItemStageState,
		store.PrizegivingPresentationRun,
		error,
	)
}

// actedItemIsOutput admits actions that present what is already on stage.
func actedItemIsOutput(input resultTransitionInput) bool {
	return input.channel.Output.SameIdentity(input.selected)
}

// actedItemIsNext admits actions that omit an item before it reaches stage.
func actedItemIsNext(input resultTransitionInput) bool {
	return input.channel.Next.SameIdentity(input.selected)
}

// resultTransitions is the single source for which Result action is available
// against which stage position.
var resultTransitions = map[ResultAction]resultTransition{
	ResultReveal: {
		staged: actedItemIsOutput,
		apply: func(input resultTransitionInput) (
			results.ResultItemStageState,
			store.PrizegivingPresentationRun,
			error,
		) {
			next, presentation, err := results.StartPrizegivingReveal(
				input.locked(), input.stage(), input.now,
			)
			return next, prizegivingPresentationRun(false, presentation), err
		},
	},
	ResultReplayReveal: {
		staged: actedItemIsOutput,
		apply: func(input resultTransitionInput) (
			results.ResultItemStageState,
			store.PrizegivingPresentationRun,
			error,
		) {
			next, presentation, err := results.ReplayPrizegivingReveal(
				input.locked(), input.stage(), input.now,
			)
			return next, prizegivingPresentationRun(true, presentation), err
		},
	},
	ResultSkipToFinal: {
		staged: actedItemIsOutput,
		apply: func(input resultTransitionInput) (
			results.ResultItemStageState,
			store.PrizegivingPresentationRun,
			error,
		) {
			next, err := results.SkipPrizegivingResultToFinal(
				input.locked(), input.stage(), input.now,
			)
			return next, existingPresentationRun(input.channel.Output), err
		},
	},
	ResultSkipFromStage: {
		staged: actedItemIsNext,
		apply: func(input resultTransitionInput) (
			results.ResultItemStageState,
			store.PrizegivingPresentationRun,
			error,
		) {
			next, err := results.SkipPrizegivingResultFromStage(
				input.locked(), input.stage(), input.now,
			)
			return next, store.PrizegivingPresentationRun{}, err
		},
	},
}

func transitionResult(input resultTransitionInput) (
	results.ResultItemStageState,
	store.PrizegivingPresentationRun,
	error,
) {
	transition, available := resultTransitions[input.action]
	if !available || !transition.staged(input) {
		return input.stage(), store.PrizegivingPresentationRun{}, results.ErrResultItemTransition
	}
	return transition.apply(input)
}

func prizegivingPresentationRun(
	replay bool,
	value results.ResultPresentation,
) store.PrizegivingPresentationRun {
	return store.PrizegivingPresentationRun{
		Replay: replay, StartedAt: value.StartedAt, Duration: value.Duration,
	}
}

func existingPresentationRun(
	item store.ProgramItem,
) store.PrizegivingPresentationRun {
	if item.Result == nil {
		return store.PrizegivingPresentationRun{}
	}
	return store.PrizegivingPresentationRun{
		Replay:    item.Result.Replay,
		StartedAt: item.Result.PresentationStartedAt,
		Duration:  item.Result.PresentationDuration,
	}
}

func (service *Service) state(channel store.ProgramChannelState, control controlState) State {
	result := State{
		Channel:         exposedChannel(channel),
		ControlRevision: control.revision,
		Preview:         exposedItem(control.preview),
	}
	if result.Preview.Kind == "" {
		result.Preview = result.Channel.Next
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
		if item.SameIdentity(wanted) {
			return item, true
		}
	}
	return store.ProgramItem{}, false
}

func lockedResultItem(item store.ProgramItem) results.LockedResultItem {
	if item.Result == nil {
		return results.LockedResultItem{}
	}
	return results.LockedResultItem{
		ResultItem: results.ResultItem{
			Kind:                 results.ResultItemKind(item.Result.Ref.Kind),
			CompetitionSessionID: item.Result.Ref.CompetitionSessionID,
			AwardKey:             item.Result.Ref.AwardKey,
			DisplayOrder:         item.Result.Ref.DisplayOrder,
			RevealMethod:         results.RevealMethod(item.Result.RevealMethod),
		},
		RevealSeed: item.Result.RevealSeed,
	}
}

func resultItemStageState(item store.ProgramItem) results.ResultItemStageState {
	return results.ResultItemStageStateFromProgramResult(item.Result)
}

func prizegivingStageState(
	value results.ResultItemStageState,
) store.PrizegivingStageState {
	return store.PrizegivingStageState{
		Ref: store.PrizegivingResultItemRef{
			Kind:                 prizegivingvalue.ItemKind(value.Ref.Kind),
			CompetitionSessionID: value.Ref.CompetitionSessionID,
			AwardKey:             value.Ref.AwardKey,
			DisplayOrder:         value.Ref.DisplayOrder,
		},
		Status:               value.Status,
		Release:              value.Release,
		TakenAt:              value.TakenAt,
		RevealStartedAt:      value.RevealStartedAt,
		RevealDuration:       value.RevealDuration,
		RevealPausedAt:       value.RevealPausedAt,
		RevealPausedDuration: value.RevealPausedDuration,
		RevealCompletedAt:    value.RevealCompletedAt,
		SkippedAt:            value.SkippedAt,
	}
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

// programRejections is the single source for Program control rejection codes in
// both directions, so a replayed receipt restores the same failure the original
// command produced.
var programRejections = command.RejectionTable{
	Rejections: []command.Rejection{
		{Err: ErrOperatorRequired, Code: string(rejectionOperatorRequired)},
		{Err: ErrControlOwned, Code: string(rejectionControlOwned)},
		{Err: ErrControlOwnerRequired, Code: string(rejectionControlOwnerRequired)},
		{Err: ErrTakeoverConfirmation, Code: string(rejectionTakeoverConfirmation)},
		{Err: ErrHandoverUnavailable, Code: string(rejectionHandoverUnavailable)},
		{Err: ErrPreviewItem, Code: string(rejectionPreviewItemInvalid)},
		{Err: ErrProgramRevision, Code: string(rejectionProgramRevision)},
		{Err: ErrControlRevision, Code: string(rejectionControlRevision)},
		{Err: ErrProgramItem, Code: string(rejectionProgramItemInvalid)},
		{Err: store.ErrEntryOrderRevision, Code: string(rejectionEntryOrderRevision)},
		{Err: store.ErrEntryOrderPreviewStale, Code: string(rejectionEntryOrderStale)},
		{Err: ErrEntryRevision, Code: string(rejectionEntryRevision)},
		{Err: ErrEntryDefer, Code: string(rejectionEntryDefer)},
		{Err: ErrResultTransition, Code: string(rejectionResultTransition)},
		{Err: ErrResultRevealRunning, Code: string(rejectionResultRevealRunning)},
	},
	RecordMessage: true,
}

func rejectionError(code, fallback string) error {
	if sentinel := programRejections.Sentinel(code); sentinel != nil {
		return sentinel
	}
	return errors.New(fallback)
}
