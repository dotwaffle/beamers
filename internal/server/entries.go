package server

import (
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dotwaffle/beamers/internal/attachments"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/competition"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/frontend"
	"github.com/dotwaffle/beamers/internal/presentation"
	"github.com/dotwaffle/beamers/internal/programcontrol"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/voting"
)

type entryHandlers struct {
	browser      frontendHandlers
	competition  *competition.Service
	presentation *presentation.Service
	voting       *voting.Service
	attachments  *attachments.Service
	program      *programcontrol.Service
	events       *events.Service
	rundown      *rundown.Queries
}

func registerEntryRoutes(
	mux *routeMux,
	authentication *auth.Service,
	competitionService *competition.Service,
	presentationService *presentation.Service,
	votingService *voting.Service,
	attachmentService *attachments.Service,
	programService *programcontrol.Service,
	eventService *events.Service,
	rundownQueries *rundown.Queries,
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
		competition:  competitionService,
		presentation: presentationService,
		voting:       votingService,
		attachments:  attachmentService,
		program:      programService,
		events:       eventService,
		rundown:      rundownQueries,
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
	mux.HandleFunc("/my-participation", submissionRoute, handlers.submissions)
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
		handlers.render(response, request, actor, eventID, sessionID, csrfToken, http.StatusOK, nil)
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
	}
	if err == nil {
		http.Redirect(
			response,
			request,
			competitionEntriesPath(eventID, sessionID),
			http.StatusSeeOther,
		)
		return
	}
	status, formErrors := entryFormErrors(err, request.Form)
	handlers.render(response, request, actor, eventID, sessionID, csrfToken, status, formErrors)
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
		"competition-release-policy", "create-reopen-window",
		"extend-reopen-window", "close-reopen-window":
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

