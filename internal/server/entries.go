package server

import (
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dotwaffle/beamers/internal/attachments"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/competition"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/frontend"
	"github.com/dotwaffle/beamers/internal/presentation"
	"github.com/dotwaffle/beamers/internal/programcontrol"
	"github.com/dotwaffle/beamers/internal/rundown"
)

type entryHandlers struct {
	browser        frontendHandlers
	competition    *competition.Service
	presentation   *presentation.Service
	attachments    *attachments.Service
	program        *programcontrol.Service
	events         *events.Service
	rundown        *rundown.Queries
	notifyDisplays func()
	notifySchedule func()
}

func registerEntryRoutes(
	mux *routeMux,
	authentication *auth.Service,
	competitionService *competition.Service,
	presentationService *presentation.Service,
	attachmentService *attachments.Service,
	programService *programcontrol.Service,
	eventService *events.Service,
	rundownQueries *rundown.Queries,
	notifyDisplays func(),
	notifySchedule func(),
	logger *slog.Logger,
	uploadLimiter *authFailureLimiter,
) {
	handlers := entryHandlers{
		browser: frontendHandlers{
			authentication: authentication,
			logger:         logger,
			limiter:        uploadLimiter,
			random:         rand.Reader,
		},
		competition:    competitionService,
		presentation:   presentationService,
		attachments:    attachmentService,
		program:        programService,
		events:         eventService,
		rundown:        rundownQueries,
		notifyDisplays: notifyDisplays,
		notifySchedule: notifySchedule,
	}
	route := backstagePageRoute()
	route.maxBodyBytes = defaultRequestBytes
	mux.HandleFunc(
		"/backstage/events/{eventID}/competitions/{sessionID}/entries",
		route,
		handlers.entries,
	)
	uploadRoute := backstagePageRoute()
	uploadRoute.timeout = uploadRequestTimeout
	uploadRoute.maxBodyBytes = maxUploadRequestBytes
	mux.HandleFunc(
		"/backstage/events/{eventID}/competitions/{sessionID}/entries/upload",
		uploadRoute,
		handlers.upload,
	)
	submissionRoute := browserPageRoute()
	submissionRoute.maxBodyBytes = defaultRequestBytes
	mux.HandleFunc("/submissions", submissionRoute, handlers.submissions)
	submissionUploadRoute := browserPageRoute()
	submissionUploadRoute.timeout = uploadRequestTimeout
	submissionUploadRoute.maxBodyBytes = maxUploadRequestBytes
	mux.HandleFunc(
		"/submissions/{eventID}/entries/{entryID}/upload",
		submissionUploadRoute,
		handlers.entrySubmissionUpload,
	)
	mux.HandleFunc(
		"/submissions/{eventID}/presentations/{sessionID}/upload",
		submissionUploadRoute,
		handlers.presentationSubmissionUpload,
	)
	presentationRoute := backstagePageRoute()
	presentationRoute.maxBodyBytes = defaultRequestBytes
	mux.HandleFunc(
		"/backstage/events/{eventID}/presentations/{sessionID}/submission",
		presentationRoute,
		handlers.presentationSubmission,
	)
}

func (handlers entryHandlers) entries(response http.ResponseWriter, request *http.Request) {
	actor, ok := handlers.browser.browserAccount(response, request)
	if !ok {
		return
	}
	eventID, eventErr := positivePathID(request, "eventID")
	sessionID, sessionErr := positivePathID(request, "sessionID")
	if eventErr != nil || sessionErr != nil || !actor.CanOperateEvent(eventID) {
		http.NotFound(response, request)
		return
	}
	allowed, err := handlers.canAccessCompetition(request, actor, eventID, sessionID)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Competition scope", err)
		return
	}
	if !allowed {
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
		handlers.render(response, request, actor, eventID, sessionID, csrfToken, http.StatusOK, "")
	case http.MethodPost:
		if !handlers.browser.validForm(response, request) {
			return
		}
		handlers.submit(response, request, actor, eventID, sessionID, csrfToken)
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
	}
}

