// Package overrides owns temporary Display Override commands.
package overrides

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/authz"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
)

var (
	// ErrProducerRequired means configuration lacked Producer authority.
	ErrProducerRequired = errors.New("producer authority required")
	// ErrScopeDenied means the actor cannot operate the target Display Group.
	ErrScopeDenied = store.ErrDisplayOverrideScope
	// ErrInvalidInput means an Override command is malformed.
	ErrInvalidInput = store.ErrDisplayOverrideInput
	// ErrNotFound hides unknown and cross-Event Overrides.
	ErrNotFound = store.ErrDisplayOverrideNotFound
	// ErrRevision means an Override changed after observation.
	ErrRevision = store.ErrDisplayOverrideRevision
	// ErrConfigurationRevision means Stage Message configuration changed after observation.
	ErrConfigurationRevision = store.ErrStageMessageConfigurationRevision
	// ErrCommandConflict means a command ID was reused for different work.
	ErrCommandConflict = store.ErrCommandConflict
	// ErrEventNotActive means the command targeted an inactive Event.
	ErrEventNotActive = store.ErrEventNotActive
)

// Service owns Override commands.
type Service struct {
	storage        *store.SQLite
	now            func() time.Time
	notifyDisplays func()

	recoveryMu        sync.Mutex
	degradedMu        sync.Mutex
	displaySnapshots  map[string]store.DisplaySnapshotState
	degraded          bool
	degradedCause     error
	degradedCurrent   *store.DisplayOverride
	degradedReceipts  map[degradedReceiptKey]degradedReceipt
	degradedConflicts map[degradedConflictKey]struct{}
	degradedPending   []degradedCommand
	nextDegradedID    int
}

// PreviewStageMessage resolves content and Displays without activation.
func (service *Service) PreviewStageMessage(
	ctx context.Context,
	actor auth.Account,
	input SendStageMessageInput,
) (Preview, error) {
	if input.EventID <= 0 || input.DurationSeconds < 0 {
		return Preview{}, ErrInvalidInput
	}
	return service.storage.PreviewStageMessage(actor.Context(ctx), store.ActivateStageMessageParams{
		EventID: input.EventID, PresetKey: input.PresetKey, Text: input.Text,
		TargetGroupKey: input.TargetGroupKey, DurationSeconds: input.DurationSeconds,
		Emphasis: input.Emphasis, UntilCleared: input.UntilCleared, Now: service.now().UTC(),
	})
}

// PreviewTechnicalDifficulties resolves Displays without activation.
func (service *Service) PreviewTechnicalDifficulties(
	ctx context.Context,
	actor auth.Account,
	input TechnicalDifficultiesInput,
) (Preview, error) {
	if !validTechnicalDifficultiesInput(input) {
		return Preview{}, ErrInvalidInput
	}
	return service.storage.PreviewTechnicalDifficulties(
		actor.Context(ctx),
		store.ActivateTechnicalDifficultiesParams{
			EventID: input.EventID, TargetGroupKey: input.TargetGroupKey, Text: input.Text,
			UntilCleared: input.UntilCleared,
			Duration:     time.Duration(input.DurationSeconds) * time.Second,
			Now:          service.now().UTC(),
		},
	)
}

// PreviewUrgentNotice resolves an Urgent Notice without activation.
func (service *Service) PreviewUrgentNotice(
	ctx context.Context,
	actor auth.Account,
	input PriorityInput,
) (PriorityPreview, error) {
	return service.previewPriority(ctx, actor, input, store.DisplayOverrideUrgentNotice)
}

// PreviewEmergencyAlert resolves and binds an Emergency Alert confirmation.
func (service *Service) PreviewEmergencyAlert(
	ctx context.Context,
	actor auth.Account,
	input PriorityInput,
) (PriorityPreview, error) {
	input.Presentation = string(store.DisplayOverrideReplace)
	input.UntilCleared = true
	input.DurationSeconds = 0
	if service.isDegraded() {
		return service.previewDegradedEmergency(actor, input, nil)
	}
	preview, err := service.previewPriority(
		ctx,
		actor,
		input,
		store.DisplayOverrideEmergencyAlert,
	)
	if err == nil || knownOverrideError(err) {
		return preview, err
	}
	return service.previewDegradedEmergency(actor, input, err)
}

