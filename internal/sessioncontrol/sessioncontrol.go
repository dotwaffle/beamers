// Package sessioncontrol owns durable Session progression commands.
package sessioncontrol

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/sessiontarget"
	"github.com/dotwaffle/beamers/internal/store"
)

var (
	// ErrOperatorRequired means the actor lacks baseline live-control authority.
	ErrOperatorRequired = errors.New("operator authority required")
	// ErrSessionNotFound means the target is not a Published Session in the Event.
	ErrSessionNotFound = store.ErrSessionNotFound
	// ErrLiveStateRevisionConflict means the command observed stale Session state.
	ErrLiveStateRevisionConflict = store.ErrLiveStateRevisionConflict
	// ErrSessionLifecycleTransition means the command is invalid for the current lifecycle.
	ErrSessionLifecycleTransition = store.ErrSessionLifecycleTransition
	// ErrEventNotActive means a live command targeted an inactive Event.
	ErrEventNotActive = store.ErrEventNotActive
	// ErrSessionScopeRequired means an Operator lacks one or more Session Lanes.
	ErrSessionScopeRequired = store.ErrSessionScopeRequired
	// ErrCommandConflict means a Command ID was reused for different work.
	ErrCommandConflict = store.ErrCommandConflict
	// ErrLiveDetailConfirmation means a correction was not explicitly confirmed.
	ErrLiveDetailConfirmation = errors.New("live detail correction requires confirmation")
	// ErrLiveDetailFields means a correction selected unsupported or invalid detail fields.
	ErrLiveDetailFields = errors.New("invalid Live Detail Correction fields")
	// ErrTargetPreviewStale means live timing changed after the Operator's preview.
	ErrTargetPreviewStale = store.ErrTargetPreviewStale
	// ErrTargetConfirmation means the Operator did not explicitly confirm the preview.
	ErrTargetConfirmation = store.ErrTargetConfirmation
	// ErrHardBoundaryConfirmation means a Hard Boundary override was not explicitly confirmed.
	ErrHardBoundaryConfirmation = store.ErrHardBoundaryConfirmation
	// ErrPullForwardPreviewStale means timing changed after the Operator's preview.
	ErrPullForwardPreviewStale = store.ErrPullForwardPreviewStale
	// ErrPullForwardConfirmation means Pull Forward lacked explicit confirmation.
	ErrPullForwardConfirmation = store.ErrPullForwardConfirmation
	// ErrPresetNotConfigured means a preset is not part of the Event configuration.
	ErrPresetNotConfigured = sessiontarget.ErrPresetNotConfigured
	// ErrTargetBeforeNow directs the Operator to End Now instead.
	ErrTargetBeforeNow = sessiontarget.ErrTargetBeforeNow
	// ErrNoCountdownTarget means the Session uses Manual End.
	ErrNoCountdownTarget = sessiontarget.ErrNoCountdownTarget
)

// StartInput is one exact Start Session command.
type StartInput struct {
	EventID                   int    `json:"event_id"`
	SessionID                 int    `json:"session_id"`
	CommandID                 string `json:"command_id"`
	ExpectedLiveStateRevision int    `json:"expected_live_state_revision"`
}

// EndInput is one exact End Session command.
type EndInput struct {
	EventID                   int    `json:"event_id"`
	SessionID                 int    `json:"session_id"`
	CommandID                 string `json:"command_id"`
	ExpectedLiveStateRevision int    `json:"expected_live_state_revision"`
}

// TargetAdjustment is one preset or custom signed target change.
type TargetAdjustment struct {
	Duration time.Duration `json:"duration"`
	Preset   bool          `json:"preset"`
}

// PreviewAdjustTargetInput requests a read-only downstream impact decision.
type PreviewAdjustTargetInput struct {
	EventID    int
	SessionID  int
	Adjustment TargetAdjustment
}

