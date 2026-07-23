// Package competitionconnect adapts versioned Connect contracts to Competitions.
package competitionconnect

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	competitionv1 "github.com/dotwaffle/beamers/gen/beamers/competition/v1"
	"github.com/dotwaffle/beamers/gen/beamers/competition/v1/competitionv1connect"
	rundownv1 "github.com/dotwaffle/beamers/gen/beamers/rundown/v1"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/competition"
	"github.com/dotwaffle/beamers/internal/connectapi"
)

// Handler translates Competition Connect requests.
type Handler struct {
	competitionv1connect.UnimplementedCompetitionServiceHandler
	service *competition.Service
}

// NewHandler creates a Competition Connect adapter.
func NewHandler(service *competition.Service) (*Handler, error) {
	if service == nil {
		return nil, errors.New("competition service is required")
	}
	return &Handler{service: service}, nil
}

// ErrorInterceptor translates Competition failures into stable Connect codes.
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

// GetCompetition returns one fixed Competition configuration and its Entries.
func (handler *Handler) GetCompetition(
	ctx context.Context,
	request *connect.Request[competitionv1.GetCompetitionRequest],
) (*connect.Response[competitionv1.GetCompetitionResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	found, err := handler.service.Get(ctx, actor, int(request.Msg.GetEventId()), int(request.Msg.GetSessionId()))
	if err != nil {
		return nil, err
	}
	response := &competitionv1.GetCompetitionResponse{
		EventId: int64(found.EventID), SessionId: int64(found.SessionID),
		SubmissionDeadline:          timestamppb.New(found.SubmissionDeadline),
		EffectiveDefaultDisposition: disposition(found.EffectiveDefaultDisposition),
	}
	for _, foundEntry := range found.Entries {
		response.Entries = append(response.Entries, entry(foundEntry))
	}
	return connect.NewResponse(response), nil
}

// CreateEntry creates one Entry before the Deadline.
func (handler *Handler) CreateEntry(
	ctx context.Context,
	request *connect.Request[competitionv1.CreateEntryRequest],
) (*connect.Response[competitionv1.CreateEntryResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	created, err := handler.service.CreateEntry(ctx, actor, competition.CreateEntryInput{
		EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
		CommandID: request.Msg.GetCommandId(), Name: request.Msg.GetName(),
		PublicDetails: request.Msg.GetPublicDetails(), CrewNotes: request.Msg.GetCrewNotes(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&competitionv1.CreateEntryResponse{Entry: entry(created)}), nil
}

// UpdateEntry changes one Entry before the Deadline.
func (handler *Handler) UpdateEntry(
	ctx context.Context,
	request *connect.Request[competitionv1.UpdateEntryRequest],
) (*connect.Response[competitionv1.UpdateEntryResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	updated, err := handler.service.UpdateEntry(ctx, actor, competition.UpdateEntryInput{
		EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
		EntryID: int(request.Msg.GetEntryId()), CommandID: request.Msg.GetCommandId(),
		ExpectedRevision: int(request.Msg.GetExpectedRevision()), Name: request.Msg.GetName(),
		PublicDetails: request.Msg.GetPublicDetails(), CrewNotes: request.Msg.GetCrewNotes(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&competitionv1.UpdateEntryResponse{Entry: entry(updated)}), nil
}

// ChangeEntryDisposition changes participation before the Deadline.
func (handler *Handler) ChangeEntryDisposition(
	ctx context.Context,
	request *connect.Request[competitionv1.ChangeEntryDispositionRequest],
) (*connect.Response[competitionv1.ChangeEntryDispositionResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	updated, err := handler.service.ChangeDisposition(ctx, actor, competition.ChangeDispositionInput{
		EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
		EntryID: int(request.Msg.GetEntryId()), CommandID: request.Msg.GetCommandId(),
		ExpectedRevision:      int(request.Msg.GetExpectedRevision()),
		Disposition:           dispositionFromProto(request.Msg.GetDisposition()),
		ConfirmedLiveOverride: request.Msg.GetConfirmedLiveOverride(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&competitionv1.ChangeEntryDispositionResponse{Entry: entry(updated)}), nil
}

func entry(found competition.Entry) *competitionv1.Entry {
	return &competitionv1.Entry{
		Id: int64(found.ID), CompetitionSessionId: int64(found.CompetitionSessionID),
		Name: found.Name, PublicDetails: found.PublicDetails, CrewNotes: found.CrewNotes,
		Disposition: disposition(found.Disposition), Revision: int64(found.Revision),
		CreatedAt:     timestamppb.New(found.CreatedAt),
		Participating: found.Disposition == competition.DispositionIncluded,
	}
}

func disposition(value competition.Disposition) rundownv1.EntryDisposition {
	return map[competition.Disposition]rundownv1.EntryDisposition{
		competition.DispositionPending:  rundownv1.EntryDisposition_ENTRY_DISPOSITION_PENDING,
		competition.DispositionIncluded: rundownv1.EntryDisposition_ENTRY_DISPOSITION_INCLUDED,
		competition.DispositionRejected: rundownv1.EntryDisposition_ENTRY_DISPOSITION_REJECTED,
	}[value]
}

func dispositionFromProto(value rundownv1.EntryDisposition) competition.Disposition {
	return map[rundownv1.EntryDisposition]competition.Disposition{
		rundownv1.EntryDisposition_ENTRY_DISPOSITION_PENDING:  competition.DispositionPending,
		rundownv1.EntryDisposition_ENTRY_DISPOSITION_INCLUDED: competition.DispositionIncluded,
		rundownv1.EntryDisposition_ENTRY_DISPOSITION_REJECTED: competition.DispositionRejected,
	}[value]
}

func connectError(err error) error {
	switch {
	case errors.Is(err, competition.ErrProducerRequired):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, competition.ErrCompetitionNotFound), errors.Is(err, competition.ErrEntryNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, competition.ErrSubmissionClosed), errors.Is(err, competition.ErrLiveDispositionConfirmation):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, competition.ErrEntryRevisionConflict), errors.Is(err, competition.ErrCommandConflict):
		return connect.NewError(connect.CodeAborted, err)
	case errors.Is(err, command.ErrInvalidID), errors.Is(err, competition.ErrInvalidInput):
		return connect.NewError(connect.CodeInvalidArgument, err)
	default:
		return connect.NewError(connect.CodeInternal, errors.New("competition request failed"))
	}
}