func (handlers entryHandlers) submit(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	eventID, sessionID int,
	csrfToken string,
) {
	err := handlers.validateEntryActionTargets(request, actor, eventID, sessionID)
	if err == nil {
		err = handlers.submitEntryAction(request, actor, eventID, sessionID)
		if err == nil {
			handlers.notifyDisplays()
			if publicScheduleEntryAction(request.Form.Get("action")) {
				handlers.notifySchedule()
			}
		}
	}
	if err == nil {
		http.Redirect( //nolint:gosec // Target has fixed segments and validated integer IDs.
			response,
			request,
			competitionEntriesPath(eventID, sessionID),
			http.StatusSeeOther,
		)
		return
	}
	status, message := entryError(err)
	handlers.render(response, request, actor, eventID, sessionID, csrfToken, status, message)
}

func publicScheduleEntryAction(action string) bool {
	return action == "create-entry" ||
		action == "update-entry" ||
		action == "change-disposition" ||
		action == "resolve-entry"
}

func (handlers entryHandlers) submitEntryAction(
	request *http.Request,
	actor auth.Account,
	eventID, sessionID int,
) error {
	switch request.Form.Get("action") {
	case "create-entry", "update-entry", "change-disposition", "review-entry",
		"configure-readiness", "configure-order", "configure-submission-eligibility",
		"assign-submitter":
		return handlers.submitEntryConfiguration(request, actor, eventID, sessionID)
	case "attachment-readiness", "version-release-hold",
		"competition-release-policy", "create-reopen-window":
		return handlers.submitEntryAttachment(request, actor, eventID, sessionID)
	case "record-technical-failure", "resolve-entry", "entry-release-hold",
		"claim-control", "defer-entry":
		return handlers.submitEntryLiveAction(request, actor, eventID, sessionID)
	default:
		return competition.ErrInvalidInput
	}
}

func (handlers entryHandlers) submitEntryConfiguration(
	request *http.Request,
	actor auth.Account,
	eventID, sessionID int,
) error {
	switch request.Form.Get("action") {
	case "create-entry":
		_, err := handlers.competition.CreateEntry(
			request.Context(),
			actor,
			competition.CreateEntryInput{
				EventID: eventID, SessionID: sessionID,
				CommandID: request.Form.Get("command_id"),
				Name:      request.Form.Get("entry_name"), PublicDetails: request.Form.Get("public_details"),
				CrewNotes: request.Form.Get("crew_notes"),
			},
		)
		return err
	case "update-entry":
		entryID, revision, err := entryFormTarget(request)
		if err != nil {
			return err
		}
		_, err = handlers.competition.UpdateEntry(
			request.Context(),
			actor,
			competition.UpdateEntryInput{
				EventID: eventID, SessionID: sessionID, EntryID: entryID,
				CommandID: request.Form.Get("command_id"), ExpectedRevision: revision,
				Name: request.Form.Get("entry_name"), PublicDetails: request.Form.Get("public_details"),
				CrewNotes: request.Form.Get("crew_notes"),
			},
		)
		return err
	case "change-disposition":
		entryID, revision, err := entryFormTarget(request)
		if err != nil {
			return err
		}
		_, err = handlers.competition.ChangeDisposition(
			request.Context(),
			actor,
			competition.ChangeDispositionInput{
				EventID: eventID, SessionID: sessionID, EntryID: entryID,
				CommandID: request.Form.Get("command_id"), ExpectedRevision: revision,
				Disposition:           competition.Disposition(request.Form.Get("disposition")),
				ConfirmedLiveOverride: request.Form.Get("confirmed_live_override") == "true",
			},
		)
		return err
	case "review-entry":
		entryID, revision, err := entryFormTarget(request)
		if err != nil {
			return err
		}
		_, err = handlers.competition.ReviewEntry(
			request.Context(),
			actor,
			competition.ReviewEntryInput{
				EventID: eventID, SessionID: sessionID, EntryID: entryID,
				CommandID: request.Form.Get("command_id"), ExpectedRevision: revision,
			},
		)
		return err
	case "configure-readiness":
		revision, err := entryFormNonnegativeInt(request, "expected_readiness_revision")
		if err != nil {
			return err
		}
		_, err = handlers.competition.ConfigureReadiness(
			request.Context(),
			actor,
			competition.ConfigureReadinessInput{
				EventID: eventID, SessionID: sessionID,
				CommandID: request.Form.Get("command_id"), ExpectedReadinessRevision: revision,
				RequireEntryReview:   request.Form.Get("require_entry_review") == "true",
				FileDeliveryRequired: request.Form.Get("file_delivery_required") == "true",
			},
		)
		return err
	case "configure-order":
		return handlers.configureEntryOrder(request, actor, eventID, sessionID)
	case "configure-submission-eligibility":
		revision, err := entryFormNonnegativeInt(
			request,
			"expected_submission_eligibility_revision",
		)
		if err != nil {
			return err
		}
		_, err = handlers.competition.ConfigureSubmissionEligibility(
			request.Context(),
			actor,
			competition.ConfigureSubmissionEligibilityInput{
				EventID: eventID, SessionID: sessionID,
				CommandID:        request.Form.Get("command_id"),
				ExpectedRevision: revision,
				Eligibility: competition.SubmissionEligibility(
					request.Form.Get("submission_eligibility"),
				),
				Override: request.Form.Get("override") == "true",
			},
		)
		return err
	case "assign-submitter":
		entryID, revision, err := entryFormTarget(request)
		if err != nil {
			return err
		}
		accountID, err := entryFormPositiveInt(request, "account_id")
		if err != nil {
			return err
		}
		_, err = handlers.competition.AssignSubmitter(
			request.Context(),
			actor,
			competition.AssignSubmitterInput{
				EventID: eventID, SessionID: sessionID, EntryID: entryID,
				AccountID: accountID, ExpectedRevision: revision,
				CommandID: request.Form.Get("command_id"),
			},
		)
		return err
	default:
		return competition.ErrInvalidInput
	}
}

