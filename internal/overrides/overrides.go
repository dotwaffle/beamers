// Package overrides owns temporary Display Override commands.
package overrides

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
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

// Emphasis is accessible Stage Message emphasis without priority changes.
type Emphasis = store.StageMessageEmphasis

const (
	// Normal is routine information.
	Normal = store.StageMessageNormal
	// Attention requests elevated attention.
	Attention = store.StageMessageAttention
	// Urgent requests immediate attention without emergency semantics.
	Urgent = store.StageMessageUrgent
)

// Preset contains Event-configured Stage Message defaults.
type Preset = store.StageMessagePreset

// StageMessageConfiguration is one Event's Stage Message configuration.
type StageMessageConfiguration = store.StageMessageConfiguration

// Override is one durable activation.
type Override = store.DisplayOverride

// Preview is one currently resolved Override target set.
type Preview = store.DisplayOverridePreview

// ActiveOverride is one active Override with live Display membership.
type ActiveOverride = store.ActiveDisplayOverride

// Target identifies one logical or fixed Override scope.
type Target = store.DisplayOverrideTarget

// TargetType is one logical or fixed Override scope discriminator.
type TargetType = store.DisplayOverrideTargetType

// PriorityInput activates an Urgent Notice or Emergency Alert.
type PriorityInput struct {
	EventID            int    `json:"event_id"`
	Target             Target `json:"target"`
	Text               string `json:"text"`
	Presentation       string `json:"presentation"`
	DurationSeconds    int    `json:"duration_seconds"`
	UntilCleared       bool   `json:"until_cleared"`
	Confirmed          bool   `json:"confirmed"`
	ConfirmationMethod string `json:"confirmation_method"`
	PreviewFingerprint string `json:"preview_fingerprint"`
	CommandID          string `json:"command_id"`
}

// PriorityPreview binds normalized content to the currently resolved Displays.
type PriorityPreview struct {
	Preview
	ConfirmationFingerprint string `json:"confirmation_fingerprint,omitempty"`
}

// ConfigureInput replaces Event Stage Message defaults.
type ConfigureInput struct {
	EventID                int      `json:"event_id"`
	DefaultDurationSeconds int      `json:"default_duration_seconds"`
	Presets                []Preset `json:"presets"`
	ExpectedRevision       int      `json:"expected_revision"`
	CommandID              string   `json:"command_id"`
}

// SendStageMessageInput selects a preset or free-form message.
type SendStageMessageInput struct {
	EventID         int      `json:"event_id"`
	PresetKey       string   `json:"preset_key"`
	Text            string   `json:"text"`
	TargetGroupKey  string   `json:"target_group_key"`
	DurationSeconds int      `json:"duration_seconds"`
	Emphasis        Emphasis `json:"emphasis"`
	UntilCleared    bool     `json:"until_cleared"`
	CommandID       string   `json:"command_id"`
}

// TechnicalDifficultiesInput activates a fullscreen wait message.
type TechnicalDifficultiesInput struct {
	EventID         int    `json:"event_id"`
	TargetGroupKey  string `json:"target_group_key"`
	Text            string `json:"text"`
	DurationSeconds int    `json:"duration_seconds"`
	UntilCleared    bool   `json:"until_cleared"`
	CommandID       string `json:"command_id"`
}

// ClearInput clears one exact Override activation.
type ClearInput struct {
	EventID            int    `json:"event_id"`
	OverrideID         int    `json:"override_id"`
	ExpectedRevision   int    `json:"expected_revision"`
	CommandID          string `json:"command_id"`
	Confirmed          bool   `json:"confirmed"`
	ConfirmationMethod string `json:"confirmation_method"`
}

// Service owns Override commands.
type Service struct {
	storage *store.SQLite
	now     func() time.Time
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
	return service.previewPriority(ctx, actor, input, store.DisplayOverrideEmergencyAlert)
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
		result.ConfirmationFingerprint = store.DisplayOverridePreviewFingerprint(preview)
	}
	return result, nil
}

// New creates an Override service with explicit dependencies.
func New(storage *store.SQLite, now func() time.Time) (*Service, error) {
	if storage == nil {
		return nil, errors.New("override storage is required")
	}
	if now == nil {
		return nil, errors.New("override clock is required")
	}
	return &Service{storage: storage, now: now}, nil
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
	return service.storage.ListActiveDisplayOverrides(
		actor.Context(ctx), eventID, service.now().UTC(),
	)
}

