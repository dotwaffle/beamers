// Package overrides owns temporary Display Override commands.
package overrides

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/viewer"
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
	Nondurable              bool   `json:"nondurable,omitempty"`
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

	recoveryMu       sync.Mutex
	degradedMu       sync.Mutex
	displaySnapshots map[string]store.DisplaySnapshotState
	degraded         bool
	degradedCause    error
	degradedCurrent  *store.DisplayOverride
	degradedReceipts map[degradedReceiptKey]degradedReceipt
	degradedPending  []degradedCommand
	nextDegradedID   int
}

type degradedReceiptKey struct {
	actorID   int
	commandID string
}

type degradedReceipt struct {
	action      string
	payloadHash string
	outcome     store.DisplayOverride
}

type degradedCommandKind uint8

const (
	degradedActivate degradedCommandKind = iota + 1
	degradedClear
)

type degradedCommand struct {
	kind     degradedCommandKind
	actor    auth.Account
	identity store.CommandIdentity
	outcome  store.DisplayOverride
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
		result.ConfirmationFingerprint = store.DisplayOverridePreviewFingerprint(preview)
	}
	return result, nil
}

// New creates an Override service with explicit dependencies.
func New(ctx context.Context, storage *store.SQLite, now func() time.Time) (*Service, error) {
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
		storage:          storage,
		now:              now,
		displaySnapshots: make(map[string]store.DisplaySnapshotState),
		degradedReceipts: make(map[degradedReceiptKey]degradedReceipt),
		nextDegradedID:   nextDegradedID,
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
	return service.activateDurably(func() (Override, error) {
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
	})
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
	return service.activateDurably(func() (Override, error) {
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
	if !input.Confirmed || !validEmergencyConfirmation(input.ConfirmationMethod) ||
		input.PreviewFingerprint == "" {
		return Override{}, ErrInvalidInput
	}
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
	return service.activateDurably(func() (Override, error) {
		return execute(
			ctx, service, actor, input.EventID, input.CommandID,
			"Activate"+string(kind), string(input.Target.Type), displayTargetID(input.Target), input,
			func(transaction *store.CommandTx, now time.Time) (Override, error) {
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
	if input.EventID <= 0 || input.OverrideID <= 0 || input.ExpectedRevision <= 0 {
		return Override{}, ErrInvalidInput
	}
	if service.isDegraded() {
		return service.clearDegradedEmergency(actor, input)
	}
	cleared, err := execute(
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
	if err == nil || knownOverrideError(err) {
		return cleared, err
	}
	service.markDegraded(err)
	return Override{}, ErrRevision
}

// PrepareEmergencyStorage detects loss of the command evidence boundary before
// authentication or confirmation can expand degraded authority.
func (service *Service) PrepareEmergencyStorage(ctx context.Context) error {
	service.degradedMu.Lock()
	if service.degraded {
		cause := service.degradedCause
		service.degradedMu.Unlock()
		return cause
	}
	service.degradedMu.Unlock()

	err := service.storage.ProbeCommandEvidence(ctx, service.now().UTC())
	if err != nil &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded) {
		service.markDegraded(err)
	}
	return err
}

// DegradedCause returns the storage failure that opened degraded operation.
func (service *Service) DegradedCause() error {
	service.degradedMu.Lock()
	defer service.degradedMu.Unlock()
	return service.degradedCause
}

// ProjectDisplaySnapshot retains healthy Display routing and overlays the
// process-owned Emergency Alert while authoritative storage is unavailable.
func (service *Service) ProjectDisplaySnapshot(
	credentialHash string,
	current store.DisplaySnapshotState,
	loadErr error,
) (store.DisplaySnapshotState, error) {
	if credentialHash == "" {
		return store.DisplaySnapshotState{}, errors.New("display credential hash is required")
	}
	service.degradedMu.Lock()
	defer service.degradedMu.Unlock()

	if loadErr == nil && !service.degraded {
		service.displaySnapshots[credentialHash] = cloneDisplaySnapshot(current)
	} else {
		if !service.degraded {
			return store.DisplaySnapshotState{}, loadErr
		}
		cached, ok := service.displaySnapshots[credentialHash]
		if !ok {
			if loadErr == nil {
				loadErr = errors.New("display snapshot was not validated before storage degraded")
			}
			return store.DisplaySnapshotState{}, loadErr
		}
		current = cloneDisplaySnapshot(cached)
	}
	if service.degradedCurrent == nil {
		return current, nil
	}
	emergency := *service.degradedCurrent
	if !emergency.ClearedAt.IsZero() {
		if current.EmergencyAlert != nil && current.EmergencyAlert.ID == emergency.ID {
			current.EmergencyAlert = nil
		}
		return current, nil
	}
	if degradedTargetMatches(current, emergency.EventID, emergency.Target) {
		current.EmergencyAlert = &emergency
	}
	return current, nil
}

// Recover persists process-owned Emergency Alert state and evidence in original
// command order after authoritative storage becomes writable again.
func (service *Service) Recover(ctx context.Context) (bool, error) {
	service.recoveryMu.Lock()
	defer service.recoveryMu.Unlock()

	if service.isDegraded() {
		if err := service.storage.ProbeCommandEvidence(ctx, service.now().UTC()); err != nil {
			return false, err
		}
	}

	recovered := false
	for {
		service.degradedMu.Lock()
		if len(service.degradedPending) == 0 {
			if recovered {
				service.degraded = false
				service.degradedCause = nil
				service.degradedCurrent = nil
				clear(service.degradedReceipts)
			} else if service.degradedCurrent == nil && len(service.degradedReceipts) == 0 {
				service.degraded = false
				service.degradedCause = nil
			}
			service.degradedMu.Unlock()
			return recovered, nil
		}
		pending := service.degradedPending[0]
		service.degradedMu.Unlock()

		if err := service.persistDegradedCommand(ctx, pending); err != nil {
			return recovered, err
		}
		service.degradedMu.Lock()
		service.degradedPending = service.degradedPending[1:]
		service.degradedMu.Unlock()
		recovered = true
	}
}

func (service *Service) persistDegradedCommand(
	ctx context.Context,
	pending degradedCommand,
) error {
	_, err := command.Execute(
		pending.actor.Context(ctx),
		command.Plan[store.DisplayOverride]{
			Storage:  service.storage,
			Identity: pending.identity,
			Replay: func(outcome string) (store.DisplayOverride, error) {
				var replayed store.DisplayOverride
				err := store.DecodeCommandReceipt(outcome, &replayed)
				return replayed, err
			},
			Apply: func(
				transaction *store.CommandTx,
			) (command.Execution[store.DisplayOverride], error) {
				if _, persistErr := transaction.PersistDegradedEmergencyAlert(
					pending.actor.Context(ctx),
					pending.outcome,
				); persistErr != nil {
					return command.Execution[store.DisplayOverride]{}, persistErr
				}
				encoded, encodeErr := json.Marshal(pending.outcome)
				if encodeErr != nil {
					return command.Execution[store.DisplayOverride]{},
						errors.New("encode recovered Emergency outcome")
				}
				return command.Success(pending.outcome, string(encoded)), nil
			},
		},
	)
	return err
}

func (service *Service) previewDegradedEmergency(
	actor auth.Account,
	input PriorityInput,
	storageErr error,
) (PriorityPreview, error) {
	service.degradedMu.Lock()
	defer service.degradedMu.Unlock()
	preview, err := service.degradedEmergencyPreviewLocked(actor, input)
	if err != nil {
		if errors.Is(err, ErrEventNotActive) && storageErr != nil {
			return PriorityPreview{}, storageErr
		}
		return PriorityPreview{}, err
	}
	service.degraded = true
	if storageErr != nil && service.degradedCause == nil {
		service.degradedCause = storageErr
	}
	return preview, nil
}

func (service *Service) degradedEmergencyPreviewLocked(
	actor auth.Account,
	input PriorityInput,
) (PriorityPreview, error) {
	input.Text = strings.TrimSpace(input.Text)
	if input.EventID <= 0 || input.Text == "" || len(input.Text) > 2000 ||
		!validPriorityTarget(input.Target) {
		return PriorityPreview{}, ErrInvalidInput
	}
	if !canOperateDegradedEmergency(actor, input.EventID, input.Target) {
		return PriorityPreview{}, ErrScopeDenied
	}
	displays, foundEvent := service.resolveDegradedDisplaysLocked(
		input.EventID,
		input.Target,
	)
	if !foundEvent {
		return PriorityPreview{}, ErrEventNotActive
	}
	preview := store.DisplayOverridePreview{
		Kind:           store.DisplayOverrideEmergencyAlert,
		Target:         input.Target,
		TargetGroupKey: degradedTargetKey(input.Target),
		Text:           input.Text,
		Emphasis:       store.StageMessageNormal,
		Presentation:   store.DisplayOverrideReplace,
		UntilCleared:   true,
		Displays:       displays,
	}
	return PriorityPreview{
		Preview: preview,
		ConfirmationFingerprint: command.PayloadHash(
			"nondurable",
			store.DisplayOverridePreviewFingerprint(preview),
		),
		Nondurable: true,
	}, nil
}

func (service *Service) resolveDegradedDisplaysLocked(
	eventID int,
	target Target,
) ([]store.DisplayOverrideResolvedDisplay, bool) {
	byID := make(map[int]store.DisplayOverrideResolvedDisplay)
	foundEvent := false
	for _, snapshot := range service.displaySnapshots {
		if snapshot.ActiveEventID != eventID {
			continue
		}
		foundEvent = true
		if !degradedTargetMatches(snapshot, eventID, target) {
			continue
		}
		byID[snapshot.Display.ID] = store.DisplayOverrideResolvedDisplay{
			ID: snapshot.Display.ID, Name: snapshot.Display.Name, ViewKey: snapshot.ViewKey,
		}
	}
	result := make([]store.DisplayOverrideResolvedDisplay, 0, len(byID))
	for _, display := range byID {
		result = append(result, display)
	}
	sort.Slice(result, func(first, second int) bool {
		return result[first].ID < result[second].ID
	})
	return result, foundEvent
}

func (service *Service) activateDegradedEmergency(
	actor auth.Account,
	input PriorityInput,
) (Override, error) {
	if input.EventID <= 0 || input.DurationSeconds != 0 ||
		input.Presentation != string(store.DisplayOverrideReplace) ||
		!input.UntilCleared || !input.Confirmed ||
		!validEmergencyConfirmation(input.ConfirmationMethod) ||
		input.PreviewFingerprint == "" {
		return Override{}, ErrInvalidInput
	}
	identity, err := service.degradedCommandIdentity(
		actor,
		input.CommandID,
		"ActivateEmergencyAlert",
		string(input.Target.Type),
		displayTargetID(input.Target),
		input,
	)
	if err != nil {
		return Override{}, err
	}
	key := degradedReceiptKey{actorID: actor.ID, commandID: input.CommandID}

	service.degradedMu.Lock()
	defer service.degradedMu.Unlock()
	if replayed, ok, replayErr := service.degradedReplayLocked(key, identity); ok {
		return replayed, replayErr
	}
	preview, err := service.degradedEmergencyPreviewLocked(actor, input)
	if err != nil {
		return Override{}, err
	}
	if preview.ConfirmationFingerprint != input.PreviewFingerprint {
		return Override{}, ErrRevision
	}
	if service.degradedCurrent != nil && service.degradedCurrent.ClearedAt.IsZero() {
		return Override{}, ErrRevision
	}
	if service.nextDegradedID == math.MaxInt {
		return Override{}, errors.New("degraded Emergency Alert identity space is exhausted")
	}
	service.nextDegradedID++
	activated := store.DisplayOverride{
		ID: service.nextDegradedID, EventID: input.EventID,
		TargetGroupKey: degradedTargetKey(input.Target), Target: input.Target,
		Kind: store.DisplayOverrideEmergencyAlert, Presentation: store.DisplayOverrideReplace,
		Text: input.Text, Emphasis: store.StageMessageNormal, UntilCleared: true,
		Revision: 1, CreatedByAccountID: actor.ID, CreatedAt: identity.Now,
		Nondurable: true,
	}
	service.degraded = true
	service.degradedCurrent = cloneDisplayOverride(&activated)
	service.degradedReceipts[key] = degradedReceipt{
		action: identity.Action, payloadHash: identity.PayloadHash, outcome: activated,
	}
	service.degradedPending = append(service.degradedPending, degradedCommand{
		kind: degradedActivate, actor: cloneActor(actor), identity: identity, outcome: activated,
	})
	return activated, nil
}

func (service *Service) clearDegradedEmergency(
	actor auth.Account,
	input ClearInput,
) (Override, error) {
	if input.EventID <= 0 || input.OverrideID <= 0 || input.ExpectedRevision <= 0 ||
		!input.Confirmed || !validEmergencyConfirmation(input.ConfirmationMethod) {
		return Override{}, ErrInvalidInput
	}
	identity, err := service.degradedCommandIdentity(
		actor,
		input.CommandID,
		"ClearDisplayOverride",
		"DisplayOverride",
		strconv.Itoa(input.OverrideID),
		input,
	)
	if err != nil {
		return Override{}, err
	}
	key := degradedReceiptKey{actorID: actor.ID, commandID: input.CommandID}

	service.degradedMu.Lock()
	defer service.degradedMu.Unlock()
	if replayed, ok, replayErr := service.degradedReplayLocked(key, identity); ok {
		return replayed, replayErr
	}
	current := service.degradedCurrent
	if current == nil || current.ID != input.OverrideID {
		current = service.cachedEmergencyLocked(input.EventID, input.OverrideID)
	}
	if current == nil || current.EventID != input.EventID {
		return Override{}, ErrNotFound
	}
	if !current.ClearedAt.IsZero() || current.Revision != input.ExpectedRevision {
		return *current, ErrRevision
	}
	if !canOperateDegradedEmergency(actor, input.EventID, current.Target) {
		return Override{}, ErrScopeDenied
	}
	cleared := *current
	cleared.Revision++
	cleared.ClearedAt = identity.Now
	cleared.Nondurable = true
	service.degraded = true
	service.degradedCurrent = cloneDisplayOverride(&cleared)
	service.degradedReceipts[key] = degradedReceipt{
		action: identity.Action, payloadHash: identity.PayloadHash, outcome: cleared,
	}
	service.degradedPending = append(service.degradedPending, degradedCommand{
		kind: degradedClear, actor: cloneActor(actor), identity: identity, outcome: cleared,
	})
	return cleared, nil
}

func (service *Service) cachedEmergencyLocked(
	eventID int,
	overrideID int,
) *store.DisplayOverride {
	for _, snapshot := range service.displaySnapshots {
		if snapshot.ActiveEventID == eventID && snapshot.EmergencyAlert != nil &&
			snapshot.EmergencyAlert.ID == overrideID {
			return cloneDisplayOverride(snapshot.EmergencyAlert)
		}
	}
	return nil
}

func (service *Service) listDegradedEmergency(
	actor auth.Account,
	eventID int,
	storageErr error,
) ([]ActiveOverride, error) {
	service.degradedMu.Lock()
	defer service.degradedMu.Unlock()
	current := service.degradedCurrent
	if current == nil {
		for _, snapshot := range service.displaySnapshots {
			if snapshot.ActiveEventID != eventID || snapshot.EmergencyAlert == nil {
				continue
			}
			if current == nil ||
				snapshot.EmergencyAlert.CreatedAt.After(current.CreatedAt) ||
				snapshot.EmergencyAlert.CreatedAt.Equal(current.CreatedAt) &&
					snapshot.EmergencyAlert.ID > current.ID {
				current = snapshot.EmergencyAlert
			}
		}
	}
	if current == nil {
		if storageErr != nil {
			return nil, storageErr
		}
		return []ActiveOverride{}, nil
	}
	if !canOperateDegradedEmergency(actor, eventID, current.Target) {
		return nil, ErrScopeDenied
	}
	service.degraded = true
	if storageErr != nil && service.degradedCause == nil {
		service.degradedCause = storageErr
	}
	if !current.ClearedAt.IsZero() {
		return []ActiveOverride{}, nil
	}
	displays, _ := service.resolveDegradedDisplaysLocked(eventID, current.Target)
	projected := *current
	projected.Nondurable = true
	return []ActiveOverride{{
		DisplayOverride: projected,
		Displays:        displays,
	}}, nil
}

func (service *Service) degradedReplayLocked(
	key degradedReceiptKey,
	identity store.CommandIdentity,
) (store.DisplayOverride, bool, error) {
	receipt, ok := service.degradedReceipts[key]
	if !ok {
		return store.DisplayOverride{}, false, nil
	}
	if receipt.action != identity.Action || receipt.payloadHash != identity.PayloadHash {
		return store.DisplayOverride{}, true, ErrCommandConflict
	}
	return receipt.outcome, true, nil
}

func (service *Service) degradedCommandIdentity(
	actor auth.Account,
	commandID string,
	action string,
	targetType string,
	targetID string,
	payload any,
) (store.CommandIdentity, error) {
	if err := command.ValidateID(commandID); err != nil {
		return store.CommandIdentity{}, err
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return store.CommandIdentity{}, errors.New("encode degraded Emergency command")
	}
	return store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: commandID,
		PayloadHash: command.PayloadHash(string(encodedPayload)), Action: action,
		TargetType: targetType, TargetID: targetID, Now: service.now().UTC(),
	}, nil
}

func (service *Service) isDegraded() bool {
	service.degradedMu.Lock()
	defer service.degradedMu.Unlock()
	return service.degraded
}

func (service *Service) markDegraded(cause error) {
	service.degradedMu.Lock()
	defer service.degradedMu.Unlock()
	service.degraded = true
	if cause != nil && service.degradedCause == nil {
		service.degradedCause = cause
	}
}

func (service *Service) activateDurably(
	activate func() (Override, error),
) (Override, error) {
	service.degradedMu.Lock()
	defer service.degradedMu.Unlock()
	if service.degraded {
		if service.degradedCause != nil {
			return Override{}, service.degradedCause
		}
		return Override{}, auth.ErrStorageDegraded
	}
	activated, err := activate()
	if err == nil && activated.ID > 0 {
		service.nextDegradedID = max(service.nextDegradedID, activated.ID)
	}
	return activated, err
}

// Degraded reports whether Emergency Alert commands are being retained in
// process memory while durable command evidence is unavailable.
func (service *Service) Degraded() bool {
	return service.isDegraded()
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

func canOperateDegradedEmergency(
	actor auth.Account,
	eventID int,
	target Target,
) bool {
	identity := viewer.Identity{
		AccountID: actor.ID, Administrator: actor.Administrator,
		EventRoles: actor.EventRoles, EventScopes: actor.EventScopes,
	}
	if !identity.HasCapability(eventID, viewer.EmergencyAlert) {
		return false
	}
	if identity.CanProduceEvent(eventID) {
		return true
	}
	if target.Type == store.DisplayOverrideTargetLane {
		return identity.CanOperateLane(eventID, target.ID)
	}
	return identity.CanOperateDisplayGroup(eventID, degradedTargetKey(target))
}

func degradedTargetKey(target Target) string {
	switch target.Type {
	case store.DisplayOverrideTargetEvent:
		return "event"
	case store.DisplayOverrideTargetPublic:
		return "public"
	case store.DisplayOverrideTargetCrew:
		return "crew"
	case store.DisplayOverrideTargetDisplayGroup:
		return target.Key
	case store.DisplayOverrideTargetLocation,
		store.DisplayOverrideTargetLane,
		store.DisplayOverrideTargetProgramChannel,
		store.DisplayOverrideTargetDisplay:
		return strings.ToLower(string(target.Type)) + ":" + strconv.Itoa(target.ID)
	default:
		return ""
	}
}

func degradedTargetMatches(
	snapshot store.DisplaySnapshotState,
	eventID int,
	target Target,
) bool {
	if snapshot.ActiveEventID != eventID || snapshot.Standby || snapshot.LocationID <= 0 {
		return false
	}
	switch target.Type {
	case store.DisplayOverrideTargetEvent:
		return true
	case store.DisplayOverrideTargetPublic:
		return snapshot.ViewKey != "stage-timer"
	case store.DisplayOverrideTargetCrew:
		return snapshot.ViewKey == "stage-timer"
	case store.DisplayOverrideTargetLocation:
		return snapshot.LocationID == target.ID
	case store.DisplayOverrideTargetLane:
		return slices.Contains(snapshot.TargetLaneIDs, target.ID)
	case store.DisplayOverrideTargetProgramChannel:
		return snapshot.ViewKey == "competition-output" &&
			snapshot.ProgramChannelID == target.ID
	case store.DisplayOverrideTargetDisplayGroup:
		return slices.Contains(snapshot.DisplayGroupKeys, target.Key)
	case store.DisplayOverrideTargetDisplay:
		return snapshot.Display.ID == target.ID
	default:
		return false
	}
}

func cloneDisplaySnapshot(source store.DisplaySnapshotState) store.DisplaySnapshotState {
	cloned := source
	cloned.DisplayGroupKeys = slices.Clone(source.DisplayGroupKeys)
	cloned.TargetLaneIDs = slices.Clone(source.TargetLaneIDs)
	cloned.Sessions = slices.Clone(source.Sessions)
	cloned.StageMessage = cloneDisplayOverride(source.StageMessage)
	cloned.TechnicalDifficulties = cloneDisplayOverride(source.TechnicalDifficulties)
	cloned.UrgentNotice = cloneDisplayOverride(source.UrgentNotice)
	cloned.EmergencyAlert = cloneDisplayOverride(source.EmergencyAlert)
	return cloned
}

func cloneDisplayOverride(source *store.DisplayOverride) *store.DisplayOverride {
	if source == nil {
		return nil
	}
	cloned := *source
	return &cloned
}

func cloneActor(source auth.Account) auth.Account {
	cloned := source
	cloned.EventRoles = make(map[int]viewer.Role, len(source.EventRoles))
	maps.Copy(cloned.EventRoles, source.EventRoles)
	cloned.EventScopes = make(map[int]viewer.EventScope, len(source.EventScopes))
	for eventID, scope := range source.EventScopes {
		clonedScope := viewer.EventScope{
			LaneIDs:          make(map[int]struct{}, len(scope.LaneIDs)),
			DisplayGroupKeys: make(map[string]struct{}, len(scope.DisplayGroupKeys)),
			Capabilities:     make(map[viewer.Capability]struct{}, len(scope.Capabilities)),
		}
		for laneID := range scope.LaneIDs {
			clonedScope.LaneIDs[laneID] = struct{}{}
		}
		for key := range scope.DisplayGroupKeys {
			clonedScope.DisplayGroupKeys[key] = struct{}{}
		}
		for capability := range scope.Capabilities {
			clonedScope.Capabilities[capability] = struct{}{}
		}
		cloned.EventScopes[eventID] = clonedScope
	}
	return cloned
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
