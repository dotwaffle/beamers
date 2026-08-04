package server

import (
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/dotwaffle/beamers/internal/activation"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/competition"
	"github.com/dotwaffle/beamers/internal/displays"
	"github.com/dotwaffle/beamers/internal/displaystream"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/frontend"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/sessioncontrol"
)

type operationHandlers struct {
	browser     frontendHandlers
	activation  *activation.Service
	events      *events.Service
	rundown     *rundown.Queries
	sessions    *sessioncontrol.Service
	competition *competition.Service
	displays    *displays.Service
	stream      *displaystream.Hub
}

type operationPreview struct {
	sessionID     int
	revision      int
	adjustment    time.Duration
	target        *sessioncontrol.TargetPreview
	end           *competition.EndPreflight
	forecastStart time.Time
	laneIDs       []int
	locationIDs   []int
	reinstatement *sessioncontrol.ReinstatePreview
}

var errInvalidOperationInput = errors.New("invalid operation input")

func registerOperationRoutes(
	mux *routeMux,
	authentication *auth.Service,
	activationService *activation.Service,
	eventService *events.Service,
	rundownQueries *rundown.Queries,
	sessionService *sessioncontrol.Service,
	competitionService *competition.Service,
	displayService *displays.Service,
	stream *displaystream.Hub,
	logger *slog.Logger,
) {
	handlers := operationHandlers{
		browser: frontendHandlers{
			authentication: authentication,
			logger:         logger,
			random:         rand.Reader,
		},
		activation: activationService, events: eventService,
		rundown: rundownQueries, sessions: sessionService,
		competition: competitionService, displays: displayService, stream: stream,
	}
	route := backstagePageRoute()
	route.maxBodyBytes = maxAuthBodyBytes
	mux.HandleFunc("/backstage/events/{eventID}/operations", route, handlers.operations)
}

func (handlers operationHandlers) operations(
	response http.ResponseWriter,
	request *http.Request,
) {
	actor, ok := handlers.browser.browserAccount(response, request)
	if !ok {
		return
	}
	eventID, err := positivePathID(request, "eventID")
	if err != nil {
		http.NotFound(response, request)
		return
	}
	var event events.Event
	if actor.Administrator {
		allEvents, listErr := handlers.events.List(request.Context(), actor)
		err = listErr
		for _, candidate := range allEvents {
			if candidate.ID == eventID {
				event = candidate
				break
			}
		}
		if err == nil && event.ID == 0 {
			err = events.ErrEventNotFound
		}
	} else {
		event, err = handlers.events.CrewEvent(request.Context(), actor, eventID)
	}
	if errors.Is(err, events.ErrEventAccessDenied) {
		http.Error(response, "Event crew authority required", http.StatusForbidden)
		return
	}
	if errors.Is(err, events.ErrEventNotFound) {
		http.NotFound(response, request)
		return
	}
	if err != nil {
		handlers.browser.frontendError(response, request, "read Event", err)
		return
	}
	if actor.Administrator && !actor.CanOperateEvent(eventID) {
		active, activeErr := handlers.activation.ActiveEvent(request.Context(), actor)
		if activeErr != nil {
			handlers.browser.frontendError(response, request, "read Active Event", activeErr)
			return
		}
		if active.EventID != eventID {
			http.NotFound(response, request)
			return
		}
	}
	if !actor.Administrator && !actor.CanOperateEvent(eventID) {
		http.Error(response, "Session operation authority required", http.StatusForbidden)
		return
	}
	csrfToken, err := handlers.browser.csrfToken(response, request)
	if err != nil {
		handlers.browser.frontendError(response, request, "create CSRF proof", err)
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		handlers.render(
			response, request, actor, event, csrfToken,
			http.StatusOK, nil, operationPreview{},
		)
	case http.MethodPost:
		if !handlers.browser.validForm(response, request) {
			return
		}
		handlers.submit(response, request, actor, event, csrfToken)
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
	}
}