func (handlers entryHandlers) configureEntryOrder(
	request *http.Request,
	actor auth.Account,
	eventID, sessionID int,
) error {
	revision, err := entryFormNonnegativeInt(request, "expected_order_revision")
	if err != nil {
		return err
	}
	var seed int64
	if value := strings.TrimSpace(request.Form.Get("order_seed")); value != "" {
		seed, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			return competition.ErrInvalidInput
		}
	}
	ids, err := commaSeparatedInts(request.Form.Get("manual_entry_ids"))
	if err != nil {
		return err
	}
	for _, id := range ids {
		if id <= 0 {
			return competition.ErrInvalidInput
		}
	}
	_, err = handlers.competition.ConfigureEntryOrder(
		request.Context(),
		actor,
		competition.ConfigureEntryOrderInput{
			EventID: eventID, SessionID: sessionID,
			CommandID: request.Form.Get("command_id"), ExpectedRevision: revision,
			Policy: competition.EntryOrderPolicy(request.Form.Get("order_policy")),
			Seed:   seed, ManualEntryIDs: ids,
		},
	)
	return err
}

func (handlers entryHandlers) submitEntryAttachment(
	request *http.Request,
	actor auth.Account,
	eventID, sessionID int,
) error {
	switch request.Form.Get("action") {
	case "attachment-readiness":
		entryID, revision, err := entryFormTarget(request)
		if err != nil {
			return err
		}
		versionID, err := entryFormPositiveInt(request, "version_id")
		if err != nil {
			return err
		}
		_, err = handlers.competition.SetEntryAttachmentReadiness(
			request.Context(),
			actor,
			competition.SetEntryAttachmentReadinessInput{
				EventID: eventID, SessionID: sessionID, EntryID: entryID,
				AttachmentVersionID: versionID, CommandID: request.Form.Get("command_id"),
				ExpectedRevision: revision, Final: request.Form.Get("final") == "true",
				Primary: request.Form.Get("primary") == "true",
			},
		)
		return err
	case "version-release-hold":
		versionID, err := entryFormPositiveInt(request, "version_id")
		if err != nil {
			return err
		}
		revision, err := entryFormNonnegativeInt(request, "expected_revision")
		if err != nil {
			return err
		}
		_, err = handlers.attachments.SetVersionRelease(
			request.Context(),
			actor,
			attachments.SetVersionReleaseInput{
				EventID: eventID, VersionID: versionID,
				ExpectedRevision: revision, Hold: request.Form.Get("hold") == "true",
				CommandID: request.Form.Get("command_id"),
			},
		)
		return err
	case "competition-release-policy":
		revision, err := entryFormNonnegativeInt(request, "expected_revision")
		if err != nil {
			return err
		}
		_, err = handlers.attachments.ConfigureCompetitionRelease(
			request.Context(),
			actor,
			attachments.ConfigureCompetitionReleaseInput{
				EventID: eventID, SessionID: sessionID, ExpectedRevision: revision,
				Policy:    attachments.ReleasePolicy(request.Form.Get("release_policy")),
				Override:  request.Form.Get("override") == "true",
				CommandID: request.Form.Get("command_id"),
			},
		)
		return err
	case "create-reopen-window":
		entryID, err := entryFormPositiveInt(request, "entry_id")
		if err != nil {
			return err
		}
		event, err := handlers.events.CrewEvent(request.Context(), actor, eventID)
		if err != nil {
			return err
		}
		location, err := time.LoadLocation(event.Timezone)
		if err != nil {
			return err
		}
		expiresAt, err := time.ParseInLocation(
			"2006-01-02T15:04",
			strings.TrimSpace(request.Form.Get("expires_at")),
			location,
		)
		if err != nil {
			return competition.ErrInvalidInput
		}
		_, err = handlers.attachments.CreateReopenWindow(
			request.Context(),
			actor,
			attachments.ReopenInput{
				EventID: eventID, TargetID: entryID, TargetType: attachments.TargetEntry,
				Reason: request.Form.Get("reason"), ExpiresAt: expiresAt.UTC(),
				CommandID: request.Form.Get("command_id"),
			},
		)
		return err
	default:
		return competition.ErrInvalidInput
	}
}