// AdjustTargetInput confirms one exact preview.
type AdjustTargetInput struct {
	EventID                   int              `json:"event_id"`
	SessionID                 int              `json:"session_id"`
	CommandID                 string           `json:"command_id"`
	ExpectedLiveStateRevision int              `json:"expected_live_state_revision"`
	Adjustment                TargetAdjustment `json:"adjustment"`
	PreviewFingerprint        string           `json:"preview_fingerprint"`
	Confirmed                 bool             `json:"confirmed"`
	HardBoundaryConfirmed     bool             `json:"hard_boundary_confirmed"`
}

// TargetEffect is one downstream overlap exposed before confirmation.
type TargetEffect struct {
	SessionID             int
	CurrentForecastStart  time.Time
	CurrentForecastEnd    time.Time
	ProposedForecastStart time.Time
	ProposedForecastEnd   time.Time
	CurrentOverlap        time.Duration
	ProposedOverlap       time.Duration
}

// TargetPreview is the complete current Adjust Target decision.
type TargetPreview struct {
	CurrentTarget                    time.Time
	ProposedTarget                   time.Time
	Adjustment                       time.Duration
	Effects                          []TargetEffect
	RequiresHardBoundaryConfirmation bool
	Fingerprint                      string
	ConfiguredPresets                []time.Duration
}

// TargetAdjustmentResult is one committed Forecast target.
type TargetAdjustmentResult struct {
	State       State
	ForecastEnd time.Time
	Adjustment  time.Duration
	AdjustedAt  time.Time
	Changes     []ForecastChange
}

// ForecastChange is one committed Session Forecast interval.
type ForecastChange struct {
	SessionID     int
	ForecastStart time.Time
	ForecastEnd   time.Time
}

// PreviewPullForwardInput requests a read-only early-finish recalculation.
type PreviewPullForwardInput struct {
	EventID   int
	SessionID int
}

// PullForwardInput confirms one exact early-finish preview.
type PullForwardInput struct {
	EventID                   int    `json:"event_id"`
	SessionID                 int    `json:"session_id"`
	CommandID                 string `json:"command_id"`
	ExpectedLiveStateRevision int    `json:"expected_live_state_revision"`
	PreviewFingerprint        string `json:"preview_fingerprint"`
	Confirmed                 bool   `json:"confirmed"`
}

// PullForwardPreview is the complete current early-finish decision.
type PullForwardPreview struct {
	Effects     []TargetEffect
	Changes     []ForecastChange
	Fingerprint string
}

// PullForwardResult is one committed early-finish recalculation.
type PullForwardResult struct {
	State   State
	Changes []ForecastChange
}

// CorrectLiveDetailsInput is one confirmed descriptive correction.
type CorrectLiveDetailsInput struct {
	EventID                   int      `json:"event_id"`
	SessionID                 int      `json:"session_id"`
	CommandID                 string   `json:"command_id"`
	ExpectedLiveStateRevision int      `json:"expected_live_state_revision"`
	Confirmed                 bool     `json:"confirmed"`
	Title                     string   `json:"title,omitempty"`
	Speaker                   string   `json:"speaker,omitempty"`
	PublicDetails             string   `json:"public_details,omitempty"`
	UpdateFields              []string `json:"update_fields"`
}

// Details are the correctable public facts for one Session.
type Details struct {
	Title         string
	Speaker       string
	PublicDetails string
}

// Correction is one committed Live Detail Correction.
type Correction struct {
	State       State
	AmendmentID int
	Details     Details
}

// Amendment is immutable correction evidence for one Run.
type Amendment struct {
	ID            int
	Details       Details
	ChangedFields []string
	CreatedAt     time.Time
}

// RunSnapshot is the immutable Published execution context captured at Start.
type RunSnapshot struct {
	PublishedRevision      int
	Title                  string
	Speaker                string
	Type                   string
	PublicDetails          string
	PlannedStart           time.Time
	PlannedEnd             time.Time
	TimingPolicy           string
	MinimumDurationSeconds int
	StartBoundary          string
	EndBoundary            string
	LaneIDs                []int
	LocationIDs            []int
	TrackIDs               []int
}

