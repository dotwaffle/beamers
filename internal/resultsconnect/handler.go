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
	awards := make([]*resultsv1.EventAward, 0, len(value.Awards))
	for _, award := range value.Awards {
		awards = append(awards, &resultsv1.EventAward{
			Key: award.Key, Name: award.Name, Recipients: awardRecipients(award.Recipients),
			DisplayOrder: int32(award.DisplayOrder), //nolint:gosec // Award count is limited to 1000.
			ReleasePath:  awardReleasePath(award.ReleasePath),
		})
	}
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
		Awards: awards, PathStates: states, CreatedByAccountId: int64(value.CreatedByAccountID),
	}
	if !value.CreatedAt.IsZero() {
		result.CreatedAt = timestamppb.New(value.CreatedAt)
	}
	return result
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
		errors.Is(err, results.ErrCommandConflict):
		return connect.NewError(connect.CodeAborted, err)
	case errors.Is(err, results.ErrIncomplete),
		errors.Is(err, results.ErrCompetitionRanking),
		errors.Is(err, results.ErrUnplacedOrder),
		errors.Is(err, results.ErrDisposition),
		errors.Is(err, results.ErrScoreRequired):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, command.ErrInvalidID),
		errors.Is(err, results.ErrInvalidInput),
		errors.Is(err, results.ErrEntryOutsideCompetition),
		errors.Is(err, results.ErrAwardEntryOutsideScope),
		errors.Is(err, results.ErrEventAwardPath),
		errors.Is(err, results.ErrPrizegivingSession),
		errors.Is(err, results.ErrCrewReasonRequired),
		errors.Is(err, results.ErrInvalidScore),
		errors.Is(err, results.ErrInvalidAward):
		return connect.NewError(connect.CodeInvalidArgument, err)
	default:
		return connect.NewError(connect.CodeInternal, errors.New("results request failed"))
	}
}