func (handlers entryHandlers) submitEntryLiveAction(
	request *http.Request,
	actor auth.Account,
	eventID, sessionID int,
) error {
	switch request.Form.Get("action") {
	case "record-technical-failure":
		entryID, revision, err := entryFormTarget(request)
		if err != nil {
			return err
		}
		_, err = handlers.competition.RecordTechnicalFailure(
			request.Context(),
			actor,
			competition.RecordTechnicalFailureInput{
				EventID: eventID, SessionID: sessionID, EntryID: entryID,
				CommandID: request.Form.Get("command_id"), ExpectedRevision: revision,
				Reason: request.Form.Get("crew_reason"),
			},
		)
		return err
	case "resolve-entry":
		entryID, revision, err := entryFormTarget(request)
		if err != nil {
			return err
		}
		_, err = handlers.competition.ResolveEntry(
			request.Context(),
			actor,
			competition.ResolveEntryInput{
				EventID: eventID, SessionID: sessionID, EntryID: entryID,
				CommandID: request.Form.Get("command_id"), ExpectedRevision: revision,
				ResultDisposition:             request.Form.Get("result_disposition"),
				CrewReason:                    request.Form.Get("crew_reason"),
				PublicDisqualificationMessage: request.Form.Get("public_disqualification_message"),
			},
		)
		return err
	case "entry-release-hold":
		entryID, revision, err := entryFormTarget(request)
		if err != nil {
			return err
		}
		_, err = handlers.competition.SetEntryReleaseHold(
			request.Context(),
			actor,
			competition.SetEntryReleaseHoldInput{
				EventID: eventID, SessionID: sessionID, EntryID: entryID,
				CommandID: request.Form.Get("command_id"), ExpectedRevision: revision,
				Hold:       request.Form.Get("hold") == "true",
				CrewReason: request.Form.Get("crew_reason"),
			},
		)
		return err
	case "claim-control":
		revision, err := entryFormNonnegativeInt(request, "expected_control_revision")
		if err != nil {
			return err
		}
		_, err = handlers.program.Control(
			request.Context(),
			actor,
			programcontrol.ControlInput{
				EventID: eventID, SessionID: sessionID,
				Action: programcontrol.ControlClaim, ExpectedRevision: revision,
				CommandID: request.Form.Get("command_id"),
			},
		)
		return err
	case "defer-entry":
		entryID, entryRevision, err := entryFormTarget(request)
		if err != nil {
			return err
		}
		programRevision, err := entryFormNonnegativeInt(request, "expected_program_revision")
		if err != nil {
			return err
		}
		controlRevision, err := entryFormNonnegativeInt(request, "expected_control_revision")
		if err != nil {
			return err
		}
		_, err = handlers.program.DeferEntry(
			request.Context(),
			actor,
			programcontrol.DeferEntryInput{
				EventID: eventID, SessionID: sessionID, EntryID: entryID,
				CommandID:             request.Form.Get("command_id"),
				ExpectedEntryRevision: entryRevision, ExpectedProgramRevision: programRevision,
				ExpectedControlRevision: controlRevision,
			},
		)
		return err
	default:
		return competition.ErrInvalidInput
	}
}

