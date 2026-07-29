package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/a-h/templ"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/dotwaffle/beamers/gen/beamers/results/v1/resultsv1connect"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/competition"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/frontend"
	"github.com/dotwaffle/beamers/internal/results"
	"github.com/dotwaffle/beamers/internal/resultsconnect"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/viewer"
)

const maxResultsRPCBodyBytes = 128 << 10

type backstageResultsHandlers struct {
	browser     frontendHandlers
	events      *events.Service
	rundown     *rundown.Queries
	competition *competition.Service
	results     *results.Service
}

func registerResultsFrontendRoutes(
	mux *routeMux,
	authentication *auth.Service,
	eventService *events.Service,
	rundownQueries *rundown.Queries,
	competitionService *competition.Service,
	resultsService *results.Service,
	logger *slog.Logger,
) {
	handlers := backstageResultsHandlers{
		browser: frontendHandlers{
			authentication: authentication,
			logger:         logger,
			random:         rand.Reader,
		},
		events: eventService, rundown: rundownQueries,
		competition: competitionService, results: resultsService,
	}
	route := backstagePageRoute()
	route.maxBodyBytes = maxResultsRPCBodyBytes
	mux.HandleFunc("/backstage/events/{eventID}/results", route, handlers.page)
}

func (handlers backstageResultsHandlers) page(
	response http.ResponseWriter,
	request *http.Request,
) {
	actor, ok := handlers.browser.browserAccount(response, request)
	if !ok {
		return
	}
	eventID, err := positivePathID(request, "eventID")
	if err != nil || !actor.HasCapability(eventID, viewer.ViewResults) {
		http.NotFound(response, request)
		return
	}
	csrfToken, err := handlers.browser.csrfToken(response, request)
	if err != nil {
		handlers.browser.frontendError(response, request, "create CSRF proof", err)
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		handlers.renderPage(response, request, actor, eventID, csrfToken, http.StatusOK, "")
	case http.MethodPost:
		if !handlers.browser.validForm(response, request) {
			return
		}
		handlers.submitPage(response, request, actor, eventID, csrfToken)
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
	}
}

func (handlers backstageResultsHandlers) submitPage(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	eventID int,
	csrfToken string,
) {
	var err error
	switch request.Form.Get("action") {
	case "save-results-draft":
		var sessionID int
		sessionID, err = positiveFormInt(request, "competition_session_id")
		if err == nil {
			var input results.SaveInput
			input, err = resultsDraftInput(request, eventID, sessionID)
			if err == nil {
				_, err = handlers.results.Save(request.Context(), actor, input)
			}
		}
	case "mark-results-ready":
		var sessionID int
		sessionID, err = positiveFormInt(request, "competition_session_id")
		if err == nil {
			var revision int
			revision, err = nonnegativeFormInt(request, "expected_revision")
			if err == nil {
				_, err = handlers.results.MarkReady(
					request.Context(),
					actor,
					results.MarkReadyInput{
						EventID: eventID, SessionID: sessionID,
						CommandID:        request.Form.Get("command_id"),
						ExpectedRevision: revision,
					},
				)
			}
		}
	case "save-competition-awards":
		var sessionID int
		sessionID, err = positiveFormInt(request, "competition_session_id")
		if err == nil {
			var revision int
			revision, err = nonnegativeFormInt(request, "expected_revision")
			var awards []results.Award
			if err == nil {
				awards, err = competitionAwardsInput(request)
			}
			if err == nil {
				_, err = handlers.results.SaveCompetitionAwards(
					request.Context(),
					actor,
					results.SaveCompetitionAwardsInput{
						EventID: eventID, SessionID: sessionID,
						CommandID:        request.Form.Get("command_id"),
						ExpectedRevision: revision, Awards: awards,
					},
				)
			}
		}
	case "designate-prizegiving":
		var ceremonyID int
		ceremonyID, err = positiveFormInt(request, "ceremony_session_id")
		if err == nil {
			_, err = handlers.results.DesignatePrizegiving(
				request.Context(),
				actor,
				results.DesignatePrizegivingInput{
					EventID: eventID, CeremonySessionID: ceremonyID,
					CommandID: request.Form.Get("command_id"),
				},
			)
		}
	case "save-prizegiving-plan":
		var input results.SavePrizegivingPlanInput
		input, err = prizegivingPlanInput(request, eventID)
		if err == nil {
			_, err = handlers.results.SavePrizegivingPlan(request.Context(), actor, input)
		}
	case "run-prizegiving-preflight":
		var ceremonyID, revision int
		ceremonyID, err = positiveFormInt(request, "ceremony_session_id")
		if err == nil {
			revision, err = nonnegativeFormInt(request, "expected_revision")
		}
		if err == nil {
			var preflight results.PrizegivingPreflight
			preflight, err = handlers.results.RunPrizegivingPreflight(
				request.Context(),
				actor,
				results.RunPrizegivingPreflightInput{
					EventID: eventID, CeremonySessionID: ceremonyID,
					CommandID:        request.Form.Get("command_id"),
					ExpectedRevision: revision,
				},
			)
			if errors.Is(err, results.ErrPrizegivingPreflightBlocked) {
				err = fmt.Errorf(
					"%w: %s",
					err,
					prizegivingPreflightFindings(preflight.Findings),
				)
			}
		}
	case "fire-prizegiving-cue":
		var ceremonyID int
		ceremonyID, err = positiveFormInt(request, "ceremony_session_id")
		if err == nil {
			_, err = handlers.results.FirePrizegivingResultsCue(
				request.Context(),
				actor,
				results.FirePrizegivingResultsCueInput{
					EventID: eventID, CeremonySessionID: ceremonyID,
					CommandID: request.Form.Get("command_id"),
				},
			)
		}
	case "release-standalone-results":
		var sessionID int
		sessionID, err = positiveFormInt(request, "competition_session_id")
		if err == nil {
			_, err = handlers.results.ReleaseStandaloneResults(
				request.Context(),
				actor,
				results.ReleaseStandaloneResultsInput{
					EventID: eventID, CompetitionSessionID: sessionID,
					CommandID: request.Form.Get("command_id"),
				},
			)
		}
	case "save-results-correction":
		var input results.SaveCorrectionInput
		input, err = handlers.resultsCorrectionInput(request, actor, eventID)
		if err == nil {
			_, err = handlers.results.SaveCorrection(request.Context(), actor, input)
		}
	case "review-results-correction":
		var input results.ReviewCorrectionInput
		input, err = resultsCorrectionReviewInput(request, eventID)
		if err == nil {
			_, err = handlers.results.ReviewCorrection(request.Context(), actor, input)
		}
	case "publish-results-correction":
		var input results.ReviewCorrectionInput
		input, err = resultsCorrectionReviewInput(request, eventID)
		if err == nil {
			_, err = handlers.results.PublishCorrection(request.Context(), actor, input)
		}
	case "save-event-awards":
		var revision int
		revision, err = nonnegativeFormInt(request, "expected_revision")
		var awards []results.EventAward
		if err == nil {
			awards, err = eventAwardsInput(request)
		}
		if err == nil {
			_, err = handlers.results.SaveEventAwards(
				request.Context(),
				actor,
				results.SaveEventAwardsInput{
					EventID: eventID, CommandID: request.Form.Get("command_id"),
					ExpectedRevision: revision, Awards: awards,
				},
			)
		}
	case "mark-event-awards-ready":
		var revision, pathRevision int
		revision, err = nonnegativeFormInt(request, "expected_revision")
		if err == nil {
			pathRevision, err = positiveFormInt(request, "expected_path_revision")
		}
		var path results.AwardReleasePath
		if err == nil {
			path, err = eventAwardPath(
				request.Form.Get("event_award_path_kind"),
				request.Form.Get("event_award_path_prizegiving_session_id"),
			)
		}
		if err == nil {
			_, err = handlers.results.MarkEventAwardsReady(
				request.Context(),
				actor,
				results.MarkEventAwardsReadyInput{
					EventID: eventID, CommandID: request.Form.Get("command_id"),
					ExpectedRevision: revision, ReleasePath: path,
					ExpectedPathRevision: pathRevision,
				},
			)
		}
	case "release-standalone-event-awards":
		var revision, pathRevision int
		revision, err = positiveFormInt(request, "expected_revision")
		if err == nil {
			pathRevision, err = positiveFormInt(request, "expected_path_revision")
		}
		if err == nil {
			_, err = handlers.results.ReleaseStandaloneEventAwards(
				request.Context(),
				actor,
				results.ReleaseStandaloneEventAwardsInput{
					EventID: eventID, CommandID: request.Form.Get("command_id"),
					ExpectedDraftRevision: revision,
					ExpectedPathRevision:  pathRevision,
				},
			)
		}
	default:
		err = results.ErrInvalidInput
	}
	if err == nil {
		http.Redirect(
			response,
			request,
			"/backstage/events/"+strconv.Itoa(eventID)+"/results",
			http.StatusSeeOther,
		)
		return
	}
	status, message := backstageResultsError(err)
	handlers.renderPage(response, request, actor, eventID, csrfToken, status, message)
}