func (service *Service) previewPriority(
	ctx context.Context,
	actor auth.Account,
	input PriorityInput,
	kind store.DisplayOverrideKind,
) (PriorityPreview, error) {
	if input.EventID <= 0 || input.DurationSeconds < 0 ||
		input.DurationSeconds > 24*60*60 {
		return PriorityPreview{}, ErrInvalidInput
	}
	preview, err := service.storage.PreviewPriorityOverride(
		actor.Context(ctx),
		priorityParams(input, kind, service.now().UTC()),
	)
	if err != nil {
		return PriorityPreview{}, err
	}
	result := PriorityPreview{Preview: preview}
	if kind == store.DisplayOverrideEmergencyAlert {
		fingerprint, fingerprintErr := store.DisplayOverridePreviewFingerprint(preview)
		if fingerprintErr != nil {
			return PriorityPreview{}, fingerprintErr
		}
		result.ConfirmationFingerprint = fingerprint
	}
	return result, nil
}

// New creates an Override service with explicit dependencies.
func New(
	ctx context.Context,
	storage *store.SQLite,
	now func() time.Time,
	notifyDisplays func(),
) (*Service, error) {
	if storage == nil {
		return nil, errors.New("override storage is required")
	}
	if now == nil {
		return nil, errors.New("override clock is required")
	}
	nextDegradedID, err := storage.DegradedEmergencyIDFloor(ctx)
	if err != nil {
		return nil, err
	}
	if nextDegradedID == math.MaxInt {
		return nil, errors.New("degraded Emergency Alert identity space is exhausted")
	}
	return &Service{
		storage:           storage,
		now:               now,
		notifyDisplays:    notifyDisplays,
		displaySnapshots:  make(map[string]store.DisplaySnapshotState),
		degradedReceipts:  make(map[degradedReceiptKey]degradedReceipt),
		degradedConflicts: make(map[degradedConflictKey]struct{}),
		nextDegradedID:    nextDegradedID,
	}, nil
}

// ListActive returns current Overrides with continuously resolved Displays.
func (service *Service) ListActive(
	ctx context.Context,
	actor auth.Account,
	eventID int,
) ([]ActiveOverride, error) {
	if eventID <= 0 {
		return nil, ErrInvalidInput
	}
	if service.isDegraded() {
		return service.listDegradedEmergency(actor, eventID, nil)
	}
	active, err := service.storage.ListActiveDisplayOverrides(
		actor.Context(ctx), eventID, service.now().UTC(),
	)
	if err == nil || knownOverrideError(err) {
		return active, err
	}
	return service.listDegradedEmergency(actor, eventID, err)
}

// ConfigureStageMessages replaces Event presets and default duration.
func (service *Service) ConfigureStageMessages(
	ctx context.Context,
	actor auth.Account,
	input ConfigureInput,
) (StageMessageConfiguration, error) {
	if input.EventID <= 0 {
		return StageMessageConfiguration{}, ErrInvalidInput
	}
	validationErr := invalidInputError(input.ExpectedRevision < 0)
	if rejectionLacksCommandIdentity(input.CommandID, validationErr) {
		return StageMessageConfiguration{}, validationErr
	}
	return execute(
		ctx, service, actor, input.EventID, input.CommandID,
		"ConfigureStageMessages", "Event", strconv.Itoa(input.EventID), input,
		func(context.Context, *store.CommandTx) (authz.Facts, error) {
			if validationErr != nil {
				return authz.Facts{}, validationErr
			}
			return authz.Event(input.EventID), nil
		},
		func(transaction *store.CommandTx, _ time.Time) (StageMessageConfiguration, error) {
			if validationErr != nil {
				return StageMessageConfiguration{}, validationErr
			}
			if !actor.CanProduceEvent(input.EventID) {
				return StageMessageConfiguration{}, ErrProducerRequired
			}
			return transaction.ConfigureStageMessages(
				actor.Context(ctx),
				store.ConfigureStageMessagesParams{
					EventID: input.EventID, ExpectedRevision: input.ExpectedRevision,
					DefaultDurationSeconds: input.DefaultDurationSeconds,
					Presets:                input.Presets,
				},
			)
		},
	)
}