// RunHistory exposes one immutable Run Snapshot with later amendments.
type RunHistory struct {
	ID          int
	ActualStart time.Time
	ActualEnd   *time.Time
	Snapshot    RunSnapshot
	Amendments  []Amendment
}

// State is one committed Session lifecycle state.
type State struct {
	SessionID         int
	SessionRunID      int
	Lifecycle         string
	LiveStateRevision int
	ActualStart       time.Time
	ActualEnd         *time.Time
}

// RevisionConflictError returns the current Session state with a stale-command rejection.
type RevisionConflictError struct {
	Current State
}

// Error implements error.
func (err *RevisionConflictError) Error() string {
	return ErrLiveStateRevisionConflict.Error()
}

// Unwrap preserves stable stale-command classification.
func (err *RevisionConflictError) Unwrap() error {
	return ErrLiveStateRevisionConflict
}

// Service owns Session progression command lifecycle.
type Service struct {
	storage *store.SQLite
	now     func() time.Time
}

// New creates a Session control service with explicit persistence and clock dependencies.
func New(storage *store.SQLite, now func() time.Time) (*Service, error) {
	if storage == nil {
		return nil, errors.New("session control storage is required")
	}
	if now == nil {
		return nil, errors.New("session control clock is required")
	}
	return &Service{storage: storage, now: now}, nil
}

// Start creates one immutable Run and advances a Scheduled Session to Live.
func (service *Service) Start(
	ctx context.Context,
	actor auth.Account,
	input StartInput,
) (State, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return State{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return State{}, errors.New("encode Start Session command")
	}
	return service.execute(
		ctx, actor,
		sessionCommand{EventID: input.EventID, SessionID: input.SessionID, CommandID: input.CommandID,
			Action: "StartSession", Payload: string(payload)},
		func(transaction *store.CommandTx, now time.Time) (store.LiveSessionState, error) {
			return transaction.StartSession(
				actor.Context(ctx), input.EventID, input.SessionID, input.ExpectedLiveStateRevision, now,
			)
		},
	)
}

// End records Actual End without moving later Sessions.
func (service *Service) End(
	ctx context.Context,
	actor auth.Account,
	input EndInput,
) (State, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return State{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return State{}, errors.New("encode End Session command")
	}
	return service.execute(
		ctx, actor,
		sessionCommand{EventID: input.EventID, SessionID: input.SessionID, CommandID: input.CommandID,
			Action: "EndSession", Payload: string(payload)},
		func(transaction *store.CommandTx, now time.Time) (store.LiveSessionState, error) {
			return transaction.EndSession(
				actor.Context(ctx), input.EventID, input.SessionID, input.ExpectedLiveStateRevision, now,
			)
		},
	)
}

// PreviewAdjustTarget returns all effects without mutating live state.
func (service *Service) PreviewAdjustTarget(
	ctx context.Context,
	actor auth.Account,
	input PreviewAdjustTargetInput,
) (TargetPreview, error) {
	if !actor.CanOperateEvent(input.EventID) {
		return TargetPreview{}, ErrOperatorRequired
	}
	found, err := service.storage.PreviewSessionTarget(
		actor.Context(ctx), input.EventID, input.SessionID,
		sessiontarget.Adjustment{
			Duration: input.Adjustment.Duration,
			Preset:   input.Adjustment.Preset,
		},
		service.now().UTC(),
	)
	if err != nil {
		return TargetPreview{}, err
	}
	return targetPreview(found), nil
}