func (handlers operationHandlers) submit(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	event events.Event,
	csrfToken string,
) {
	var (
		preview operationPreview
		err     error
	)
	switch request.Form.Get("action") {
	case "enroll-display":
		err = handlers.enrollDisplay(request, actor)
	case "assign-display":
		err = handlers.assignDisplay(request, actor, event.ID)
	default:
		preview.sessionID, preview.revision, err = operationSessionInput(request)
		if err == nil {
			err = handlers.submitSessionAction(request, actor, event, &preview)
		}
	}
	if err != nil {
		handlers.renderOperationError(
			response, request, actor, event, csrfToken, err, preview,
		)
		return
	}
	if preview.target != nil || preview.end != nil || preview.reinstatement != nil {
		handlers.render(
			response, request, actor, event, csrfToken,
			http.StatusOK, nil, preview,
		)
		return
	}
	http.Redirect(
		response,
		request,
		"/backstage/events/"+strconv.Itoa(event.ID)+"/operations",
		http.StatusSeeOther,
	)
}

func (handlers operationHandlers) submitSessionAction(
	request *http.Request,
	actor auth.Account,
	event events.Event,
	preview *operationPreview,
) error {
	switch request.Form.Get("action") {
	case "start-session":
		_, err := handlers.sessions.Start(request.Context(), actor, sessioncontrol.StartInput{
			EventID: event.ID, SessionID: preview.sessionID,
			CommandID:                 request.Form.Get("command_id"),
			ExpectedLiveStateRevision: preview.revision,
		})
		return err
	case "end-session":
		_, err := handlers.sessions.End(request.Context(), actor, sessioncontrol.EndInput{
			EventID: event.ID, SessionID: preview.sessionID,
			CommandID:                  request.Form.Get("command_id"),
			ExpectedLiveStateRevision:  preview.revision,
			ConfirmedDeferredEntries:   request.Form.Get("confirmed_deferred_entries") == "true",
			DeferredEntriesFingerprint: request.Form.Get("deferred_entries_fingerprint"),
		})
		return err
	case "preview-end-session":
		found, err := handlers.competition.PreflightEnd(
			request.Context(), actor, event.ID, preview.sessionID,
		)
		preview.end = &found
		return err
	case "cancel-session":
		_, err := handlers.sessions.Cancel(request.Context(), actor, sessioncontrol.CancelInput{
			EventID: event.ID, SessionID: preview.sessionID,
			CommandID:                 request.Form.Get("command_id"),
			ExpectedLiveStateRevision: preview.revision,
			Confirmed:                 request.Form.Get("confirmed") == "true",
			PublicCancellationMessage: request.Form.Get("public_cancellation_message"),
			CrewNotes:                 request.Form.Get("crew_notes"),
		})
		return err
	case "preview-adjust-target":
		return handlers.previewTarget(request, actor, event.ID, preview)
	case "adjust-target":
		return handlers.adjustTarget(request, actor, event.ID, preview)
	case "preview-reinstate":
		return handlers.previewReinstate(request, actor, event, preview)
	case "reinstate-session":
		return handlers.reinstate(request, actor, event.ID, preview)
	default:
		return errInvalidOperationInput
	}
}

func (handlers operationHandlers) previewTarget(
	request *http.Request,
	actor auth.Account,
	eventID int,
	preview *operationPreview,
) error {
	adjustment, err := operationAdjustment(request)
	if err != nil {
		return errInvalidOperationInput
	}
	found, err := handlers.sessions.PreviewAdjustTarget(
		request.Context(),
		actor,
		sessioncontrol.PreviewAdjustTargetInput{
			EventID: eventID, SessionID: preview.sessionID,
			Adjustment: sessioncontrol.TargetAdjustment{Duration: adjustment},
		},
	)
	preview.adjustment, preview.target = adjustment, &found
	return err
}