func prizegivingPreflightFindings(
	findings []results.PrizegivingPreflightFinding,
) string {
	values := make([]string, 0, len(findings))
	for _, finding := range findings {
		values = append(values, finding.Code+": "+finding.Message)
	}
	return strings.Join(values, "; ")
}

func eventAwardsInput(request *http.Request) ([]results.EventAward, error) {
	keys := request.Form["event_award_key"]
	names := request.Form["event_award_name"]
	entryIDs := request.Form["event_award_recipient_entry_ids"]
	displayNames := request.Form["event_award_recipient_names"]
	paths := request.Form["event_award_path"]
	orders := request.Form["event_award_display_order"]
	if len(keys) != len(names) ||
		len(keys) != len(entryIDs) ||
		len(keys) != len(displayNames) ||
		len(keys) != len(paths) ||
		len(keys) != len(orders) {
		return nil, results.ErrInvalidInput
	}
	awards := make([]results.EventAward, 0, len(keys))
	for index := range keys {
		if strings.TrimSpace(keys[index]) == "" &&
			strings.TrimSpace(names[index]) == "" &&
			strings.TrimSpace(entryIDs[index]) == "" &&
			strings.TrimSpace(displayNames[index]) == "" {
			continue
		}
		order, err := strconv.Atoi(orders[index])
		if err != nil || order <= 0 {
			return nil, results.ErrInvalidInput
		}
		recipients, err := awardRecipients(entryIDs[index], displayNames[index])
		if err != nil {
			return nil, err
		}
		pathKind, pathSessionID, _ := strings.Cut(paths[index], ":")
		path, err := eventAwardPath(pathKind, pathSessionID)
		if err != nil {
			return nil, err
		}
		awards = append(awards, results.EventAward{
			Award: results.Award{
				Key: strings.TrimSpace(keys[index]), Name: strings.TrimSpace(names[index]),
				Recipients: recipients, DisplayOrder: order,
			},
			ReleasePath: path,
		})
	}
	return awards, nil
}

func eventAwardPath(kind, prizegivingSessionID string) (results.AwardReleasePath, error) {
	switch results.AwardReleasePathKind(kind) {
	case results.StandaloneRelease:
		return results.AwardReleasePath{Kind: results.StandaloneRelease}, nil
	case results.PrizegivingRelease:
		sessionID, err := strconv.Atoi(prizegivingSessionID)
		if err != nil || sessionID <= 0 {
			return results.AwardReleasePath{}, results.ErrInvalidInput
		}
		return results.AwardReleasePath{
			Kind: results.PrizegivingRelease, PrizegivingSessionID: sessionID,
		}, nil
	default:
		return results.AwardReleasePath{}, results.ErrInvalidInput
	}
}

