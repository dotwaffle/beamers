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
	// ErrSessionScopeRequired means an Operator lacks one or more Session Lanes.
	ErrSessionScopeRequired = store.ErrSessionScopeRequired
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
