package server

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/competition"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/frontend"
	"github.com/dotwaffle/beamers/internal/results"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/viewer"
)

const maxResultsRPCBodyBytes = 128 << 10

type backstageResultsHandlers struct {
	browser frontendHandlers
	results *results.Service
}

func registerResultsFrontendRoutes(
	mux *routeMux,
	authentication *auth.Service,
	resultsService *results.Service,
	logger *slog.Logger,
) {
	handlers := backstageResultsHandlers{
		browser: frontendHandlers{
			authentication: authentication,
			logger:         logger,
			random:         rand.Reader,
		},
		results: resultsService,
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
	prologue, ok := handlers.browser.backstagePagePrologue(response, request, actor)
	if !ok {
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		handlers.renderPage(response, request, actor, eventID, prologue, http.StatusOK, nil)
	case http.MethodPost:
		if !handlers.browser.validForm(response, request) {
			return
		}
		handlers.submitPage(response, request, actor, eventID, prologue)
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
	}
}

func (handlers backstageResultsHandlers) submitPage(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	eventID int,
	prologue backstagePrologue,
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
	handlers.renderPage(
		response,
		request,
		actor,
		eventID,
		prologue,
		status,
		resultsFormErrors(err, request.Form, message),
	)
}

func resultsFormErrors(
	err error,
	values url.Values,
	message string,
) frontend.FormErrors {
	action := values.Get("action")
	targetID, _ := strconv.Atoi(values.Get("competition_session_id"))
	if strings.Contains(action, "prizegiving") {
		targetID, _ = strconv.Atoi(values.Get("ceremony_session_id"))
	}
	if action == "save-results-correction" {
		targetID, _ = strconv.Atoi(values.Get("correction_scope_session_id"))
	}
	var validation *rundown.ValidationError
	if errors.As(err, &validation) {
		return frontend.FormErrors{{
			FieldID: frontend.ResultsFieldID(action, targetID, validation.Field),
			Label:   resultsFieldLabel(validation.Field),
			Message: validation.Message,
		}}
	}
	if errors.Is(err, results.ErrCrewReasonRequired) &&
		action == "save-results-correction" {
		return frontend.FormErrors{{
			FieldID: frontend.ResultsFieldID(action, targetID, "crew_reason"),
			Label:   "Crew Reason",
			Message: "Enter a Crew Reason.",
		}}
	}
	if action == "save-results-correction" &&
		strings.TrimSpace(values.Get("crew_reason")) == "" {
		return frontend.FormErrors{{
			FieldID: frontend.ResultsFieldID(action, targetID, "crew_reason"),
			Label:   "Crew Reason",
			Message: "Enter a Crew Reason.",
		}}
	}
	if action == "save-prizegiving-plan" {
		templateRevision, templateRevisionErr := strconv.Atoi(
			values.Get("results_text_template_revision"),
		)
		switch {
		case templateRevisionErr != nil || templateRevision <= 0:
			return frontend.FormErrors{{
				FieldID: frontend.ResultsFieldID(
					action, targetID, "results_text_template_revision",
				),
				Label:   "Results text template revision",
				Message: "Enter a positive integer.",
			}}
		case strings.TrimSpace(values.Get("results_text_template")) == "":
			return frontend.FormErrors{{
				FieldID: frontend.ResultsFieldID(
					action, targetID, "results_text_template",
				),
				Label:   "Results text template",
				Message: "Enter a Results text template.",
			}}
		case values.Get("release_policy") != "ProgressiveOnReveal" &&
			values.Get("release_policy") != "AllAtCue" &&
			values.Get("release_policy") != "AtCeremonyEnd":
			return frontend.FormErrors{{
				FieldID: frontend.ResultsFieldID(action, targetID, "release_policy"),
				Label:   "Release policy",
				Message: "Choose a valid release policy.",
			}}
		}
	}
	field, label, fieldMessage, fieldAction := resolveFormErrorField(resultsFormErrorRules, err, action)
	return formErrorResult(field, label, fieldMessage, fieldAction, message,
		func(field, action string) string {
			return frontend.ResultsFieldID(action, targetID, field)
		},
	)
}

// resultsFormErrorRules is the classify-error-to-field table for
// resultsFormErrors' common tail shape (the bespoke prizegiving-plan and
// crew-reason pre-checks above stay outside the table). Adding a new
// validation rule means adding one row here.
var resultsFormErrorRules = []formErrorRule{
	ruleErrorMessage(matchErr(results.ErrDisposition),
		"save-results-draft", "disposition", "Disposition"),
	ruleErrorMessage(
		matchErrAny(
			results.ErrIncomplete, results.ErrCompetitionRanking, results.ErrUnplacedOrder,
			results.ErrScoreRequired, results.ErrInvalidScore,
		),
		"save-results-draft", "result_standings", "Result Standings",
	),
	ruleErrorMessage(matchErr(results.ErrInvalidAward), "", "award_details", "Award details"),
}

func resultsFieldLabel(field string) string {
	switch field {
	case "score_precision":
		return "Precision"
	case "crew_reason":
		return "Crew Reason"
	default:
		return strings.ReplaceAll(field, "_", " ")
	}
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
	displayNames := request.Form["event_award_recipient_names"]
	paths := request.Form["event_award_path"]
	orders := request.Form["event_award_display_order"]
	if len(keys) != len(names) ||
		len(keys) != len(displayNames) ||
		len(keys) != len(paths) ||
		len(keys) != len(orders) {
		return nil, formValidationError(
			"award_details",
			"must contain complete Event Award rows",
		)
	}
	awards := make([]results.EventAward, 0, len(keys))
	for index := range keys {
		rowEntryIDs := request.Form["event_award_recipient_entry_ids_"+strconv.Itoa(index)]
		if strings.TrimSpace(keys[index]) == "" &&
			strings.TrimSpace(names[index]) == "" &&
			len(rowEntryIDs) == 0 &&
			strings.TrimSpace(displayNames[index]) == "" {
			continue
		}
		order, err := strconv.Atoi(orders[index])
		if err != nil || order <= 0 {
			return nil, formValidationError(
				"event_award_display_order_"+strconv.Itoa(index),
				"must be a positive integer",
			)
		}
		recipients, err := awardRecipients(rowEntryIDs, displayNames[index])
		if err != nil {
			return nil, formValidationError(
				"event_award_recipient_entry_ids_"+strconv.Itoa(index),
				"must identify valid Entries",
			)
		}
		pathKind, pathSessionID, _ := strings.Cut(paths[index], ":")
		path, err := eventAwardPath(pathKind, pathSessionID)
		if err != nil {
			return nil, formValidationError(
				"event_award_path_"+strconv.Itoa(index),
				"must identify a valid release path",
			)
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
		return results.SaveCorrectionInput{}, formValidationError(
			"corrected_results_json",
			"must be valid Results JSON",
		)
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
			return nil, nil, formValidationError(
				"sequence_display_order_"+strconv.Itoa(index),
				"must be a positive integer",
			)
		}
		publicationOrder, err := strconv.Atoi(publicationOrders[index])
		if err != nil || publicationOrder <= 0 {
			return nil, nil, formValidationError(
				"publication_display_order_"+strconv.Itoa(index),
				"must be a positive integer",
			)
		}
		revealMethod := results.RevealMethod(revealMethods[index])
		switch revealMethod {
		case results.RevealStatic,
			results.RevealSequentialPodium,
			results.RevealAnimatedScoreBars:
		default:
			return nil, nil, formValidationError(
				"reveal_method_"+strconv.Itoa(index),
				"must be a valid Reveal Method",
			)
		}
		rows = append(rows, orderRow{
			item: results.ResultItem{
				Kind:                 results.ResultItemKind(kinds[index]),
				CompetitionSessionID: sessionID, AwardKey: awardKeys[index],
				DisplayOrder: sequenceOrder,
				RevealMethod: revealMethod,
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
	displayNames := request.Form["award_recipient_names"]
	promoted := request.Form["award_promoted"]
	orders := request.Form["award_display_order"]
	if len(keys) != len(names) ||
		len(keys) != len(displayNames) ||
		len(keys) != len(promoted) ||
		len(keys) != len(orders) {
		return nil, formValidationError(
			"award_details",
			"must contain complete Competition Award rows",
		)
	}
	awards := make([]results.Award, 0, len(keys))
	for index := range keys {
		rowEntryIDs := request.Form["award_recipient_entry_ids_"+strconv.Itoa(index)]
		if strings.TrimSpace(keys[index]) == "" &&
			strings.TrimSpace(names[index]) == "" &&
			len(rowEntryIDs) == 0 &&
			strings.TrimSpace(displayNames[index]) == "" {
			continue
		}
		order, err := strconv.Atoi(orders[index])
		if err != nil || order <= 0 {
			return nil, formValidationError(
				"award_display_order_"+strconv.Itoa(index),
				"must be a positive integer",
			)
		}
		isPromoted, err := strconv.ParseBool(promoted[index])
		if err != nil {
			return nil, formValidationError(
				"award_promoted_"+strconv.Itoa(index),
				"must be Yes or No",
			)
		}
		recipients, err := awardRecipients(rowEntryIDs, displayNames[index])
		if err != nil {
			return nil, formValidationError(
				"award_recipient_entry_ids_"+strconv.Itoa(index),
				"must identify valid Entries",
			)
		}
		awards = append(awards, results.Award{
			Key: strings.TrimSpace(keys[index]), Name: strings.TrimSpace(names[index]),
			Recipients: recipients, Promoted: isPromoted, DisplayOrder: order,
		})
	}
	return awards, nil
}

func awardRecipients(entryIDs []string, displayNames string) ([]results.AwardRecipient, error) {
	recipients := make([]results.AwardRecipient, 0)
	ids, err := positiveFormIDs(entryIDs)
	if err != nil {
		return nil, results.ErrInvalidInput
	}
	for _, entryID := range ids {
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
			return nil, formValidationError(
				"placement_"+strconv.Itoa(entryID),
				"must be a positive integer",
			)
		}
		displayOrder, err := strconv.Atoi(orders[index])
		if err != nil || displayOrder <= 0 {
			return nil, formValidationError(
				"display_order_"+strconv.Itoa(entryID),
				"must be a positive integer",
			)
		}
		score, err := resultScoreValue(scoreType, scores[index])
		if err != nil {
			return nil, formValidationError(
				"score_"+strconv.Itoa(entryID),
				"must match the selected score type",
			)
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
	prologue backstagePrologue,
	status int,
	formErrors frontend.FormErrors,
) {
	workspaceRequest, err := resultsWorkspaceRequest(request, eventID)
	if err != nil {
		handlers.writeResultsWorkspaceError(response, request, err)
		return
	}
	workspace, err := handlers.results.Workspace(
		request.Context(),
		actor,
		workspaceRequest,
	)
	if err != nil {
		handlers.writeResultsWorkspaceError(response, request, err)
		return
	}
	competitions, prizegivings := resultsWorkspacePage(workspace)
	for _, prizegiving := range workspace.Prizegivings {
		if len(prizegiving.PreviewFindings) != 0 {
			status = http.StatusUnprocessableEntity
			formErrors = append(formErrors, frontend.FormError{
				Message: prizegivingPreflightFindings(
					prizegiving.PreviewFindings,
				),
			})
		}
	}
	commandID, err := planningCommandID(handlers.browser.random)
	if err != nil {
		handlers.browser.frontendError(response, request, "create Results command identity", err)
		return
	}
	handlers.browser.render(response, request, status, frontend.Results(frontend.ResultsPage{
		AccountName: actor.Name, CSRFToken: prologue.CSRFToken,
		ReducedEffects: prologue.ReducedEffects,
		Navigation:     prologue.Navigation,
		CommandID:      commandID,
		Event: events.Event{
			ID: workspace.Event.ID, Name: workspace.Event.Name,
			EventLocale: workspace.Event.EventLocale,
		},
		CanManage:    actor.HasCapability(eventID, viewer.ManageResults),
		Producer:     actor.CanProduceEvent(eventID),
		Competitions: competitions, Prizegivings: prizegivings,
		SubmittedAction: request.Form.Get("action"), Form: request.Form, Errors: formErrors,
		EventAwards:          workspace.EventAwards,
		EventAwardsPreflight: workspace.EventAwardsPreflight,
	}))
}

func resultsWorkspaceRequest(
	request *http.Request,
	eventID int,
) (results.WorkspaceRequest, error) {
	workspaceRequest := results.WorkspaceRequest{
		EventID: eventID,
		EventAwardsPreflight: request.URL.Query().Get(
			"event_awards_preflight",
		) == "true",
	}
	mode := results.PrizegivingPreviewMode(request.URL.Query().Get("preview"))
	if mode == "" {
		return workspaceRequest, nil
	}
	ceremonyID, err := strconv.Atoi(request.URL.Query().Get("ceremony_id"))
	if err != nil || ceremonyID <= 0 {
		return results.WorkspaceRequest{}, results.ErrInvalidInput
	}
	workspaceRequest.PrizegivingPreview =
		&results.WorkspacePrizegivingPreviewRequest{
			CeremonySessionID: ceremonyID,
			Mode:              mode,
		}
	return workspaceRequest, nil
}

func (handlers backstageResultsHandlers) writeResultsWorkspaceError(
	response http.ResponseWriter,
	request *http.Request,
	err error,
) {
	status, message := backstageResultsError(err)
	if status == http.StatusInternalServerError {
		handlers.browser.frontendError(
			response,
			request,
			"read Results Workspace",
			err,
		)
		return
	}
	http.Error(response, message, status)
}

func resultsWorkspacePage(
	workspace results.Workspace,
) ([]frontend.ResultsCompetitionPage, []frontend.ResultsPrizegivingPage) {
	competitions := make(
		[]frontend.ResultsCompetitionPage,
		0,
		len(workspace.Competitions),
	)
	for _, found := range workspace.Competitions {
		entries := make([]frontend.ResultsEntryPage, 0, len(found.Entries))
		for _, entry := range found.Entries {
			entries = append(entries, frontend.ResultsEntryPage{
				Entry: competition.Entry{
					ID: entry.ID, Name: entry.Name,
				},
				Standing: entry.Standing,
			})
		}
		var correction *frontend.ResultsCorrectionPage
		if found.Correction != nil {
			correction = &frontend.ResultsCorrectionPage{
				Scope:                   found.Correction.Scope,
				ScopeSessionID:          found.Correction.ScopeSessionID,
				Current:                 found.Correction.Current,
				PublicationRevision:     found.Correction.PublicationRevision,
				CorrectedResultsJSON:    found.Correction.CorrectedResultsJSON,
				PublicationHistoryCount: found.Correction.PublicationHistoryCount,
			}
		}
		competitions = append(competitions, frontend.ResultsCompetitionPage{
			Session: rundown.CrewSession{
				ID: found.Session.ID, Title: found.Session.Title,
			},
			Draft: found.Draft, Entries: entries,
			AssignedPrizegivingID: found.AssignedPrizegivingID,
			Correction:            correction,
		})
	}
	prizegivings := make(
		[]frontend.ResultsPrizegivingPage,
		0,
		len(workspace.Prizegivings),
	)
	for _, found := range workspace.Prizegivings {
		prizegivings = append(prizegivings, frontend.ResultsPrizegivingPage{
			Session: rundown.CrewSession{
				ID: found.Session.ID, Title: found.Session.Title,
			},
			Designated: found.Designated, Plan: found.Plan,
			Preview: found.Preview,
		})
	}
	return competitions, prizegivings
}

func backstageResultsError(err error) (int, string) {
	var validation *rundown.ValidationError
	switch {
	case errors.As(err, &validation):
		return http.StatusUnprocessableEntity, "Check the Results details and try again."
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