// manualEntryOrderIDs parses the Manual Order position picker's repeated
// "manual_entry_ids" values, one per rendered position select. A blank
// position (left "unranked") is skipped rather than rejected, so a Producer
// need not fill every slot to submit a partial order.
func manualEntryOrderIDs(values []string) ([]int, error) {
	ids := make([]int, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		id, err := strconv.Atoi(value)
		if err != nil || id <= 0 {
			return nil, competition.ErrInvalidInput
		}
		ids = append(ids, id)
	}
	return ids, nil
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
	ids, err := manualEntryOrderIDs(request.Form["manual_entry_ids"])
	if err != nil {
		return err
	}
	policy := competition.EntryOrderPolicy(request.Form.Get("order_policy"))
	if policy != competition.EntryOrderManual {
		// The Manual order picker renders one position select per Entry
		// regardless of the currently chosen policy, so a Producer can
		// switch away from Manual order without first clearing every
		// position back to "unranked." Only Manual order consumes the
		// picker's selections; Submission order and Deterministic
		// shuffle require no Manual Entry IDs.
		ids = nil
	}
	_, err = handlers.competition.ConfigureEntryOrder(
		request.Context(),
		actor,
		competition.ConfigureEntryOrderInput{
			EventID: eventID, SessionID: sessionID,
			CommandID: request.Form.Get("command_id"), ExpectedRevision: revision,
			Policy: policy,
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
		if strings.TrimSpace(request.Form.Get("reason")) == "" {
			return formValidationError("reason", "is required")
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
			return formValidationError("expires_at", "must be a valid local date and time")
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
	case "extend-reopen-window", "close-reopen-window":
		entryID, err := entryFormPositiveInt(request, "entry_id")
		if err != nil {
			return err
		}
		return handlers.updateReopenWindow(
			request,
			actor,
			eventID,
			attachments.TargetEntry,
			entryID,
		)
	default:
		return competition.ErrInvalidInput
	}
}

func (handlers entryHandlers) updateReopenWindow(
	request *http.Request,
	actor auth.Account,
	eventID int,
	targetType attachments.TargetKind,
	targetID int,
) error {
	windowID, err := entryFormPositiveInt(request, "window_id")
	if err != nil {
		return err
	}
	revision, err := entryFormPositiveInt(request, "expected_revision")
	if err != nil {
		return err
	}
	windows, err := handlers.attachments.CrewReopenWindows(
		request.Context(),
		actor,
		eventID,
		targetType,
		targetID,
	)
	if err != nil {
		return err
	}
	found := false
	for _, window := range windows {
		if window.ID == windowID {
			found = true
			break
		}
	}
	if !found {
		return attachments.ErrUploadTargetNotFound
	}
	input := attachments.UpdateReopenInput{
		EventID: eventID, WindowID: windowID, ExpectedRevision: revision,
		CommandID: request.Form.Get("command_id"),
	}
	switch request.Form.Get("action") {
	case "close-reopen-window":
		if request.Form.Get("confirm_close") != "true" {
			return formValidationError("confirm_close", "must be checked")
		}
		input.Close = true
	case "extend-reopen-window":
		event, eventErr := handlers.events.CrewEvent(request.Context(), actor, eventID)
		if eventErr != nil {
			return eventErr
		}
		location, locationErr := time.LoadLocation(event.Timezone)
		if locationErr != nil {
			return locationErr
		}
		input.ExpiresAt, err = time.ParseInLocation(
			"2006-01-02T15:04",
			strings.TrimSpace(request.Form.Get("expires_at")),
			location,
		)
		if err != nil {
			return formValidationError("expires_at", "must be a valid local date and time")
		}
		input.ExpiresAt = input.ExpiresAt.UTC()
	default:
		return attachments.ErrInvalidInput
	}
	_, err = handlers.attachments.UpdateReopenWindow(request.Context(), actor, input)
	return err
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
	name, filename, mediaType, body, uploadErr := readMultipartUpload(request)
	if uploadErr != nil {
		handlers.renderEntryUploadFailure(
			response, request, actor, eventID, sessionID, entryID, uploadErr,
		)
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
		handlers.renderEntryUploadFailure(
			response, request, actor, eventID, sessionID, entryID, err,
		)
		return
	}
	http.Redirect(
		response,
		request,
		competitionEntriesPath(eventID, sessionID),
		http.StatusSeeOther,
	)
}

func (handlers entryHandlers) renderEntryUploadFailure(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	eventID, sessionID, entryID int,
	err error,
) {
	csrfToken, csrfErr := handlers.browser.csrfToken(response, request)
	if csrfErr != nil {
		handlers.browser.frontendError(response, request, "create CSRF proof", csrfErr)
		return
	}
	request.Form.Set("action", "upload-entry")
	status, message := attachmentEntryError(err)
	var typedErrors frontend.FormErrors
	if errors.Is(err, attachments.ErrInvalidName) {
		typedErrors = append(typedErrors, frontend.FormError{
			FieldID: frontend.WorkflowFieldID("upload-entry", entryID, 0, 0, "name"),
			Label:   "Attachment name", Message: "Enter an Attachment name.",
		})
	}
	if errors.Is(err, attachments.ErrInvalidFilename) {
		typedErrors = append(typedErrors, frontend.FormError{
			FieldID: frontend.WorkflowFieldID("upload-entry", entryID, 0, 0, "file"),
			Label:   "File", Message: "Choose a file with a valid name.",
		})
	}
	if len(typedErrors) > 0 {
		handlers.render(
			response, request, actor, eventID, sessionID, csrfToken, status, typedErrors,
		)
		return
	}
	field, label := "", ""
	switch {
	case errors.Is(err, errInvalidUpload):
		status, message = http.StatusBadRequest, "Invalid upload."
	case errors.Is(err, errUploadFileRequired):
		status, field, label, message = http.StatusUnprocessableEntity, "file", "File", "Choose a file."
	}
	formErrors := frontend.FormErrors{{Message: message}}
	if field != "" {
		formErrors[0].FieldID = frontend.WorkflowFieldID("upload-entry", entryID, 0, 0, field)
		formErrors[0].Label = label
	}
	handlers.render(
		response, request, actor, eventID, sessionID, csrfToken, status, formErrors,
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
	formErrors frontend.FormErrors,
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
			ReducedEffects: reducedEffectsCookie(request),
			Navigation:     backstageNavigation(actor, request.URL.Path),
			CommandID:      commandID, Event: event, State: state, Preflight: preflight,
			Attachments: attachmentState, Program: programState,
			SubmissionAccounts: submissionAccounts,
			SubmittedAction:    request.Form.Get("action"), Form: request.Form, Errors: formErrors,
		},
	))
}

