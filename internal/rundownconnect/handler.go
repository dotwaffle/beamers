// Package rundownconnect adapts versioned Connect contracts to Rundown application services.
package rundownconnect

import (
	"context"
	"errors"
	"math"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	rundownv1 "github.com/dotwaffle/beamers/gen/beamers/rundown/v1"
	"github.com/dotwaffle/beamers/gen/beamers/rundown/v1/rundownv1connect"
	"github.com/dotwaffle/beamers/internal/connectapi"
	"github.com/dotwaffle/beamers/internal/rundown"
)

// Handler translates Connect requests without owning Rundown transitions.
type Handler struct {
	rundownv1connect.UnimplementedRundownServiceHandler
	commands *rundown.Commands
	queries  *rundown.Queries
}

// NewHandler creates a Rundown Connect adapter.
func NewHandler(commands *rundown.Commands, queries *rundown.Queries) (*Handler, error) {
	if commands == nil || queries == nil {
		return nil, errors.New("rundown commands and queries are required")
	}
	return &Handler{commands: commands, queries: queries}, nil
}

// ErrorInterceptor translates domain classifications into stable Connect codes.
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

// ValidationInterceptor rejects malformed protobuf shapes before application dispatch.
func ValidationInterceptor() connect.Interceptor {
	return validationInterceptor{}
}

type validationInterceptor struct{}