// ConfigureStageMessages replaces Event presets and default duration.
func (service *Service) ConfigureStageMessages(
	ctx context.Context,
	actor auth.Account,
	input ConfigureInput,
) (StageMessageConfiguration, error) {
	if input.EventID <= 0 || input.ExpectedRevision < 0 {
		return StageMessageConfiguration{}, ErrInvalidInput
	}
	return execute(
		ctx, service, actor, input.EventID, input.CommandID,
		"ConfigureStageMessages", "Event", strconv.Itoa(input.EventID), input,
		func(transaction *store.CommandTx, _ time.Time) (StageMessageConfiguration, error) {
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
	if input.EventID <= 0 || input.DurationSeconds < 0 {
		return Override{}, ErrInvalidInput
	}
	targetID := input.TargetGroupKey
	if targetID == "" {
		targetID = "preset:" + input.PresetKey
	}
	return execute(
		ctx, service, actor, input.EventID, input.CommandID,
		"SendStageMessage", "DisplayGroup", targetID, input,
		func(transaction *store.CommandTx, now time.Time) (Override, error) {
			return transaction.ActivateStageMessage(
				actor.Context(ctx),
				store.ActivateStageMessageParams{
					EventID: input.EventID, PresetKey: input.PresetKey, Text: input.Text,
					TargetGroupKey:  input.TargetGroupKey,
					DurationSeconds: input.DurationSeconds, Emphasis: input.Emphasis,
					UntilCleared: input.UntilCleared, Now: now,
				},
			)
		},
	)
}

// ActivateTechnicalDifficulties activates one Replace Override.
func (service *Service) ActivateTechnicalDifficulties(
	ctx context.Context,
	actor auth.Account,
	input TechnicalDifficultiesInput,
) (Override, error) {
	if !validTechnicalDifficultiesInput(input) {
		return Override{}, ErrInvalidInput
	}
	return execute(
		ctx, service, actor, input.EventID, input.CommandID,
		"ActivateTechnicalDifficulties", "DisplayGroup", input.TargetGroupKey, input,
		func(transaction *store.CommandTx, now time.Time) (Override, error) {
			return transaction.ActivateTechnicalDifficulties(
				actor.Context(ctx),
				store.ActivateTechnicalDifficultiesParams{
					EventID: input.EventID, TargetGroupKey: input.TargetGroupKey,
					Text: input.Text, UntilCleared: input.UntilCleared,
					Duration: time.Duration(input.DurationSeconds) * time.Second, Now: now,
				},
			)
		},
	)
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
	if !input.Confirmed || !validEmergencyConfirmation(input.ConfirmationMethod) ||
		input.PreviewFingerprint == "" {
		return Override{}, ErrInvalidInput
	}
	return service.activatePriority(ctx, actor, input, store.DisplayOverrideEmergencyAlert)
}

func validEmergencyConfirmation(method string) bool {
	return method == "Keyboard" || method == "TwoSecondHold"
}

func (service *Service) activatePriority(
	ctx context.Context,
	actor auth.Account,
	input PriorityInput,
	kind store.DisplayOverrideKind,
) (Override, error) {
	if input.EventID <= 0 || input.DurationSeconds < 0 ||
		input.DurationSeconds > 24*60*60 {
		return Override{}, ErrInvalidInput
	}
	return execute(
		ctx, service, actor, input.EventID, input.CommandID,
		"Activate"+string(kind), string(input.Target.Type), displayTargetID(input.Target), input,
		func(transaction *store.CommandTx, now time.Time) (Override, error) {
			return transaction.ActivatePriorityOverride(
				actor.Context(ctx), priorityParams(input, kind, now),
			)
		},
	)
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
	if input.EventID <= 0 || input.OverrideID <= 0 || input.ExpectedRevision <= 0 {
		return Override{}, ErrInvalidInput
	}
	return execute(
		ctx, service, actor, input.EventID, input.CommandID,
		"ClearDisplayOverride", "DisplayOverride", strconv.Itoa(input.OverrideID), input,
		func(transaction *store.CommandTx, now time.Time) (Override, error) {
			return transaction.ClearDisplayOverride(
				actor.Context(ctx), input.EventID, input.OverrideID,
				input.ExpectedRevision, now,
				input.Confirmed && validEmergencyConfirmation(input.ConfirmationMethod),
			)
		},
	)
}

func execute[T any](
	ctx context.Context,
	service *Service,
	actor auth.Account,
	eventID int,
	commandID, action, targetType, targetID string,
	payload any,
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
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (T, error) {
			var replayed T
			err := store.DecodeCommandReceipt(outcome, &replayed)
			return replayed, err
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[T], error) {
			result, applyErr := apply(transaction, identity.Now)
			if applyErr != nil {
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
