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
		RequireEntryReview:          found.RequireEntryReview,
		FileDeliveryRequired:        found.FileDeliveryRequired,
		ReadinessRevision:           int64(found.ReadinessRevision),
	}
	for _, foundEntry := range found.Entries {
		response.Entries = append(response.Entries, entry(foundEntry))
	}
	return connect.NewResponse(response), nil
}

// ConfigureReadiness changes independent Competition Start policies.
func (handler *Handler) ConfigureReadiness(
	ctx context.Context,
	request *connect.Request[competitionv1.ConfigureReadinessRequest],
) (*connect.Response[competitionv1.ConfigureReadinessResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	configured, err := handler.service.ConfigureReadiness(
		ctx, actor, competition.ConfigureReadinessInput{
			EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
			CommandID:                 request.Msg.GetCommandId(),
			ExpectedReadinessRevision: int(request.Msg.GetExpectedReadinessRevision()),
			RequireEntryReview:        request.Msg.GetRequireEntryReview(),
			FileDeliveryRequired:      request.Msg.GetFileDeliveryRequired(),
		},
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&competitionv1.ConfigureReadinessResponse{
		RequireEntryReview:   configured.RequireEntryReview,
		FileDeliveryRequired: configured.FileDeliveryRequired,
		ReadinessRevision:    int64(configured.ReadinessRevision),
	}), nil
}

// PreflightStart reports current Competition Start blockers.
func (handler *Handler) PreflightStart(
	ctx context.Context,
	request *connect.Request[competitionv1.PreflightStartRequest],
) (*connect.Response[competitionv1.PreflightStartResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	found, err := handler.service.PreflightStart(
		ctx, actor, int(request.Msg.GetEventId()), int(request.Msg.GetSessionId()),
	)
	if err != nil {
		return nil, err
	}
	response := &competitionv1.PreflightStartResponse{
		EventId: int64(found.EventID), SessionId: int64(found.SessionID),
		RequireEntryReview:   found.RequireEntryReview,
		FileDeliveryRequired: found.FileDeliveryRequired,
	}
	for _, blocker := range found.Blockers {
		response.Blockers = append(response.Blockers, &competitionv1.PreflightFinding{
			Code: blocker.Code, Message: blocker.Message, EntryId: int64(blocker.EntryID),
		})
	}
	for _, attachment := range found.Attachments {
		response.Attachments = append(response.Attachments, attachmentReadiness(attachment))
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

// ReviewEntry confirms one exact current Entry content revision.
func (handler *Handler) ReviewEntry(
	ctx context.Context,
	request *connect.Request[competitionv1.ReviewEntryRequest],
) (*connect.Response[competitionv1.ReviewEntryResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	reviewed, err := handler.service.ReviewEntry(ctx, actor, competition.ReviewEntryInput{
		EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
		EntryID: int(request.Msg.GetEntryId()), CommandID: request.Msg.GetCommandId(),
		ExpectedRevision: int(request.Msg.GetExpectedRevision()),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&competitionv1.ReviewEntryResponse{Entry: entry(reviewed)}), nil
}

// SetEntryAttachmentReadiness changes Final and Primary independently.
func (handler *Handler) SetEntryAttachmentReadiness(
	ctx context.Context,
	request *connect.Request[competitionv1.SetEntryAttachmentReadinessRequest],
) (*connect.Response[competitionv1.SetEntryAttachmentReadinessResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	updated, err := handler.service.SetEntryAttachmentReadiness(
		ctx, actor, competition.SetEntryAttachmentReadinessInput{
			EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
			EntryID:             int(request.Msg.GetEntryId()),
			AttachmentVersionID: int(request.Msg.GetAttachmentVersionId()),
			CommandID:           request.Msg.GetCommandId(),
			ExpectedRevision:    int(request.Msg.GetExpectedRevision()),
			Final:               request.Msg.GetFinal(), Primary: request.Msg.GetPrimary(),
		},
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&competitionv1.SetEntryAttachmentReadinessResponse{
		Attachment: attachmentReadiness(updated),
	}), nil
}

func attachmentReadiness(found competition.AttachmentReadiness) *competitionv1.AttachmentReadiness {
	return &competitionv1.AttachmentReadiness{
		AttachmentVersionId: int64(found.AttachmentVersionID),
		Revision:            int64(found.ReadinessRevision),
		Final:               found.Final,
		Primary:             found.Primary,
		EntryId:             int64(found.EntryID),
		AttachmentVersion:   int64(found.AttachmentVersion),
		LogicalName:         found.LogicalName,
		OriginalFilename:    found.OriginalFilename,
	}
}

func entry(found competition.Entry) *competitionv1.Entry {
	return &competitionv1.Entry{
		Id: int64(found.ID), CompetitionSessionId: int64(found.CompetitionSessionID),
		Name: found.Name, PublicDetails: found.PublicDetails, CrewNotes: found.CrewNotes,
		Disposition: disposition(found.Disposition), Revision: int64(found.Revision),
		CreatedAt:       timestamppb.New(found.CreatedAt),
		Participating:   found.Disposition == competition.DispositionIncluded,
		ContentRevision: int64(found.ContentRevision),
		ReviewCurrent:   found.ReviewCurrent,
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
	case errors.Is(err, competition.ErrEntryRevisionConflict),
		errors.Is(err, competition.ErrReadinessRevisionConflict),
		errors.Is(err, competition.ErrAttachmentReadinessRevisionConflict),
		errors.Is(err, competition.ErrCommandConflict):
		return connect.NewError(connect.CodeAborted, err)
	case errors.Is(err, command.ErrInvalidID), errors.Is(err, competition.ErrInvalidInput):
		return connect.NewError(connect.CodeInvalidArgument, err)
	default:
		return connect.NewError(connect.CodeInternal, errors.New("competition request failed"))
	}
}