func (handlers backstageResultsHandlers) resultsCorrectionInput(
	request *http.Request,
	actor auth.Account,
	eventID int,
) (results.SaveCorrectionInput, error) {
	scope, scopeSessionID, revision, err := correctionTarget(request)
	if err != nil {
		return results.SaveCorrectionInput{}, err
	}
	baseRevision, err := positiveFormInt(request, "base_publication_revision")
	if err != nil {
		return results.SaveCorrectionInput{}, err
	}
	history, err := handlers.results.GetCorrectionHistory(
		request.Context(), actor, eventID, scope, scopeSessionID,
	)
	if err != nil {
		return results.SaveCorrectionInput{}, err
	}
	var base *results.PublicationHistoryRevision
	for index := range history.Publications {
		if history.Publications[index].Revision == baseRevision {
			base = &history.Publications[index]
			break
		}
	}
	if base == nil {
		return results.SaveCorrectionInput{}, results.ErrCorrectionBase
	}
	var corrected results.PublicResultsPublication
	if err = json.Unmarshal(
		[]byte(request.Form.Get("corrected_results_json")),
		&corrected,
	); err != nil {
		return results.SaveCorrectionInput{}, results.ErrInvalidInput
	}
	return results.SaveCorrectionInput{
		EventID: eventID, Scope: scope, ScopeSessionID: scopeSessionID,
		CommandID:               request.Form.Get("command_id"),
		ExpectedRevision:        revision,
		BasePublicationRevision: baseRevision,
		PublicationOrder:        base.PublicationOrder,
		Items:                   corrected.Items,
		Template:                base.Template,
		CrewReason:              request.Form.Get("crew_reason"),
		PublicNote:              request.Form.Get("public_note"),
	}, nil
}

func resultsCorrectionReviewInput(
	request *http.Request,
	eventID int,
) (results.ReviewCorrectionInput, error) {
	scope, scopeSessionID, revision, err := correctionTarget(request)
	if err != nil {
		return results.ReviewCorrectionInput{}, err
	}
	return results.ReviewCorrectionInput{
		EventID: eventID, Scope: scope, ScopeSessionID: scopeSessionID,
		CommandID: request.Form.Get("command_id"), ExpectedRevision: revision,
	}, nil
}

func correctionTarget(
	request *http.Request,
) (results.PublicationScope, int, int, error) {
	scope := results.PublicationScope(request.Form.Get("correction_scope"))
	switch scope {
	case results.PublicationScopePrizegiving,
		results.PublicationScopeStandalone,
		results.PublicationScopeEventAwards,
		results.PublicationScopeEvent:
	default:
		return "", 0, 0, results.ErrInvalidInput
	}
	scopeSessionID, err := positiveFormInt(request, "correction_scope_session_id")
	if err != nil {
		return "", 0, 0, err
	}
	revision, err := nonnegativeFormInt(request, "expected_correction_revision")
	if err != nil {
		return "", 0, 0, err
	}
	return scope, scopeSessionID, revision, nil
}

func prizegivingPlanInput(
	request *http.Request,
	eventID int,
) (results.SavePrizegivingPlanInput, error) {
	ceremonyID, err := positiveFormInt(request, "ceremony_session_id")
	if err != nil {
		return results.SavePrizegivingPlanInput{}, err
	}
	revision, err := nonnegativeFormInt(request, "expected_revision")
	if err != nil {
		return results.SavePrizegivingPlanInput{}, err
	}
	templateRevision, err := positiveFormInt(request, "results_text_template_revision")
	if err != nil {
		return results.SavePrizegivingPlanInput{}, err
	}
	competitionIDs, err := positiveFormInts(request.Form["plan_competition_session_id"])
	if err != nil {
		return results.SavePrizegivingPlanInput{}, err
	}
	sequence, publicationOrder, err := prizegivingOrders(request)
	if err != nil {
		return results.SavePrizegivingPlanInput{}, err
	}
	return results.SavePrizegivingPlanInput{
		EventID: eventID, CeremonySessionID: ceremonyID,
		CommandID:             request.Form.Get("command_id"),
		ExpectedRevision:      revision,
		CompetitionSessionIDs: competitionIDs,
		Sequence:              sequence,
		PublicationOrder:      publicationOrder,
		ReleasePolicy:         results.ReleasePolicy(request.Form.Get("release_policy")),
		Template: results.TextTemplate{
			Revision: templateRevision,
			Source:   request.Form.Get("results_text_template"),
		},
	}, nil
}

func positiveFormInts(values []string) ([]int, error) {
	result := make([]int, 0, len(values))
	for _, value := range values {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			return nil, results.ErrInvalidInput
		}
		result = append(result, parsed)
	}
	return result, nil
}

func prizegivingOrders(
	request *http.Request,
) ([]results.ResultItem, []results.ResultItemRef, error) {
	kinds := request.Form["item_kind"]
	sessionIDs := request.Form["item_competition_session_id"]
	awardKeys := request.Form["item_award_key"]
	sequenceOrders := request.Form["sequence_display_order"]
	revealMethods := request.Form["reveal_method"]
	publicationOrders := request.Form["publication_display_order"]
	if len(kinds) != len(sessionIDs) ||
		len(kinds) != len(awardKeys) ||
		len(kinds) != len(sequenceOrders) ||
		len(kinds) != len(revealMethods) ||
		len(kinds) != len(publicationOrders) {
		return nil, nil, results.ErrInvalidInput
	}
	type orderRow struct {
		item             results.ResultItem
		publicationOrder int
	}
	rows := make([]orderRow, 0, len(kinds))
	for index := range kinds {
		sessionID, err := optionalPositiveInt(sessionIDs[index])
		if err != nil {
			return nil, nil, err
		}
		sequenceOrder, err := strconv.Atoi(sequenceOrders[index])
		if err != nil || sequenceOrder <= 0 {
			return nil, nil, results.ErrInvalidInput
		}
		publicationOrder, err := strconv.Atoi(publicationOrders[index])
		if err != nil || publicationOrder <= 0 {
			return nil, nil, results.ErrInvalidInput
		}
		rows = append(rows, orderRow{
			item: results.ResultItem{
				Kind:                 results.ResultItemKind(kinds[index]),
				CompetitionSessionID: sessionID, AwardKey: awardKeys[index],
				DisplayOrder: sequenceOrder,
				RevealMethod: results.RevealMethod(revealMethods[index]),
			},
			publicationOrder: publicationOrder,
		})
	}
	slices.SortFunc(rows, func(first, second orderRow) int {
		return first.item.DisplayOrder - second.item.DisplayOrder
	})
	sequence := make([]results.ResultItem, 0, len(rows))
	for _, row := range rows {
		sequence = append(sequence, row.item)
	}
	slices.SortFunc(rows, func(first, second orderRow) int {
		return first.publicationOrder - second.publicationOrder
	})
	publicationOrder := make([]results.ResultItemRef, 0, len(rows))
	for _, row := range rows {
		publicationOrder = append(
			publicationOrder,
			row.item.Ref(row.publicationOrder),
		)
	}
	return sequence, publicationOrder, nil
}