func entryFormErrors(err error, values url.Values) (int, frontend.FormErrors) {
	status, message := entryError(err)
	if err == nil {
		return status, nil
	}
	action := values.Get("action")
	entryID, _ := strconv.Atoi(values.Get("entry_id"))
	versionID, _ := strconv.Atoi(values.Get("version_id"))
	windowID, _ := strconv.Atoi(values.Get("window_id"))
	if action == "create-reopen-window" {
		if validationErrors := createReopenWindowFormErrors(values, entryID); len(validationErrors) > 0 {
			return status, validationErrors
		}
	}
	if action == "create-entry" || action == "update-entry" {
		var result frontend.FormErrors
		if errors.Is(err, competition.ErrInvalidEntryName) {
			result = append(result, frontend.FormError{
				FieldID: frontend.WorkflowFieldID(action, entryID, 0, 0, "entry_name"),
				Label:   "Entry name", Message: "Enter an Entry name.",
			})
		}
		if errors.Is(err, competition.ErrInvalidEntryPublicDetails) {
			result = append(result, frontend.FormError{
				FieldID: frontend.WorkflowFieldID(action, entryID, 0, 0, "public_details"),
				Label:   "Public details", Message: "Enter no more than 10000 characters.",
			})
		}
		if utf8.RuneCountInString(values.Get("crew_notes")) > 10000 {
			result = append(result, frontend.FormError{
				FieldID: frontend.WorkflowFieldID(action, entryID, 0, 0, "crew_notes"),
				Label:   "Crew notes", Message: "Enter no more than 10000 characters.",
			})
		}
		if len(result) > 0 {
			return status, result
		}
	}
	if action == "resolve-entry" && errors.Is(err, competition.ErrCrewReasonRequired) {
		var result frontend.FormErrors
		crewReason := strings.TrimSpace(values.Get("crew_reason"))
		if crewReason == "" || utf8.RuneCountInString(crewReason) > 10000 {
			result = append(result, frontend.FormError{
				FieldID: frontend.WorkflowFieldID(action, entryID, 0, 0, "crew_reason"),
				Label:   "Crew Reason", Message: "Enter a Crew Reason of no more than 10000 characters.",
			})
		}
		if utf8.RuneCountInString(strings.TrimSpace(values.Get("public_disqualification_message"))) > 10000 {
			result = append(result, frontend.FormError{
				FieldID: frontend.WorkflowFieldID(action, entryID, 0, 0, "public_disqualification_message"),
				Label:   "Public disqualification message", Message: "Enter no more than 10000 characters.",
			})
		}
		if len(result) > 0 {
			return status, result
		}
	}
	field, label, fieldMessage := "", "", message
	var validation *rundown.ValidationError
	switch {
	case errors.As(err, &validation):
		field, fieldMessage = validation.Field, validation.Message
	case errors.Is(err, competition.ErrInvalidEntryName):
		field, label, fieldMessage = "entry_name", "Entry name", "Enter an Entry name."
	case errors.Is(err, competition.ErrInvalidEntryPublicDetails):
		field, label, fieldMessage = "public_details", "Public details", "Enter no more than 10000 characters."
	case errors.Is(err, competition.ErrCrewReasonRequired):
		field, label = "crew_reason", "Crew Reason"
	case errors.Is(err, competition.ErrEntryOrderInvalid):
		field, label = "manual_entry_ids", "Manual Entry IDs"
	case errors.Is(err, competition.ErrLiveDispositionConfirmation):
		field, label = "confirmed_live_override", "Live disposition confirmation"
	case errors.Is(err, competition.ErrInvalidInput) &&
		(action == "create-entry" || action == "update-entry"):
		field, label, fieldMessage = "crew_notes", "Crew notes", "Enter no more than 10000 characters."
	case errors.Is(err, competition.ErrInvalidInput) && action == "assign-submitter":
		field, label = "account_id", "Submitter Account"
	case errors.Is(err, competition.ErrInvalidInput) && action == "change-disposition":
		field, label = "disposition", "Disposition"
	case errors.Is(err, competition.ErrInvalidInput) && action == "configure-submission-eligibility":
		field, label = "submission_eligibility", "Eligibility"
	case errors.Is(err, attachments.ErrReleasePolicy):
		field, label = "release_policy", "Release Policy"
	case errors.Is(err, attachments.ErrReopenWindowExtension),
		errors.Is(err, attachments.ErrReopenWindowExpiry):
		field, label = "expires_at", "Expiry"
	case errors.Is(err, attachments.ErrInvalidInput) && action == "create-reopen-window":
		field, label = "expires_at", "Expiry"
	case errors.Is(err, attachments.ErrInvalidInput) && action == "close-reopen-window":
		field, label = "confirm_close", "Early closure confirmation"
	case errors.Is(err, attachments.ErrInvalidInput) && action == "attachment-readiness":
		field, label = "primary", "Primary Attachment"
	}
	if field == "" {
		return status, frontend.FormErrors{{Message: message}}
	}
	if label == "" {
		label = strings.ReplaceAll(field, "_", " ")
	}
	return status, frontend.FormErrors{{
		FieldID: frontend.WorkflowFieldID(action, entryID, versionID, windowID, field),
		Label:   label, Message: fieldMessage,
	}}
}

func createReopenWindowFormErrors(values url.Values, targetID int) frontend.FormErrors {
	var result frontend.FormErrors
	if strings.TrimSpace(values.Get("reason")) == "" {
		result = append(result, frontend.FormError{
			FieldID: frontend.WorkflowFieldID("create-reopen-window", targetID, 0, 0, "reason"),
			Label:   "Reopen reason", Message: "is required",
		})
	}
	if _, err := time.Parse("2006-01-02T15:04", strings.TrimSpace(values.Get("expires_at"))); err != nil {
		result = append(result, frontend.FormError{
			FieldID: frontend.WorkflowFieldID("create-reopen-window", targetID, 0, 0, "expires_at"),
			Label:   "Expiry", Message: "must be a valid local date and time",
		})
	}
	return result
}

func entryError(err error) (int, string) {
	var validation *rundown.ValidationError
	switch {
	case errors.As(err, &validation):
		return http.StatusUnprocessableEntity, validation.Error()
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
		errors.Is(err, attachments.ErrReleasePolicy):
		return http.StatusUnprocessableEntity, "Check the Attachment details and try again."
	case errors.Is(err, attachments.ErrReopenWindowExtension):
		return http.StatusUnprocessableEntity,
			"Choose an expiry later than the current expiry."
	case errors.Is(err, attachments.ErrReopenWindowExpiry):
		return http.StatusUnprocessableEntity, "Choose a future expiry within 7 days."
	case errors.Is(err, attachments.ErrReopenWindowInactive):
		return http.StatusUnprocessableEntity, "Only active Reopen Windows can be changed."
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