// SendStageMessage activates one crew-only Overlay.
func (service *Service) SendStageMessage(
	ctx context.Context,
	actor auth.Account,
	input SendStageMessageInput,
) (Override, error) {
	validationErr := invalidInputError(input.EventID <= 0 || input.DurationSeconds < 0)
	if rejectionLacksCommandIdentity(input.CommandID, validationErr) {
		return Override{}, validationErr
	}
	targetID := input.TargetGroupKey
	if targetID == "" {
		targetID = "preset:" + input.PresetKey
	}
	params := store.ActivateStageMessageParams{
		EventID: input.EventID, PresetKey: input.PresetKey, Text: input.Text,
		TargetGroupKey:  input.TargetGroupKey,
		DurationSeconds: input.DurationSeconds, Emphasis: input.Emphasis,
		UntilCleared: input.UntilCleared,
	}
	return service.activateDurably(func() (Override, error) {
		return execute(
			ctx, service, actor, input.EventID, input.CommandID,
			"SendStageMessage", "DisplayGroup", targetID, input,
			func(ctx context.Context, transaction *store.CommandTx) (authz.Facts, error) {
				if validationErr != nil {
					return authz.Facts{}, validationErr
				}
				return transaction.StageMessageScope(ctx, params)
			},
			func(transaction *store.CommandTx, now time.Time) (Override, error) {
				if validationErr != nil {
					return Override{}, validationErr
				}
				activation := params
				activation.Now = now
				return transaction.ActivateStageMessage(actor.Context(ctx), activation)
			},
		)
	})
}

// ActivateTechnicalDifficulties activates one Replace Override.
func (service *Service) ActivateTechnicalDifficulties(
	ctx context.Context,
	actor auth.Account,
	input TechnicalDifficultiesInput,
) (Override, error) {
	validationErr := invalidInputError(!validTechnicalDifficultiesInput(input))
	if rejectionLacksCommandIdentity(input.CommandID, validationErr) {
		return Override{}, validationErr
	}
	targetID := input.TargetGroupKey
	if targetID == "" {
		targetID = "unresolved"
	}
	params := store.ActivateTechnicalDifficultiesParams{
		EventID: input.EventID, TargetGroupKey: input.TargetGroupKey,
		Text: input.Text, UntilCleared: input.UntilCleared,
		Duration: time.Duration(input.DurationSeconds) * time.Second,
	}
	return service.activateDurably(func() (Override, error) {
		return execute(
			ctx, service, actor, input.EventID, input.CommandID,
			"ActivateTechnicalDifficulties", "DisplayGroup", targetID, input,
			func(ctx context.Context, transaction *store.CommandTx) (authz.Facts, error) {
				if validationErr != nil {
					return authz.Facts{}, validationErr
				}
				return transaction.TechnicalDifficultiesScope(ctx, params)
			},
			func(transaction *store.CommandTx, now time.Time) (Override, error) {
				if validationErr != nil {
					return Override{}, validationErr
				}
				activation := params
				activation.Now = now
				return transaction.ActivateTechnicalDifficulties(actor.Context(ctx), activation)
			},
		)
	})
}

// ActivateUrgentNotice activates one operational Override.
func (service *Service) ActivateUrgentNotice(
	ctx context.Context,
	actor auth.Account,
	input PriorityInput,
) (Override, error) {
	return service.activatePriority(ctx, actor, input, store.DisplayOverrideUrgentNotice)
}

// ActivateEmergencyAlert activates one confirmed highest-priority Override.
func (service *Service) ActivateEmergencyAlert(
	ctx context.Context,
	actor auth.Account,
	input PriorityInput,
) (Override, error) {
	input.Presentation = string(store.DisplayOverrideReplace)
	input.UntilCleared = true
	input.DurationSeconds = 0
	if service.isDegraded() {
		return service.activateDegradedEmergency(actor, input)
	}
	activated, err := service.activatePriority(
		ctx,
		actor,
		input,
		store.DisplayOverrideEmergencyAlert,
	)
	if err == nil || knownOverrideError(err) {
		return activated, err
	}
	service.markDegraded(err)
	return Override{}, ErrRevision
}

func validEmergencyConfirmation(method string) bool {
	return method == "Keyboard" || method == "TwoSecondHold"
}

func validEmergencyInput(input PriorityInput) bool {
	return input.Confirmed &&
		validEmergencyConfirmation(input.ConfirmationMethod) &&
		input.PreviewFingerprint != ""
}