func competitionAwardsInput(request *http.Request) ([]results.Award, error) {
	keys := request.Form["award_key"]
	names := request.Form["award_name"]
	entryIDs := request.Form["award_recipient_entry_ids"]
	displayNames := request.Form["award_recipient_names"]
	promoted := request.Form["award_promoted"]
	orders := request.Form["award_display_order"]
	if len(keys) != len(names) ||
		len(keys) != len(entryIDs) ||
		len(keys) != len(displayNames) ||
		len(keys) != len(promoted) ||
		len(keys) != len(orders) {
		return nil, results.ErrInvalidInput
	}
	awards := make([]results.Award, 0, len(keys))
	for index := range keys {
		if strings.TrimSpace(keys[index]) == "" &&
			strings.TrimSpace(names[index]) == "" &&
			strings.TrimSpace(entryIDs[index]) == "" &&
			strings.TrimSpace(displayNames[index]) == "" {
			continue
		}
		order, err := strconv.Atoi(orders[index])
		if err != nil || order <= 0 {
			return nil, results.ErrInvalidInput
		}
		isPromoted, err := strconv.ParseBool(promoted[index])
		if err != nil {
			return nil, results.ErrInvalidInput
		}
		recipients, err := awardRecipients(entryIDs[index], displayNames[index])
		if err != nil {
			return nil, err
		}
		awards = append(awards, results.Award{
			Key: strings.TrimSpace(keys[index]), Name: strings.TrimSpace(names[index]),
			Recipients: recipients, Promoted: isPromoted, DisplayOrder: order,
		})
	}
	return awards, nil
}

func awardRecipients(entryIDs, displayNames string) ([]results.AwardRecipient, error) {
	recipients := make([]results.AwardRecipient, 0)
	for value := range strings.SplitSeq(entryIDs, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		entryID, err := strconv.Atoi(value)
		if err != nil || entryID <= 0 {
			return nil, results.ErrInvalidInput
		}
		recipients = append(recipients, results.AwardRecipient{EntryID: entryID})
	}
	for value := range strings.SplitSeq(displayNames, "\n") {
		value = strings.TrimSpace(value)
		if value != "" {
			recipients = append(recipients, results.AwardRecipient{DisplayName: value})
		}
	}
	return recipients, nil
}

func resultsDraftInput(
	request *http.Request,
	eventID, sessionID int,
) (results.SaveInput, error) {
	revision, err := nonnegativeFormInt(request, "expected_revision")
	if err != nil {
		return results.SaveInput{}, err
	}
	precision, err := nonnegativeFormInt(request, "score_precision")
	if err != nil {
		return results.SaveInput{}, err
	}
	standings, err := resultsStandings(request)
	if err != nil {
		return results.SaveInput{}, err
	}
	return results.SaveInput{
		EventID: eventID, SessionID: sessionID,
		CommandID:           request.Form.Get("command_id"),
		ExpectedRevision:    revision,
		Disposition:         results.Disposition(request.Form.Get("disposition")),
		NoPublicReason:      request.Form.Get("no_public_reason"),
		TallyOverrideReason: request.Form.Get("tally_override_reason"),
		PublicExplanation:   request.Form.Get("public_explanation"),
		Score: results.ScorePolicy{
			Type:           results.ScoreType(request.Form.Get("score_type")),
			Visibility:     results.ScoreVisibility(request.Form.Get("score_visibility")),
			Unit:           request.Form.Get("score_unit"),
			Precision:      precision,
			Requirement:    results.ScoreRequirement(request.Form.Get("score_requirement")),
			Interpretation: results.ScoreInterpretation(request.Form.Get("score_interpretation")),
		},
		Standings: standings,
	}, nil
}

func resultsStandings(request *http.Request) ([]results.Standing, error) {
	entryIDs := request.Form["standing_entry_id"]
	states := request.Form["standing"]
	placements := request.Form["placement"]
	orders := request.Form["display_order"]
	scores := request.Form["score"]
	if len(entryIDs) != len(states) ||
		len(entryIDs) != len(placements) ||
		len(entryIDs) != len(orders) ||
		len(entryIDs) != len(scores) {
		return nil, results.ErrInvalidInput
	}
	scoreType := results.ScoreType(request.Form.Get("score_type"))
	standings := make([]results.Standing, 0, len(entryIDs))
	for index := range entryIDs {
		entryID, err := strconv.Atoi(entryIDs[index])
		if err != nil || entryID <= 0 {
			return nil, results.ErrInvalidInput
		}
		placement, err := optionalPositiveInt(placements[index])
		if err != nil {
			return nil, err
		}
		displayOrder, err := strconv.Atoi(orders[index])
		if err != nil || displayOrder <= 0 {
			return nil, results.ErrInvalidInput
		}
		score, err := resultScoreValue(scoreType, scores[index])
		if err != nil {
			return nil, err
		}
		standings = append(standings, results.Standing{
			EntryID: entryID, Standing: results.ResultStanding(states[index]),
			Placement: placement, DisplayOrder: displayOrder, Score: score,
		})
	}
	return standings, nil
}

func resultScoreValue(scoreType results.ScoreType, value string) (results.ScoreValue, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return results.ScoreValue{}, nil
	}
	switch scoreType {
	case results.Decimal:
		return results.ScoreValue{Decimal: &value}, nil
	case results.Duration:
		duration, err := time.ParseDuration(value)
		if err != nil {
			return results.ScoreValue{}, results.ErrInvalidInput
		}
		return results.ScoreValue{Duration: &duration}, nil
	case results.None:
		return results.ScoreValue{}, results.ErrInvalidInput
	default:
		return results.ScoreValue{}, results.ErrInvalidInput
	}
}

