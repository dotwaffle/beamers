// Package sessionconnect adapts versioned Connect contracts to Session control services.
package sessionconnect

import (
	"context"
	"errors"
	"math"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	sessionv1 "github.com/dotwaffle/beamers/gen/beamers/session/v1"
	"github.com/dotwaffle/beamers/gen/beamers/session/v1/sessionv1connect"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/connectapi"
	"github.com/dotwaffle/beamers/internal/sessioncontrol"
)

// Handler translates Connect requests without owning Session transitions.
type Handler struct {
	sessionv1connect.UnimplementedSessionControlServiceHandler
	service *sessioncontrol.Service
}

// NewHandler creates a Session control Connect adapter.
func NewHandler(service *sessioncontrol.Service) (*Handler, error) {
	if service == nil {
		return nil, errors.New("session control service is required")
	}
	return &Handler{service: service}, nil
}

// ErrorInterceptor translates Session control failures into stable Connect codes.
func ErrorInterceptor() connect.Interceptor { return errorInterceptor{} }

type errorInterceptor struct{}

func (errorInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
		response, err := next(ctx, request)
		if err == nil {
			return response, nil
		}
		var connectErr *connect.Error
		if errors.As(err, &connectErr) {
			return response, err
		}
		return response, connectError(err)
	}
}

func (errorInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (errorInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// ValidationInterceptor rejects malformed protobuf requests before dispatch.
func ValidationInterceptor() connect.Interceptor { return validationInterceptor{} }

type validationInterceptor struct{}

func (validationInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
		if err := validateRequest(request.Any()); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return next(ctx, request)
	}
}

func (validationInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (validationInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// StartSession starts one Published Session explicitly.
func (handler *Handler) StartSession(
	ctx context.Context,
	request *connect.Request[sessionv1.StartSessionRequest],
) (*connect.Response[sessionv1.StartSessionResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := handler.service.Start(ctx, actor, sessioncontrol.StartInput{
		EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
		CommandID:                 request.Msg.GetCommandId(),
		ExpectedLiveStateRevision: int(request.Msg.GetExpectedLiveStateRevision()),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&sessionv1.StartSessionResponse{State: sessionState(result)}), nil
}

// EndSession ends one Live Session explicitly.
func (handler *Handler) EndSession(
	ctx context.Context,
	request *connect.Request[sessionv1.EndSessionRequest],
) (*connect.Response[sessionv1.EndSessionResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := handler.service.End(ctx, actor, sessioncontrol.EndInput{
		EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
		CommandID:                 request.Msg.GetCommandId(),
		ExpectedLiveStateRevision: int(request.Msg.GetExpectedLiveStateRevision()),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&sessionv1.EndSessionResponse{State: sessionState(result)}), nil
}

func validateRequest(message any) error {
	switch request := message.(type) {
	case *sessionv1.StartSessionRequest:
		if err := positiveID(request.GetEventId(), "event_id"); err != nil {
			return err
		}
		if err := positiveID(request.GetSessionId(), "session_id"); err != nil {
			return err
		}
		if err := command.ValidateID(request.GetCommandId()); err != nil {
			return err
		}
		if request.ExpectedLiveStateRevision == nil {
			return errors.New("expected_live_state_revision is required")
		}
		if request.GetExpectedLiveStateRevision() < 0 || request.GetExpectedLiveStateRevision() > math.MaxInt {
			return errors.New("expected_live_state_revision must be a supported non-negative integer")
		}
		return nil
	case *sessionv1.EndSessionRequest:
		if err := positiveID(request.GetEventId(), "event_id"); err != nil {
			return err
		}
		if err := positiveID(request.GetSessionId(), "session_id"); err != nil {
			return err
		}
		if err := command.ValidateID(request.GetCommandId()); err != nil {
			return err
		}
		if request.ExpectedLiveStateRevision == nil {
			return errors.New("expected_live_state_revision is required")
		}
		if request.GetExpectedLiveStateRevision() < 0 || request.GetExpectedLiveStateRevision() > math.MaxInt {
			return errors.New("expected_live_state_revision must be a supported non-negative integer")
		}
		return nil
	default:
		return errors.New("unsupported Session control request")
	}
}

func positiveID(value int64, field string) error {
	if value <= 0 || value > math.MaxInt {
		return errors.New(field + " must be a positive supported integer")
	}
	return nil
}

func sessionState(found sessioncontrol.State) *sessionv1.SessionState {
	result := &sessionv1.SessionState{
		SessionId: int64(found.SessionID), SessionRunId: int64(found.SessionRunID),
		Lifecycle: lifecycle(found.Lifecycle), LiveStateRevision: int64(found.LiveStateRevision),
	}
	if !found.ActualStart.IsZero() {
		result.ActualStart = timestamppb.New(found.ActualStart)
	}
	if found.ActualEnd != nil {
		result.ActualEnd = timestamppb.New(*found.ActualEnd)
	}
	return result
}

func lifecycle(value string) sessionv1.SessionLifecycle {
	switch value {
	case "Scheduled":
		return sessionv1.SessionLifecycle_SESSION_LIFECYCLE_SCHEDULED
	case "Live":
		return sessionv1.SessionLifecycle_SESSION_LIFECYCLE_LIVE
	case "Ended":
		return sessionv1.SessionLifecycle_SESSION_LIFECYCLE_ENDED
	case "Canceled":
		return sessionv1.SessionLifecycle_SESSION_LIFECYCLE_CANCELED
	default:
		return sessionv1.SessionLifecycle_SESSION_LIFECYCLE_UNSPECIFIED
	}
}

func connectError(err error) error {
	switch {
	case errors.Is(err, sessioncontrol.ErrOperatorRequired):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, sessioncontrol.ErrSessionScopeRequired):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, sessioncontrol.ErrSessionNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, sessioncontrol.ErrLiveStateRevisionConflict):
		connectErr := connect.NewError(connect.CodeAborted, err)
		var revisionErr *sessioncontrol.RevisionConflictError
		if errors.As(err, &revisionErr) {
			detail, detailErr := connect.NewErrorDetail(sessionState(revisionErr.Current))
			if detailErr != nil {
				return connect.NewError(connect.CodeInternal, errors.New("session control unavailable"))
			}
			connectErr.AddDetail(detail)
		}
		return connectErr
	case errors.Is(err, sessioncontrol.ErrSessionLifecycleTransition):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, sessioncontrol.ErrEventNotActive):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, sessioncontrol.ErrCommandConflict):
		return connect.NewError(connect.CodeAlreadyExists, err)
	default:
		return connect.NewError(connect.CodeInternal, errors.New("session control unavailable"))
	}
}