func invalidInputError(invalid bool) error {
	if invalid {
		return ErrInvalidInput
	}
	return nil
}

func rejectionLacksCommandIdentity(commandID string, rejection error) bool {
	return rejection != nil && command.ValidateID(commandID) != nil
}

func (service *Service) activatePriority(
	ctx context.Context,
	actor auth.Account,
	input PriorityInput,
	kind store.DisplayOverrideKind,
) (Override, error) {
	validationErr := invalidInputError(
		input.EventID <= 0 ||
			input.DurationSeconds < 0 ||
			input.DurationSeconds > 24*60*60 ||
			kind == store.DisplayOverrideEmergencyAlert && !validEmergencyInput(input),
	)
	if rejectionLacksCommandIdentity(input.CommandID, validationErr) {
		return Override{}, validationErr
	}
	return service.activateDurably(func() (Override, error) {
		return execute(
			ctx, service, actor, input.EventID, input.CommandID,
			"Activate"+string(kind), string(input.Target.Type), displayTargetID(input.Target), input,
			func(ctx context.Context, transaction *store.CommandTx) (authz.Facts, error) {
				if validationErr != nil {
					return authz.Facts{}, validationErr
				}
				return transaction.PriorityOverrideScope(
					ctx, priorityParams(input, kind, time.Time{}),
				)
			},
			func(transaction *store.CommandTx, now time.Time) (Override, error) {
				if validationErr != nil {
					return Override{}, validationErr
				}
				return transaction.ActivatePriorityOverride(
					actor.Context(ctx), priorityParams(input, kind, now),
				)
			},
		)
	})
}

func priorityParams(
	input PriorityInput,
	kind store.DisplayOverrideKind,
	now time.Time,
) store.ActivatePriorityOverrideParams {
	return store.ActivatePriorityOverrideParams{
		EventID: input.EventID, Target: input.Target, Kind: kind,
		Presentation: store.DisplayOverridePresentation(input.Presentation),
		Text:         input.Text, UntilCleared: input.UntilCleared,
		Duration: time.Duration(input.DurationSeconds) * time.Second, Now: now,
		ConfirmationFingerprint: input.PreviewFingerprint,
	}
}

func displayTargetID(target Target) string {
	if target.Key != "" {
		return target.Key
	}
	return strconv.Itoa(target.ID)
}

func validTechnicalDifficultiesInput(input TechnicalDifficultiesInput) bool {
	return input.EventID > 0 &&
		input.DurationSeconds >= 0 &&
		input.DurationSeconds <= 24*60*60 &&
		(input.UntilCleared || input.DurationSeconds > 0)
}

// Clear clears one Override without changing its underlying View or Session.
func (service *Service) Clear(
	ctx context.Context,
	actor auth.Account,
	input ClearInput,
) (Override, error) {
	validationErr := invalidInputError(
		input.EventID <= 0 || input.OverrideID <= 0 || input.ExpectedRevision <= 0,
	)
	if service.isDegraded() {
		return service.clearDegradedEmergency(actor, input)
	}
	if rejectionLacksCommandIdentity(input.CommandID, validationErr) {
		return Override{}, validationErr
	}
	cleared, err := execute(
		ctx, service, actor, input.EventID, input.CommandID,
		"ClearDisplayOverride", "DisplayOverride", strconv.Itoa(input.OverrideID), input,
		func(ctx context.Context, transaction *store.CommandTx) (authz.Facts, error) {
			if validationErr != nil {
				return authz.Facts{}, validationErr
			}
			return transaction.ClearDisplayOverrideScope(ctx, input.EventID, input.OverrideID)
		},
		func(transaction *store.CommandTx, now time.Time) (Override, error) {
			if validationErr != nil {
				return Override{}, validationErr
			}
			return transaction.ClearDisplayOverride(
				actor.Context(ctx), input.EventID, input.OverrideID,
				input.ExpectedRevision, now,
				input.Confirmed && validEmergencyConfirmation(input.ConfirmationMethod),
			)
		},
	)
	if err == nil || knownOverrideError(err) {
		return cleared, err
	}
	service.markDegraded(err)
	return Override{}, ErrRevision
}