func (handlers operationHandlers) adjustTarget(
	request *http.Request,
	actor auth.Account,
	eventID int,
	preview *operationPreview,
) error {
	adjustment, err := operationAdjustment(request)
	if err != nil {
		return errInvalidOperationInput
	}
	_, err = handlers.sessions.AdjustTarget(
		request.Context(),
		actor,
		sessioncontrol.AdjustTargetInput{
			EventID: eventID, SessionID: preview.sessionID,
			CommandID:                 request.Form.Get("command_id"),
			ExpectedLiveStateRevision: preview.revision,
			Adjustment:                sessioncontrol.TargetAdjustment{Duration: adjustment},
			PreviewFingerprint:        request.Form.Get("preview_fingerprint"),
			Confirmed:                 request.Form.Get("confirmed") == "true",
			HardBoundaryConfirmed:     request.Form.Get("hard_boundary_confirmed") == "true",
		},
	)
	return err
}

func (handlers operationHandlers) previewReinstate(
	request *http.Request,
	actor auth.Account,
	event events.Event,
	preview *operationPreview,
) error {
	forecastStart, err := planningFormTime(
		request.Form.Get("forecast_start"),
		event.Timezone,
		"",
		time.Time{},
	)
	if err != nil {
		return errInvalidOperationInput
	}
	return handlers.previewReinstateAt(
		request,
		actor,
		event.ID,
		preview,
		forecastStart,
	)
}

func (handlers operationHandlers) previewReinstateAt(
	request *http.Request,
	actor auth.Account,
	eventID int,
	preview *operationPreview,
	forecastStart time.Time,
) error {
	laneIDs, locationIDs, err := operationPlacement(request)
	if err != nil {
		return err
	}
	found, err := handlers.sessions.PreviewReinstate(
		request.Context(),
		actor,
		sessioncontrol.PreviewReinstateInput{
			EventID: eventID, SessionID: preview.sessionID,
			ForecastStart: forecastStart, LaneIDs: laneIDs, LocationIDs: locationIDs,
		},
	)
	preview.forecastStart = forecastStart
	preview.laneIDs, preview.locationIDs = laneIDs, locationIDs
	preview.reinstatement = &found
	return err
}

func (handlers operationHandlers) reinstate(
	request *http.Request,
	actor auth.Account,
	eventID int,
	preview *operationPreview,
) error {
	forecastStart, err := time.Parse(time.RFC3339, request.Form.Get("forecast_start"))
	if err != nil {
		return errInvalidOperationInput
	}
	laneIDs, locationIDs, err := operationPlacement(request)
	if err != nil {
		return err
	}
	_, err = handlers.sessions.Reinstate(
		request.Context(),
		actor,
		sessioncontrol.ReinstateInput{
			EventID: eventID, SessionID: preview.sessionID,
			CommandID:                 request.Form.Get("command_id"),
			ExpectedLiveStateRevision: preview.revision,
			ForecastStart:             forecastStart,
			LaneIDs:                   laneIDs,
			LocationIDs:               locationIDs,
			PreviewFingerprint:        request.Form.Get("preview_fingerprint"),
			Confirmed:                 request.Form.Get("confirmed") == "true",
			HardBoundaryConfirmed:     request.Form.Get("hard_boundary_confirmed") == "true",
		},
	)
	return err
}

func (handlers operationHandlers) renderOperationError(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	event events.Event,
	csrfToken string,
	err error,
	preview operationPreview,
) {
	status, message, known := operationError(err)
	if !known {
		handlers.browser.frontendError(response, request, "submit Session operation", err)
		return
	}
	if refreshErr := handlers.refreshOperationPreview(
		request,
		actor,
		event,
		err,
		&preview,
	); refreshErr != nil {
		status, message, known = operationError(refreshErr)
		if !known {
			handlers.browser.frontendError(
				response,
				request,
				"refresh Session operation preview",
				refreshErr,
			)
			return
		}
		err = refreshErr
	}
	handlers.render(
		response,
		request,
		actor,
		event,
		csrfToken,
		status,
		operationFormErrors(
			err,
			request.Form.Get("action"),
			preview.sessionID,
			request.Form,
			message,
		),
		preview,
	)
}

