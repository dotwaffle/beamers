// Package sessioncontrol owns durable Session progression commands.
package sessioncontrol

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
	// ErrCommandConflict means a Command ID was reused for different work.
	ErrCommandConflict = store.ErrCommandConflict
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
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(string(payload)), Action: "StartSession",
		TargetType: "Session", TargetID: strconv.Itoa(input.SessionID), Now: service.now().UTC(),
	}
	transaction, err := service.storage.BeginCommand(actor.Context(ctx))
	if err != nil {
		return State{}, err
	}
	defer func() { _ = transaction.Rollback() }()
	outcome, retry, err := transaction.LookupReceipt(ctx, identity)
	if errors.Is(err, ErrCommandConflict) {
		if commitErr := transaction.CommitConflict(actor.Context(ctx), identity); commitErr != nil {
			return State{}, commitErr
		}
		return State{}, ErrCommandConflict
	}
	if err != nil {
		return State{}, err
	}
	if retry {
		var original store.LiveSessionState
		if decodeErr := store.DecodeCommandReceipt(outcome, &original); decodeErr != nil {
			return restoreRejected(decodeErr)
		}
		return state(original), nil
	}
	if !actor.CanOperateEvent(input.EventID) {
		_, rejectionErr := service.reject(ctx, transaction, actor, identity, "operator_required", ErrOperatorRequired)
		return State{}, rejectionErr
	}
	stored, err := transaction.StartSession(
		actor.Context(ctx), input.EventID, input.SessionID,
		input.ExpectedLiveStateRevision, identity.Now,
	)
	if err != nil {
		code, rejected := rejectionCode(err)
		if !rejected {
			return State{}, err
		}
		current := state(stored)
		committed, rejectionErr := service.reject(ctx, transaction, actor, identity, code, err, stored)
		if errors.Is(err, ErrLiveStateRevisionConflict) && committed {
			return current, &RevisionConflictError{Current: current}
		}
		return current, rejectionErr
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		return State{}, errors.New("encode Start Session outcome")
	}
	if err := transaction.RecordOutcome(actor.Context(ctx), identity, string(encoded), false); err != nil {
		return State{}, err
	}
	if err := transaction.Commit(); err != nil {
		return State{}, err
	}
	return state(stored), nil
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
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(string(payload)), Action: "EndSession",
		TargetType: "Session", TargetID: strconv.Itoa(input.SessionID), Now: service.now().UTC(),
	}
	transaction, err := service.storage.BeginCommand(actor.Context(ctx))
	if err != nil {
		return State{}, err
	}
	defer func() { _ = transaction.Rollback() }()
	outcome, retry, err := transaction.LookupReceipt(ctx, identity)
	if errors.Is(err, ErrCommandConflict) {
		if commitErr := transaction.CommitConflict(actor.Context(ctx), identity); commitErr != nil {
			return State{}, commitErr
		}
		return State{}, ErrCommandConflict
	}
	if err != nil {
		return State{}, err
	}
	if retry {
		var original store.LiveSessionState
		if decodeErr := store.DecodeCommandReceipt(outcome, &original); decodeErr != nil {
			return restoreRejected(decodeErr)
		}
		return state(original), nil
	}
	if !actor.CanOperateEvent(input.EventID) {
		_, rejectionErr := service.reject(ctx, transaction, actor, identity, "operator_required", ErrOperatorRequired)
		return State{}, rejectionErr
	}
	stored, err := transaction.EndSession(
		actor.Context(ctx), input.EventID, input.SessionID,
		input.ExpectedLiveStateRevision, identity.Now,
	)
	if err != nil {
		code, rejected := rejectionCode(err)
		if !rejected {
			return State{}, err
		}
		current := state(stored)
		committed, rejectionErr := service.reject(ctx, transaction, actor, identity, code, err, stored)
		if errors.Is(err, ErrLiveStateRevisionConflict) && committed {
			return current, &RevisionConflictError{Current: current}
		}
		return current, rejectionErr
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		return State{}, errors.New("encode End Session outcome")
	}
	if err := transaction.RecordOutcome(actor.Context(ctx), identity, string(encoded), false); err != nil {
		return State{}, err
	}
	if err := transaction.Commit(); err != nil {
		return State{}, err
	}
	return state(stored), nil
}

func (service *Service) reject(
	ctx context.Context,
	transaction *store.CommandTx,
	actor auth.Account,
	identity store.CommandIdentity,
	code string,
	reason error,
	current ...store.LiveSessionState,
) (bool, error) {
	rejection := store.CommandRejection{Code: code, Message: reason.Error()}
	if len(current) > 0 && errors.Is(reason, ErrLiveStateRevisionConflict) {
		encoded, err := json.Marshal(current[0])
		if err != nil {
			return false, errors.Join(reason, errors.New("encode stale Session state"))
		}
		rejection.Details = encoded
	}
	if err := transaction.RecordRejection(actor.Context(ctx), identity, rejection); err != nil {
		return false, errors.Join(reason, err)
	}
	if err := transaction.Commit(); err != nil {
		return false, errors.Join(reason, err)
	}
	return true, reason
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
