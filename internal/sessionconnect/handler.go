// Package sessionconnect adapts versioned Connect contracts to Session control services.
package sessionconnect

import (
	"context"
	"errors"
	"math"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	rundownv1 "github.com/dotwaffle/beamers/gen/beamers/rundown/v1"
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
	case *sessionv1.CorrectLiveDetailsRequest:
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
		if request.GetUpdateMask() == nil || len(request.GetUpdateMask().GetPaths()) == 0 {
			return errors.New("update_mask must select corrected details")
		}
		return nil
	case *sessionv1.GetSessionHistoryRequest:
		if err := positiveID(request.GetEventId(), "event_id"); err != nil {
			return err
		}
		return positiveID(request.GetSessionId(), "session_id")
	default:
		return errors.New("unsupported Session control request")
	}
}

// CorrectLiveDetails applies one confirmed descriptive correction.
func (handler *Handler) CorrectLiveDetails(
	ctx context.Context,
	request *connect.Request[sessionv1.CorrectLiveDetailsRequest],
) (*connect.Response[sessionv1.CorrectLiveDetailsResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := handler.service.CorrectLiveDetails(ctx, actor, sessioncontrol.CorrectLiveDetailsInput{
		EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
		CommandID: request.Msg.GetCommandId(), ExpectedLiveStateRevision: int(request.Msg.GetExpectedLiveStateRevision()),
		Confirmed: request.Msg.GetConfirmed(), Title: request.Msg.GetTitle(), Speaker: request.Msg.GetSpeaker(),
		PublicDetails: request.Msg.GetPublicDetails(), UpdateFields: request.Msg.GetUpdateMask().GetPaths(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&sessionv1.CorrectLiveDetailsResponse{
		State: sessionState(result.State), AmendmentId: int64(result.AmendmentID), Details: sessionDetails(result.Details),
	}), nil
}

// GetSessionHistory returns immutable Run Snapshots and amendments.
func (handler *Handler) GetSessionHistory(
	ctx context.Context,
	request *connect.Request[sessionv1.GetSessionHistoryRequest],
) (*connect.Response[sessionv1.GetSessionHistoryResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	runs, err := handler.service.History(ctx, actor, int(request.Msg.GetEventId()), int(request.Msg.GetSessionId()))
	if err != nil {
		return nil, err
	}
	response := &sessionv1.GetSessionHistoryResponse{}
	for _, run := range runs {
		found := &sessionv1.SessionRunHistory{
			Id: int64(run.ID), ActualStart: timestamppb.New(run.ActualStart), Snapshot: runSnapshot(run.Snapshot),
		}
		if run.ActualEnd != nil {
			found.ActualEnd = timestamppb.New(*run.ActualEnd)
		}
		for _, amendment := range run.Amendments {
			found.Amendments = append(found.Amendments, &sessionv1.RunAmendment{
				Id: int64(amendment.ID), Details: sessionDetails(amendment.Details),
				ChangedFields: amendment.ChangedFields, CreatedAt: timestamppb.New(amendment.CreatedAt),
			})
		}
		response.Runs = append(response.Runs, found)
	}
	return connect.NewResponse(response), nil
}

func runSnapshot(snapshot sessioncontrol.RunSnapshot) *sessionv1.RunSnapshot {
	return &sessionv1.RunSnapshot{
		PublishedRevision: int64(snapshot.PublishedRevision),
		Title:             snapshot.Title, Speaker: snapshot.Speaker, Type: sessionType(snapshot.Type),
		PublicDetails: snapshot.PublicDetails,
		PlannedStart:  timestamppb.New(snapshot.PlannedStart), PlannedEnd: timestamppb.New(snapshot.PlannedEnd),
		TimingPolicy:    timingPolicy(snapshot.TimingPolicy),
		MinimumDuration: durationpb.New(time.Duration(snapshot.MinimumDurationSeconds) * time.Second),
		StartBoundary:   boundary(snapshot.StartBoundary), EndBoundary: boundary(snapshot.EndBoundary),
		LaneIds: int64s(snapshot.LaneIDs), LocationIds: int64s(snapshot.LocationIDs),
		TrackIds: int64s(snapshot.TrackIDs),
	}
}

func sessionType(value string) rundownv1.SessionType {
	return map[string]rundownv1.SessionType{
		"Presentation": rundownv1.SessionType_SESSION_TYPE_PRESENTATION,
		"Competition":  rundownv1.SessionType_SESSION_TYPE_COMPETITION,
		"Break":        rundownv1.SessionType_SESSION_TYPE_BREAK,
		"Activity":     rundownv1.SessionType_SESSION_TYPE_ACTIVITY,
		"Ceremony":     rundownv1.SessionType_SESSION_TYPE_CEREMONY,
		"Performance":  rundownv1.SessionType_SESSION_TYPE_PERFORMANCE,
		"Hold":         rundownv1.SessionType_SESSION_TYPE_HOLD,
	}[value]
}

func timingPolicy(value string) rundownv1.TimingPolicy {
	return map[string]rundownv1.TimingPolicy{
		"FixedEnd":      rundownv1.TimingPolicy_TIMING_POLICY_FIXED_END,
		"FixedDuration": rundownv1.TimingPolicy_TIMING_POLICY_FIXED_DURATION,
		"ManualEnd":     rundownv1.TimingPolicy_TIMING_POLICY_MANUAL_END,
	}[value]
}

func boundary(value string) rundownv1.Boundary {
	return map[string]rundownv1.Boundary{
		"Hard": rundownv1.Boundary_BOUNDARY_HARD,
		"Soft": rundownv1.Boundary_BOUNDARY_SOFT,
	}[value]
}

func int64s(values []int) []int64 {
	result := make([]int64, len(values))
	for index, value := range values {
		result[index] = int64(value)
	}
	return result
}

func sessionDetails(details sessioncontrol.Details) *sessionv1.SessionDetails {
	return &sessionv1.SessionDetails{
		Title: details.Title, Speaker: details.Speaker, PublicDetails: details.PublicDetails,
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
	case errors.Is(err, sessioncontrol.ErrLiveDetailConfirmation):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, sessioncontrol.ErrLiveDetailFields):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, sessioncontrol.ErrEventNotActive):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, sessioncontrol.ErrCommandConflict):
		return connect.NewError(connect.CodeAlreadyExists, err)
	default:
		return connect.NewError(connect.CodeInternal, errors.New("session control unavailable"))
	}
}
