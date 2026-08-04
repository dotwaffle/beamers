package server

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dotwaffle/beamers/internal/attachments"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/frontend"
	"github.com/dotwaffle/beamers/internal/presentation"
	"github.com/dotwaffle/beamers/internal/rundown"
)

func (handlers entryHandlers) presentationSubmission(
	response http.ResponseWriter,
	request *http.Request,
) {
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
	prologue, ok := handlers.browser.backstagePagePrologue(response, request, actor)
	if !ok {
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		handlers.renderPresentationSubmission(
			response,
			request,
			actor,
			eventID,
			sessionID,
			prologue,
			http.StatusOK,
			nil,
		)
	case http.MethodPost:
		if !handlers.browser.validForm(response, request) {
			return
		}
		err := handlers.submitPresentationSubmission(request, actor, eventID, sessionID)
		if err == nil {
			http.Redirect(
				response,
				request,
				"/backstage/events/"+strconv.Itoa(eventID)+
					"/presentations/"+strconv.Itoa(sessionID)+"/submission",
				http.StatusSeeOther,
			)
			return
		}
		status, formErrors := presentationSubmissionFormErrors(err, request.Form, sessionID)
		handlers.renderPresentationSubmission(
			response,
			request,
			actor,
			eventID,
			sessionID,
			prologue,
			status,
			formErrors,
		)
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
	}
}

func (handlers entryHandlers) submitPresentationSubmission(
	request *http.Request,
	actor auth.Account,
	eventID, sessionID int,
) error {
	switch request.Form.Get("action") {
	case "assign-submitter":
		revision, err := entryFormNonnegativeInt(request, "expected_revision")
		if err != nil {
			return err
		}
		accountID, err := entryFormPositiveInt(request, "account_id")
		if err != nil {
			return formValidationError("account_id", "must identify a Submitter Account")
		}
		_, err = handlers.presentation.AssignSubmitter(
			request.Context(),
			actor,
			presentation.AssignSubmitterInput{
				EventID: eventID, SessionID: sessionID, AccountID: accountID,
				ExpectedRevision: revision, CommandID: request.Form.Get("command_id"),
			},
		)
		return err
	case "create-reopen-window":
		if strings.TrimSpace(request.Form.Get("reason")) == "" {
			return formValidationError("reason", "is required")
		}
		foundEvent, err := handlers.events.CrewEvent(request.Context(), actor, eventID)
		if err != nil {
			return err
		}
		location, err := time.LoadLocation(foundEvent.Timezone)
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
				EventID: eventID, TargetID: sessionID,
				TargetType: attachments.TargetPresentation,
				Reason:     request.Form.Get("reason"), ExpiresAt: expiresAt.UTC(),
				CommandID: request.Form.Get("command_id"),
			},
		)
		return err
	case "extend-reopen-window", "close-reopen-window":
		return handlers.updateReopenWindow(
			request,
			actor,
			eventID,
			attachments.TargetPresentation,
			sessionID,
		)
	default:
		return presentation.ErrInvalidInput
	}
}

func (handlers entryHandlers) renderPresentationSubmission(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	eventID, sessionID int,
	prologue backstagePrologue,
	status int,
	formErrors frontend.FormErrors,
) {
	foundEvent, err := handlers.events.CrewEvent(request.Context(), actor, eventID)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Presentation Event", err)
		return
	}
	state, err := handlers.presentation.Get(request.Context(), actor, eventID, sessionID)
	if err != nil {
		if errors.Is(err, presentation.ErrPresentationNotFound) {
			http.NotFound(response, request)
			return
		}
		handlers.browser.frontendError(response, request, "read Presentation submission", err)
		return
	}
	windows, err := handlers.attachments.CrewReopenWindows(
		request.Context(),
		actor,
		eventID,
		attachments.TargetPresentation,
		sessionID,
	)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Presentation Reopen Windows", err)
		return
	}
	accounts, err := handlers.presentation.AssignableAccounts(request.Context(), actor, eventID)
	if err != nil {
		handlers.browser.frontendError(response, request, "list Presentation Submitter Accounts", err)
		return
	}
	commandID, err := planningCommandID(handlers.browser.random)
	if err != nil {
		handlers.browser.frontendError(response, request, "create Presentation command identity", err)
		return
	}
	handlers.browser.render(response, request, status, frontend.PresentationSubmission(
		frontend.PresentationSubmissionPage{
			AccountName: actor.Name, CSRFToken: prologue.CSRFToken,
			ReducedEffects: prologue.ReducedEffects,
			Navigation:     prologue.Navigation,
			CommandID:      commandID, Event: foundEvent, State: state, Accounts: accounts,
			ReopenWindows: windows, SubmittedAction: request.Form.Get("action"),
			Form: request.Form, Errors: formErrors,
		},
	))
}