// AdjustTarget revalidates and atomically commits a previewed Forecast target.
func (service *Service) AdjustTarget(
	ctx context.Context,
	actor auth.Account,
	input AdjustTargetInput,
) (TargetAdjustmentResult, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return TargetAdjustmentResult{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return TargetAdjustmentResult{}, errors.New("encode Adjust Target command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(string(payload)), Action: "AdjustTarget",
		TargetType: "Session", TargetID: strconv.Itoa(input.SessionID), Now: service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[TargetAdjustmentResult]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (TargetAdjustmentResult, error) {
			var stored store.SessionTargetAdjustment
			if decodeErr := store.DecodeCommandReceipt(outcome, &stored); decodeErr != nil {
				return TargetAdjustmentResult{}, restoreTimingRejection(
					decodeErr, "adjust target unavailable",
				)
			}
			return targetAdjustmentResult(stored), nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[TargetAdjustmentResult], error) {
			if !actor.CanOperateEvent(input.EventID) {
				return timingCommandRejection(
					TargetAdjustmentResult{}, State{}, store.LiveSessionState{},
					"operator_required", ErrOperatorRequired,
				)
			}
			stored, adjustErr := transaction.AdjustSessionTarget(
				actor.Context(ctx),
				store.AdjustSessionTargetParams{
					EventID: input.EventID, SessionID: input.SessionID,
					ExpectedRevision: input.ExpectedLiveStateRevision,
					Adjustment: sessiontarget.Adjustment{
						Duration: input.Adjustment.Duration,
						Preset:   input.Adjustment.Preset,
					},
					PreviewFingerprint: input.PreviewFingerprint,
					Confirmed:          input.Confirmed, HardBoundaryConfirmed: input.HardBoundaryConfirmed,
					Now: identity.Now,
				},
			)
			if adjustErr != nil {
				code, rejected := timingRejectionCode(adjustErr)
				if !rejected {
					return command.Execution[TargetAdjustmentResult]{}, adjustErr
				}
				current := targetAdjustmentResult(stored)
				return timingCommandRejection(
					current, current.State, stored.State, code, adjustErr,
				)
			}
			encoded, encodeErr := json.Marshal(stored)
			if encodeErr != nil {
				return command.Execution[TargetAdjustmentResult]{}, errors.New("encode Adjust Target outcome")
			}
			return command.Success(targetAdjustmentResult(stored), string(encoded)), nil
		},
	})
}

// PreviewPullForward returns eligible later Soft-Boundary movement without mutation.
func (service *Service) PreviewPullForward(
	ctx context.Context,
	actor auth.Account,
	input PreviewPullForwardInput,
) (PullForwardPreview, error) {
	if !actor.CanOperateEvent(input.EventID) {
		return PullForwardPreview{}, ErrOperatorRequired
	}
	found, err := service.storage.PreviewPullForward(
		actor.Context(ctx), input.EventID, input.SessionID,
	)
	if err != nil {
		return PullForwardPreview{}, err
	}
	return pullForwardPreview(found), nil
}

// PullForward revalidates and atomically commits one early-finish preview.
func (service *Service) PullForward(
	ctx context.Context,
	actor auth.Account,
	input PullForwardInput,
) (PullForwardResult, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return PullForwardResult{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return PullForwardResult{}, errors.New("encode Pull Forward command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(string(payload)), Action: "PullForward",
		TargetType: "Session", TargetID: strconv.Itoa(input.SessionID), Now: service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[PullForwardResult]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (PullForwardResult, error) {
			var stored store.PullForwardAdjustment
			if decodeErr := store.DecodeCommandReceipt(outcome, &stored); decodeErr != nil {
				return PullForwardResult{}, restoreTimingRejection(
					decodeErr, "pull forward unavailable",
				)
			}
			return pullForwardResult(stored), nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[PullForwardResult], error) {
			if !actor.CanOperateEvent(input.EventID) {
				return timingCommandRejection(
					PullForwardResult{}, State{}, store.LiveSessionState{},
					"operator_required", ErrOperatorRequired,
				)
			}
			stored, pullErr := transaction.PullForward(actor.Context(ctx), store.PullForwardParams{
				EventID: input.EventID, SessionID: input.SessionID,
				ExpectedRevision:   input.ExpectedLiveStateRevision,
				PreviewFingerprint: input.PreviewFingerprint, Confirmed: input.Confirmed,
			})
			if pullErr != nil {
				code, rejected := timingRejectionCode(pullErr)
				if !rejected {
					return command.Execution[PullForwardResult]{}, pullErr
				}
				current := pullForwardResult(stored)
				return timingCommandRejection(
					current, current.State, stored.State, code, pullErr,
				)
			}
			encoded, encodeErr := json.Marshal(stored)
			if encodeErr != nil {
				return command.Execution[PullForwardResult]{}, errors.New("encode Pull Forward outcome")
			}
			return command.Success(pullForwardResult(stored), string(encoded)), nil
		},
	})
}