func (handlers entryHandlers) validateEntryActionTargets(
	request *http.Request,
	actor auth.Account,
	eventID, sessionID int,
) error {
	entryValue := strings.TrimSpace(request.Form.Get("entry_id"))
	versionValue := strings.TrimSpace(request.Form.Get("version_id"))
	if entryValue == "" && versionValue == "" {
		return nil
	}
	state, err := handlers.competition.Get(request.Context(), actor, eventID, sessionID)
	if err != nil {
		return err
	}
	if entryID, parseErr := strconv.Atoi(entryValue); entryValue != "" &&
		parseErr == nil && entryID > 0 && !competitionContainsEntry(state, entryID) {
		return competition.ErrCompetitionNotFound
	}
	if versionID, parseErr := strconv.Atoi(versionValue); versionValue != "" &&
		parseErr == nil && versionID > 0 {
		attachmentState, stateErr := handlers.attachments.CompetitionCrewState(
			request.Context(), actor, eventID, sessionID,
		)
		if stateErr != nil {
			return stateErr
		}
		if !competitionContainsVersion(state, attachmentState, versionID) {
			return competition.ErrCompetitionNotFound
		}
	}
	return nil
}

func (handlers entryHandlers) upload(response http.ResponseWriter, request *http.Request) {
	actor, ok := handlers.browser.browserAccount(response, request)
	if !ok {
		return
	}
	eventID, eventErr := positivePathID(request, "eventID")
	sessionID, sessionErr := positivePathID(request, "sessionID")
	if eventErr != nil || sessionErr != nil || !actor.CanProduceEvent(eventID) {
		http.NotFound(response, request)
		return
	}
	if request.Method != http.MethodPost {
		frontendMethodNotAllowed(response, http.MethodPost)
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, maxUploadRequestBytes)
	if !handlers.browser.validMultipartForm(response, request) {
		return
	}
	entryID, err := entryFormPositiveInt(request, "entry_id")
	if err != nil {
		http.Error(response, "invalid Attachment owner", http.StatusUnprocessableEntity)
		return
	}
	state, err := handlers.competition.Get(
		request.Context(), actor, eventID, sessionID,
	)
	if err != nil || !competitionContainsEntry(state, entryID) {
		http.NotFound(response, request)
		return
	}
	name, filename, mediaType, body, ok := multipartUpload(response, request)
	if !ok {
		return
	}
	defer func() {
		if closeErr := body.Close(); closeErr != nil {
			handlers.browser.logger.Warn("close browser-uploaded Attachment", "error", closeErr)
		}
	}()
	_, err = handlers.attachments.UploadForCrew(
		request.Context(),
		actor,
		attachments.CrewUploadInput{
			EventID: eventID, TargetID: entryID, TargetType: attachments.TargetEntry,
			CommandID: request.FormValue("command_id"), Name: name,
			OriginalFilename: filename, MediaType: mediaType, Body: body,
			CrewOnly: request.FormValue("crew_only") == "true",
		},
	)
	if err != nil {
		status, message := attachmentEntryError(err)
		http.Error(response, message, status)
		return
	}
	http.Redirect( //nolint:gosec // Target has fixed segments and validated integer IDs.
		response,
		request,
		competitionEntriesPath(eventID, sessionID),
		http.StatusSeeOther,
	)
}