func knownOverrideError(err error) bool {
	return errors.Is(err, ErrProducerRequired) ||
		errors.Is(err, ErrScopeDenied) ||
		errors.Is(err, ErrInvalidInput) ||
		errors.Is(err, ErrNotFound) ||
		errors.Is(err, ErrRevision) ||
		errors.Is(err, ErrConfigurationRevision) ||
		errors.Is(err, ErrCommandConflict) ||
		errors.Is(err, ErrEventNotActive) ||
		errors.Is(err, command.ErrInvalidID) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

func validPriorityTarget(target Target) bool {
	switch target.Type {
	case store.DisplayOverrideTargetEvent,
		store.DisplayOverrideTargetPublic,
		store.DisplayOverrideTargetCrew:
		return target.ID == 0 && target.Key == ""
	case store.DisplayOverrideTargetLocation,
		store.DisplayOverrideTargetLane,
		store.DisplayOverrideTargetProgramChannel,
		store.DisplayOverrideTargetDisplay:
		return target.ID > 0 && target.Key == ""
	case store.DisplayOverrideTargetDisplayGroup:
		return target.ID == 0 && strings.TrimSpace(target.Key) != ""
	default:
		return false
	}
}

// execute runs one Display Override command. scope resolves the target the
// Capability Table judges, inside the transaction and before the application
// runs. It returns the command's validation error where the application would
// have returned it first, so judging the target early cannot reorder a
// validation refusal behind a scope refusal.
func execute[T any](
	ctx context.Context,
	service *Service,
	actor auth.Account,
	eventID int,
	commandID, action, targetType, targetID string,
	payload any,
	scope func(context.Context, *store.CommandTx) (authz.Facts, error),
	apply func(*store.CommandTx, time.Time) (T, error),
) (T, error) {
	var zero T
	if eventID <= 0 {
		return zero, ErrInvalidInput
	}
	if err := command.ValidateID(commandID); err != nil {
		return zero, err
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return zero, errors.New("encode Display Override command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: commandID,
		PayloadHash: command.PayloadHash(string(encodedPayload)), Action: action,
		TargetType: targetType, TargetID: targetID, Now: service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[T]{
		Storage: service.storage, Identity: identity, Notify: service.notifyDisplays,
		Authorization: command.Authorization{LoadFacts: scope, Refusals: overrideRejections},
		Replay: func(outcome string) (T, error) {
			var replayed T
			if decodeErr := store.DecodeCommandReceipt(outcome, &replayed); decodeErr != nil {
				return replayed, restoreOverrideRejection(decodeErr)
			}
			return replayed, nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[T], error) {
			result, applyErr := apply(transaction, identity.Now)
			if applyErr != nil {
				if rejection, rejected := overrideRejection(applyErr); rejected {
					return command.Reject(result, rejection, applyErr), nil
				}
				return command.Execution[T]{}, applyErr
			}
			outcome, marshalErr := json.Marshal(result)
			if marshalErr != nil {
				return command.Execution[T]{}, errors.New("encode Display Override outcome")
			}
			return command.Success(result, string(outcome)), nil
		},
	})
}

// overrideRejections is the single source for Display Override rejection codes
// in both directions.
var overrideRejections = command.RejectionTable{
	Rejections: []command.Rejection{
		{Err: ErrProducerRequired, Code: "producer_required"},
		{Err: ErrScopeDenied, Code: "override_scope_denied"},
		{Err: ErrInvalidInput, Code: "override_invalid_input"},
		{Err: ErrNotFound, Code: "override_not_found"},
		{Err: ErrRevision, Code: "override_revision_conflict"},
		{
			Err:  ErrConfigurationRevision,
			Code: "stage_message_configuration_revision_conflict",
		},
		{Err: ErrEventNotActive, Code: "event_not_active"},
	},
	RecordMessage: true,
}

func overrideRejection(err error) (store.CommandRejection, bool) {
	return overrideRejections.Rejection(err)
}

func restoreOverrideRejection(err error) error {
	var rejected *store.RejectedCommandError
	if !errors.As(err, &rejected) {
		return err
	}
	return overrideRejectionError(rejected.Rejection.Code)
}

func overrideRejectionError(code string) error {
	if sentinel := overrideRejections.Sentinel(code); sentinel != nil {
		return sentinel
	}
	return errors.New("display Override command unavailable")
}
