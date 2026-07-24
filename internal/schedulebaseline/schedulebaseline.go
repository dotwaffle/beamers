// Package schedulebaseline previews and captures an Event's immutable attendee schedule baseline.
package schedulebaseline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
)

var (
	// ErrProducerRequired means the actor cannot produce the Event.
	ErrProducerRequired = errors.New("producer authority required")
	// ErrEventNotFound means the requested Event does not exist.
	ErrEventNotFound = store.ErrEventNotFound
	// ErrAlreadyCaptured means the Event already has its immutable baseline.
	ErrAlreadyCaptured = store.ErrPublicScheduleBaselineExists
	// ErrStalePreview means current Event routing or Published state differs from Preview.
	ErrStalePreview = errors.New("public schedule baseline preview is stale")
	// ErrInvalidBaseline means one or more Public Sessions lack a valid Forecast Start.
	ErrInvalidBaseline = errors.New("public schedule baseline preview is invalid")
	// ErrNonActiveAcknowledgment means capture needs the exact non-Active Event name.
	ErrNonActiveAcknowledgment = errors.New("non-Active Event acknowledgment is required")
	// ErrCommandConflict means a Command ID was reused for different work.
	ErrCommandConflict = store.ErrCommandConflict
)

// Session is one Public Session and Forecast Start shown for confirmation.
type Session struct {
	ID            int       `json:"id"`
	Title         string    `json:"title"`
	ForecastStart time.Time `json:"forecast_start"`
}

// Finding identifies one Session that prevents baseline capture.
type Finding struct {
	SessionID int    `json:"session_id"`
	Message   string `json:"message"`
}

// Confirmation binds capture to one exact Preview.
type Confirmation struct {
	PublishedRevision int    `json:"published_revision"`
	Fingerprint       string `json:"fingerprint"`
}

// Preview is the complete Producer confirmation model.
type Preview struct {
	EventID                         int          `json:"event_id"`
	EventName                       string       `json:"event_name"`
	Active                          bool         `json:"active"`
	RequiresNonActiveAcknowledgment bool         `json:"requires_non_active_acknowledgment"`
	Confirmation                    Confirmation `json:"confirmation"`
	Sessions                        []Session    `json:"sessions"`
	ValidationFailures              []Finding    `json:"validation_failures,omitempty"`
}

// CaptureInput contains one exact confirmed baseline command.
type CaptureInput struct {
	EventID               int          `json:"event_id"`
	CommandID             string       `json:"command_id"`
	Confirmation          Confirmation `json:"confirmation"`
	AcknowledgedEventName string       `json:"acknowledged_event_name,omitempty"`
}

// CaptureResult is the minimal durable baseline outcome.
type CaptureResult struct {
	EventID           int       `json:"event_id"`
	PublishedRevision int       `json:"published_revision"`
	SessionCount      int       `json:"session_count"`
	CapturedAt        time.Time `json:"captured_at"`
}

// Queries owns revision-bound baseline previews.
type Queries struct {
	storage *store.SQLite
}

// NewQueries creates baseline queries with explicit persistence.
func NewQueries(storage *store.SQLite) (*Queries, error) {
	if storage == nil {
		return nil, errors.New("public schedule baseline storage is required")
	}
	return &Queries{storage: storage}, nil
}

// Preview returns the exact Public Sessions and Event identity requiring confirmation.
func (queries *Queries) Preview(
	ctx context.Context,
	actor auth.Account,
	eventID int,
) (Preview, error) {
	if !actor.CanProduceEvent(eventID) {
		return Preview{}, ErrProducerRequired
	}
	state, err := queries.storage.LoadPublicScheduleBaselineState(actor.Context(ctx), eventID)
	if err != nil {
		return Preview{}, err
	}
	if state.Captured {
		return Preview{}, ErrAlreadyCaptured
	}
	return formPreview(state), nil
}

// Commands owns immutable baseline capture and its durable evidence.
type Commands struct {
	storage *store.SQLite
	now     func() time.Time
}

// NewCommands creates baseline commands with explicit persistence and clock dependencies.
func NewCommands(storage *store.SQLite, now func() time.Time) (*Commands, error) {
	if storage == nil {
		return nil, errors.New("public schedule baseline storage is required")
	}
	if now == nil {
		return nil, errors.New("public schedule baseline clock is required")
	}
	return &Commands{storage: storage, now: now}, nil
}

