// Package resultsconnect adapts versioned Connect contracts to Results.
package resultsconnect

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	resultsv1 "github.com/dotwaffle/beamers/gen/beamers/results/v1"
	"github.com/dotwaffle/beamers/gen/beamers/results/v1/resultsv1connect"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/connectapi"
	"github.com/dotwaffle/beamers/internal/results"
)

// Handler translates Results Connect requests.
type Handler struct {
	resultsv1connect.UnimplementedResultsServiceHandler
	service *results.Service
}

// NewHandler creates a Results Connect adapter.
func NewHandler(service *results.Service) (*Handler, error) {
	if service == nil {
		return nil, errors.New("results service is required")
	}
	return &Handler{service: service}, nil
}

// ErrorInterceptor translates Results failures into stable Connect codes.
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

// GetCompetitionResultsDraft returns the current unreleased Draft.
func (handler *Handler) GetCompetitionResultsDraft(
	ctx context.Context,
	request *connect.Request[resultsv1.GetCompetitionResultsDraftRequest],
) (*connect.Response[resultsv1.GetCompetitionResultsDraftResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	found, err := handler.service.Get(
		ctx, actor, int(request.Msg.GetEventId()), int(request.Msg.GetSessionId()),
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.GetCompetitionResultsDraftResponse{
		Draft: draft(found),
	}), nil
}

// SaveCompetitionResultsDraft appends one immutable Draft revision.
func (handler *Handler) SaveCompetitionResultsDraft(
	ctx context.Context,
	request *connect.Request[resultsv1.SaveCompetitionResultsDraftRequest],
) (*connect.Response[resultsv1.SaveCompetitionResultsDraftResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	standings, err := standingsFromProto(request.Msg.GetStandings())
	if err != nil {
		return nil, err
	}
	saved, err := handler.service.Save(ctx, actor, results.SaveInput{
		EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
		CommandID:         request.Msg.GetCommandId(),
		ExpectedRevision:  int(request.Msg.GetExpectedRevision()),
		Disposition:       resultsDispositionFromProto(request.Msg.GetDisposition()),
		NoPublicReason:    request.Msg.GetNoPublicCrewReason(),
		PublicExplanation: request.Msg.GetPublicExplanation(),
		Score:             scorePolicyFromProto(request.Msg.GetScore()),
		Standings:         standings,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.SaveCompetitionResultsDraftResponse{
		Draft: draft(saved),
	}), nil
}

// SaveCompetitionAwards replaces Awards without changing Placement or Score.
func (handler *Handler) SaveCompetitionAwards(
	ctx context.Context,
	request *connect.Request[resultsv1.SaveCompetitionAwardsRequest],
) (*connect.Response[resultsv1.SaveCompetitionAwardsResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	awards, err := competitionAwardsFromProto(request.Msg.GetAwards())
	if err != nil {
		return nil, err
	}
	saved, err := handler.service.SaveCompetitionAwards(
		ctx,
		actor,
		results.SaveCompetitionAwardsInput{
			EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
			CommandID:        request.Msg.GetCommandId(),
			ExpectedRevision: int(request.Msg.GetExpectedRevision()), Awards: awards,
		},
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.SaveCompetitionAwardsResponse{
		Draft: draft(saved),
	}), nil
}

// MarkCompetitionResultsReady records Producer review of one exact revision.
func (handler *Handler) MarkCompetitionResultsReady(
	ctx context.Context,
	request *connect.Request[resultsv1.MarkCompetitionResultsReadyRequest],
) (*connect.Response[resultsv1.MarkCompetitionResultsReadyResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	ready, err := handler.service.MarkReady(ctx, actor, results.MarkReadyInput{
		EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
		CommandID:        request.Msg.GetCommandId(),
		ExpectedRevision: int(request.Msg.GetExpectedRevision()),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.MarkCompetitionResultsReadyResponse{
		Draft: draft(ready),
	}), nil
}

// DesignatePrizegiving records one Producer-selected Ceremony release path.
func (handler *Handler) DesignatePrizegiving(
	ctx context.Context,
	request *connect.Request[resultsv1.DesignatePrizegivingRequest],
) (*connect.Response[resultsv1.DesignatePrizegivingResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	designated, err := handler.service.DesignatePrizegiving(
		ctx,
		actor,
		results.DesignatePrizegivingInput{
			EventID:           int(request.Msg.GetEventId()),
			CeremonySessionID: int(request.Msg.GetCeremonySessionId()),
			CommandID:         request.Msg.GetCommandId(),
		},
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.DesignatePrizegivingResponse{
		Prizegiving: prizegiving(designated),
	}), nil
}

// GetEventAwardsDraft returns the current unreleased Event Awards Draft.
func (handler *Handler) GetEventAwardsDraft(
	ctx context.Context,
	request *connect.Request[resultsv1.GetEventAwardsDraftRequest],
) (*connect.Response[resultsv1.GetEventAwardsDraftResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	found, err := handler.service.GetEventAwards(ctx, actor, int(request.Msg.GetEventId()))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.GetEventAwardsDraftResponse{
		Draft: eventAwardsDraft(found),
	}), nil
}

// SaveEventAwardsDraft appends one complete Event Awards snapshot.
func (handler *Handler) SaveEventAwardsDraft(
	ctx context.Context,
	request *connect.Request[resultsv1.SaveEventAwardsDraftRequest],
) (*connect.Response[resultsv1.SaveEventAwardsDraftResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	awards, err := eventAwardsFromProto(request.Msg.GetAwards())
	if err != nil {
		return nil, err
	}
	saved, err := handler.service.SaveEventAwards(ctx, actor, results.SaveEventAwardsInput{
		EventID: int(request.Msg.GetEventId()), CommandID: request.Msg.GetCommandId(),
		ExpectedRevision: int(request.Msg.GetExpectedRevision()), Awards: awards,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.SaveEventAwardsDraftResponse{
		Draft: eventAwardsDraft(saved),
	}), nil
}

// MarkEventAwardsReady records Producer review of one exact release path.
func (handler *Handler) MarkEventAwardsReady(
	ctx context.Context,
	request *connect.Request[resultsv1.MarkEventAwardsReadyRequest],
) (*connect.Response[resultsv1.MarkEventAwardsReadyResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	path, err := awardReleasePathFromProto(request.Msg.GetReleasePath())
	if err != nil {
		return nil, err
	}
	ready, err := handler.service.MarkEventAwardsReady(
		ctx,
		actor,
		results.MarkEventAwardsReadyInput{
			EventID: int(request.Msg.GetEventId()), CommandID: request.Msg.GetCommandId(),
			ExpectedRevision: int(request.Msg.GetExpectedRevision()), ReleasePath: path,
			ExpectedPathRevision: int(request.Msg.GetExpectedPathRevision()),
		},
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.MarkEventAwardsReadyResponse{
		Draft: eventAwardsDraft(ready),
	}), nil
}

// GetPrizegivingPlan returns one designated Prizegiving's editable plan.
func (handler *Handler) GetPrizegivingPlan(
	ctx context.Context,
	request *connect.Request[resultsv1.GetPrizegivingPlanRequest],
) (*connect.Response[resultsv1.GetPrizegivingPlanResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	found, err := handler.service.GetPrizegivingPlan(
		ctx,
		actor,
		int(request.Msg.GetEventId()),
		int(request.Msg.GetCeremonySessionId()),
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.GetPrizegivingPlanResponse{
		Plan: prizegivingPlan(found),
	}), nil
}

// SavePrizegivingPlan replaces assignments and both explicit orders.
func (handler *Handler) SavePrizegivingPlan(
	ctx context.Context,
	request *connect.Request[resultsv1.SavePrizegivingPlanRequest],
) (*connect.Response[resultsv1.SavePrizegivingPlanResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	sequence, err := resultItemsFromProto(request.Msg.GetSequence())
	if err != nil {
		return nil, err
	}
	publicationOrder, err := resultItemRefsFromProto(
		request.Msg.GetPublicationOrder(),
	)
	if err != nil {
		return nil, err
	}
	saved, err := handler.service.SavePrizegivingPlan(
		ctx,
		actor,
		results.SavePrizegivingPlanInput{
			EventID:               int(request.Msg.GetEventId()),
			CeremonySessionID:     int(request.Msg.GetCeremonySessionId()),
			CommandID:             request.Msg.GetCommandId(),
			ExpectedRevision:      int(request.Msg.GetExpectedRevision()),
			CompetitionSessionIDs: int64sToInts(request.Msg.GetCompetitionSessionIds()),
			Sequence:              sequence, PublicationOrder: publicationOrder,
			ReleasePolicy: resultsReleasePolicyFromProto(
				request.Msg.GetReleasePolicy(),
			),
			Template: resultsTextTemplateFromProto(
				request.Msg.GetResultsTextTemplate(),
			),
		},
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.SavePrizegivingPlanResponse{
		Plan: prizegivingPlan(saved),
	}), nil
}

// RunPrizegivingPreflight reports blockers or locks the exact plan.
func (handler *Handler) RunPrizegivingPreflight(
	ctx context.Context,
	request *connect.Request[resultsv1.RunPrizegivingPreflightRequest],
) (*connect.Response[resultsv1.RunPrizegivingPreflightResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	preflight, err := handler.service.RunPrizegivingPreflight(
		ctx,
		actor,
		results.RunPrizegivingPreflightInput{
			EventID:           int(request.Msg.GetEventId()),
			CeremonySessionID: int(request.Msg.GetCeremonySessionId()),
			CommandID:         request.Msg.GetCommandId(),
			ExpectedRevision:  int(request.Msg.GetExpectedRevision()),
		},
	)
	if err != nil && !errors.Is(err, results.ErrPrizegivingPreflightBlocked) {
		return nil, err
	}
	findings := make(
		[]*resultsv1.PrizegivingPreflightFinding,
		0,
		len(preflight.Findings),
	)
	for _, finding := range preflight.Findings {
		findings = append(findings, &resultsv1.PrizegivingPreflightFinding{
			Code: finding.Code, Message: finding.Message,
		})
	}
	return connect.NewResponse(&resultsv1.RunPrizegivingPreflightResponse{
		Plan: prizegivingPlan(preflight.Plan), Findings: findings,
	}), nil
}

// PreviewPrizegiving returns a side-effect-free Preview or rehearsal snapshot.
func (handler *Handler) PreviewPrizegiving(
	ctx context.Context,
	request *connect.Request[resultsv1.PreviewPrizegivingRequest],
) (*connect.Response[resultsv1.PreviewPrizegivingResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	preview, err := handler.service.PreviewPrizegiving(
		ctx,
		actor,
		int(request.Msg.GetEventId()),
		int(request.Msg.GetCeremonySessionId()),
		prizegivingPreviewModeFromProto(request.Msg.GetMode()),
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.PreviewPrizegivingResponse{
		Preview: &resultsv1.PrizegivingPreview{
			Mode:               prizegivingPreviewMode(preview.Mode),
			Watermark:          preview.Watermark,
			Plan:               prizegivingPlan(preview.Plan),
			CompetitionResults: competitionResultsDrafts(preview.CompetitionResults),
			EventAwards:        eventAwards(preview.EventAwards),
		},
	}), nil
}

// FirePrizegivingResultsCue publishes one complete locked Prizegiving set.
func (handler *Handler) FirePrizegivingResultsCue(
	ctx context.Context,
	request *connect.Request[resultsv1.FirePrizegivingResultsCueRequest],
) (*connect.Response[resultsv1.FirePrizegivingResultsCueResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	released, err := handler.service.FirePrizegivingResultsCue(
		ctx,
		actor,
		results.FirePrizegivingResultsCueInput{
			EventID:           int(request.Msg.GetEventId()),
			CeremonySessionID: int(request.Msg.GetCeremonySessionId()),
			CommandID:         request.Msg.GetCommandId(),
		},
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.FirePrizegivingResultsCueResponse{
		Publication: resultsPublication(released),
	}), nil
}

// ReleaseStandaloneResults publishes one reviewed unassigned Competition.
func (handler *Handler) ReleaseStandaloneResults(
	ctx context.Context,
	request *connect.Request[resultsv1.ReleaseStandaloneResultsRequest],
) (*connect.Response[resultsv1.ReleaseStandaloneResultsResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	released, err := handler.service.ReleaseStandaloneResults(
		ctx,
		actor,
		results.ReleaseStandaloneResultsInput{
			EventID:              int(request.Msg.GetEventId()),
			CompetitionSessionID: int(request.Msg.GetCompetitionSessionId()),
			CommandID:            request.Msg.GetCommandId(),
		},
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.ReleaseStandaloneResultsResponse{
		Publication: resultsPublication(released),
	}), nil
}

// GetResultsCorrection returns the latest crew-visible correction revision.
func (handler *Handler) GetResultsCorrection(
	ctx context.Context,
	request *connect.Request[resultsv1.GetResultsCorrectionRequest],
) (*connect.Response[resultsv1.GetResultsCorrectionResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	found, err := handler.service.GetCorrection(
		ctx,
		actor,
		int(request.Msg.GetEventId()),
		publicationScopeFromProto(request.Msg.GetScope()),
		int(request.Msg.GetScopeSessionId()),
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.GetResultsCorrectionResponse{
		Correction: resultsCorrection(found),
	}), nil
}

// SaveResultsCorrection appends one complete Draft correction proposal.
func (handler *Handler) SaveResultsCorrection(
	ctx context.Context,
	request *connect.Request[resultsv1.SaveResultsCorrectionRequest],
) (*connect.Response[resultsv1.SaveResultsCorrectionResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	proposal, err := correctionProposalFromProto(request.Msg.GetProposal())
	if err != nil {
		return nil, err
	}
	saved, err := handler.service.SaveCorrection(ctx, actor, results.SaveCorrectionInput{
		EventID:                 int(request.Msg.GetEventId()),
		Scope:                   publicationScopeFromProto(request.Msg.GetScope()),
		ScopeSessionID:          int(request.Msg.GetScopeSessionId()),
		CommandID:               request.Msg.GetCommandId(),
		ExpectedRevision:        int(request.Msg.GetExpectedRevision()),
		BasePublicationRevision: int(request.Msg.GetBasePublicationRevision()),
		PublicationOrder:        proposal.PublicationOrder,
		Items:                   proposal.Items,
		Template:                proposal.Template,
		CrewReason:              proposal.CrewReason,
		PublicNote:              proposal.PublicNote,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.SaveResultsCorrectionResponse{
		Correction: resultsCorrection(saved),
	}), nil
}

// ReviewResultsCorrection marks one exact Draft correction Ready.
func (handler *Handler) ReviewResultsCorrection(
	ctx context.Context,
	request *connect.Request[resultsv1.ReviewResultsCorrectionRequest],
) (*connect.Response[resultsv1.ReviewResultsCorrectionResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	reviewed, err := handler.service.ReviewCorrection(
		ctx,
		actor,
		correctionReviewInput(
			request.Msg.GetEventId(),
			request.Msg.GetScope(),
			request.Msg.GetScopeSessionId(),
			request.Msg.GetCommandId(),
			request.Msg.GetExpectedRevision(),
		),
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.ReviewResultsCorrectionResponse{
		Correction: resultsCorrection(reviewed),
	}), nil
}

// PublishResultsCorrection atomically publishes one exact Ready correction.
func (handler *Handler) PublishResultsCorrection(
	ctx context.Context,
	request *connect.Request[resultsv1.PublishResultsCorrectionRequest],
) (*connect.Response[resultsv1.PublishResultsCorrectionResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	published, err := handler.service.PublishCorrection(
		ctx,
		actor,
		correctionReviewInput(
			request.Msg.GetEventId(),
			request.Msg.GetScope(),
			request.Msg.GetScopeSessionId(),
			request.Msg.GetCommandId(),
			request.Msg.GetExpectedRevision(),
		),
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.PublishResultsCorrectionResponse{
		Correction:  resultsCorrection(published.Correction),
		Publication: resultsPublication(published.Publication),
	}), nil
}

// GetResultsCorrectionHistory returns immutable correction and artifact history.
func (handler *Handler) GetResultsCorrectionHistory(
	ctx context.Context,
	request *connect.Request[resultsv1.GetResultsCorrectionHistoryRequest],
) (*connect.Response[resultsv1.GetResultsCorrectionHistoryResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	found, err := handler.service.GetCorrectionHistory(
		ctx,
		actor,
		int(request.Msg.GetEventId()),
		publicationScopeFromProto(request.Msg.GetScope()),
		int(request.Msg.GetScopeSessionId()),
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.GetResultsCorrectionHistoryResponse{
		History: resultsCorrectionHistory(found),
	}), nil
}

func standingsFromProto(
	values []*resultsv1.CompetitionResultStanding,
) ([]results.Standing, error) {
	standings := make([]results.Standing, 0, len(values))
	for _, value := range values {
		if value == nil {
			return nil, results.ErrInvalidInput
		}
		score, err := scoreValueFromProto(value.GetScore())
		if err != nil {
			return nil, err
		}
		standings = append(standings, results.Standing{
			EntryID:      int(value.GetEntryId()),
			Standing:     resultStandingFromProto(value.GetStanding()),
			Placement:    int(value.GetPlacement()),
			DisplayOrder: int(value.GetDisplayOrder()),
			Score:        score,
		})
	}
	return standings, nil
}

func resultItemsFromProto(
	values []*resultsv1.ResultItem,
) ([]results.ResultItem, error) {
	items := make([]results.ResultItem, 0, len(values))
	for _, value := range values {
		if value == nil {
			return nil, results.ErrInvalidInput
		}
		kind := resultItemKindFromProto(value.GetKind())
		method := revealMethodFromProto(value.GetRevealMethod())
		if kind == "" || method == "" {
			return nil, results.ErrInvalidInput
		}
		items = append(items, results.ResultItem{
			Kind: kind, CompetitionSessionID: int(value.GetCompetitionSessionId()),
			AwardKey: value.GetAwardKey(), DisplayOrder: int(value.GetDisplayOrder()),
			RevealMethod: method,
		})
	}
	return items, nil
}

func resultItemRefsFromProto(
	values []*resultsv1.ResultItemRef,
) ([]results.ResultItemRef, error) {
	items := make([]results.ResultItemRef, 0, len(values))
	for _, value := range values {
		if value == nil {
			return nil, results.ErrInvalidInput
		}
		kind := resultItemKindFromProto(value.GetKind())
		if kind == "" {
			return nil, results.ErrInvalidInput
		}
		items = append(items, results.ResultItemRef{
			Kind: kind, CompetitionSessionID: int(value.GetCompetitionSessionId()),
			AwardKey: value.GetAwardKey(), DisplayOrder: int(value.GetDisplayOrder()),
		})
	}
	return items, nil
}

func competitionAwardsFromProto(
	values []*resultsv1.CompetitionAward,
) ([]results.Award, error) {
	awards := make([]results.Award, 0, len(values))
	for _, value := range values {
		if value == nil {
			return nil, results.ErrInvalidAward
		}
		recipients, err := awardRecipientsFromProto(value.GetRecipients())
		if err != nil {
			return nil, err
		}
		awards = append(awards, results.Award{
			Key: value.GetKey(), Name: value.GetName(), Recipients: recipients,
			Promoted: value.GetPromoted(), DisplayOrder: int(value.GetDisplayOrder()),
		})
	}
	return awards, nil
}

func eventAwardsFromProto(
	values []*resultsv1.EventAward,
) ([]results.EventAward, error) {
	awards := make([]results.EventAward, 0, len(values))
	for _, value := range values {
		if value == nil {
			return nil, results.ErrInvalidAward
		}
		recipients, err := awardRecipientsFromProto(value.GetRecipients())
		if err != nil {
			return nil, err
		}
		path, err := awardReleasePathFromProto(value.GetReleasePath())
		if err != nil {
			return nil, err
		}
		awards = append(awards, results.EventAward{
			Award: results.Award{
				Key: value.GetKey(), Name: value.GetName(), Recipients: recipients,
				DisplayOrder: int(value.GetDisplayOrder()),
			},
			ReleasePath: path,
		})
	}
	return awards, nil
}

func awardRecipientsFromProto(
	values []*resultsv1.AwardRecipient,
) ([]results.AwardRecipient, error) {
	recipients := make([]results.AwardRecipient, 0, len(values))
	for _, value := range values {
		if value == nil {
			return nil, results.ErrInvalidAward
		}
		switch recipient := value.GetRecipient().(type) {
		case *resultsv1.AwardRecipient_EntryId:
			recipients = append(recipients, results.AwardRecipient{
				EntryID: int(recipient.EntryId),
			})
		case *resultsv1.AwardRecipient_DisplayName:
			recipients = append(recipients, results.AwardRecipient{
				DisplayName: recipient.DisplayName,
			})
		default:
			return nil, results.ErrInvalidAward
		}
	}
	return recipients, nil
}

func scoreValueFromProto(value *resultsv1.ScoreValue) (results.ScoreValue, error) {
	if value == nil {
		return results.ScoreValue{}, nil
	}
	switch exact := value.GetValue().(type) {
	case *resultsv1.ScoreValue_Decimal:
		decimal := exact.Decimal
		return results.ScoreValue{Decimal: &decimal}, nil
	case *resultsv1.ScoreValue_Duration:
		if exact.Duration == nil || exact.Duration.CheckValid() != nil {
			return results.ScoreValue{}, results.ErrInvalidScore
		}
		duration := exact.Duration.AsDuration()
		roundTrip := durationpb.New(duration)
		if roundTrip.GetSeconds() != exact.Duration.GetSeconds() ||
			roundTrip.GetNanos() != exact.Duration.GetNanos() {
			return results.ScoreValue{}, results.ErrInvalidScore
		}
		return results.ScoreValue{Duration: &duration}, nil
	default:
		return results.ScoreValue{}, results.ErrInvalidScore
	}
}

func draft(value results.Draft) *resultsv1.CompetitionResultsDraft {
	standings := make([]*resultsv1.CompetitionResultStanding, 0, len(value.Standings))
	for _, standing := range value.Standings {
		found := &resultsv1.CompetitionResultStanding{
			EntryId:      int64(standing.EntryID),
			Standing:     resultStanding(standing.Standing),
			DisplayOrder: int32(standing.DisplayOrder), //nolint:gosec // Validated maximum is 10000.
			Score:        scoreValue(standing.Score),
		}
		if standing.Placement > 0 {
			placement := int64(standing.Placement)
			found.Placement = &placement
		}
		standings = append(standings, found)
	}
	result := &resultsv1.CompetitionResultsDraft{
		Id: int64(value.ID), EventId: int64(value.EventID), SessionId: int64(value.SessionID),
		Revision: int64(value.Revision), Disposition: resultsDisposition(value.Disposition),
		NoPublicCrewReason: value.NoPublicReason, PublicExplanation: value.PublicExplanation,
		Score: scorePolicy(value.Score), Standings: standings, Ready: value.Ready,
		ReadyByAccountId:   int64(value.ReadyByAccountID),
		CreatedByAccountId: int64(value.CreatedByAccountID),
		Awards:             competitionAwards(value.Awards),
	}
	if !value.ReadyAt.IsZero() {
		result.ReadyAt = timestamppb.New(value.ReadyAt)
	}
	if !value.CreatedAt.IsZero() {
		result.CreatedAt = timestamppb.New(value.CreatedAt)
	}
	return result
}

func competitionResultsDrafts(
	values []results.Draft,
) []*resultsv1.CompetitionResultsDraft {
	drafts := make([]*resultsv1.CompetitionResultsDraft, 0, len(values))
	for _, value := range values {
		drafts = append(drafts, draft(value))
	}
	return drafts
}

func competitionAwards(values []results.Award) []*resultsv1.CompetitionAward {
	awards := make([]*resultsv1.CompetitionAward, 0, len(values))
	for _, value := range values {
		awards = append(awards, &resultsv1.CompetitionAward{
			Key: value.Key, Name: value.Name, Recipients: awardRecipients(value.Recipients),
			Promoted:     value.Promoted,
			DisplayOrder: int32(value.DisplayOrder), //nolint:gosec // Award count is limited to 1000.
		})
	}
	return awards
}

func awardRecipients(values []results.AwardRecipient) []*resultsv1.AwardRecipient {
	recipients := make([]*resultsv1.AwardRecipient, 0, len(values))
	for _, value := range values {
		found := &resultsv1.AwardRecipient{}
		if value.EntryID > 0 {
			found.Recipient = &resultsv1.AwardRecipient_EntryId{EntryId: int64(value.EntryID)}
		} else {
			found.Recipient = &resultsv1.AwardRecipient_DisplayName{
				DisplayName: value.DisplayName,
			}
		}
		recipients = append(recipients, found)
	}
	return recipients
}

func awardReleasePathFromProto(
	value *resultsv1.AwardReleasePath,
) (results.AwardReleasePath, error) {
	if value == nil {
		return results.AwardReleasePath{}, results.ErrInvalidAward
	}
	kind := map[resultsv1.AwardReleasePathKind]results.AwardReleasePathKind{
		resultsv1.AwardReleasePathKind_AWARD_RELEASE_PATH_KIND_STANDALONE:  results.StandaloneRelease,
		resultsv1.AwardReleasePathKind_AWARD_RELEASE_PATH_KIND_PRIZEGIVING: results.PrizegivingRelease,
	}[value.GetKind()]
	if kind == "" {
		return results.AwardReleasePath{}, results.ErrInvalidAward
	}
	return results.AwardReleasePath{
		Kind: kind, PrizegivingSessionID: int(value.GetPrizegivingSessionId()),
	}, nil
}

func awardReleasePath(value results.AwardReleasePath) *resultsv1.AwardReleasePath {
	return &resultsv1.AwardReleasePath{
		Kind: map[results.AwardReleasePathKind]resultsv1.AwardReleasePathKind{
			results.StandaloneRelease:  resultsv1.AwardReleasePathKind_AWARD_RELEASE_PATH_KIND_STANDALONE,
			results.PrizegivingRelease: resultsv1.AwardReleasePathKind_AWARD_RELEASE_PATH_KIND_PRIZEGIVING,
		}[value.Kind],
		PrizegivingSessionId: int64(value.PrizegivingSessionID),
	}
}

func eventAwardsDraft(value results.EventAwardsDraft) *resultsv1.EventAwardsDraft {
	states := make([]*resultsv1.EventAwardPathState, 0, len(value.PathStates))
	for _, state := range value.PathStates {
		found := &resultsv1.EventAwardPathState{
			ReleasePath: awardReleasePath(state.ReleasePath), Revision: int64(state.Revision),
			Ready: state.Ready, ReadyByAccountId: int64(state.ReadyByAccountID),
		}
		if !state.ReadyAt.IsZero() {
			found.ReadyAt = timestamppb.New(state.ReadyAt)
		}
		states = append(states, found)
	}
	result := &resultsv1.EventAwardsDraft{
		Id: int64(value.ID), EventId: int64(value.EventID), Revision: int64(value.Revision),
		Awards: eventAwards(value.Awards), PathStates: states,
		CreatedByAccountId: int64(value.CreatedByAccountID),
	}
	if !value.CreatedAt.IsZero() {
		result.CreatedAt = timestamppb.New(value.CreatedAt)
	}
	return result
}

func eventAwards(values []results.EventAward) []*resultsv1.EventAward {
	awards := make([]*resultsv1.EventAward, 0, len(values))
	for _, award := range values {
		awards = append(awards, &resultsv1.EventAward{
			Key: award.Key, Name: award.Name, Recipients: awardRecipients(award.Recipients),
			DisplayOrder: int32(award.DisplayOrder), //nolint:gosec // Award count is limited to 1000.
			ReleasePath:  awardReleasePath(award.ReleasePath),
		})
	}
	return awards
}

func prizegiving(value results.Prizegiving) *resultsv1.Prizegiving {
	result := &resultsv1.Prizegiving{
		Id: int64(value.ID), EventId: int64(value.EventID),
		CeremonySessionId:  int64(value.CeremonySessionID),
		CreatedByAccountId: int64(value.CreatedByAccountID),
	}
	if !value.CreatedAt.IsZero() {
		result.CreatedAt = timestamppb.New(value.CreatedAt)
	}
	return result
}

func prizegivingPlan(value results.PrizegivingPlan) *resultsv1.PrizegivingPlan {
	competitionSessionIDs := make([]int64, 0, len(value.CompetitionSessionIDs))
	for _, competitionSessionID := range value.CompetitionSessionIDs {
		competitionSessionIDs = append(competitionSessionIDs, int64(competitionSessionID))
	}
	result := &resultsv1.PrizegivingPlan{
		Id: int64(value.ID), EventId: int64(value.EventID),
		CeremonySessionId:     int64(value.CeremonySessionID),
		Revision:              int64(value.Revision),
		CompetitionSessionIds: competitionSessionIDs,
		Sequence:              resultItems(value.Sequence),
		PublicationOrder:      resultItemRefs(value.PublicationOrder),
		ReleasePolicy:         resultsReleasePolicy(value.ReleasePolicy),
		ResultsTextTemplate:   resultsTextTemplate(value.Template),
		Locked:                value.Locked,
		LockedByAccountId:     int64(value.LockedByAccountID),
	}
	if value.Locked {
		result.PreflightLock = prizegivingPreflightLock(value.Lock)
	}
	if !value.LockedAt.IsZero() {
		result.LockedAt = timestamppb.New(value.LockedAt)
	}
	return result
}

func resultItems(values []results.ResultItem) []*resultsv1.ResultItem {
	items := make([]*resultsv1.ResultItem, 0, len(values))
	for _, value := range values {
		items = append(items, &resultsv1.ResultItem{
			Kind:                 resultItemKind(value.Kind),
			CompetitionSessionId: int64(value.CompetitionSessionID),
			AwardKey:             value.AwardKey,
			DisplayOrder:         int32(value.DisplayOrder), //nolint:gosec // Result Items are bounded to 3000.
			RevealMethod:         revealMethod(value.RevealMethod),
		})
	}
	return items
}

func resultItemRefs(values []results.ResultItemRef) []*resultsv1.ResultItemRef {
	items := make([]*resultsv1.ResultItemRef, 0, len(values))
	for _, value := range values {
		items = append(items, &resultsv1.ResultItemRef{
			Kind:                 resultItemKind(value.Kind),
			CompetitionSessionId: int64(value.CompetitionSessionID),
			AwardKey:             value.AwardKey,
			DisplayOrder:         int32(value.DisplayOrder), //nolint:gosec // Result Items are bounded to 3000.
		})
	}
	return items
}

func resultsTextTemplate(
	value results.TextTemplate,
) *resultsv1.ResultsTextTemplate {
	return &resultsv1.ResultsTextTemplate{
		Revision: int64(value.Revision),
		Source:   value.Source,
	}
}

func resultsTextTemplateFromProto(
	value *resultsv1.ResultsTextTemplate,
) results.TextTemplate {
	if value == nil {
		return results.TextTemplate{}
	}
	return results.TextTemplate{
		Revision: int(value.GetRevision()),
		Source:   value.GetSource(),
	}
}

func prizegivingPreflightLock(
	value results.PrizegivingPreflightLock,
) *resultsv1.PrizegivingPreflightLock {
	sources := make(
		[]*resultsv1.PrizegivingCompetitionLock,
		0,
		len(value.CompetitionSources),
	)
	for _, source := range value.CompetitionSources {
		sources = append(sources, &resultsv1.PrizegivingCompetitionLock{
			SessionId: int64(source.SessionID), DraftId: int64(source.DraftID),
			DraftRevision: int64(source.DraftRevision),
			Disposition:   resultsDisposition(source.Disposition),
		})
	}
	sequence := make([]*resultsv1.LockedResultItem, 0, len(value.Sequence))
	for _, item := range value.Sequence {
		sequence = append(sequence, &resultsv1.LockedResultItem{
			Item:       resultItems([]results.ResultItem{item.ResultItem})[0],
			RevealSeed: item.RevealSeed,
		})
	}
	return &resultsv1.PrizegivingPreflightLock{
		PlanRevision:             int64(value.PlanRevision),
		CompetitionSources:       sources,
		EventAwardsDraftRevision: int64(value.EventAwardsDraftRevision),
		EventAwardsPathRevision:  int64(value.EventAwardsPathRevision),
		Sequence:                 sequence, PublicationOrder: resultItemRefs(value.PublicationOrder),
		ReleasePolicy:       resultsReleasePolicy(value.ReleasePolicy),
		ResultsTextTemplate: resultsTextTemplate(value.Template),
	}
}

func resultsReleasePolicy(
	value results.ReleasePolicy,
) resultsv1.ResultsReleasePolicy {
	return map[results.ReleasePolicy]resultsv1.ResultsReleasePolicy{
		results.ResultsAllAtCue:            resultsv1.ResultsReleasePolicy_RESULTS_RELEASE_POLICY_ALL_AT_CUE,
		results.ResultsProgressiveOnReveal: resultsv1.ResultsReleasePolicy_RESULTS_RELEASE_POLICY_PROGRESSIVE_ON_REVEAL,
		results.ResultsAtCeremonyEnd:       resultsv1.ResultsReleasePolicy_RESULTS_RELEASE_POLICY_AT_CEREMONY_END,
		results.ResultsStandalone:          resultsv1.ResultsReleasePolicy_RESULTS_RELEASE_POLICY_STANDALONE,
	}[value]
}

func resultsReleasePolicyFromProto(
	value resultsv1.ResultsReleasePolicy,
) results.ReleasePolicy {
	return map[resultsv1.ResultsReleasePolicy]results.ReleasePolicy{
		resultsv1.ResultsReleasePolicy_RESULTS_RELEASE_POLICY_ALL_AT_CUE:            results.ResultsAllAtCue,
		resultsv1.ResultsReleasePolicy_RESULTS_RELEASE_POLICY_PROGRESSIVE_ON_REVEAL: results.ResultsProgressiveOnReveal,
		resultsv1.ResultsReleasePolicy_RESULTS_RELEASE_POLICY_AT_CEREMONY_END:       results.ResultsAtCeremonyEnd,
		resultsv1.ResultsReleasePolicy_RESULTS_RELEASE_POLICY_STANDALONE:            results.ResultsStandalone,
	}[value]
}

func correctionReviewInput(
	eventID int64,
	scope resultsv1.ResultsPublicationScope,
	scopeSessionID int64,
	commandID string,
	expectedRevision int64,
) results.ReviewCorrectionInput {
	return results.ReviewCorrectionInput{
		EventID: int(eventID), Scope: publicationScopeFromProto(scope),
		ScopeSessionID: int(scopeSessionID), CommandID: commandID,
		ExpectedRevision: int(expectedRevision),
	}
}

func correctionProposalFromProto(
	value *resultsv1.ResultsCorrectionProposal,
) (results.CorrectionProposal, error) {
	if value == nil {
		return results.CorrectionProposal{}, results.ErrInvalidInput
	}
	order, err := resultItemRefsFromProto(value.GetPublicationOrder())
	if err != nil {
		return results.CorrectionProposal{}, err
	}
	items, err := publicResultsItemsFromProto(value.GetItems())
	if err != nil {
		return results.CorrectionProposal{}, err
	}
	return results.CorrectionProposal{
		PublicationOrder: order,
		Items:            items,
		Template: resultsTextTemplateFromProto(
			value.GetResultsTextTemplate(),
		),
		CrewReason: value.GetCrewReason(),
		PublicNote: value.GetPublicNote(),
	}, nil
}

func resultsCorrection(
	value results.Correction,
) *resultsv1.ResultsCorrection {
	projected := &resultsv1.ResultsCorrection{
		EventId: int64(value.EventID), Scope: publicationScope(value.Scope),
		ScopeSessionId:          int64(value.ScopeSessionID),
		Revision:                int64(value.Revision),
		BasePublicationRevision: int64(value.BasePublicationRevision),
		Status:                  correctionStatus(value.Status),
		Proposal: &resultsv1.ResultsCorrectionProposal{
			PublicationOrder: resultItemRefs(value.Proposal.PublicationOrder),
			Items:            publicResultsItems(value.Proposal.Items),
			ResultsTextTemplate: resultsTextTemplate(
				value.Proposal.Template,
			),
			CrewReason: value.Proposal.CrewReason,
			PublicNote: value.Proposal.PublicNote,
		},
		PublishedResultsRevision: int64(value.PublishedResultsRevision),
		CreatedByAccountId:       int64(value.CreatedByAccountID),
	}
	if !value.CreatedAt.IsZero() {
		projected.CreatedAt = timestamppb.New(value.CreatedAt)
	}
	return projected
}

func publicResultsItemsFromProto(
	values []*resultsv1.PublicResultsItem,
) ([]results.PublicResultsItem, error) {
	items := make([]results.PublicResultsItem, 0, len(values))
	for _, value := range values {
		if value == nil {
			return nil, results.ErrInvalidInput
		}
		item := results.PublicResultsItem{
			Kind: resultItemKindFromProto(value.GetKind()),
		}
		switch concrete := value.GetItem().(type) {
		case *resultsv1.PublicResultsItem_Competition:
			item.Competition = publicCompetitionResultsFromProto(
				concrete.Competition,
			)
		case *resultsv1.PublicResultsItem_NoPublicResults:
			item.NoPublicResults = publicNoResultsFromProto(
				concrete.NoPublicResults,
			)
		case *resultsv1.PublicResultsItem_Award:
			item.Award = publicResultsAwardFromProto(concrete.Award)
		default:
			return nil, results.ErrInvalidInput
		}
		if item.Kind == "" {
			return nil, results.ErrInvalidInput
		}
		items = append(items, item)
	}
	return items, nil
}

func publicCompetitionResultsFromProto(
	value *resultsv1.PublicCompetitionResults,
) *results.PublicCompetitionResults {
	if value == nil {
		return nil
	}
	return &results.PublicCompetitionResults{
		SessionID:    int(value.GetSessionId()),
		Title:        value.GetTitle(),
		Placed:       publicResultEntriesFromProto(value.GetPlaced()),
		Unplaced:     publicResultEntriesFromProto(value.GetUnplaced()),
		Disqualified: publicResultEntriesFromProto(value.GetDisqualified()),
		Awards:       publicResultsAwardsFromProto(value.GetAwards()),
	}
}

func publicNoResultsFromProto(
	value *resultsv1.PublicNoResults,
) *results.PublicNoResults {
	if value == nil {
		return nil
	}
	return &results.PublicNoResults{
		SessionID:   int(value.GetSessionId()),
		Title:       value.GetTitle(),
		Explanation: value.GetExplanation(),
	}
}

func publicResultEntriesFromProto(
	values []*resultsv1.PublicResultEntry,
) []results.PublicResultEntry {
	entries := make([]results.PublicResultEntry, 0, len(values))
	for _, value := range values {
		if value == nil {
			entries = append(entries, results.PublicResultEntry{})
			continue
		}
		entries = append(entries, results.PublicResultEntry{
			EntryID: int(value.GetEntryId()), Name: value.GetName(),
			Placement: int(value.GetPlacement()), Score: value.GetScore(),
			Message: value.GetMessage(),
		})
	}
	return entries
}

func publicResultsAwardsFromProto(
	values []*resultsv1.PublicResultsAward,
) []results.PublicResultsAward {
	awards := make([]results.PublicResultsAward, 0, len(values))
	for _, value := range values {
		award := publicResultsAwardFromProto(value)
		if award == nil {
			awards = append(awards, results.PublicResultsAward{})
			continue
		}
		awards = append(awards, *award)
	}
	return awards
}

func publicResultsAwardFromProto(
	value *resultsv1.PublicResultsAward,
) *results.PublicResultsAward {
	if value == nil {
		return nil
	}
	return &results.PublicResultsAward{
		Key: value.GetKey(), Name: value.GetName(),
		Recipients: append([]string(nil), value.GetRecipients()...),
	}
}

func publicResultsItems(
	values []results.PublicResultsItem,
) []*resultsv1.PublicResultsItem {
	items := make([]*resultsv1.PublicResultsItem, 0, len(values))
	for _, value := range values {
		item := &resultsv1.PublicResultsItem{Kind: resultItemKind(value.Kind)}
		switch {
		case value.Competition != nil:
			item.Item = &resultsv1.PublicResultsItem_Competition{
				Competition: publicCompetitionResults(value.Competition),
			}
		case value.NoPublicResults != nil:
			item.Item = &resultsv1.PublicResultsItem_NoPublicResults{
				NoPublicResults: publicNoResults(value.NoPublicResults),
			}
		case value.Award != nil:
			item.Item = &resultsv1.PublicResultsItem_Award{
				Award: publicResultsAward(value.Award),
			}
		}
		items = append(items, item)
	}
	return items
}

func publicCompetitionResults(
	value *results.PublicCompetitionResults,
) *resultsv1.PublicCompetitionResults {
	if value == nil {
		return nil
	}
	return &resultsv1.PublicCompetitionResults{
		SessionId: int64(value.SessionID), Title: value.Title,
		Placed:       publicResultEntries(value.Placed),
		Unplaced:     publicResultEntries(value.Unplaced),
		Disqualified: publicResultEntries(value.Disqualified),
		Awards:       publicResultsAwards(value.Awards),
	}
}

func publicNoResults(
	value *results.PublicNoResults,
) *resultsv1.PublicNoResults {
	if value == nil {
		return nil
	}
	return &resultsv1.PublicNoResults{
		SessionId: int64(value.SessionID), Title: value.Title,
		Explanation: value.Explanation,
	}
}

func publicResultEntries(
	values []results.PublicResultEntry,
) []*resultsv1.PublicResultEntry {
	entries := make([]*resultsv1.PublicResultEntry, 0, len(values))
	for _, value := range values {
		entries = append(entries, &resultsv1.PublicResultEntry{
			EntryId: int64(value.EntryID), Name: value.Name,
			Placement: int64(value.Placement), Score: value.Score,
			Message: value.Message,
		})
	}
	return entries
}

func publicResultsAwards(
	values []results.PublicResultsAward,
) []*resultsv1.PublicResultsAward {
	awards := make([]*resultsv1.PublicResultsAward, 0, len(values))
	for index := range values {
		awards = append(awards, publicResultsAward(&values[index]))
	}
	return awards
}

func publicResultsAward(
	value *results.PublicResultsAward,
) *resultsv1.PublicResultsAward {
	if value == nil {
		return nil
	}
	return &resultsv1.PublicResultsAward{
		Key: value.Key, Name: value.Name,
		Recipients: append([]string(nil), value.Recipients...),
	}
}

func resultsCorrectionHistory(
	value results.CorrectionHistory,
) *resultsv1.ResultsCorrectionHistory {
	projected := &resultsv1.ResultsCorrectionHistory{
		Corrections: make(
			[]*resultsv1.ResultsCorrection,
			0,
			len(value.Corrections),
		),
		Publications: make(
			[]*resultsv1.ResultsPublicationHistoryRevision,
			0,
			len(value.Publications),
		),
	}
	for _, correction := range value.Corrections {
		projected.Corrections = append(
			projected.Corrections,
			resultsCorrection(correction),
		)
	}
	for _, publication := range value.Publications {
		item := &resultsv1.ResultsPublicationHistoryRevision{
			Revision:            int64(publication.Revision),
			Status:              resultsPublicationStatus(publication.Status),
			PublicationOrder:    resultItemRefs(publication.PublicationOrder),
			ResultsTextTemplate: resultsTextTemplate(publication.Template),
			RenderedHtml:        publication.HTML,
			RenderedText:        publication.Text,
			RenderedJson:        publication.JSON,
			ResultsCorrectionRevision: int64(
				publication.ResultsCorrectionRevision,
			),
		}
		if !publication.CreatedAt.IsZero() {
			item.CreatedAt = timestamppb.New(publication.CreatedAt)
		}
		projected.Publications = append(projected.Publications, item)
	}
	return projected
}

func publicationScope(
	value results.PublicationScope,
) resultsv1.ResultsPublicationScope {
	return map[results.PublicationScope]resultsv1.ResultsPublicationScope{
		results.PublicationScopePrizegiving: resultsv1.ResultsPublicationScope_RESULTS_PUBLICATION_SCOPE_PRIZEGIVING,
		results.PublicationScopeStandalone:  resultsv1.ResultsPublicationScope_RESULTS_PUBLICATION_SCOPE_STANDALONE,
	}[value]
}

func publicationScopeFromProto(
	value resultsv1.ResultsPublicationScope,
) results.PublicationScope {
	return map[resultsv1.ResultsPublicationScope]results.PublicationScope{
		resultsv1.ResultsPublicationScope_RESULTS_PUBLICATION_SCOPE_PRIZEGIVING: results.PublicationScopePrizegiving,
		resultsv1.ResultsPublicationScope_RESULTS_PUBLICATION_SCOPE_STANDALONE:  results.PublicationScopeStandalone,
	}[value]
}

func correctionStatus(
	value results.CorrectionStatus,
) resultsv1.ResultsCorrectionStatus {
	return map[results.CorrectionStatus]resultsv1.ResultsCorrectionStatus{
		results.CorrectionDraft:     resultsv1.ResultsCorrectionStatus_RESULTS_CORRECTION_STATUS_DRAFT,
		results.CorrectionReady:     resultsv1.ResultsCorrectionStatus_RESULTS_CORRECTION_STATUS_READY,
		results.CorrectionPublished: resultsv1.ResultsCorrectionStatus_RESULTS_CORRECTION_STATUS_PUBLISHED,
	}[value]
}

func resultsPublicationStatus(
	value results.PublicationStatus,
) resultsv1.ResultsPublicationStatus {
	return map[results.PublicationStatus]resultsv1.ResultsPublicationStatus{
		results.ResultsPublicationPartial: resultsv1.ResultsPublicationStatus_RESULTS_PUBLICATION_STATUS_PARTIAL,
		results.ResultsPublicationFinal:   resultsv1.ResultsPublicationStatus_RESULTS_PUBLICATION_STATUS_FINAL,
	}[value]
}

func resultsPublication(value results.Publication) *resultsv1.ResultsPublication {
	return &resultsv1.ResultsPublication{
		Revision: int64(value.Revision),
		Status:   resultsPublicationStatus(value.Status),
		Items:    resultItemRefs(value.Items),
	}
}

func resultItemKind(value results.ResultItemKind) resultsv1.ResultItemKind {
	return map[results.ResultItemKind]resultsv1.ResultItemKind{
		results.ResultItemCompetition:      resultsv1.ResultItemKind_RESULT_ITEM_KIND_COMPETITION_RESULTS,
		results.ResultItemNoPublicResults:  resultsv1.ResultItemKind_RESULT_ITEM_KIND_NO_PUBLIC_RESULTS,
		results.ResultItemCompetitionAward: resultsv1.ResultItemKind_RESULT_ITEM_KIND_COMPETITION_AWARD,
		results.ResultItemEventAward:       resultsv1.ResultItemKind_RESULT_ITEM_KIND_EVENT_AWARD,
	}[value]
}

func resultItemKindFromProto(value resultsv1.ResultItemKind) results.ResultItemKind {
	return map[resultsv1.ResultItemKind]results.ResultItemKind{
		resultsv1.ResultItemKind_RESULT_ITEM_KIND_COMPETITION_RESULTS: results.ResultItemCompetition,
		resultsv1.ResultItemKind_RESULT_ITEM_KIND_NO_PUBLIC_RESULTS:   results.ResultItemNoPublicResults,
		resultsv1.ResultItemKind_RESULT_ITEM_KIND_COMPETITION_AWARD:   results.ResultItemCompetitionAward,
		resultsv1.ResultItemKind_RESULT_ITEM_KIND_EVENT_AWARD:         results.ResultItemEventAward,
	}[value]
}

func revealMethod(value results.RevealMethod) resultsv1.RevealMethod {
	return map[results.RevealMethod]resultsv1.RevealMethod{
		results.RevealStatic:            resultsv1.RevealMethod_REVEAL_METHOD_STATIC_RESULT,
		results.RevealSequentialPodium:  resultsv1.RevealMethod_REVEAL_METHOD_SEQUENTIAL_PODIUM,
		results.RevealAnimatedScoreBars: resultsv1.RevealMethod_REVEAL_METHOD_ANIMATED_SCORE_BARS,
	}[value]
}

func revealMethodFromProto(value resultsv1.RevealMethod) results.RevealMethod {
	if method := map[resultsv1.RevealMethod]results.RevealMethod{
		resultsv1.RevealMethod_REVEAL_METHOD_STATIC_RESULT:       results.RevealStatic,
		resultsv1.RevealMethod_REVEAL_METHOD_SEQUENTIAL_PODIUM:   results.RevealSequentialPodium,
		resultsv1.RevealMethod_REVEAL_METHOD_ANIMATED_SCORE_BARS: results.RevealAnimatedScoreBars,
	}[value]; method != "" {
		return method
	}
	return results.RevealMethod("Invalid")
}

func prizegivingPreviewMode(
	value results.PrizegivingPreviewMode,
) resultsv1.PrizegivingPreviewMode {
	return map[results.PrizegivingPreviewMode]resultsv1.PrizegivingPreviewMode{
		results.PrizegivingPreviewModePreview:   resultsv1.PrizegivingPreviewMode_PRIZEGIVING_PREVIEW_MODE_PREVIEW,
		results.PrizegivingPreviewModeRehearsal: resultsv1.PrizegivingPreviewMode_PRIZEGIVING_PREVIEW_MODE_REHEARSAL,
	}[value]
}

func prizegivingPreviewModeFromProto(
	value resultsv1.PrizegivingPreviewMode,
) results.PrizegivingPreviewMode {
	return map[resultsv1.PrizegivingPreviewMode]results.PrizegivingPreviewMode{
		resultsv1.PrizegivingPreviewMode_PRIZEGIVING_PREVIEW_MODE_PREVIEW:   results.PrizegivingPreviewModePreview,
		resultsv1.PrizegivingPreviewMode_PRIZEGIVING_PREVIEW_MODE_REHEARSAL: results.PrizegivingPreviewModeRehearsal,
	}[value]
}

func int64sToInts(values []int64) []int {
	result := make([]int, 0, len(values))
	for _, value := range values {
		result = append(result, int(value))
	}
	return result
}

func scoreValue(value results.ScoreValue) *resultsv1.ScoreValue {
	switch {
	case value.Decimal != nil:
		return &resultsv1.ScoreValue{
			Value: &resultsv1.ScoreValue_Decimal{Decimal: *value.Decimal},
		}
	case value.Duration != nil:
		return &resultsv1.ScoreValue{
			Value: &resultsv1.ScoreValue_Duration{Duration: durationpb.New(*value.Duration)},
		}
	default:
		return nil
	}
}

func scorePolicy(value results.ScorePolicy) *resultsv1.ScorePolicy {
	return &resultsv1.ScorePolicy{
		Type: scoreType(value.Type), Visibility: scoreVisibility(value.Visibility),
		Unit:           value.Unit,
		Precision:      int32(value.Precision), //nolint:gosec // Domain precision is limited to 0..9.
		Requirement:    scoreRequirement(value.Requirement),
		Interpretation: scoreInterpretation(value.Interpretation),
	}
}

func scorePolicyFromProto(value *resultsv1.ScorePolicy) results.ScorePolicy {
	if value == nil {
		return results.ScorePolicy{}
	}
	return results.ScorePolicy{
		Type:       scoreTypeFromProto(value.GetType()),
		Visibility: scoreVisibilityFromProto(value.GetVisibility()),
		Unit:       value.GetUnit(), Precision: int(value.GetPrecision()),
		Requirement:    scoreRequirementFromProto(value.GetRequirement()),
		Interpretation: scoreInterpretationFromProto(value.GetInterpretation()),
	}
}

func resultsDisposition(value results.Disposition) resultsv1.ResultsDisposition {
	return map[results.Disposition]resultsv1.ResultsDisposition{
		results.Pending:         resultsv1.ResultsDisposition_RESULTS_DISPOSITION_PENDING,
		results.Publish:         resultsv1.ResultsDisposition_RESULTS_DISPOSITION_PUBLISH,
		results.NoPublicResults: resultsv1.ResultsDisposition_RESULTS_DISPOSITION_NO_PUBLIC_RESULTS,
	}[value]
}

func resultsDispositionFromProto(value resultsv1.ResultsDisposition) results.Disposition {
	return map[resultsv1.ResultsDisposition]results.Disposition{
		resultsv1.ResultsDisposition_RESULTS_DISPOSITION_PENDING:           results.Pending,
		resultsv1.ResultsDisposition_RESULTS_DISPOSITION_PUBLISH:           results.Publish,
		resultsv1.ResultsDisposition_RESULTS_DISPOSITION_NO_PUBLIC_RESULTS: results.NoPublicResults,
	}[value]
}

func resultStanding(value results.ResultStanding) resultsv1.ResultStanding {
	return map[results.ResultStanding]resultsv1.ResultStanding{
		results.Placed:   resultsv1.ResultStanding_RESULT_STANDING_PLACED,
		results.Unplaced: resultsv1.ResultStanding_RESULT_STANDING_UNPLACED,
	}[value]
}

func resultStandingFromProto(value resultsv1.ResultStanding) results.ResultStanding {
	return map[resultsv1.ResultStanding]results.ResultStanding{
		resultsv1.ResultStanding_RESULT_STANDING_PLACED:   results.Placed,
		resultsv1.ResultStanding_RESULT_STANDING_UNPLACED: results.Unplaced,
	}[value]
}

func scoreType(value results.ScoreType) resultsv1.ScoreType {
	return map[results.ScoreType]resultsv1.ScoreType{
		results.None:     resultsv1.ScoreType_SCORE_TYPE_NONE,
		results.Decimal:  resultsv1.ScoreType_SCORE_TYPE_DECIMAL,
		results.Duration: resultsv1.ScoreType_SCORE_TYPE_DURATION,
	}[value]
}

func scoreTypeFromProto(value resultsv1.ScoreType) results.ScoreType {
	return map[resultsv1.ScoreType]results.ScoreType{
		resultsv1.ScoreType_SCORE_TYPE_NONE:     results.None,
		resultsv1.ScoreType_SCORE_TYPE_DECIMAL:  results.Decimal,
		resultsv1.ScoreType_SCORE_TYPE_DURATION: results.Duration,
	}[value]
}

func scoreVisibility(value results.ScoreVisibility) resultsv1.ScoreVisibility {
	return map[results.ScoreVisibility]resultsv1.ScoreVisibility{
		results.ScorePublic:   resultsv1.ScoreVisibility_SCORE_VISIBILITY_PUBLIC,
		results.ScoreCrewOnly: resultsv1.ScoreVisibility_SCORE_VISIBILITY_CREW_ONLY,
	}[value]
}

func scoreVisibilityFromProto(value resultsv1.ScoreVisibility) results.ScoreVisibility {
	return map[resultsv1.ScoreVisibility]results.ScoreVisibility{
		resultsv1.ScoreVisibility_SCORE_VISIBILITY_PUBLIC:    results.ScorePublic,
		resultsv1.ScoreVisibility_SCORE_VISIBILITY_CREW_ONLY: results.ScoreCrewOnly,
	}[value]
}

func scoreRequirement(value results.ScoreRequirement) resultsv1.ScoreRequirement {
	return map[results.ScoreRequirement]resultsv1.ScoreRequirement{
		results.ScoreOptional: resultsv1.ScoreRequirement_SCORE_REQUIREMENT_OPTIONAL,
		results.ScoreRequired: resultsv1.ScoreRequirement_SCORE_REQUIREMENT_REQUIRED,
	}[value]
}

func scoreRequirementFromProto(value resultsv1.ScoreRequirement) results.ScoreRequirement {
	return map[resultsv1.ScoreRequirement]results.ScoreRequirement{
		resultsv1.ScoreRequirement_SCORE_REQUIREMENT_OPTIONAL: results.ScoreOptional,
		resultsv1.ScoreRequirement_SCORE_REQUIREMENT_REQUIRED: results.ScoreRequired,
	}[value]
}

func scoreInterpretation(value results.ScoreInterpretation) resultsv1.ScoreInterpretation {
	return map[results.ScoreInterpretation]resultsv1.ScoreInterpretation{
		results.HigherWins:    resultsv1.ScoreInterpretation_SCORE_INTERPRETATION_HIGHER_WINS,
		results.LowerWins:     resultsv1.ScoreInterpretation_SCORE_INTERPRETATION_LOWER_WINS,
		results.Informational: resultsv1.ScoreInterpretation_SCORE_INTERPRETATION_INFORMATIONAL,
	}[value]
}

func scoreInterpretationFromProto(
	value resultsv1.ScoreInterpretation,
) results.ScoreInterpretation {
	return map[resultsv1.ScoreInterpretation]results.ScoreInterpretation{
		resultsv1.ScoreInterpretation_SCORE_INTERPRETATION_HIGHER_WINS:   results.HigherWins,
		resultsv1.ScoreInterpretation_SCORE_INTERPRETATION_LOWER_WINS:    results.LowerWins,
		resultsv1.ScoreInterpretation_SCORE_INTERPRETATION_INFORMATIONAL: results.Informational,
	}[value]
}

func connectError(err error) error {
	switch {
	case errors.Is(err, results.ErrViewRequired),
		errors.Is(err, results.ErrManageRequired),
		errors.Is(err, results.ErrProducerRequired):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, results.ErrCompetitionNotFound),
		errors.Is(err, results.ErrEventNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, results.ErrRevisionConflict),
		errors.Is(err, results.ErrEventAwardsRevision),
		errors.Is(err, results.ErrPrizegivingPlanRevision),
		errors.Is(err, results.ErrCorrectionRevision),
		errors.Is(err, results.ErrCommandConflict):
		return connect.NewError(connect.CodeAborted, err)
	case errors.Is(err, results.ErrIncomplete),
		errors.Is(err, results.ErrCompetitionRanking),
		errors.Is(err, results.ErrUnplacedOrder),
		errors.Is(err, results.ErrDisposition),
		errors.Is(err, results.ErrScoreRequired),
		errors.Is(err, results.ErrCompetitionPrizegivingAssignment),
		errors.Is(err, results.ErrPrizegivingLocked),
		errors.Is(err, results.ErrPrizegivingPreflightBlocked),
		errors.Is(err, results.ErrPrizegivingPreflightRequired),
		errors.Is(err, results.ErrResultsReleasePolicy),
		errors.Is(err, results.ErrResultsPublicationRequired),
		errors.Is(err, results.ErrCorrectionTransition),
		errors.Is(err, results.ErrCorrectionBase):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, command.ErrInvalidID),
		errors.Is(err, results.ErrInvalidInput),
		errors.Is(err, results.ErrEntryOutsideCompetition),
		errors.Is(err, results.ErrAwardEntryOutsideScope),
		errors.Is(err, results.ErrEventAwardPath),
		errors.Is(err, results.ErrPrizegivingSession),
		errors.Is(err, results.ErrCrewReasonRequired),
		errors.Is(err, results.ErrInvalidScore),
		errors.Is(err, results.ErrInvalidAward),
		errors.Is(err, results.ErrResultsCorrection):
		return connect.NewError(connect.CodeInvalidArgument, err)
	default:
		return connect.NewError(connect.CodeInternal, errors.New("results request failed"))
	}
}