func (validationInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
		if err := validateTransportRequest(request.Any()); err != nil {
			return nil, invalidArgument(err)
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

func validateTransportRequest(message any) error {
	switch typed := message.(type) {
	case *rundownv1.EditDraftRequest:
		_, err := editDraftInput(typed)
		return err
	case *rundownv1.DeleteDraftSessionRequest:
		if _, err := positiveInt64("event_id", typed.GetEventId()); err != nil {
			return err
		}
		if _, err := positiveInt64("session_id", typed.GetSessionId()); err != nil {
			return err
		}
		_, err := nonnegativeInt64("expected_draft_revision", typed.GetExpectedDraftRevision())
		return err
	case *rundownv1.PublishPreviewRequest:
		if _, err := positiveInt64("event_id", typed.GetEventId()); err != nil {
			return err
		}
		_, err := ints64("change_ids", typed.GetChangeIds())
		return err
	case *rundownv1.PublishRequest:
		if _, err := positiveInt64("event_id", typed.GetEventId()); err != nil {
			return err
		}
		confirmation := typed.GetConfirmation()
		if confirmation == nil {
			return errors.New("confirmation is required")
		}
		if _, err := nonnegativeInt64("confirmation.draft_revision", confirmation.GetDraftRevision()); err != nil {
			return err
		}
		if _, err := nonnegativeInt64("confirmation.published_revision", confirmation.GetPublishedRevision()); err != nil {
			return err
		}
		_, err := ints64("confirmation.change_ids", confirmation.GetChangeIds())
		return err
	case *rundownv1.DiscardDraftChangesRequest:
		if _, err := positiveInt64("event_id", typed.GetEventId()); err != nil {
			return err
		}
		if _, err := nonnegativeInt64("expected_draft_revision", typed.GetExpectedDraftRevision()); err != nil {
			return err
		}
		_, err := ints64("change_ids", typed.GetChangeIds())
		return err
	case *rundownv1.RevertDraftChangeRequest:
		if _, err := positiveInt64("event_id", typed.GetEventId()); err != nil {
			return err
		}
		if _, err := nonnegativeInt64("expected_draft_revision", typed.GetExpectedDraftRevision()); err != nil {
			return err
		}
		_, err := positiveInt64("change_id", typed.GetChangeId())
		return err
	case *rundownv1.GetCrewRundownRequest:
		_, err := positiveInt64("event_id", typed.GetEventId())
		return err
	case *rundownv1.PreviewCSVImportRequest:
		_, err := positiveInt64("event_id", typed.GetEventId())
		return err
	case *rundownv1.ImportCSVRequest:
		if _, err := positiveInt64("event_id", typed.GetEventId()); err != nil {
			return err
		}
		_, err := nonnegativeInt64("expected_draft_revision", typed.GetExpectedDraftRevision())
		return err
	default:
		return errors.New("unsupported Rundown request")
	}
}

// PreviewCSVImport maps CSV data into reviewable Draft proposals.
func (handler *Handler) PreviewCSVImport(
	ctx context.Context,
	request *connect.Request[rundownv1.PreviewCSVImportRequest],
) (*connect.Response[rundownv1.PreviewCSVImportResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	eventID, err := positiveInt64("event_id", request.Msg.GetEventId())
	if err != nil {
		return nil, invalidArgument(err)
	}
	preview, err := handler.queries.PreviewCSVImport(ctx, actor, rundown.CSVImportPreviewInput{
		EventID: eventID, CSVData: string(request.Msg.GetCsvData()), Mappings: csvMappings(request.Msg.GetMappings()),
	})
	if err != nil {
		return nil, err
	}
	response := &rundownv1.PreviewCSVImportResponse{
		DraftRevision: int64(preview.DraftRevision), Fingerprint: preview.Fingerprint,
		IgnoredFields: preview.IgnoredFields, ValidationFailures: preview.ValidationFailures,
	}
	for _, proposal := range preview.Proposals {
		response.Proposals = append(response.Proposals, &rundownv1.CSVImportProposal{
			Id: proposal.ID, RowNumber: int64(proposal.RowNumber), RecordType: proposal.RecordType,
			ExternalKey: proposal.ExternalKey, Classification: proposal.Classification,
			SessionId: int64(proposal.SessionID), Field: proposal.Field,
			CurrentValue: proposal.CurrentValue, ProposedValue: proposal.ProposedValue, Message: proposal.Message,
		})
	}
	return connect.NewResponse(response), nil
}

// ImportCSV applies selected proposals to shared Draft state.
func (handler *Handler) ImportCSV(
	ctx context.Context,
	request *connect.Request[rundownv1.ImportCSVRequest],
) (*connect.Response[rundownv1.ImportCSVResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	eventID, err := positiveInt64("event_id", request.Msg.GetEventId())
	if err != nil {
		return nil, invalidArgument(err)
	}
	revision, err := nonnegativeInt64("expected_draft_revision", request.Msg.GetExpectedDraftRevision())
	if err != nil {
		return nil, invalidArgument(err)
	}
	result, err := handler.commands.ImportCSV(ctx, actor, rundown.CSVImportInput{
		EventID: eventID, CommandID: request.Msg.GetCommandId(), ExpectedDraftRevision: revision,
		CSVData: string(request.Msg.GetCsvData()), Mappings: csvMappings(request.Msg.GetMappings()),
		Fingerprint: request.Msg.GetFingerprint(), ProposalIDs: request.Msg.GetProposalIds(),
	})
	if err != nil {
		return nil, err
	}
	response := &rundownv1.ImportCSVResponse{DraftRevision: int64(result.DraftRevision)}
	for _, change := range result.Changes {
		response.Changes = append(response.Changes, draftChange(change))
	}
	return connect.NewResponse(response), nil
}

func csvMappings(values []*rundownv1.CSVFieldMapping) []rundown.CSVFieldMapping {
	result := make([]rundown.CSVFieldMapping, 0, len(values))
	for _, value := range values {
		if value != nil {
			result = append(result, rundown.CSVFieldMapping{
				SourceColumn: value.GetSourceColumn(), TargetField: value.GetTargetField(),
			})
		}
	}
	return result
}

// DeleteDraftSession permanently removes one eligible Draft-only Session.
func (handler *Handler) DeleteDraftSession(
	ctx context.Context,
	request *connect.Request[rundownv1.DeleteDraftSessionRequest],
) (*connect.Response[rundownv1.DeleteDraftSessionResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	eventID, err := positiveInt64("event_id", request.Msg.GetEventId())
	if err != nil {
		return nil, invalidArgument(err)
	}
	sessionID, err := positiveInt64("session_id", request.Msg.GetSessionId())
	if err != nil {
		return nil, invalidArgument(err)
	}
	revision, err := nonnegativeInt64("expected_draft_revision", request.Msg.GetExpectedDraftRevision())
	if err != nil {
		return nil, invalidArgument(err)
	}
	result, err := handler.commands.DeleteDraftSession(ctx, actor, rundown.DeleteDraftSessionInput{
		EventID: eventID, SessionID: sessionID, CommandID: request.Msg.GetCommandId(), ExpectedDraftRevision: revision,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&rundownv1.DeleteDraftSessionResponse{
		DraftRevision: int64(result.DraftRevision), SessionId: int64(result.SessionID),
	}), nil
}

// DiscardDraftChanges restores selected effective facts without publishing them.
func (handler *Handler) DiscardDraftChanges(
	ctx context.Context,
	request *connect.Request[rundownv1.DiscardDraftChangesRequest],
) (*connect.Response[rundownv1.DiscardDraftChangesResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	eventID, err := positiveInt64("event_id", request.Msg.GetEventId())
	if err != nil {
		return nil, invalidArgument(err)
	}
	revision, err := nonnegativeInt64("expected_draft_revision", request.Msg.GetExpectedDraftRevision())
	if err != nil {
		return nil, invalidArgument(err)
	}
	changeIDs, err := ints64("change_ids", request.Msg.GetChangeIds())
	if err != nil {
		return nil, invalidArgument(err)
	}
	result, err := handler.commands.DiscardDraftChanges(ctx, actor, rundown.DraftHistoryInput{
		EventID: eventID, CommandID: request.Msg.GetCommandId(), ExpectedDraftRevision: revision, ChangeIDs: changeIDs,
	})
	if err != nil {
		return nil, err
	}
	response := &rundownv1.DiscardDraftChangesResponse{DraftRevision: int64(result.DraftRevision)}
	for _, change := range result.Changes {
		response.Changes = append(response.Changes, draftChange(change))
	}
	return connect.NewResponse(response), nil
}

// RevertDraftChange appends an inverse fact change from immutable history.
func (handler *Handler) RevertDraftChange(
	ctx context.Context,
	request *connect.Request[rundownv1.RevertDraftChangeRequest],
) (*connect.Response[rundownv1.RevertDraftChangeResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	eventID, err := positiveInt64("event_id", request.Msg.GetEventId())
	if err != nil {
		return nil, invalidArgument(err)
	}
	revision, err := nonnegativeInt64("expected_draft_revision", request.Msg.GetExpectedDraftRevision())
	if err != nil {
		return nil, invalidArgument(err)
	}
	changeID, err := positiveInt64("change_id", request.Msg.GetChangeId())
	if err != nil {
		return nil, invalidArgument(err)
	}
	result, err := handler.commands.RevertDraftChange(ctx, actor, rundown.DraftHistoryInput{
		EventID: eventID, CommandID: request.Msg.GetCommandId(), ExpectedDraftRevision: revision, ChangeIDs: []int{changeID},
	})
	if err != nil {
		return nil, err
	}
	response := &rundownv1.RevertDraftChangeResponse{DraftRevision: int64(result.DraftRevision)}
	for _, change := range result.Changes {
		response.Changes = append(response.Changes, draftChange(change))
	}
	return connect.NewResponse(response), nil
}

// EditDraft translates one atomic structural Draft Edit.
func (handler *Handler) EditDraft(
	ctx context.Context,
	request *connect.Request[rundownv1.EditDraftRequest],
) (*connect.Response[rundownv1.EditDraftResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	input, err := editDraftInput(request.Msg)
	if err != nil {
		return nil, invalidArgument(err)
	}
	result, err := handler.commands.EditDraft(ctx, actor, input)
	if err != nil {
		return nil, err
	}
	response := &rundownv1.EditDraftResponse{DraftRevision: int64(result.DraftRevision)}
	for _, change := range result.Changes {
		response.Changes = append(response.Changes, draftChange(change))
	}
	return connect.NewResponse(response), nil
}

// PublishPreview forms one revision-bound dependency closure.
func (handler *Handler) PublishPreview(
	ctx context.Context,
	request *connect.Request[rundownv1.PublishPreviewRequest],
) (*connect.Response[rundownv1.PublishPreviewResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	eventID, err := positiveInt64("event_id", request.Msg.GetEventId())
	if err != nil {
		return nil, invalidArgument(err)
	}
	changeIDs, err := ints64("change_ids", request.Msg.GetChangeIds())
	if err != nil {
		return nil, invalidArgument(err)
	}
	preview, err := handler.queries.PublishPreview(ctx, actor, rundown.PublishPreviewInput{
		EventID: eventID, ChangeIDs: changeIDs,
	})
	if err != nil {
		return nil, err
	}
	response := &rundownv1.PublishPreviewResponse{
		DraftRevision: int64(preview.DraftRevision), PublishedRevision: int64(preview.PublishedRevision),
		ChangeIds: ints(preview.ChangeIDs), AutoIncludedChangeIds: ints(preview.AutoIncludedChangeIDs),
		Fingerprint: preview.Fingerprint, ValidationFailures: preview.ValidationFailures,
		AffectedStructure: preview.AffectedStructure,
	}
	for _, change := range preview.Changes {
		response.Changes = append(response.Changes, draftChange(change))
	}
	return connect.NewResponse(response), nil
}

// Publish applies one exact confirmed dependency closure.
func (handler *Handler) Publish(
	ctx context.Context,
	request *connect.Request[rundownv1.PublishRequest],
) (*connect.Response[rundownv1.PublishResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	eventID, err := positiveInt64("event_id", request.Msg.GetEventId())
	if err != nil {
		return nil, invalidArgument(err)
	}
	confirmation := request.Msg.GetConfirmation()
	if confirmation == nil {
		return nil, invalidArgument(errors.New("confirmation is required"))
	}
	changeIDs, err := ints64("confirmation.change_ids", confirmation.GetChangeIds())
	if err != nil {
		return nil, invalidArgument(err)
	}
	draftRevision, err := nonnegativeInt64("confirmation.draft_revision", confirmation.GetDraftRevision())
	if err != nil {
		return nil, invalidArgument(err)
	}
	publishedRevision, err := nonnegativeInt64("confirmation.published_revision", confirmation.GetPublishedRevision())
	if err != nil {
		return nil, invalidArgument(err)
	}
	result, err := handler.commands.Publish(ctx, actor, rundown.PublishInput{
		EventID: eventID, CommandID: request.Msg.GetCommandId(), PublishNote: request.Msg.GetPublishNote(),
		Confirmation: rundown.PublishConfirmation{
			DraftRevision: draftRevision, PublishedRevision: publishedRevision,
			ChangeIDs: changeIDs, Fingerprint: confirmation.GetFingerprint(),
		},
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&rundownv1.PublishResponse{
		DraftRevision: int64(result.DraftRevision), PublishedRevision: int64(result.PublishedRevision),
		ChangeIds: ints(result.ChangeIDs),
	}), nil
}

// GetCrewRundown returns only the purpose-built current Published projection.
func (handler *Handler) GetCrewRundown(
	ctx context.Context,
	request *connect.Request[rundownv1.GetCrewRundownRequest],
) (*connect.Response[rundownv1.GetCrewRundownResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	eventID, err := positiveInt64("event_id", request.Msg.GetEventId())
	if err != nil {
		return nil, invalidArgument(err)
	}
	projection, err := handler.queries.CrewRundown(ctx, actor, eventID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(crewRundown(projection)), nil
}

func draftChange(change rundown.DraftChange) *rundownv1.DraftChange {
	return &rundownv1.DraftChange{
		Id: int64(change.ID), Kind: change.Kind, TargetType: change.TargetType, TargetId: int64(change.TargetID),
		FactKey: change.FactKey, Status: change.Status,
		CurrentValueJson: change.CurrentValueJSON,
	}
}

func connectError(err error) error {
	var validation *rundown.ValidationError
	switch {
	case errors.As(err, &validation):
		return connect.NewError(connect.CodeInvalidArgument, errors.New(validation.Error()))
	case errors.Is(err, rundown.ErrEventAccessDenied):
		return connect.NewError(connect.CodePermissionDenied, errors.New("event access denied"))
	case errors.Is(err, rundown.ErrCommandConflict):
		return connect.NewError(connect.CodeAlreadyExists, errors.New("command ID conflict"))
	case errors.Is(err, rundown.ErrDraftRevisionConflict):
		connectErr := connect.NewError(connect.CodeAborted, errors.New("rundown revision conflict"))
		var conflict *rundown.DraftRevisionConflictError
		if errors.As(err, &conflict) {
			message := &rundownv1.DraftRevisionConflict{CurrentDraftRevision: int64(conflict.CurrentDraftRevision)}
			for _, change := range conflict.OverlappingChanges {
				message.OverlappingChanges = append(message.OverlappingChanges, draftChange(rundown.DraftChange{
					ID: change.ID, Kind: change.Kind, TargetType: change.TargetType, TargetID: change.TargetID,
					FactKey: change.FactKey, Status: change.Status,
					CurrentValueJSON: change.CurrentValueJSON,
				}))
			}
			detail, detailErr := connect.NewErrorDetail(message)
			if detailErr != nil {
				return connect.NewError(connect.CodeInternal, errors.New("rundown operation failed"))
			}
			connectErr.AddDetail(detail)
		}
		return connectErr
	case errors.Is(err, rundown.ErrStalePreview):
		return connect.NewError(connect.CodeAborted, errors.New("rundown revision conflict"))
	case errors.Is(err, rundown.ErrPublishSelection):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("publish selection is invalid"))
	case errors.Is(err, rundown.ErrDraftSessionDeletion):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, rundown.ErrSessionNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	default:
		return connect.NewError(connect.CodeInternal, errors.New("rundown operation failed"))
	}
}

func invalidArgument(err error) error {
	return connect.NewError(connect.CodeInvalidArgument, err)
}

func positiveInt64(field string, value int64) (int, error) {
	if value <= 0 || value > int64(math.MaxInt) {
		return 0, errors.New(field + " must be a positive integer")
	}
	return int(value), nil
}

func nonnegativeInt64(field string, value int64) (int, error) {
	if value < 0 || value > int64(math.MaxInt) {
		return 0, errors.New(field + " must be a nonnegative integer")
	}
	return int(value), nil
}

func ints64(field string, values []int64) ([]int, error) {
	result := make([]int, 0, len(values))
	for _, value := range values {
		converted, err := positiveInt64(field, value)
		if err != nil {
			return nil, err
		}
		result = append(result, converted)
	}
	return result, nil
}

func ints(values []int) []int64 {
	result := make([]int64, 0, len(values))
	for _, value := range values {
		result = append(result, int64(value))
	}
	return result
}

func timestamp(field string, value *timestamppb.Timestamp) (time.Time, error) {
	if value == nil {
		return time.Time{}, errors.New(field + " is required")
	}
	if err := value.CheckValid(); err != nil {
		return time.Time{}, errors.New(field + " is invalid")
	}
	return value.AsTime(), nil
}

func duration(field string, value *durationpb.Duration) (time.Duration, error) {
	if value == nil {
		return 0, errors.New(field + " is required")
	}
	if err := value.CheckValid(); err != nil {
		return 0, errors.New(field + " is invalid")
	}
	return value.AsDuration(), nil
}