func (handlers entryHandlers) canAccessCompetition(
	request *http.Request,
	actor auth.Account,
	eventID, sessionID int,
) (bool, error) {
	if actor.CanProduceEvent(eventID) {
		return true, nil
	}
	if len(actor.EventScopes[eventID].LaneIDs) == 0 {
		return false, nil
	}
	state, err := handlers.rundown.CrewRundown(request.Context(), actor, eventID)
	if err != nil {
		return false, err
	}
	for _, session := range state.Sessions {
		if session.ID == sessionID && session.Type == rundown.SessionCompetition {
			return canAccessCompetition(actor, eventID, session), nil
		}
	}
	return false, nil
}

func competitionEntriesPath(eventID, sessionID int) string {
	return "/backstage/events/" + strconv.Itoa(eventID) + "/competitions/" +
		strconv.Itoa(sessionID) + "/entries"
}

func competitionContainsEntry(state competition.State, entryID int) bool {
	for _, entry := range state.Entries {
		if entry.ID == entryID {
			return true
		}
	}
	return false
}

func competitionContainsVersion(
	state competition.State,
	attachmentState attachments.CrewState,
	versionID int,
) bool {
	for _, version := range attachmentState.Versions {
		if version.ID == versionID && competitionContainsEntry(state, version.OwnerID) {
			return true
		}
	}
	return false
}

func entryFormTarget(request *http.Request) (int, int, error) {
	entryID, err := entryFormPositiveInt(request, "entry_id")
	if err != nil {
		return 0, 0, err
	}
	revision, err := entryFormPositiveInt(request, "expected_revision")
	if err != nil {
		return 0, 0, err
	}
	return entryID, revision, nil
}

func entryFormPositiveInt(request *http.Request, field string) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(request.Form.Get(field)))
	if err != nil || value <= 0 {
		return 0, competition.ErrInvalidInput
	}
	return value, nil
}

func entryFormNonnegativeInt(request *http.Request, field string) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(request.Form.Get(field)))
	if err != nil || value < 0 {
		return 0, competition.ErrInvalidInput
	}
	return value, nil
}

func (handlers entryHandlers) render(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	eventID, sessionID int,
	csrfToken string,
	status int,
	message string,
) {
	state, err := handlers.competition.Get(request.Context(), actor, eventID, sessionID)
	if err != nil {
		if errors.Is(err, competition.ErrCompetitionNotFound) {
			http.NotFound(response, request)
			return
		}
		handlers.browser.frontendError(response, request, "read Competition Entries", err)
		return
	}
	event, err := handlers.events.CrewEvent(request.Context(), actor, eventID)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Competition Event", err)
		return
	}
	preflight, err := handlers.competition.PreflightStart(
		request.Context(), actor, eventID, sessionID,
	)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Competition Start preflight", err)
		return
	}
	attachmentState, err := handlers.attachments.CompetitionCrewState(
		request.Context(), actor, eventID, sessionID,
	)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Competition Attachments", err)
		return
	}
	programState, err := handlers.program.Current(
		request.Context(), actor, eventID, sessionID,
	)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Competition Program control", err)
		return
	}
	var submissionAccounts []competition.SubmissionAccount
	if actor.CanProduceEvent(eventID) {
		submissionAccounts, err = handlers.competition.AssignableAccounts(
			request.Context(),
			actor,
			eventID,
		)
		if err != nil {
			handlers.browser.frontendError(
				response,
				request,
				"list Submitter Accounts",
				err,
			)
			return
		}
	}
	commandID, err := planningCommandID(handlers.browser.random)
	if err != nil {
		handlers.browser.frontendError(response, request, "create Entry command identity", err)
		return
	}
	handlers.browser.render(response, request, status, frontend.CompetitionEntries(
		frontend.CompetitionEntriesPage{
			AccountID: actor.ID, AccountName: actor.Name, Producer: actor.CanProduceEvent(eventID),
			CSRFToken:      csrfToken,
			ReducedEffects: reducedEffectsCookie(request), Navigation: backstageNavigation(actor),
			CommandID: commandID, Event: event, State: state, Preflight: preflight,
			Attachments: attachmentState, Program: programState, Error: message,
			SubmissionAccounts: submissionAccounts,
		},
	))
}