// CorrectLiveDetails applies confirmed descriptive facts without rewriting the Run Snapshot.
func (service *Service) CorrectLiveDetails(
	ctx context.Context,
	actor auth.Account,
	input CorrectLiveDetailsInput,
) (Correction, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return Correction{}, err
	}
	if !input.Confirmed {
		return Correction{}, ErrLiveDetailConfirmation
	}
	input.Title = strings.TrimSpace(input.Title)
	input.Speaker = strings.TrimSpace(input.Speaker)
	if err := validateLiveDetailFields(input); err != nil {
		return Correction{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return Correction{}, errors.New("encode Live Detail Correction command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(string(payload)), Action: "CorrectLiveDetails",
		TargetType: "Session", TargetID: strconv.Itoa(input.SessionID), Now: service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[Correction]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (Correction, error) {
			var stored store.LiveDetailCorrection
			if decodeErr := store.DecodeCommandReceipt(outcome, &stored); decodeErr != nil {
				return Correction{}, restoreCorrectionRejection(decodeErr)
			}
			return correction(stored), nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[Correction], error) {
			if !actor.CanOperateEvent(input.EventID) {
				return correctionRejection(Correction{}, store.LiveDetailCorrection{}, "operator_required", ErrOperatorRequired)
			}
			stored, correctionErr := transaction.CorrectLiveDetails(actor.Context(ctx), store.LiveDetailCorrectionParams{
				EventID: input.EventID, SessionID: input.SessionID, ActorAccountID: actor.ID,
				ExpectedRevision: input.ExpectedLiveStateRevision, Fields: input.UpdateFields,
				Details: store.SessionDetails{Title: input.Title, Speaker: input.Speaker, PublicDetails: input.PublicDetails},
				Now:     identity.Now,
			})
			if correctionErr != nil {
				code, rejected := rejectionCode(correctionErr)
				if !rejected {
					return command.Execution[Correction]{}, correctionErr
				}
				return correctionRejection(correction(stored), stored, code, correctionErr)
			}
			encoded, encodeErr := json.Marshal(stored)
			if encodeErr != nil {
				return command.Execution[Correction]{}, errors.New("encode Live Detail Correction outcome")
			}
			return command.Success(correction(stored), string(encoded)), nil
		},
	})
}

// History returns immutable Run history to authorized Event crew.
func (service *Service) History(
	ctx context.Context,
	actor auth.Account,
	eventID int,
	sessionID int,
) ([]RunHistory, error) {
	if _, allowed := actor.EventRoles[eventID]; !allowed {
		return nil, ErrSessionNotFound
	}
	stored, err := service.storage.LoadSessionHistory(actor.Context(ctx), eventID, sessionID)
	if err != nil {
		return nil, err
	}
	result := make([]RunHistory, 0, len(stored))
	for _, run := range stored {
		found := RunHistory{
			ID: run.ID, ActualStart: run.ActualStart, ActualEnd: run.ActualEnd,
			Snapshot: RunSnapshot{
				PublishedRevision: run.Snapshot.PublishedRevision,
				Title:             run.Snapshot.Title, Speaker: run.Snapshot.Speaker, Type: run.Snapshot.Type,
				PublicDetails: run.Snapshot.PublicDetails,
				PlannedStart:  run.Snapshot.PlannedStart, PlannedEnd: run.Snapshot.PlannedEnd,
				TimingPolicy:           run.Snapshot.TimingPolicy,
				MinimumDurationSeconds: run.Snapshot.MinimumDurationSeconds,
				StartBoundary:          run.Snapshot.StartBoundary, EndBoundary: run.Snapshot.EndBoundary,
				LaneIDs: run.Snapshot.LaneIDs, LocationIDs: run.Snapshot.LocationIDs,
				TrackIDs: run.Snapshot.TrackIDs,
			},
		}
		for _, amendment := range run.Amendments {
			found.Amendments = append(found.Amendments, Amendment{
				ID:            amendment.ID,
				Details:       Details{Title: amendment.Details.Title, Speaker: amendment.Details.Speaker, PublicDetails: amendment.Details.PublicDetails},
				ChangedFields: amendment.ChangedFields, CreatedAt: amendment.CreatedAt,
			})
		}
		result = append(result, found)
	}
	return result, nil
}