func optionalPositiveInt(value string) (int, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, results.ErrInvalidInput
	}
	return parsed, nil
}

func positiveFormInt(request *http.Request, name string) (int, error) {
	value, err := strconv.Atoi(request.Form.Get(name))
	if err != nil || value <= 0 {
		return 0, results.ErrInvalidInput
	}
	return value, nil
}

func (handlers backstageResultsHandlers) renderPage(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	eventID int,
	csrfToken string,
	status int,
	message string,
) {
	event, err := handlers.events.CrewEvent(request.Context(), actor, eventID)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Results Event", err)
		return
	}
	crewRundown, err := handlers.rundown.CrewRundown(request.Context(), actor, eventID)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Results Rundown", err)
		return
	}
	competitions := make([]frontend.ResultsCompetitionPage, 0)
	prizegivings := make([]frontend.ResultsPrizegivingPage, 0)
	assignments := make(map[int]int)
	for _, session := range crewRundown.Sessions {
		switch session.Type {
		case rundown.SessionCompetition:
			state, stateErr := handlers.competition.Get(
				request.Context(), actor, eventID, session.ID,
			)
			if stateErr != nil {
				handlers.browser.frontendError(response, request, "read Results Competition", stateErr)
				return
			}
			draft, draftErr := handlers.results.Get(
				request.Context(), actor, eventID, session.ID,
			)
			if draftErr != nil {
				handlers.browser.frontendError(response, request, "read Results Draft", draftErr)
				return
			}
			competitions = append(competitions, resultsCompetitionPage(session, state, draft))
		case rundown.SessionCeremony:
			plan, planErr := handlers.results.GetPrizegivingPlan(
				request.Context(), actor, eventID, session.ID,
			)
			if errors.Is(planErr, results.ErrPrizegivingSession) {
				prizegivings = append(prizegivings, frontend.ResultsPrizegivingPage{
					Session: session,
				})
				continue
			}
			if planErr != nil {
				handlers.browser.frontendError(response, request, "read Prizegiving plan", planErr)
				return
			}
			if plan.Template.Revision == 0 {
				plan.Template = results.DefaultResultsTextTemplate()
			}
			var preview *results.PrizegivingPreview
			if request.URL.Query().Get("ceremony_id") == strconv.Itoa(session.ID) {
				mode := results.PrizegivingPreviewMode(request.URL.Query().Get("preview"))
				if mode != "" {
					value, previewErr := handlers.results.PreviewPrizegiving(
						request.Context(), actor, eventID, session.ID, mode,
					)
					if previewErr != nil {
						status, message = backstageResultsError(previewErr)
					} else {
						preview = &value
					}
				}
			}
			for _, competitionID := range plan.CompetitionSessionIDs {
				assignments[competitionID] = session.ID
			}
			prizegivings = append(prizegivings, frontend.ResultsPrizegivingPage{
				Session: session, Designated: true, Plan: plan, Preview: preview,
			})
		default:
			continue
		}
	}
	for index := range competitions {
		competitions[index].AssignedPrizegivingID =
			assignments[competitions[index].Session.ID]
		correction, correctionErr := handlers.correctionPage(
			request,
			actor,
			eventID,
			results.PublicationScopeStandalone,
			competitions[index].Session.ID,
		)
		if correctionErr != nil {
			handlers.browser.frontendError(response, request, "read Results Correction", correctionErr)
			return
		}
		competitions[index].Correction = correction
	}
	eventAwards, err := handlers.results.GetEventAwards(request.Context(), actor, eventID)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Event Awards", err)
		return
	}
	var eventAwardsPreflight *results.StandaloneEventAwardsPreflight
	if actor.CanProduceEvent(eventID) &&
		request.URL.Query().Get("event_awards_preflight") == "true" {
		value, preflightErr := handlers.results.PreflightStandaloneEventAwards(
			request.Context(), actor, eventID,
		)
		if preflightErr != nil {
			status, message = backstageResultsError(preflightErr)
		} else {
			eventAwardsPreflight = &value
		}
	}
	commandID, err := planningCommandID(handlers.browser.random)
	if err != nil {
		handlers.browser.frontendError(response, request, "create Results command identity", err)
		return
	}
	handlers.browser.render(response, request, status, frontend.Results(frontend.ResultsPage{
		AccountName: actor.Name, CSRFToken: csrfToken,
		ReducedEffects: reducedEffectsCookie(request),
		Navigation:     backstageNavigation(actor, request.URL.Path),
		CommandID:      commandID, Event: event,
		CanManage:    actor.HasCapability(eventID, viewer.ManageResults),
		Producer:     actor.CanProduceEvent(eventID),
		Competitions: competitions, Prizegivings: prizegivings, Error: message,
		EventAwards: eventAwards, EventAwardsPreflight: eventAwardsPreflight,
	}))
}

func (handlers backstageResultsHandlers) correctionPage(
	request *http.Request,
	actor auth.Account,
	eventID int,
	scope results.PublicationScope,
	scopeSessionID int,
) (*frontend.ResultsCorrectionPage, error) {
	history, err := handlers.results.GetCorrectionHistory(
		request.Context(), actor, eventID, scope, scopeSessionID,
	)
	if err != nil {
		return nil, err
	}
	if len(history.Publications) == 0 {
		return nil, nil
	}
	latest := history.Publications[0]
	for _, publication := range history.Publications[1:] {
		if publication.Revision > latest.Revision {
			latest = publication
		}
	}
	current, err := handlers.results.GetCorrection(
		request.Context(), actor, eventID, scope, scopeSessionID,
	)
	if err != nil {
		return nil, err
	}
	correctedJSON := latest.JSON
	if current.Revision > 0 &&
		(current.Status == results.CorrectionDraft ||
			current.Status == results.CorrectionReady) {
		var model results.PublicResultsPublication
		if unmarshalErr := json.Unmarshal([]byte(latest.JSON), &model); unmarshalErr != nil {
			return nil, unmarshalErr
		}
		model.Items = current.Proposal.Items
		encoded, marshalErr := json.MarshalIndent(model, "", "  ")
		if marshalErr != nil {
			return nil, marshalErr
		}
		correctedJSON = string(encoded)
	}
	return &frontend.ResultsCorrectionPage{
		Scope: scope, ScopeSessionID: scopeSessionID,
		Current: current, PublicationRevision: latest.Revision,
		CorrectedResultsJSON:    correctedJSON,
		PublicationHistoryCount: len(history.Publications),
	}, nil
}

