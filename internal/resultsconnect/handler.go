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
	"github.com/dotwaffle/beamers/internal/resultsprojection"
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
func ErrorInterceptor() connect.Interceptor {
	return connectapi.ErrorInterceptor(connectError)
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
	projected, err := resultsprojection.Draft(&found)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.GetCompetitionResultsDraftResponse{
		Draft: projected,
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
	projected, err := resultsprojection.Draft(&saved)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.SaveCompetitionResultsDraftResponse{
		Draft: projected,
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
	projected, err := resultsprojection.Draft(&saved)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.SaveCompetitionAwardsResponse{
		Draft: projected,
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
	projected, err := resultsprojection.Draft(&ready)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.MarkCompetitionResultsReadyResponse{
		Draft: projected,
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
	projected, err := resultsprojection.EventAwardsDraft(&found)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.GetEventAwardsDraftResponse{
		Draft: projected,
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
	projected, err := resultsprojection.EventAwardsDraft(&saved)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.SaveEventAwardsDraftResponse{
		Draft: projected,
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
	projected, err := resultsprojection.EventAwardsDraft(&ready)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.MarkEventAwardsReadyResponse{
		Draft: projected,
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
	projected, err := prizegivingPlan(found)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.GetPrizegivingPlanResponse{
		Plan: projected,
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
			CompetitionSessionIDs: connectapi.Ints(request.Msg.GetCompetitionSessionIds()),
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
	projected, err := prizegivingPlan(saved)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.SavePrizegivingPlanResponse{
		Plan: projected,
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
	projected, projectionErr := prizegivingPlan(preflight.Plan)
	if projectionErr != nil {
		return nil, projectionErr
	}
	return connect.NewResponse(&resultsv1.RunPrizegivingPreflightResponse{
		Plan: projected, Findings: findings,
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
	drafts, err := resultsprojection.Drafts(preview.CompetitionResults)
	if err != nil {
		return nil, err
	}
	awards, err := resultsprojection.EventAwards(preview.EventAwards)
	if err != nil {
		return nil, err
	}
	plan, err := prizegivingPlan(preview.Plan)
	if err != nil {
		return nil, err
	}
	mode, err := resultsprojection.PreviewMode(string(preview.Mode))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.PreviewPrizegivingResponse{
		Preview: &resultsv1.PrizegivingPreview{
			Mode: mode, Watermark: preview.Watermark,
			Plan:               plan,
			CompetitionResults: drafts, EventAwards: awards,
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
	publication, err := resultsPublication(released)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.FirePrizegivingResultsCueResponse{
		Publication: publication,
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
	publication, err := resultsPublication(released)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.ReleaseStandaloneResultsResponse{
		Publication: publication,
	}), nil
}

// PreflightStandaloneEventAwards reports blockers without changing release state.
func (handler *Handler) PreflightStandaloneEventAwards(
	ctx context.Context,
	request *connect.Request[resultsv1.PreflightStandaloneEventAwardsRequest],
) (*connect.Response[resultsv1.PreflightStandaloneEventAwardsResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	preflight, err := handler.service.PreflightStandaloneEventAwards(
		ctx,
		actor,
		int(request.Msg.GetEventId()),
	)
	if err != nil {
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
	lock, err := prizegivingPreflightLock(preflight.Lock)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.PreflightStandaloneEventAwardsResponse{
		Lock: lock, Findings: findings,
	}), nil
}

// ReleaseStandaloneEventAwards publishes one exact reviewed standalone Award set.
func (handler *Handler) ReleaseStandaloneEventAwards(
	ctx context.Context,
	request *connect.Request[resultsv1.ReleaseStandaloneEventAwardsRequest],
) (*connect.Response[resultsv1.ReleaseStandaloneEventAwardsResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	released, err := handler.service.ReleaseStandaloneEventAwards(
		ctx,
		actor,
		results.ReleaseStandaloneEventAwardsInput{
			EventID: int(request.Msg.GetEventId()), CommandID: request.Msg.GetCommandId(),
			ExpectedDraftRevision: int(request.Msg.GetExpectedDraftRevision()),
			ExpectedPathRevision:  int(request.Msg.GetExpectedPathRevision()),
		},
	)
	if err != nil {
		return nil, err
	}
	publication, err := resultsPublication(released)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.ReleaseStandaloneEventAwardsResponse{
		Publication: publication,
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
	correction, err := resultsCorrection(found)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.GetResultsCorrectionResponse{
		Correction: correction,
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
	correction, err := resultsCorrection(saved)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.SaveResultsCorrectionResponse{
		Correction: correction,
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
	correction, err := resultsCorrection(reviewed)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.ReviewResultsCorrectionResponse{
		Correction: correction,
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
	correction, err := resultsCorrection(published.Correction)
	if err != nil {
		return nil, err
	}
	publication, err := resultsPublication(published.Publication)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.PublishResultsCorrectionResponse{
		Correction: correction, Publication: publication,
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
	history, err := resultsCorrectionHistory(found)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&resultsv1.GetResultsCorrectionHistoryResponse{
		History: history,
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

func prizegivingPlan(value results.PrizegivingPlan) (*resultsv1.PrizegivingPlan, error) {
	competitionSessionIDs := make([]int64, 0, len(value.CompetitionSessionIDs))
	for _, competitionSessionID := range value.CompetitionSessionIDs {
		competitionSessionIDs = append(competitionSessionIDs, int64(competitionSessionID))
	}
	sequence, err := resultsprojection.Items(value.Sequence)
	if err != nil {
		return nil, err
	}
	publicationOrder, err := resultsprojection.ItemRefs(value.PublicationOrder)
	if err != nil {
		return nil, err
	}
	releasePolicy, err := resultsprojection.ReleasePolicy(string(value.ReleasePolicy))
	if err != nil {
		return nil, err
	}
	result := &resultsv1.PrizegivingPlan{
		Id: int64(value.ID), EventId: int64(value.EventID),
		CeremonySessionId:     int64(value.CeremonySessionID),
		Revision:              int64(value.Revision),
		CompetitionSessionIds: competitionSessionIDs,
		Sequence:              sequence,
		PublicationOrder:      publicationOrder,
		ReleasePolicy:         releasePolicy,
		ResultsTextTemplate:   resultsTextTemplate(value.Template),
		Locked:                value.Locked,
		LockedByAccountId:     int64(value.LockedByAccountID),
	}
	if value.Locked {
		result.PreflightLock, err = prizegivingPreflightLock(value.Lock)
		if err != nil {
			return nil, err
		}
	}
	if !value.LockedAt.IsZero() {
		result.LockedAt = timestamppb.New(value.LockedAt)
	}
	return result, nil
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
) (*resultsv1.PrizegivingPreflightLock, error) {
	sources := make(
		[]*resultsv1.PrizegivingCompetitionLock,
		0,
		len(value.CompetitionSources),
	)
	for _, source := range value.CompetitionSources {
		disposition, err := resultsprojection.Disposition(string(source.Disposition))
		if err != nil {
			return nil, err
		}
		sources = append(sources, &resultsv1.PrizegivingCompetitionLock{
			SessionId: int64(source.SessionID), DraftId: int64(source.DraftID),
			DraftRevision: int64(source.DraftRevision),
			Disposition:   disposition,
		})
	}
	sequence := make([]*resultsv1.LockedResultItem, 0, len(value.Sequence))
	for _, item := range value.Sequence {
		projected, err := resultsprojection.Item(item.ResultItem)
		if err != nil {
			return nil, err
		}
		sequence = append(sequence, &resultsv1.LockedResultItem{
			Item: projected, RevealSeed: item.RevealSeed,
		})
	}
	publicationOrder, err := resultsprojection.ItemRefs(value.PublicationOrder)
	if err != nil {
		return nil, err
	}
	releasePolicy, err := resultsprojection.ReleasePolicy(string(value.ReleasePolicy))
	if err != nil {
		return nil, err
	}
	return &resultsv1.PrizegivingPreflightLock{
		PlanRevision:             int64(value.PlanRevision),
		CompetitionSources:       sources,
		EventAwardsDraftRevision: int64(value.EventAwardsDraftRevision),
		EventAwardsPathRevision:  int64(value.EventAwardsPathRevision),
		Sequence:                 sequence, PublicationOrder: publicationOrder,
		ReleasePolicy:       releasePolicy,
		ResultsTextTemplate: resultsTextTemplate(value.Template),
	}, nil
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
) (*resultsv1.ResultsCorrection, error) {
	publicationOrder, err := resultsprojection.ItemRefs(value.Proposal.PublicationOrder)
	if err != nil {
		return nil, err
	}
	items, err := publicResultsItems(value.Proposal.Items)
	if err != nil {
		return nil, err
	}
	scope, err := resultsprojection.PublicationScope(string(value.Scope))
	if err != nil {
		return nil, err
	}
	status, err := resultsprojection.CorrectionStatus(string(value.Status))
	if err != nil {
		return nil, err
	}
	projected := &resultsv1.ResultsCorrection{
		EventId: int64(value.EventID), Scope: scope,
		ScopeSessionId:          int64(value.ScopeSessionID),
		Revision:                int64(value.Revision),
		BasePublicationRevision: int64(value.BasePublicationRevision),
		Status:                  status,
		Proposal: &resultsv1.ResultsCorrectionProposal{
			PublicationOrder: publicationOrder,
			Items:            items,
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
	return projected, nil
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
) ([]*resultsv1.PublicResultsItem, error) {
	items := make([]*resultsv1.PublicResultsItem, 0, len(values))
	for _, value := range values {
		kind, err := resultsprojection.ItemKind(string(value.Kind))
		if err != nil {
			return nil, err
		}
		item := &resultsv1.PublicResultsItem{Kind: kind}
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
	return items, nil
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
) (*resultsv1.ResultsCorrectionHistory, error) {
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
		projectedCorrection, err := resultsCorrection(correction)
		if err != nil {
			return nil, err
		}
		projected.Corrections = append(
			projected.Corrections,
			projectedCorrection,
		)
	}
	for _, publication := range value.Publications {
		publicationOrder, err := resultsprojection.ItemRefs(publication.PublicationOrder)
		if err != nil {
			return nil, err
		}
		status, err := resultsprojection.PublicationStatus(string(publication.Status))
		if err != nil {
			return nil, err
		}
		item := &resultsv1.ResultsPublicationHistoryRevision{
			Revision:            int64(publication.Revision),
			Status:              status,
			PublicationOrder:    publicationOrder,
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
	return projected, nil
}

func publicationScopeFromProto(
	value resultsv1.ResultsPublicationScope,
) results.PublicationScope {
	return map[resultsv1.ResultsPublicationScope]results.PublicationScope{
		resultsv1.ResultsPublicationScope_RESULTS_PUBLICATION_SCOPE_PRIZEGIVING:  results.PublicationScopePrizegiving,
		resultsv1.ResultsPublicationScope_RESULTS_PUBLICATION_SCOPE_STANDALONE:   results.PublicationScopeStandalone,
		resultsv1.ResultsPublicationScope_RESULTS_PUBLICATION_SCOPE_EVENT_AWARDS: results.PublicationScopeEventAwards,
	}[value]
}

func resultsPublication(value results.Publication) (*resultsv1.ResultsPublication, error) {
	items, err := resultsprojection.ItemRefs(value.Items)
	if err != nil {
		return nil, err
	}
	status, err := resultsprojection.PublicationStatus(string(value.Status))
	if err != nil {
		return nil, err
	}
	return &resultsv1.ResultsPublication{
		Revision: int64(value.Revision),
		Status:   status,
		Items:    items,
	}, nil
}

func resultItemKindFromProto(value resultsv1.ResultItemKind) results.ResultItemKind {
	return map[resultsv1.ResultItemKind]results.ResultItemKind{
		resultsv1.ResultItemKind_RESULT_ITEM_KIND_COMPETITION_RESULTS: results.ResultItemCompetition,
		resultsv1.ResultItemKind_RESULT_ITEM_KIND_NO_PUBLIC_RESULTS:   results.ResultItemNoPublicResults,
		resultsv1.ResultItemKind_RESULT_ITEM_KIND_COMPETITION_AWARD:   results.ResultItemCompetitionAward,
		resultsv1.ResultItemKind_RESULT_ITEM_KIND_EVENT_AWARD:         results.ResultItemEventAward,
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

func prizegivingPreviewModeFromProto(
	value resultsv1.PrizegivingPreviewMode,
) results.PrizegivingPreviewMode {
	return map[resultsv1.PrizegivingPreviewMode]results.PrizegivingPreviewMode{
		resultsv1.PrizegivingPreviewMode_PRIZEGIVING_PREVIEW_MODE_PREVIEW:   results.PrizegivingPreviewModePreview,
		resultsv1.PrizegivingPreviewMode_PRIZEGIVING_PREVIEW_MODE_REHEARSAL: results.PrizegivingPreviewModeRehearsal,
	}[value]
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

func resultsDispositionFromProto(value resultsv1.ResultsDisposition) results.Disposition {
	return map[resultsv1.ResultsDisposition]results.Disposition{
		resultsv1.ResultsDisposition_RESULTS_DISPOSITION_PENDING:           results.Pending,
		resultsv1.ResultsDisposition_RESULTS_DISPOSITION_PUBLISH:           results.Publish,
		resultsv1.ResultsDisposition_RESULTS_DISPOSITION_NO_PUBLIC_RESULTS: results.NoPublicResults,
	}[value]
}

func resultStandingFromProto(value resultsv1.ResultStanding) results.ResultStanding {
	return map[resultsv1.ResultStanding]results.ResultStanding{
		resultsv1.ResultStanding_RESULT_STANDING_PLACED:   results.Placed,
		resultsv1.ResultStanding_RESULT_STANDING_UNPLACED: results.Unplaced,
	}[value]
}

func scoreTypeFromProto(value resultsv1.ScoreType) results.ScoreType {
	return map[resultsv1.ScoreType]results.ScoreType{
		resultsv1.ScoreType_SCORE_TYPE_NONE:     results.None,
		resultsv1.ScoreType_SCORE_TYPE_DECIMAL:  results.Decimal,
		resultsv1.ScoreType_SCORE_TYPE_DURATION: results.Duration,
	}[value]
}

func scoreVisibilityFromProto(value resultsv1.ScoreVisibility) results.ScoreVisibility {
	return map[resultsv1.ScoreVisibility]results.ScoreVisibility{
		resultsv1.ScoreVisibility_SCORE_VISIBILITY_PUBLIC:    results.ScorePublic,
		resultsv1.ScoreVisibility_SCORE_VISIBILITY_CREW_ONLY: results.ScoreCrewOnly,
	}[value]
}

func scoreRequirementFromProto(value resultsv1.ScoreRequirement) results.ScoreRequirement {
	return map[resultsv1.ScoreRequirement]results.ScoreRequirement{
		resultsv1.ScoreRequirement_SCORE_REQUIREMENT_OPTIONAL: results.ScoreOptional,
		resultsv1.ScoreRequirement_SCORE_REQUIREMENT_REQUIRED: results.ScoreRequired,
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
