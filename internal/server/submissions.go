package server

import (
	"errors"
	"net/http"

	"github.com/dotwaffle/beamers/internal/attachments"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/competition"
	"github.com/dotwaffle/beamers/internal/frontend"
	"github.com/dotwaffle/beamers/internal/presentation"
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
			http.Redirect(response, request, "/my-participation", http.StatusSeeOther)
			return
		}
		status, message := submissionError(err)
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
	case "update-presentation":
		revision, targetErr := entryFormNonnegativeInt(request, "expected_revision")
		if targetErr != nil {
			return targetErr
		}
		_, err = handlers.presentation.UpdateSubmission(
			request.Context(),
			actor,
			presentation.UpdateSubmissionInput{
				EventID: eventID, SessionID: sessionID,
				CommandID: request.Form.Get("command_id"), ExpectedRevision: revision,
				Speaker: request.Form.Get("speaker"), PublicDetails: request.Form.Get("public_details"),
			},
		)
	default:
		err = competition.ErrInvalidInput
	}
	return err
}

func (handlers entryHandlers) entrySubmissionUpload(
	response http.ResponseWriter,
	request *http.Request,
) {
	handlers.submissionUpload(response, request, attachments.TargetEntry, "entryID")
}

func (handlers entryHandlers) presentationSubmissionUpload(
	response http.ResponseWriter,
	request *http.Request,
) {
	handlers.submissionUpload(response, request, attachments.TargetPresentation, "sessionID")
}

func (handlers entryHandlers) submissionUpload(
	response http.ResponseWriter,
	request *http.Request,
	targetType attachments.TargetKind,
	targetPathID string,
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
	targetID, targetErr := positivePathID(request, targetPathID)
	if eventErr != nil || targetErr != nil {
		http.NotFound(response, request)
		return
	}
	clientKey, accountKey := accountUploadLimitKeys(request, actor.ID)
	if retryAfter, blocked := handlers.browser.limiter.reserve(clientKey, accountKey); blocked {
		writeUploadRateLimit(response, retryAfter)
		return
	}
	if err := handlers.attachments.AuthorizeAccountUpload(
		request.Context(), actor, eventID, targetType, targetID,
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
			EventID: eventID, TargetType: targetType, TargetID: targetID,
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
	http.Redirect(response, request, "/my-participation", http.StatusSeeOther)
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
	presentations, err := handlers.presentation.Submissions(request.Context(), actor)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Account Presentations", err)
		return
	}
	ballots, err := handlers.voting.OpenBallots(request.Context(), actor)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Account Ballots", err)
		return
	}
	publicEvents, err := handlers.events.PublicListing(request.Context())
	if err != nil {
		handlers.browser.frontendError(response, request, "read public Events", err)
		return
	}
	eventSlugs := make(map[int]string, len(publicEvents))
	for _, event := range publicEvents {
		eventSlugs[event.ID] = event.Slug
	}
	attachmentState, err := handlers.attachments.SubmittedState(request.Context(), actor)
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
			Backstage:      backstageAvailable(backstageNavigation(actor, "")),
			CommandID:      commandID, Competitions: competitions,
			Presentations: presentations,
			Ballots:       ballots,
			EventSlugs:    eventSlugs,
			Attachments:   attachmentState, Error: message,
		},
	))
}

func submissionError(err error) (int, string) {
	switch {
	case errors.Is(err, presentation.ErrInvalidInput), errors.Is(err, command.ErrInvalidID):
		return http.StatusUnprocessableEntity, "Check the Presentation details and try again."
	case errors.Is(err, presentation.ErrAccessClosed):
		return http.StatusGone, "Presentation submission access is closed."
	case errors.Is(err, presentation.ErrRevisionConflict),
		errors.Is(err, presentation.ErrCommandConflict):
		return http.StatusConflict, "Presentation state changed. Reload and try again."
	case errors.Is(err, presentation.ErrSubmitterRequired),
		errors.Is(err, presentation.ErrPresentationNotFound),
		errors.Is(err, presentation.ErrProducerRequired):
		return http.StatusNotFound, "Presentation not found."
	default:
		return entryError(err)
	}
}