func resultsCompetitionPage(
	session rundown.CrewSession,
	state competition.State,
	draft results.Draft,
) frontend.ResultsCompetitionPage {
	standings := make(map[int]results.Standing, len(draft.Standings))
	for _, standing := range draft.Standings {
		standings[standing.EntryID] = standing
	}
	entries := make([]frontend.ResultsEntryPage, 0, len(state.Entries))
	for _, entry := range state.Entries {
		if entry.Disposition != competition.DispositionIncluded ||
			entry.ResultDisposition != "Eligible" {
			continue
		}
		standing, ok := standings[entry.ID]
		if !ok {
			standing = results.Standing{
				EntryID: entry.ID, Standing: results.Unplaced,
				DisplayOrder: len(entries) + 1,
			}
		}
		entries = append(entries, frontend.ResultsEntryPage{
			Entry: entry, Standing: standing,
		})
	}
	return frontend.ResultsCompetitionPage{
		Session: session, Draft: draft, Entries: entries,
	}
}

func backstageResultsError(err error) (int, string) {
	switch {
	case errors.Is(err, results.ErrInvalidInput),
		errors.Is(err, results.ErrIncomplete),
		errors.Is(err, results.ErrCompetitionRanking),
		errors.Is(err, results.ErrUnplacedOrder),
		errors.Is(err, results.ErrCrewReasonRequired),
		errors.Is(err, results.ErrDisposition),
		errors.Is(err, results.ErrScoreRequired),
		errors.Is(err, results.ErrInvalidScore),
		errors.Is(err, results.ErrInvalidAward),
		errors.Is(err, command.ErrInvalidID):
		return http.StatusUnprocessableEntity, "Check the Results details and try again."
	case errors.Is(err, results.ErrRevisionConflict),
		errors.Is(err, results.ErrCommandConflict),
		errors.Is(err, results.ErrPrizegivingPlanRevision),
		errors.Is(err, results.ErrEventAwardsRevision):
		return http.StatusConflict, "Results changed. Reload and try again."
	case errors.Is(err, results.ErrPrizegivingPreflightBlocked),
		errors.Is(err, results.ErrPrizegivingPreflightRequired),
		errors.Is(err, results.ErrCompetitionPrizegivingAssignment),
		errors.Is(err, results.ErrPrizegivingLocked),
		errors.Is(err, results.ErrResultsReleasePolicy),
		errors.Is(err, results.ErrResultsPublicationRequired):
		return http.StatusUnprocessableEntity, err.Error()
	case errors.Is(err, results.ErrResultsCorrection),
		errors.Is(err, results.ErrCorrectionTransition),
		errors.Is(err, results.ErrCorrectionBase):
		return http.StatusUnprocessableEntity, "Check the Results Correction and Crew Reason."
	case errors.Is(err, results.ErrCorrectionRevision):
		return http.StatusConflict, "Results Correction changed. Reload and try again."
	case errors.Is(err, results.ErrManageRequired),
		errors.Is(err, results.ErrProducerRequired),
		errors.Is(err, results.ErrCompetitionNotFound):
		return http.StatusNotFound, "Results not found."
	default:
		return http.StatusInternalServerError, "Results action failed."
	}
}

func registerResultsRoutes(
	mux *routeMux,
	authentication *auth.Service,
	eventService *events.Service,
	service *results.Service,
	listenerAddress net.Addr,
	tracerProvider trace.TracerProvider,
	meterProvider metric.MeterProvider,
	propagator propagation.TextMapPropagator,
	logger *slog.Logger,
) error {
	adapter, err := resultsconnect.NewHandler(service)
	if err != nil {
		return err
	}
	if err := registerConnectRoute(mux, connectRouteConfig{
		name: "results", authentication: authentication, listenerAddress: listenerAddress,
		tracerProvider: tracerProvider, meterProvider: meterProvider, propagator: propagator,
		errorInterceptor: resultsconnect.ErrorInterceptor(),
		maxBodyBytes:     maxResultsRPCBodyBytes,
		contract:         crewRoute(),
		build: func(options ...connect.HandlerOption) (string, http.Handler) {
			return resultsv1connect.NewResultsServiceHandler(adapter, options...)
		},
	}); err != nil {
		return err
	}
	registerPublicResultsRoutes(mux, service, logger)
	registerCanonicalEventResultsRoute(mux, authentication, eventService, service, logger)
	return nil
}

type publicResultsHandlers struct {
	service publicResultsReader
	logger  *slog.Logger
}

type canonicalEventResultsHandlers struct {
	browser frontendHandlers
	events  *events.Service
	results publicResultsHandlers
}

type publicResultsReader interface {
	PublicArtifact(
		context.Context,
		int,
		results.PublicationScope,
		int,
		int,
	) (results.PublicArtifact, bool, error)
}

func registerPublicResultsRoutes(
	mux *routeMux,
	service publicResultsReader,
	logger *slog.Logger,
) {
	handlers := publicResultsHandlers{service: service, logger: logger}
	mux.HandleFunc(
		"/results/events/{eventID}/{scope}/{sessionID}/results.txt",
		publicRoute(),
		handlers.latestText,
	)
	mux.HandleFunc(
		"/results/events/{eventID}/{scope}/{sessionID}/revisions/{revision}/results.json",
		publicRoute(),
		handlers.versionedJSON,
	)
	mux.HandleFunc(
		"/results/events/{eventID}/event-awards/results.txt",
		publicRoute(),
		handlers.latestEventAwardsText,
	)
	mux.HandleFunc(
		"/results/events/{eventID}/event-awards/revisions/{revision}/results.json",
		publicRoute(),
		handlers.versionedEventAwardsJSON,
	)
	mux.HandleFunc(
		"/results/events/{eventID}/results.txt",
		publicRoute(),
		handlers.latestEventText,
	)
	mux.HandleFunc(
		"/results/events/{eventID}/revisions/{revision}/results.json",
		publicRoute(),
		handlers.versionedEventJSON,
	)
}