func (handlers operationHandlers) refreshOperationPreview(
	request *http.Request,
	actor auth.Account,
	event events.Event,
	reason error,
	preview *operationPreview,
) error {
	if request.Form.Get("action") == "adjust-target" &&
		(errors.Is(reason, sessioncontrol.ErrLiveStateRevisionConflict) ||
			errors.Is(reason, sessioncontrol.ErrTargetPreviewStale) ||
			errors.Is(reason, sessioncontrol.ErrTargetConfirmation) ||
			errors.Is(reason, sessioncontrol.ErrHardBoundaryConfirmation)) {
		return handlers.previewTarget(request, actor, event.ID, preview)
	}
	if request.Form.Get("action") == "reinstate-session" &&
		(errors.Is(reason, sessioncontrol.ErrLiveStateRevisionConflict) ||
			errors.Is(reason, sessioncontrol.ErrReinstatePreviewStale) ||
			errors.Is(reason, sessioncontrol.ErrReinstateConfirmation) ||
			errors.Is(reason, sessioncontrol.ErrHardBoundaryConfirmation)) {
		forecastStart, err := time.Parse(time.RFC3339, request.Form.Get("forecast_start"))
		if err != nil {
			return errInvalidOperationInput
		}
		return handlers.previewReinstateAt(
			request,
			actor,
			event.ID,
			preview,
			forecastStart,
		)
	}
	if request.Form.Get("action") == "end-session" &&
		(errors.Is(reason, sessioncontrol.ErrLiveStateRevisionConflict) ||
			errors.Is(reason, sessioncontrol.ErrDeferredEntriesConfirmation) ||
			errors.Is(reason, sessioncontrol.ErrDeferredEntriesPreviewStale)) {
		found, err := handlers.competition.PreflightEnd(
			request.Context(),
			actor,
			event.ID,
			preview.sessionID,
		)
		preview.end = &found
		return err
	}
	return nil
}