func validateLiveDetailFields(input CorrectLiveDetailsInput) error {
	if len(input.UpdateFields) == 0 {
		return ErrLiveDetailFields
	}
	seen := make(map[string]struct{}, len(input.UpdateFields))
	for _, field := range input.UpdateFields {
		if _, duplicate := seen[field]; duplicate || !slices.Contains([]string{"title", "speaker", "public_details"}, field) {
			return ErrLiveDetailFields
		}
		seen[field] = struct{}{}
	}
	if slices.Contains(input.UpdateFields, "title") && (input.Title == "" || utf8.RuneCountInString(input.Title) > 200) {
		return ErrLiveDetailFields
	}
	if slices.Contains(input.UpdateFields, "speaker") && utf8.RuneCountInString(input.Speaker) > 200 {
		return ErrLiveDetailFields
	}
	if slices.Contains(input.UpdateFields, "public_details") && utf8.RuneCountInString(input.PublicDetails) > 10000 {
		return ErrLiveDetailFields
	}
	return nil
}

func correction(stored store.LiveDetailCorrection) Correction {
	return Correction{
		State: state(stored.State), AmendmentID: stored.AmendmentID,
		Details: Details{Title: stored.Details.Title, Speaker: stored.Details.Speaker, PublicDetails: stored.Details.PublicDetails},
	}
}

func targetPreview(stored store.SessionTargetPreview) TargetPreview {
	result := TargetPreview{
		CurrentTarget: stored.Result.CurrentTarget, ProposedTarget: stored.Result.ProposedTarget,
		Adjustment:                       stored.Result.Adjustment,
		RequiresHardBoundaryConfirmation: stored.Result.RequiresHardBoundaryConfirmation,
		Fingerprint:                      stored.Result.Fingerprint, ConfiguredPresets: stored.Presets,
	}
	for _, effect := range stored.Result.Effects {
		result.Effects = append(result.Effects, TargetEffect{
			SessionID:             effect.SessionID,
			CurrentForecastStart:  effect.CurrentForecastStart,
			CurrentForecastEnd:    effect.CurrentForecastEnd,
			ProposedForecastStart: effect.ProposedForecastStart,
			ProposedForecastEnd:   effect.ProposedForecastEnd,
			CurrentOverlap:        effect.CurrentOverlap, ProposedOverlap: effect.ProposedOverlap,
		})
	}
	return result
}

func targetAdjustmentResult(stored store.SessionTargetAdjustment) TargetAdjustmentResult {
	result := TargetAdjustmentResult{
		State: state(stored.State), ForecastEnd: stored.ForecastEnd,
		Adjustment: stored.Adjustment, AdjustedAt: stored.AdjustedAt,
	}
	result.Changes = forecastChanges(stored.Changes)
	return result
}

func pullForwardPreview(stored store.PullForwardPreview) PullForwardPreview {
	result := PullForwardPreview{Fingerprint: stored.Result.Fingerprint}
	for _, effect := range stored.Result.Effects {
		result.Effects = append(result.Effects, TargetEffect{
			SessionID:             effect.SessionID,
			CurrentForecastStart:  effect.CurrentForecastStart,
			CurrentForecastEnd:    effect.CurrentForecastEnd,
			ProposedForecastStart: effect.ProposedForecastStart,
			ProposedForecastEnd:   effect.ProposedForecastEnd,
			CurrentOverlap:        effect.CurrentOverlap, ProposedOverlap: effect.ProposedOverlap,
		})
	}
	for _, change := range stored.Result.Changes {
		result.Changes = append(result.Changes, ForecastChange{
			SessionID: change.SessionID, ForecastStart: change.ForecastStart,
			ForecastEnd: change.ForecastEnd,
		})
	}
	return result
}