func registerCanonicalEventResultsRoute(
	mux *routeMux,
	authentication *auth.Service,
	eventService *events.Service,
	service publicResultsReader,
	logger *slog.Logger,
) {
	handlers := canonicalEventResultsHandlers{
		browser: frontendHandlers{
			authentication: authentication,
			logger:         logger,
			random:         rand.Reader,
		},
		events:  eventService,
		results: publicResultsHandlers{service: service, logger: logger},
	}
	mux.HandleFunc(
		"/events/{slug}/results",
		browserPageRoute(),
		handlers.latest,
	)
}

func (handlers canonicalEventResultsHandlers) latest(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !frontendReadAllowed(response, request) {
		return
	}
	event, alias, err := handlers.events.PublicEvent(
		request.Context(),
		request.PathValue("slug"),
	)
	if errors.Is(err, events.ErrEventNotFound) {
		http.NotFound(response, request)
		return
	}
	if err != nil {
		handlers.browser.frontendError(response, request, "read public Event", err)
		return
	}
	if alias {
		http.Redirect(
			response,
			request,
			"/events/"+event.Slug+"/results",
			http.StatusFound,
		)
		return
	}
	pageShell, ok := handlers.browser.shell(response, request)
	if !ok {
		return
	}
	_, found, err := handlers.results.service.PublicArtifact(
		request.Context(),
		event.ID,
		results.PublicationScopeEvent,
		event.ID,
		0,
	)
	if err != nil {
		handlers.results.logger.ErrorContext(
			request.Context(),
			"read public Event Results",
			"error",
			err,
		)
		http.Error(response, "Results unavailable", http.StatusInternalServerError)
		return
	}
	if found {
		csrfToken := ""
		if pageShell.accountName != "" {
			csrfToken, err = handlers.browser.csrfToken(response, request)
			if err != nil {
				handlers.browser.frontendError(response, request, "create CSRF proof", err)
				return
			}
		}
		shell := frontend.Shell{
			AccountName:    pageShell.accountName,
			CSRFToken:      csrfToken,
			ReturnTo:       request.URL.RequestURI(),
			ReducedEffects: pageShell.reducedEffects,
			Backstage:      pageShell.backstage,
			Event: frontend.EventContext{
				Name:       event.Name,
				Slug:       event.Slug,
				ActivePath: "/events/" + event.Slug + "/results",
				Breadcrumbs: []frontend.Breadcrumb{
					{Label: "Events", Href: "/"},
					{Label: event.Name, Href: "/events/" + event.Slug},
					{Label: "Results"},
				},
			},
		}
		handlers.results.serveArtifact(
			response,
			request,
			event.ID,
			results.PublicationScopeEvent,
			event.ID,
			0,
			"text/html; charset=utf-8",
			&shell,
		)
		return
	}
	csrfToken, err := handlers.browser.csrfToken(response, request)
	if err != nil {
		handlers.browser.frontendError(response, request, "create CSRF proof", err)
		return
	}
	handlers.browser.render(
		response,
		request,
		http.StatusOK,
		frontend.PublicEventUnavailable(
			event,
			pageShell.accountName,
			csrfToken,
			pageShell.reducedEffects,
			pageShell.backstage,
			"Results",
			"Results have not been published yet.",
		),
	)
}

func (handlers publicResultsHandlers) latestText(
	response http.ResponseWriter,
	request *http.Request,
) {
	handlers.serve(response, request, 0, "text/plain; charset=utf-8")
}

func (handlers publicResultsHandlers) versionedJSON(
	response http.ResponseWriter,
	request *http.Request,
) {
	revision, err := strconv.Atoi(request.PathValue("revision"))
	if err != nil || revision <= 0 {
		http.NotFound(response, request)
		return
	}
	handlers.serve(response, request, revision, "application/json")
}

func (handlers publicResultsHandlers) latestEventAwardsText(
	response http.ResponseWriter,
	request *http.Request,
) {
	handlers.serveEventAwards(response, request, 0, "text/plain; charset=utf-8")
}

func (handlers publicResultsHandlers) versionedEventAwardsJSON(
	response http.ResponseWriter,
	request *http.Request,
) {
	revision, err := strconv.Atoi(request.PathValue("revision"))
	if err != nil || revision <= 0 {
		http.NotFound(response, request)
		return
	}
	handlers.serveEventAwards(response, request, revision, "application/json")
}

func (handlers publicResultsHandlers) latestEventText(
	response http.ResponseWriter,
	request *http.Request,
) {
	handlers.serveEvent(response, request, 0, "text/plain; charset=utf-8")
}

func (handlers publicResultsHandlers) versionedEventJSON(
	response http.ResponseWriter,
	request *http.Request,
) {
	revision, err := strconv.Atoi(request.PathValue("revision"))
	if err != nil || revision <= 0 {
		http.NotFound(response, request)
		return
	}
	handlers.serveEvent(response, request, revision, "application/json")
}

func (handlers publicResultsHandlers) serveEvent(
	response http.ResponseWriter,
	request *http.Request,
	revision int,
	contentType string,
) {
	if !publicMethodAllowed(response, request) {
		return
	}
	eventID, err := positivePathID(request, "eventID")
	if err != nil {
		http.NotFound(response, request)
		return
	}
	handlers.serveArtifact(
		response,
		request,
		eventID,
		results.PublicationScopeEvent,
		eventID,
		revision,
		contentType,
		nil,
	)
}

func (handlers publicResultsHandlers) serveEventAwards(
	response http.ResponseWriter,
	request *http.Request,
	revision int,
	contentType string,
) {
	if !publicMethodAllowed(response, request) {
		return
	}
	eventID, err := positivePathID(request, "eventID")
	if err != nil {
		http.NotFound(response, request)
		return
	}
	handlers.serveArtifact(
		response,
		request,
		eventID,
		results.PublicationScopeEventAwards,
		eventID,
		revision,
		contentType,
		nil,
	)
}

