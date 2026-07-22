// Package activationconnect adapts versioned Connect contracts to Activation services.
package activationconnect

import (
	"context"
	"errors"
	"math"

	"connectrpc.com/connect"

	activationv1 "github.com/dotwaffle/beamers/gen/beamers/activation/v1"
	"github.com/dotwaffle/beamers/gen/beamers/activation/v1/activationv1connect"
	"github.com/dotwaffle/beamers/internal/activation"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/connectapi"
)

// Handler translates Connect requests without owning Activation transitions.
type Handler struct {
	activationv1connect.UnimplementedActivationServiceHandler
	service *activation.Service
}

// NewHandler creates an Activation Connect adapter.
func NewHandler(service *activation.Service) (*Handler, error) {
	if service == nil {
		return nil, errors.New("activation service is required")
	}
	return &Handler{service: service}, nil
}

// ErrorInterceptor translates Activation failures into stable Connect codes.
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

// ValidationInterceptor rejects malformed protobuf requests before application dispatch.
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

// Preflight returns blockers, warnings, and an exact confirmation.
func (handler *Handler) Preflight(
	ctx context.Context,
	request *connect.Request[activationv1.PreflightRequest],
) (*connect.Response[activationv1.PreflightResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := handler.service.Preflight(ctx, actor, int(request.Msg.GetEventId()))
	if err != nil {
		return nil, err
	}
	response := &activationv1.PreflightResponse{
		EventId: int64(result.EventID), Confirmation: confirmation(result.Confirmation),
		Blockers: findings(result.Blockers), Warnings: findings(result.Warnings),
	}
	return connect.NewResponse(response), nil
}

// Activate designates the exact preflighted Event.
func (handler *Handler) Activate(
	ctx context.Context,
	request *connect.Request[activationv1.ActivateRequest],
) (*connect.Response[activationv1.ActivateResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	confirmed := request.Msg.GetConfirmation()
	result, err := handler.service.Activate(ctx, actor, activation.ActivateInput{
		EventID: int(request.Msg.GetEventId()), CommandID: request.Msg.GetCommandId(),
		Confirmation: activation.Confirmation{
			EventRevision:        int(confirmed.GetEventRevision()),
			PublishedRevision:    int(confirmed.GetPublishedRevision()),
			ActivationGeneration: int(confirmed.GetActivationGeneration()),
			Fingerprint:          confirmed.GetFingerprint(),
		},
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&activationv1.ActivateResponse{
		EventId: int64(result.EventID), Generation: int64(result.Generation),
	}), nil
}

// GetActiveEvent returns current installation live authority routing.
func (handler *Handler) GetActiveEvent(
	ctx context.Context,
	_ *connect.Request[activationv1.GetActiveEventRequest],
) (*connect.Response[activationv1.GetActiveEventResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := handler.service.ActiveEvent(ctx, actor)
	if err != nil {
		return nil, err
	}
	response := &activationv1.GetActiveEventResponse{Generation: int64(result.Generation)}
	if result.EventID != 0 {
		eventID := int64(result.EventID)
		response.EventId = &eventID
	}
	return connect.NewResponse(response), nil
}

func validateRequest(message any) error {
	switch request := message.(type) {
	case *activationv1.PreflightRequest:
		return positiveID(request.GetEventId())
	case *activationv1.ActivateRequest:
		if err := positiveID(request.GetEventId()); err != nil {
			return err
		}
		if err := command.ValidateID(request.GetCommandId()); err != nil {
			return err
		}
		confirmed := request.GetConfirmation()
		if confirmed == nil {
			return errors.New("confirmation is required")
		}
		if confirmed.GetEventRevision() <= 0 || confirmed.GetPublishedRevision() <= 0 ||
			confirmed.GetActivationGeneration() < 0 || confirmed.GetFingerprint() == "" {
			return errors.New("confirmation must contain valid revisions, generation, and a fingerprint")
		}
		return nil
	case *activationv1.GetActiveEventRequest:
		return nil
	default:
		return errors.New("unsupported Activation request")
	}
}

func positiveID(value int64) error {
	if value <= 0 || value > math.MaxInt {
		return errors.New("event_id must be a positive supported integer")
	}
	return nil
}

func confirmation(found activation.Confirmation) *activationv1.Confirmation {
	return &activationv1.Confirmation{
		EventRevision:        int64(found.EventRevision),
		PublishedRevision:    int64(found.PublishedRevision),
		ActivationGeneration: int64(found.ActivationGeneration),
		Fingerprint:          found.Fingerprint,
	}
}

func findings(found []activation.Finding) []*activationv1.Finding {
	result := make([]*activationv1.Finding, 0, len(found))
	for _, finding := range found {
		result = append(result, &activationv1.Finding{Code: finding.Code, Message: finding.Message})
	}
	return result
}

func connectError(err error) error {
	switch {
	case errors.Is(err, activation.ErrAdministratorRequired):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, activation.ErrEventNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, activation.ErrPreflightBlocked):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, activation.ErrStalePreflight):
		return connect.NewError(connect.CodeAborted, err)
	case errors.Is(err, activation.ErrCommandConflict):
		return connect.NewError(connect.CodeAlreadyExists, err)
	default:
		return connect.NewError(connect.CodeInternal, errors.New("activation unavailable"))
	}
}
