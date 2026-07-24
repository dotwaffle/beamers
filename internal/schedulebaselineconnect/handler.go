// Package schedulebaselineconnect adapts versioned Connect contracts to baseline commands.
package schedulebaselineconnect

import (
	"context"
	"errors"
	"math"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	schedulebaselinev1 "github.com/dotwaffle/beamers/gen/beamers/schedulebaseline/v1"
	"github.com/dotwaffle/beamers/gen/beamers/schedulebaseline/v1/schedulebaselinev1connect"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/connectapi"
	"github.com/dotwaffle/beamers/internal/schedulebaseline"
)

// Handler translates Connect requests without owning baseline semantics.
type Handler struct {
	schedulebaselinev1connect.UnimplementedScheduleBaselineServiceHandler
	commands *schedulebaseline.Commands
	queries  *schedulebaseline.Queries
}

// NewHandler creates a Public Schedule Baseline Connect adapter.
func NewHandler(
	commands *schedulebaseline.Commands,
	queries *schedulebaseline.Queries,
) (*Handler, error) {
	if commands == nil {
		return nil, errors.New("public schedule baseline commands are required")
	}
	if queries == nil {
		return nil, errors.New("public schedule baseline queries are required")
	}
	return &Handler{commands: commands, queries: queries}, nil
}

// ErrorInterceptor translates baseline failures into stable Connect codes.
func ErrorInterceptor() connect.Interceptor {
	return errorInterceptor{}
}

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
func ValidationInterceptor() connect.Interceptor {
	return validationInterceptor{}
}

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

// Preview returns the exact Event and Public Sessions requiring confirmation.
func (handler *Handler) Preview(
	ctx context.Context,
	request *connect.Request[schedulebaselinev1.PreviewRequest],
) (*connect.Response[schedulebaselinev1.PreviewResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	preview, err := handler.queries.Preview(ctx, actor, int(request.Msg.GetEventId()))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(previewResponse(preview)), nil
}

// Capture records the exact confirmed Public Schedule Baseline.
func (handler *Handler) Capture(
	ctx context.Context,
	request *connect.Request[schedulebaselinev1.CaptureRequest],
) (*connect.Response[schedulebaselinev1.CaptureResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	confirmed := request.Msg.GetConfirmation()
	result, err := handler.commands.Capture(ctx, actor, schedulebaseline.CaptureInput{
		EventID: int(request.Msg.GetEventId()), CommandID: request.Msg.GetCommandId(),
		Confirmation: schedulebaseline.Confirmation{
			PublishedRevision: int(confirmed.GetPublishedRevision()),
			Fingerprint:       confirmed.GetFingerprint(),
		},
		AcknowledgedEventName: request.Msg.GetAcknowledgedEventName(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&schedulebaselinev1.CaptureResponse{
		EventId: int64(result.EventID), PublishedRevision: int64(result.PublishedRevision),
		SessionCount: int64(result.SessionCount), CapturedAt: timestamppb.New(result.CapturedAt),
	}), nil
}

func previewResponse(preview schedulebaseline.Preview) *schedulebaselinev1.PreviewResponse {
	response := &schedulebaselinev1.PreviewResponse{
		EventId: int64(preview.EventID), EventName: preview.EventName, Active: preview.Active,
		RequiresNonActiveAcknowledgment: preview.RequiresNonActiveAcknowledgment,
		Confirmation: &schedulebaselinev1.Confirmation{
			PublishedRevision: int64(preview.Confirmation.PublishedRevision),
			Fingerprint:       preview.Confirmation.Fingerprint,
		},
		Sessions:           make([]*schedulebaselinev1.Session, 0, len(preview.Sessions)),
		ValidationFailures: make([]*schedulebaselinev1.Finding, 0, len(preview.ValidationFailures)),
	}
	for _, session := range preview.Sessions {
		response.Sessions = append(response.Sessions, &schedulebaselinev1.Session{
			Id: int64(session.ID), Title: session.Title,
			ForecastStart: timestamppb.New(session.ForecastStart),
		})
	}
	for _, finding := range preview.ValidationFailures {
		response.ValidationFailures = append(
			response.ValidationFailures,
			&schedulebaselinev1.Finding{
				SessionId: int64(finding.SessionID), Message: finding.Message,
			},
		)
	}
	return response
}

func validateRequest(message any) error {
	switch request := message.(type) {
	case *schedulebaselinev1.PreviewRequest:
		return positiveID(request.GetEventId())
	case *schedulebaselinev1.CaptureRequest:
		if err := positiveID(request.GetEventId()); err != nil {
			return err
		}
		if err := command.ValidateID(request.GetCommandId()); err != nil {
			return err
		}
		confirmation := request.GetConfirmation()
		if confirmation == nil {
			return errors.New("confirmation is required")
		}
		if confirmation.GetPublishedRevision() < 0 || confirmation.GetFingerprint() == "" {
			return errors.New("confirmation must contain a valid revision and fingerprint")
		}
		return nil
	default:
		return errors.New("unsupported Public Schedule Baseline request")
	}
}

func positiveID(value int64) error {
	if value <= 0 || value > math.MaxInt {
		return errors.New("event_id must be a positive supported integer")
	}
	return nil
}

func connectError(err error) error {
	switch {
	case errors.Is(err, schedulebaseline.ErrProducerRequired):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, schedulebaseline.ErrEventNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, schedulebaseline.ErrAlreadyCaptured):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, schedulebaseline.ErrStalePreview):
		return connect.NewError(connect.CodeAborted, err)
	case errors.Is(err, schedulebaseline.ErrInvalidBaseline),
		errors.Is(err, schedulebaseline.ErrNonActiveAcknowledgment):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, schedulebaseline.ErrCommandConflict):
		return connect.NewError(connect.CodeAlreadyExists, err)
	default:
		return connect.NewError(connect.CodeInternal, errors.New("public schedule baseline operation failed"))
	}
}