func (handlers publicResultsHandlers) serve(
	response http.ResponseWriter,
	request *http.Request,
	revision int,
	contentType string,
) {
	if !publicMethodAllowed(response, request) {
		return
	}
	eventID, eventErr := positivePathID(request, "eventID")
	sessionID, sessionErr := positivePathID(request, "sessionID")
	scope := results.PublicationScope(strings.ToLower(request.PathValue("scope")))
	scope = map[results.PublicationScope]results.PublicationScope{
		"prizegiving": results.PublicationScopePrizegiving,
		"standalone":  results.PublicationScopeStandalone,
	}[scope]
	if eventErr != nil || sessionErr != nil || scope == "" {
		http.NotFound(response, request)
		return
	}
	handlers.serveArtifact(
		response,
		request,
		eventID,
		scope,
		sessionID,
		revision,
		contentType,
		nil,
	)
}

func (handlers publicResultsHandlers) serveArtifact(
	response http.ResponseWriter,
	request *http.Request,
	eventID int,
	scope results.PublicationScope,
	sessionID int,
	revision int,
	contentType string,
	shell *frontend.Shell,
) {
	artifact, found, err := handlers.service.PublicArtifact(
		request.Context(),
		eventID,
		scope,
		sessionID,
		revision,
	)
	if err != nil {
		handlers.logger.ErrorContext(request.Context(), "read public Results", "error", err)
		http.Error(response, "Results unavailable", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(response, request)
		return
	}
	reducedEffects := reducedEffectsCookie(request)
	etag := publicResultsETag(eventID, scope, sessionID, artifact.Revision, shell)
	cacheControl := "public, max-age=15, must-revalidate"
	if contentType == "text/html; charset=utf-8" {
		response.Header().Add("Vary", "Cookie")
	}
	if contentType == "text/html; charset=utf-8" &&
		(reducedEffectsPreferenceCookie(request) ||
			shell != nil && shell.AccountName != "") {
		cacheControl = "private, no-store"
		etag = ""
	}
	response.Header().Set("Cache-Control", cacheControl)
	response.Header().Set("Content-Type", contentType)
	if etag != "" {
		response.Header().Set("ETag", etag)
	}
	if etag != "" && request.Header.Get("If-None-Match") == etag {
		response.WriteHeader(http.StatusNotModified)
		return
	}
	if request.Method == http.MethodHead {
		return
	}
	var content string
	switch contentType {
	case "text/html; charset=utf-8":
		content = strings.Replace(
			artifact.HTML,
			"</head>",
			`<link rel="stylesheet" href="`+frontend.EventThemePath(eventID)+`"></head>`,
			1,
		)
		if slug := request.PathValue("slug"); slug != "" {
			content = strings.Replace(
				content,
				`href="/schedule"`,
				`href="/events/`+slug+`/schedule"`,
				1,
			)
		}
		if shell == nil {
			content = strings.Replace(
				content,
				"<body>",
				`<body data-reduced-effects="`+
					strconv.FormatBool(reducedEffects)+`">`,
				1,
			)
			content = strings.Replace(
				content,
				"</main>",
				`<p><a href="`+
					frontend.EffectsPath(request.URL.RequestURI())+`">`+
					frontend.EffectsLabel(reducedEffects)+`</a></p></main>`,
				1,
			)
		} else {
			content, err = wrapPublicResultsHTML(request.Context(), content, *shell)
			if err != nil {
				handlers.logger.ErrorContext(
					request.Context(),
					"render public Results shell",
					"error",
					err,
				)
				response.Header().Del("ETag")
				response.Header().Set("Cache-Control", "no-store")
				http.Error(response, "Results unavailable", http.StatusInternalServerError)
				return
			}
		}
	case "text/plain; charset=utf-8":
		content = artifact.Text
	default:
		content = artifact.JSON
	}
	// HTML artifacts are generated by the fixed html/template renderer; the
	// injected Event Theme path contains only a parsed positive integer.
	if _, err = response.Write([]byte(content)); err != nil { //nolint:gosec // Content is fixed-template HTML.
		handlers.logger.ErrorContext(request.Context(), "write public Results", "error", err)
	}
}

func publicResultsETag(
	eventID int,
	scope results.PublicationScope,
	sessionID int,
	revision int,
	shell *frontend.Shell,
) string {
	if shell == nil {
		return fmt.Sprintf(`"results-%d-%s-%d-%d"`, eventID, scope, sessionID, revision)
	}
	return fmt.Sprintf(
		`"results-%d-%s-%d-%d-%x"`,
		eventID,
		scope,
		sessionID,
		revision,
		shell.Event.Name,
	)
}

func wrapPublicResultsHTML(
	ctx context.Context,
	document string,
	shell frontend.Shell,
) (string, error) {
	bodyStart := strings.Index(document, "<body>")
	bodyEnd := strings.LastIndex(document, "</body>")
	const (
		canonicalMain = `<main id="main-content" tabindex="-1">`
		legacyMain    = "<main>"
	)
	mainStart := strings.Index(document, canonicalMain)
	mainOpening := canonicalMain
	if mainStart < 0 {
		mainStart = strings.Index(document, legacyMain)
		mainOpening = legacyMain
	}
	mainEnd := strings.LastIndex(document, "</main>")
	if bodyStart < 0 || bodyEnd < bodyStart || mainStart < bodyStart ||
		mainEnd < mainStart {
		return "", errors.New("public Results HTML has no complete body and main")
	}
	mainEnd += len("</main>")
	main := document[mainStart:mainEnd]
	if mainOpening == legacyMain {
		main = canonicalMain + strings.TrimPrefix(main, legacyMain)
	}
	// The immutable document came from the fixed public Results renderer.
	children := templ.ComponentFunc(func(_ context.Context, writer io.Writer) error {
		_, err := io.WriteString(writer, main)
		return err
	})
	var rendered strings.Builder
	if err := frontend.EventShell(shell).Render(
		templ.WithChildren(ctx, children),
		&rendered,
	); err != nil {
		return "", fmt.Errorf("render Event shell: %w", err)
	}
	return document[:bodyStart] +
		`<body data-reduced-effects="` +
		strconv.FormatBool(shell.ReducedEffects) +
		`">` +
		rendered.String() +
		document[bodyEnd:], nil
}