func entryError(err error) (int, string) {
	switch {
	case errors.Is(err, competition.ErrInvalidInput), errors.Is(err, command.ErrInvalidID):
		return http.StatusUnprocessableEntity, "Check the Entry details and try again."
	case errors.Is(err, competition.ErrSubmissionClosed):
		return http.StatusGone, "The fixed Submission Deadline has passed."
	case errors.Is(err, competition.ErrEntryRevisionConflict),
		errors.Is(err, competition.ErrSubmissionEligibilityRevision),
		errors.Is(err, competition.ErrReadinessRevisionConflict),
		errors.Is(err, competition.ErrAttachmentReadinessRevisionConflict),
		errors.Is(err, competition.ErrEntryOrderRevisionConflict),
		errors.Is(err, competition.ErrCommandConflict):
		return http.StatusConflict, "Competition state changed. Reload and try again."
	case errors.Is(err, competition.ErrEntryResolution),
		errors.Is(err, competition.ErrEntryAssigned),
		errors.Is(err, competition.ErrCrewReasonRequired),
		errors.Is(err, competition.ErrEntryOrderInvalid),
		errors.Is(err, competition.ErrEntryOrderLocked),
		errors.Is(err, competition.ErrPresentedEntryDisposition),
		errors.Is(err, competition.ErrLiveDispositionConfirmation):
		return http.StatusUnprocessableEntity, "The Entry cannot take that transition."
	case errors.Is(err, competition.ErrSubmissionIneligible):
		return http.StatusForbidden, "Submission Eligibility does not permit a new Entry."
	case errors.Is(err, competition.ErrSubmitterRequired):
		return http.StatusNotFound, "Entry not found."
	case errors.Is(err, competition.ErrProducerRequired),
		errors.Is(err, competition.ErrCompetitionNotFound):
		return http.StatusNotFound, "Competition not found."
	case errors.Is(err, attachments.ErrInvalidInput),
		errors.Is(err, attachments.ErrReleasePolicy),
		errors.Is(err, attachments.ErrReopenWindowExtension):
		return http.StatusUnprocessableEntity, "Check the Attachment details and try again."
	case errors.Is(err, attachments.ErrReleaseRevision),
		errors.Is(err, attachments.ErrReopenWindowRevision),
		errors.Is(err, attachments.ErrCommandConflict):
		return http.StatusConflict, "Attachment state changed. Reload and try again."
	case errors.Is(err, attachments.ErrProducerRequired),
		errors.Is(err, attachments.ErrUploadTargetNotFound):
		return http.StatusNotFound, "Attachment target not found."
	case errors.Is(err, programcontrol.ErrControlRevision),
		errors.Is(err, programcontrol.ErrProgramRevision),
		errors.Is(err, programcontrol.ErrEntryRevision),
		errors.Is(err, programcontrol.ErrCommandConflict):
		return http.StatusConflict, "Program control changed. Reload and try again."
	case errors.Is(err, programcontrol.ErrControlOwned),
		errors.Is(err, programcontrol.ErrControlOwnerRequired),
		errors.Is(err, programcontrol.ErrEntryDefer),
		errors.Is(err, programcontrol.ErrOperatorRequired):
		return http.StatusUnprocessableEntity, "The Entry cannot be deferred from current Program control."
	default:
		return http.StatusInternalServerError,
			"Competition Entries action failed (" + strconv.Itoa(http.StatusInternalServerError) + ")."
	}
}

func attachmentEntryError(err error) (int, string) {
	switch {
	case errors.Is(err, attachments.ErrUploadClosed):
		return http.StatusGone, "Uploads are closed."
	case errors.Is(err, attachments.ErrAttachmentTooLarge):
		return http.StatusRequestEntityTooLarge, "Attachment is too large."
	case errors.Is(err, attachments.ErrInvalidInput), errors.Is(err, command.ErrInvalidID):
		return http.StatusUnprocessableEntity, "Check the Attachment details and try again."
	case errors.Is(err, attachments.ErrCommandConflict):
		return http.StatusConflict, "Attachment command identity conflict."
	case errors.Is(err, attachments.ErrProducerRequired),
		errors.Is(err, attachments.ErrUploadTargetNotFound):
		return http.StatusNotFound, "Attachment target not found."
	default:
		return http.StatusInternalServerError, "Attachment upload failed."
	}
}