func pullForwardResult(stored store.PullForwardAdjustment) PullForwardResult {
	return PullForwardResult{
		State: state(stored.State), Changes: forecastChanges(stored.Changes),
	}
}

func forecastChanges(stored []store.ForecastChange) []ForecastChange {
	result := make([]ForecastChange, 0, len(stored))
	for _, change := range stored {
		result = append(result, ForecastChange{
			SessionID: change.SessionID, ForecastStart: change.ForecastStart,
			ForecastEnd: change.ForecastEnd,
		})
	}
	return result
}

func timingCommandRejection[T any](
	current T,
	currentState State,
	storedState store.LiveSessionState,
	code string,
	reason error,
) (command.Execution[T], error) {
	rejection := store.CommandRejection{Code: code, Message: reason.Error()}
	returnErr := reason
	if errors.Is(reason, ErrLiveStateRevisionConflict) {
		encoded, err := json.Marshal(storedState)
		if err != nil {
			return command.Execution[T]{}, errors.Join(
				reason, errors.New("encode stale Session state"),
			)
		}
		rejection.Details = encoded
		returnErr = &RevisionConflictError{Current: currentState}
	}
	return command.Reject(current, rejection, returnErr), nil
}

func timingRejectionCode(err error) (string, bool) {
	for _, rejection := range timingRejections {
		if errors.Is(err, rejection.err) {
			return rejection.code, true
		}
	}
	return "", false
}

func restoreTimingRejection(err error, unavailable string) error {
	var rejected *store.RejectedCommandError
	if !errors.As(err, &rejected) {
		return err
	}
	for _, rejection := range timingRejections {
		if rejected.Rejection.Code == rejection.code {
			if errors.Is(rejection.err, ErrLiveStateRevisionConflict) &&
				len(rejected.Rejection.Details) > 0 {
				var current store.LiveSessionState
				if decodeErr := json.Unmarshal(rejected.Rejection.Details, &current); decodeErr != nil {
					return errors.Join(rejection.err, decodeErr)
				}
				return &RevisionConflictError{Current: state(current)}
			}
			return rejection.err
		}
	}
	return errors.New(unavailable)
}

var timingRejections = []struct {
	err  error
	code string
}{
	{ErrOperatorRequired, "operator_required"},
	{ErrSessionNotFound, "session_not_found"},
	{ErrLiveStateRevisionConflict, "live_state_revision_conflict"},
	{ErrSessionLifecycleTransition, "session_lifecycle_transition"},
	{ErrEventNotActive, "event_not_active"},
	{ErrSessionScopeRequired, "session_scope_required"},
	{ErrTargetPreviewStale, "target_preview_stale"},
	{ErrTargetConfirmation, "target_confirmation_required"},
	{ErrHardBoundaryConfirmation, "hard_boundary_confirmation_required"},
	{ErrPresetNotConfigured, "preset_not_configured"},
	{ErrTargetBeforeNow, "target_before_now"},
	{ErrNoCountdownTarget, "no_countdown_target"},
	{ErrPullForwardPreviewStale, "pull_forward_preview_stale"},
	{ErrPullForwardConfirmation, "pull_forward_confirmation_required"},
}

func correctionRejection(
	current Correction,
	stored store.LiveDetailCorrection,
	code string,
	reason error,
) (command.Execution[Correction], error) {
	rejection := store.CommandRejection{Code: code, Message: reason.Error()}
	returnErr := reason
	if errors.Is(reason, ErrLiveStateRevisionConflict) {
		encoded, err := json.Marshal(stored.State)
		if err != nil {
			return command.Execution[Correction]{}, errors.Join(reason, errors.New("encode stale Session state"))
		}
		rejection.Details = encoded
		returnErr = &RevisionConflictError{Current: current.State}
	}
	return command.Reject(current, rejection, returnErr), nil
}