// Capture atomically records the exact confirmed Public Schedule Baseline.
func (commands *Commands) Capture(
	ctx context.Context,
	actor auth.Account,
	input CaptureInput,
) (CaptureResult, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return CaptureResult{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return CaptureResult{}, errors.New("encode Public Schedule Baseline command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(string(payload)), Action: "CapturePublicScheduleBaseline",
		TargetType: "Event", TargetID: strconv.Itoa(input.EventID), Now: commands.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[CaptureResult]{
		Storage: commands.storage, Identity: identity, Replay: decodeCaptureResult,
		Apply: func(transaction *store.CommandTx) (command.Execution[CaptureResult], error) {
			return commands.applyCapture(ctx, actor, input, identity, transaction)
		},
	})
}

func (commands *Commands) applyCapture(
	ctx context.Context,
	actor auth.Account,
	input CaptureInput,
	identity store.CommandIdentity,
	transaction *store.CommandTx,
) (command.Execution[CaptureResult], error) {
	if err := validateCapture(actor.Context(ctx), actor, input, transaction); err != nil {
		if rejection, rejected := captureRejectionFor(err); rejected {
			return captureRejection(rejection.code, rejection.reason), nil
		}
		return command.Execution[CaptureResult]{}, err
	}
	stored, err := transaction.CapturePublicScheduleBaseline(
		actor.Context(ctx),
		store.PublicScheduleBaselineCaptureParams{
			EventID: input.EventID, ExpectedPublishedRevision: input.Confirmation.PublishedRevision,
			Now: identity.Now,
		},
	)
	if err != nil {
		if rejection, rejected := captureRejectionFor(err); rejected {
			return captureRejection(rejection.code, rejection.reason), nil
		}
		return command.Execution[CaptureResult]{}, err
	}
	return captureSuccess(stored)
}

func validateCapture(
	ctx context.Context,
	actor auth.Account,
	input CaptureInput,
	transaction *store.CommandTx,
) error {
	if !actor.CanProduceEvent(input.EventID) {
		return ErrProducerRequired
	}
	state, err := transaction.LoadPublicScheduleBaselineState(ctx, input.EventID)
	if err != nil {
		return err
	}
	if state.Captured {
		return ErrAlreadyCaptured
	}
	preview := formPreview(state)
	if preview.Confirmation != input.Confirmation {
		return ErrStalePreview
	}
	if len(preview.ValidationFailures) > 0 {
		return ErrInvalidBaseline
	}
	if !preview.Active && input.AcknowledgedEventName != preview.EventName {
		return ErrNonActiveAcknowledgment
	}
	return nil
}

func captureSuccess(
	stored store.PublicScheduleBaselineCaptureResult,
) (command.Execution[CaptureResult], error) {
	result := CaptureResult{
		EventID: stored.EventID, PublishedRevision: stored.PublishedRevision,
		SessionCount: stored.SessionCount, CapturedAt: stored.CapturedAt,
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return command.Execution[CaptureResult]{}, errors.New(
			"encode Public Schedule Baseline outcome",
		)
	}
	return command.Success(result, string(encoded)).WithAudit(store.AuditDetails{
		Note: fmt.Sprintf(
			"source Published Revision %d; %d Sessions",
			result.PublishedRevision,
			result.SessionCount,
		),
	}), nil
}

type captureRejectionDetail struct {
	code   string
	reason error
}

func captureRejectionFor(err error) (captureRejectionDetail, bool) {
	switch {
	case errors.Is(err, ErrProducerRequired):
		return captureRejectionDetail{"producer_required", ErrProducerRequired}, true
	case errors.Is(err, ErrEventNotFound):
		return captureRejectionDetail{"event_not_found", ErrEventNotFound}, true
	case errors.Is(err, ErrAlreadyCaptured):
		return captureRejectionDetail{"already_captured", ErrAlreadyCaptured}, true
	case errors.Is(err, ErrStalePreview),
		errors.Is(err, store.ErrPublicScheduleBaselineRevision):
		return captureRejectionDetail{"stale_preview", ErrStalePreview}, true
	case errors.Is(err, ErrInvalidBaseline),
		errors.Is(err, store.ErrPublicScheduleBaselineInvalid):
		return captureRejectionDetail{"invalid_baseline", ErrInvalidBaseline}, true
	case errors.Is(err, ErrNonActiveAcknowledgment):
		return captureRejectionDetail{
			"non_active_acknowledgment_required",
			ErrNonActiveAcknowledgment,
		}, true
	default:
		return captureRejectionDetail{}, false
	}
}

func formPreview(state store.PublicScheduleBaselineState) Preview {
	result := Preview{
		EventID: state.EventID, EventName: state.EventName, Active: state.Active,
		RequiresNonActiveAcknowledgment: !state.Active,
		Confirmation:                    Confirmation{PublishedRevision: state.PublishedRevision},
		Sessions:                        make([]Session, 0, len(state.Sessions)),
	}
	fingerprint := []string{
		strconv.Itoa(state.EventID),
		state.EventName,
		strconv.FormatBool(state.Active),
		strconv.Itoa(state.PublishedRevision),
	}
	for _, candidate := range state.Sessions {
		result.Sessions = append(result.Sessions, Session{
			ID: candidate.ID, Title: candidate.Title, ForecastStart: candidate.ForecastStart,
		})
		fingerprint = append(
			fingerprint,
			strconv.Itoa(candidate.ID),
			candidate.Title,
			candidate.ForecastStart.UTC().Format(time.RFC3339Nano),
		)
		if candidate.ForecastStart.IsZero() {
			result.ValidationFailures = append(result.ValidationFailures, Finding{
				SessionID: candidate.ID,
				Message:   "Public Session requires a valid Forecast Start",
			})
		}
	}
	result.Confirmation.Fingerprint = command.PayloadHash(fingerprint...)
	return result
}

func captureRejection(code string, reason error) command.Execution[CaptureResult] {
	return command.Reject(
		CaptureResult{},
		store.CommandRejection{Code: code, Message: reason.Error()},
		reason,
	)
}

func decodeCaptureResult(outcome string) (CaptureResult, error) {
	var result CaptureResult
	if err := store.DecodeCommandReceipt(outcome, &result); err != nil {
		return CaptureResult{}, errors.Join(
			errors.New("decode Public Schedule Baseline Command Receipt"),
			err,
		)
	}
	return result, nil
}