func operationFormErrors(
	err error,
	action string,
	sessionID int,
	values url.Values,
	message string,
) frontend.FormErrors {
	if action == "assign-display" {
		sessionID, _ = strconv.Atoi(values.Get("display_id"))
	}
	fieldID, label, fieldMessage := "", "", message
	var validation *rundown.ValidationError
	switch {
	case errors.As(err, &validation):
		fieldID = frontend.OperationFieldID(action, sessionID, validation.Field)
		label, fieldMessage = operationFieldLabel(validation.Field), validation.Message
	case errors.Is(err, displays.ErrInvalidDisplay) && action == "enroll-display":
		fieldID = frontend.OperationFieldID(action, 0, "code")
		label, fieldMessage = "Enrollment code", "Enter a valid Enrollment code."
	case errors.Is(err, displays.ErrAssignmentReference) && action == "assign-display":
		fieldID = frontend.OperationFieldID(action, sessionID, "location_id")
		label, fieldMessage = "Location", "Choose a location from the active Event."
	case errors.Is(err, sessioncontrol.ErrHardBoundaryConfirmation) &&
		action == "adjust-target":
		fieldID = frontend.OperationFieldID(action, sessionID, "hard_boundary_confirmed")
		label, fieldMessage = "Hard Boundary confirmation", "Confirm Hard Boundary movement."
	case action == "adjust-target" &&
		(errors.Is(err, sessioncontrol.ErrLiveStateRevisionConflict) ||
			errors.Is(err, sessioncontrol.ErrTargetPreviewStale)):
		fieldID = frontend.OperationFieldID(action, sessionID, "confirmed")
		label = "Adjust Target confirmation"
		fieldMessage = "The timing preview changed; review and confirm Adjust Target again."
	case errors.Is(err, sessioncontrol.ErrTargetConfirmation) &&
		action == "adjust-target":
		fieldID = frontend.OperationFieldID(action, sessionID, "confirmed")
		label, fieldMessage = "Adjust Target confirmation", "Confirm Adjust Target."
	case errors.Is(err, sessioncontrol.ErrHardBoundaryConfirmation) &&
		action == "reinstate-session":
		fieldID = frontend.OperationFieldID(action, sessionID, "hard_boundary_confirmed")
		label, fieldMessage = "Hard Boundary confirmation", "Confirm Hard Boundary movement."
	case action == "reinstate-session" &&
		(errors.Is(err, sessioncontrol.ErrLiveStateRevisionConflict) ||
			errors.Is(err, sessioncontrol.ErrReinstatePreviewStale)):
		fieldID = frontend.OperationFieldID(action, sessionID, "confirmed")
		label = "Reinstate confirmation"
		fieldMessage = "The placement preview changed; review and confirm Reinstate again."
	case errors.Is(err, sessioncontrol.ErrReinstateConfirmation) &&
		action == "reinstate-session":
		fieldID = frontend.OperationFieldID(action, sessionID, "confirmed")
		label, fieldMessage = "Reinstate confirmation", "Confirm Reinstate."
	case action == "end-session" &&
		(errors.Is(err, sessioncontrol.ErrLiveStateRevisionConflict) ||
			errors.Is(err, sessioncontrol.ErrDeferredEntriesPreviewStale)):
		fieldID = frontend.OperationFieldID(action, sessionID, "confirmed_deferred_entries")
		label = "Deferred Entries confirmation"
		fieldMessage = "The deferred Entry preview changed; review and confirm again."
	case errors.Is(err, sessioncontrol.ErrDeferredEntriesConfirmation) &&
		action == "end-session":
		fieldID = frontend.OperationFieldID(action, sessionID, "confirmed_deferred_entries")
		label, fieldMessage = "Deferred Entries confirmation", "Confirm deferred Entries."
	case errors.Is(err, errInvalidOperationInput) && action == "preview-adjust-target":
		fieldID = frontend.OperationFieldID(action, sessionID, "adjustment")
		label, fieldMessage = "Target adjustment", "Enter a duration such as 5m."
	case errors.Is(err, errInvalidOperationInput) && action == "preview-reinstate":
		fieldID = frontend.OperationFieldID(action, sessionID, "forecast_start")
		label, fieldMessage = "New Forecast Start", "Enter a valid Forecast Start."
	case errors.Is(err, sessioncontrol.ErrCancelConfirmation):
		fieldID = frontend.OperationFieldID(action, sessionID, "confirmed")
		label, fieldMessage = "Cancellation confirmation", "Confirm cancellation."
	case errors.Is(err, sessioncontrol.ErrCancellationText):
		fieldID = frontend.OperationFieldID(action, sessionID, "public_cancellation_message")
		label = "Public cancellation message"
	case errors.Is(err, sessioncontrol.ErrReinstatePlacement):
		fieldID = frontend.OperationFieldID(action, sessionID, "lane_ids")
		label = "Lane IDs"
	}
	return frontend.FormErrors{{
		FieldID: fieldID, Label: label, Message: fieldMessage,
	}}
}

func operationFieldLabel(field string) string {
	switch field {
	case "lane_ids":
		return "Lane IDs"
	case "location_ids":
		return "Location IDs"
	case "display_id":
		return "Display ID"
	case "location_id":
		return "Location"
	case "view_key":
		return "View"
	case "display_group_keys":
		return "Display Group keys"
	case "name":
		return "Display name"
	default:
		return field
	}
}

func operationSessionInput(request *http.Request) (int, int, error) {
	sessionID, err := strconv.Atoi(request.Form.Get("session_id"))
	if err != nil || sessionID <= 0 {
		return 0, 0, errInvalidOperationInput
	}
	revision, err := strconv.Atoi(request.Form.Get("expected_live_state_revision"))
	if err != nil || revision < 0 {
		return 0, 0, errInvalidOperationInput
	}
	return sessionID, revision, nil
}

func operationIDs(values []string, field, label string) ([]int, error) {
	ids, err := positiveFormIDs(values)
	if err != nil || len(ids) == 0 {
		return nil, formValidationError(
			field,
			"Select at least one "+label+".",
		)
	}
	return ids, nil
}

func operationAdjustment(request *http.Request) (time.Duration, error) {
	adjustment, err := time.ParseDuration(request.Form.Get("adjustment"))
	if err != nil {
		return 0, errInvalidOperationInput
	}
	return adjustment, nil
}