func restoreCorrectionRejection(err error) error {
	_, restored := restoreRejected(err)
	return restored
}

type sessionCommand struct {
	EventID   int
	SessionID int
	CommandID string
	Action    string
	Payload   string
}

func (service *Service) execute(
	ctx context.Context,
	actor auth.Account,
	input sessionCommand,
	transition func(*store.CommandTx, time.Time) (store.LiveSessionState, error),
) (State, error) {
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(input.Payload), Action: input.Action,
		TargetType: "Session", TargetID: strconv.Itoa(input.SessionID), Now: service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[State]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (State, error) {
			var original store.LiveSessionState
			if err := store.DecodeCommandReceipt(outcome, &original); err != nil {
				return restoreRejected(err)
			}
			return state(original), nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[State], error) {
			if !actor.CanOperateEvent(input.EventID) {
				return sessionRejection(State{}, store.LiveSessionState{}, "operator_required", ErrOperatorRequired)
			}
			stored, transitionErr := transition(transaction, identity.Now)
			if transitionErr != nil {
				code, rejected := rejectionCode(transitionErr)
				if !rejected {
					return command.Execution[State]{}, transitionErr
				}
				return sessionRejection(state(stored), stored, code, transitionErr)
			}
			encoded, err := json.Marshal(stored)
			if err != nil {
				return command.Execution[State]{}, errors.New("encode Session command outcome")
			}
			return command.Success(state(stored), string(encoded)), nil
		},
	})
}

func sessionRejection(
	current State,
	stored store.LiveSessionState,
	code string,
	reason error,
) (command.Execution[State], error) {
	rejection := store.CommandRejection{Code: code, Message: reason.Error()}
	returnErr := reason
	if errors.Is(reason, ErrLiveStateRevisionConflict) {
		encoded, err := json.Marshal(stored)
		if err != nil {
			return command.Execution[State]{}, errors.Join(reason, errors.New("encode stale Session state"))
		}
		rejection.Details = encoded
		returnErr = &RevisionConflictError{Current: current}
	}
	return command.Reject(current, rejection, returnErr), nil
}

func rejectionCode(err error) (string, bool) {
	switch {
	case errors.Is(err, ErrSessionNotFound):
		return "session_not_found", true
	case errors.Is(err, ErrLiveStateRevisionConflict):
		return "live_state_revision_conflict", true
	case errors.Is(err, ErrSessionLifecycleTransition):
		return "session_lifecycle_transition", true
	case errors.Is(err, ErrEventNotActive):
		return "event_not_active", true
	case errors.Is(err, ErrSessionScopeRequired):
		return "session_scope_required", true
	default:
		return "", false
	}
}

func restoreRejected(err error) (State, error) {
	var rejected *store.RejectedCommandError
	if !errors.As(err, &rejected) {
		return State{}, err
	}
	switch rejected.Rejection.Code {
	case "operator_required":
		return State{}, ErrOperatorRequired
	case "session_not_found":
		return State{}, ErrSessionNotFound
	case "live_state_revision_conflict":
		var current store.LiveSessionState
		if len(rejected.Rejection.Details) == 0 {
			return State{}, ErrLiveStateRevisionConflict
		}
		if decodeErr := json.Unmarshal(rejected.Rejection.Details, &current); decodeErr != nil {
			return State{}, errors.New("decode stale Session state")
		}
		found := state(current)
		return found, &RevisionConflictError{Current: found}
	case "session_lifecycle_transition":
		return State{}, ErrSessionLifecycleTransition
	case "event_not_active":
		return State{}, ErrEventNotActive
	case "session_scope_required":
		return State{}, ErrSessionScopeRequired
	default:
		return State{}, errors.New("session command unavailable")
	}
}

func state(stored store.LiveSessionState) State {
	return State{
		SessionID: stored.SessionID, SessionRunID: stored.SessionRunID,
		Lifecycle: stored.Lifecycle, LiveStateRevision: stored.LiveStateRevision,
		ActualStart: stored.ActualStart, ActualEnd: stored.ActualEnd,
	}
}