// presentationSubmissionFormErrorRules is the classify-error-to-field table
// for presentationSubmissionFormErrors' common shape. Adding a new
// validation rule means adding one row here.
var presentationSubmissionFormErrorRules = []formErrorRule{
	ruleValidation(func(validation *rundown.ValidationError) (string, string, string) {
		return validation.Field, "", validation.Message
	}),
	rule(matchErrAny(attachments.ErrReopenWindowExtension, attachments.ErrReopenWindowExpiry),
		"expires_at", "Expiry", ""),
	rule(matchAll(matchErr(attachments.ErrInvalidInput), matchAction("create-reopen-window")),
		"expires_at", "Expiry", ""),
	rule(matchAll(matchErr(presentation.ErrInvalidInput), matchAction("assign-submitter")),
		"account_id", "Submitter Account", ""),
}

func presentationSubmissionFormErrors(
	err error,
	values url.Values,
	sessionID int,
) (int, frontend.FormErrors) {
	status, message := presentationSubmissionError(err)
	if err == nil {
		return status, nil
	}
	action := values.Get("action")
	targetID, _ := strconv.Atoi(values.Get("entry_id"))
	if targetID == 0 && (action == "extend-reopen-window" || action == "close-reopen-window") {
		targetID = sessionID
	}
	windowID, _ := strconv.Atoi(values.Get("window_id"))
	if action == "create-reopen-window" {
		if validationErrors := createReopenWindowFormErrors(values, 0); len(validationErrors) > 0 {
			return status, validationErrors
		}
	}
	field, label, fieldMessage, fieldAction := resolveFormErrorField(
		presentationSubmissionFormErrorRules, err, action,
	)
	return status, formErrorResult(field, label, fieldMessage, fieldAction, message,
		func(field, action string) string {
			return frontend.WorkflowFieldID(action, targetID, 0, windowID, field)
		},
	)
}

func presentationSubmissionError(err error) (int, string) {
	var validation *rundown.ValidationError
	switch {
	case errors.As(err, &validation):
		return http.StatusUnprocessableEntity, validation.Error()
	case errors.Is(err, attachments.ErrInvalidInput),
		errors.Is(err, presentation.ErrInvalidInput),
		errors.Is(err, command.ErrInvalidID):
		return http.StatusUnprocessableEntity, "Check the Presentation submission and try again."
	case errors.Is(err, attachments.ErrReopenWindowExtension):
		return http.StatusUnprocessableEntity,
			"Choose an expiry later than the current expiry."
	case errors.Is(err, attachments.ErrReopenWindowExpiry):
		return http.StatusUnprocessableEntity, "Choose a future expiry within 7 days."
	case errors.Is(err, attachments.ErrReopenWindowInactive):
		return http.StatusUnprocessableEntity, "Only active Reopen Windows can be changed."
	case errors.Is(err, attachments.ErrReopenWindowRevision),
		errors.Is(err, attachments.ErrCommandConflict),
		errors.Is(err, presentation.ErrRevisionConflict),
		errors.Is(err, presentation.ErrCommandConflict):
		return http.StatusConflict, "Presentation submission changed. Reload and try again."
	case errors.Is(err, attachments.ErrProducerRequired),
		errors.Is(err, attachments.ErrUploadTargetNotFound),
		errors.Is(err, presentation.ErrProducerRequired),
		errors.Is(err, presentation.ErrPresentationNotFound):
		return http.StatusNotFound, "Presentation not found."
	default:
		return http.StatusInternalServerError, "Presentation submission action failed."
	}
}
