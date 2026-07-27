package server

import (
	"net/http"

	"github.com/dotwaffle/beamers/internal/attachments"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/competition"
	"github.com/dotwaffle/beamers/internal/frontend"
)

func (handlers entryHandlers) submissions(response http.ResponseWriter, request *http.Request) {
	actor, ok := handlers.browser.browserAccount(response, request)
	if !ok {
		return
	}
	csrfToken, err := handlers.browser.csrfToken(response, request)
	if err != nil {
		handlers.browser.frontendError(response, request, "create CSRF proof", err)
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		handlers.renderSubmissions(response, request, actor, csrfToken, http.StatusOK, "")
	case http.MethodPost:
		if !handlers.browser.validForm(response, request) {
			return
		}
		err = handlers.submitSubmission(request, actor)
		if err == nil {
			handlers.notifySchedule()
			http.Redirect(response, request, "/submissions", http.StatusSeeOther)
			return
		}
		status, message := entryError(err)
		handlers.renderSubmissions(response, request, actor, csrfToken, status, message)
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
	}
}

func (handlers entryHandlers) submitSubmission(
	request *http.Request,
	actor auth.Account,
) error {
	eventID, err := entryFormPositiveInt(request, "event_id")
	if err != nil {
		return err
	}
	sessionID, err := entryFormPositiveInt(request, "session_id")
	if err != nil {
		return err
	}
	switch request.Form.Get("action") {
	case "create":
		_, err = handlers.competition.CreateSubmission(
			request.Context(),
			actor,
			competition.CreateSubmissionInput{
				EventID: eventID, SessionID: sessionID,
				CommandID:     request.Form.Get("command_id"),
				Name:          request.Form.Get("entry_name"),
				PublicDetails: request.Form.Get("public_details"),
			},
		)
	case "update":
		entryID, revision, targetErr := entryFormTarget(request)
		if targetErr != nil {
			return targetErr
		}
		_, err = handlers.competition.UpdateSubmission(
			request.Context(),
			actor,
			competition.UpdateSubmissionInput{
				EventID: eventID, SessionID: sessionID, EntryID: entryID,
				CommandID: request.Form.Get("command_id"), ExpectedRevision: revision,
				Name:          request.Form.Get("entry_name"),
				PublicDetails: request.Form.Get("public_details"),
			},
		)
	default:
		err = competition.ErrInvalidInput
	}
	return err
}

func (handlers entryHandlers) submissionUpload(
	response http.ResponseWriter,
	request *http.Request,
) {
	actor, ok := handlers.browser.browserAccount(response, request)
	if !ok {
		return
	}
	if request.Method != http.MethodPost {
		frontendMethodNotAllowed(response, http.MethodPost)
		return
	}
	eventID, eventErr := positivePathID(request, "eventID")
	entryID, entryErr := positivePathID(request, "entryID")
	if eventErr != nil || entryErr != nil {
		http.NotFound(response, request)
		return
	}
	clientKey, accountKey := accountUploadLimitKeys(request, actor.ID)
	if retryAfter, blocked := handlers.browser.limiter.reserve(clientKey, accountKey); blocked {
		writeUploadRateLimit(response, retryAfter)
		return
	}
	if err := handlers.attachments.AuthorizeAccountUpload(
		request.Context(), actor, eventID, entryID,
	); err != nil {
		http.NotFound(response, request)
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, maxUploadRequestBytes)
	if !handlers.browser.validMultipartForm(response, request) {
		return
	}
	name, filename, mediaType, body, ok := multipartUpload(response, request)
	if !ok {
		return
	}
	defer func() {
		if closeErr := body.Close(); closeErr != nil {
			handlers.browser.logger.Warn("close Account-uploaded Attachment", "error", closeErr)
		}
	}()
	_, err := handlers.attachments.UploadForAccount(
		request.Context(),
		actor,
		attachments.AccountUploadInput{
			EventID: eventID, EntryID: entryID,
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
	http.Redirect(response, request, "/submissions", http.StatusSeeOther)
}

func (handlers entryHandlers) renderSubmissions(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	csrfToken string,
	status int,
	message string,
) {
	competitions, err := handlers.competition.Submissions(request.Context(), actor)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Account submissions", err)
		return
	}
	attachmentState, err := handlers.attachments.SubmittedEntryState(request.Context(), actor)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Account submission files", err)
		return
	}
	commandID, err := planningCommandID(handlers.browser.random)
	if err != nil {
		handlers.browser.frontendError(response, request, "create submission command identity", err)
		return
	}
	handlers.browser.render(response, request, status, frontend.Submissions(
		frontend.SubmissionsPage{
			AccountName: actor.Name, CSRFToken: csrfToken,
			ReducedEffects: reducedEffectsCookie(request),
			Backstage:      backstageAvailable(backstageNavigation(actor)),
			CommandID:      commandID, Competitions: competitions,
			Attachments: attachmentState, Error: message,
		},
	))
}