func operationPlacement(request *http.Request) ([]int, []int, error) {
	laneIDs, err := operationIDs(request.Form["lane_ids"], "lane_ids", "Lane")
	if err != nil {
		return nil, nil, err
	}
	locationIDs, err := operationIDs(
		request.Form["location_ids"],
		"location_ids",
		"Location",
	)
	if err != nil {
		return nil, nil, err
	}
	return laneIDs, locationIDs, nil
}

func operationError(err error) (int, string, bool) {
	var validation *rundown.ValidationError
	switch {
	case errors.As(err, &validation):
		return http.StatusBadRequest, "Check the operation details and try again.", true
	case errors.Is(err, errInvalidOperationInput):
		return http.StatusBadRequest, "Check the operation details and try again.", true
	case errors.Is(err, command.ErrInvalidID):
		return http.StatusUnprocessableEntity, "A valid command identity is required.", true
	case errors.Is(err, competition.ErrInvalidInput):
		return http.StatusBadRequest, "Check the Competition operation details and try again.", true
	case errors.Is(err, competition.ErrOperatorRequired):
		return http.StatusForbidden, "Session operation authority required.", true
	case errors.Is(err, competition.ErrCompetitionNotFound):
		return http.StatusNotFound, "Competition not found.", true
	case errors.Is(err, displays.ErrAdministratorRequired):
		return http.StatusForbidden, "Administrator authority required.", true
	case errors.Is(err, displays.ErrInvalidDisplay),
		errors.Is(err, displays.ErrAssignmentReference):
		return http.StatusUnprocessableEntity, "Check the Display details and try again.", true
	case errors.Is(err, displays.ErrEnrollmentUnavailable),
		errors.Is(err, displays.ErrDisplayAlreadyEnrolled),
		errors.Is(err, displays.ErrCommandConflict):
		return http.StatusConflict, err.Error(), true
	case errors.Is(err, displays.ErrDisplayNotFound):
		return http.StatusNotFound, "Display not found.", true
	case errors.Is(err, sessioncontrol.ErrLiveStateRevisionConflict):
		return http.StatusConflict, "Session changed; reload and try again.", true
	case errors.Is(err, sessioncontrol.ErrCompetitionPreflightBlocked):
		return http.StatusConflict, "Competition readiness blocks Session start.", true
	case errors.Is(err, sessioncontrol.ErrDeferredEntriesConfirmation),
		errors.Is(err, sessioncontrol.ErrDeferredEntriesPreviewStale):
		return http.StatusConflict, "Deferred Entries changed or require confirmation.", true
	case errors.Is(err, sessioncontrol.ErrPrizegivingResultsUnresolved):
		return http.StatusConflict, "Unresolved Results block Session end.", true
	case errors.Is(err, sessioncontrol.ErrOperatorRequired),
		errors.Is(err, sessioncontrol.ErrProducerRequired),
		errors.Is(err, sessioncontrol.ErrSessionScopeRequired):
		return http.StatusForbidden, "Session operation authority required.", true
	case errors.Is(err, sessioncontrol.ErrSessionNotFound):
		return http.StatusNotFound, "Session not found.", true
	case errors.Is(err, sessioncontrol.ErrSessionLifecycleTransition),
		errors.Is(err, sessioncontrol.ErrEventNotActive):
		return http.StatusConflict, "Session operation is no longer available.", true
	case errors.Is(err, sessioncontrol.ErrTargetPreviewStale),
		errors.Is(err, sessioncontrol.ErrReinstatePreviewStale):
		return http.StatusConflict, "Timing preview is stale; run it again.", true
	case errors.Is(err, sessioncontrol.ErrTargetConfirmation),
		errors.Is(err, sessioncontrol.ErrHardBoundaryConfirmation),
		errors.Is(err, sessioncontrol.ErrReinstateConfirmation):
		return http.StatusConflict, "Confirm the timing preview and Hard Boundary warning.", true
	case errors.Is(err, sessioncontrol.ErrReinstatePlacement),
		errors.Is(err, sessioncontrol.ErrPresetNotConfigured),
		errors.Is(err, sessioncontrol.ErrTargetBeforeNow),
		errors.Is(err, sessioncontrol.ErrNoCountdownTarget),
		errors.Is(err, sessioncontrol.ErrCancelConfirmation),
		errors.Is(err, sessioncontrol.ErrCancellationText):
		return http.StatusUnprocessableEntity, "Check the Session operation details and try again.", true
	default:
		return 0, "", false
	}
}

func (handlers operationHandlers) render(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	event events.Event,
	csrfToken string,
	status int,
	formErrors frontend.FormErrors,
	preview operationPreview,
) {
	var state rundown.CrewRundown
	var err error
	if actor.CanOperateEvent(event.ID) && canReadBackstageRundown(actor, event.ID) {
		state, err = handlers.rundown.CrewRundown(request.Context(), actor, event.ID)
	} else if !actor.CanOperateEvent(event.ID) {
		state.Locations, err = handlers.rundown.DisplayLocations(
			request.Context(), actor, event.ID,
		)
	}
	if err != nil {
		handlers.browser.frontendError(response, request, "read Session operations", err)
		return
	}
	displayStatuses, err := handlers.displays.List(
		request.Context(),
		actor,
		handlers.stream.Cursor(),
	)
	displaysAvailable := true
	if errors.Is(err, displays.ErrCrewRequired) {
		displayStatuses = nil
		displaysAvailable = false
	} else if err != nil {
		handlers.browser.frontendError(response, request, "read Display operations", err)
		return
	}
	commandIDs := make(map[string]string, len(state.Sessions)*5)
	operableSessionIDs := make(map[int]bool, len(state.Sessions))
	for _, session := range state.Sessions {
		if session.ID == preview.sessionID &&
			(preview.target != nil || preview.end != nil || preview.reinstatement != nil) {
			preview.revision = session.LiveStateRevision
		}
		if !canAccessCompetition(actor, event.ID, session) {
			continue
		}
		operableSessionIDs[session.ID] = true
		for _, action := range []string{
			"start-session",
			"end-session",
			"cancel-session",
			"adjust-target",
			"reinstate-session",
		} {
			commandIDs[action+":"+strconv.Itoa(session.ID)], err = planningCommandID(
				handlers.browser.random,
			)
			if err != nil {
				handlers.browser.frontendError(
					response, request, "create Session command identity", err,
				)
				return
			}
		}
	}
	displayCommandIDs := make(map[int]string, len(displayStatuses))
	for _, display := range displayStatuses {
		displayCommandIDs[display.ID], err = planningCommandID(handlers.browser.random)
		if err != nil {
			handlers.browser.frontendError(response, request, "create Display command identity", err)
			return
		}
	}
	enrollmentCommandID, err := planningCommandID(handlers.browser.random)
	if err != nil {
		handlers.browser.frontendError(response, request, "create Display Enrollment command identity", err)
		return
	}
	handlers.browser.render(
		response,
		request,
		status,
		frontend.Operations(frontend.OperationsPage{
			AccountName: actor.Name, CSRFToken: csrfToken,
			ReducedEffects:        reducedEffectsCookie(request),
			Navigation:            backstageNavigation(actor, request.URL.Path),
			Event:                 event,
			Rundown:               state,
			Displays:              displayStatuses,
			DisplaysAvailable:     displaysAvailable,
			CommandIDs:            commandIDs,
			OperableSessionIDs:    operableSessionIDs,
			DisplayCommandIDs:     displayCommandIDs,
			EnrollmentCommandID:   enrollmentCommandID,
			CanAdministerDisplays: actor.Administrator,
			CanProduce:            actor.CanProduceEvent(event.ID),
			TargetPreview:         preview.target,
			EndPreflight:          preview.end,
			PreviewSessionID:      preview.sessionID,
			PreviewRevision:       preview.revision,
			Adjustment:            preview.adjustment,
			ReinstatePreview:      preview.reinstatement,
			ForecastStart:         preview.forecastStart,
			LaneIDs:               preview.laneIDs,
			LocationIDs:           preview.locationIDs,
			SubmittedAction:       request.Form.Get("action"),
			Form:                  request.Form,
			Errors:                formErrors,
		}),
	)
}
