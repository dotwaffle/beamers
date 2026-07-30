package server

import (
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/displaystream"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/frontend"
	"github.com/dotwaffle/beamers/internal/overrides"
	"github.com/dotwaffle/beamers/internal/rundown"
)

type controlHandlers struct {
	browser      frontendHandlers
	events       *events.Service
	rundown      *rundown.Queries
	overrides    *overrides.Service
	stream       *displaystream.Hub
	buildVersion string
}

func registerControlRoutes(
	mux *routeMux,
	authentication *auth.Service,
	eventService *events.Service,
	rundownQueries *rundown.Queries,
	overrideService *overrides.Service,
	stream *displaystream.Hub,
	buildVersion string,
	logger *slog.Logger,
) {
	handlers := controlHandlers{
		browser: frontendHandlers{
			authentication: authentication,
			logger:         logger,
			random:         rand.Reader,
		},
		events: eventService, rundown: rundownQueries,
		overrides: overrideService, stream: stream,
		buildVersion: buildVersion,
	}
	route := backstagePageRoute()
	route.maxBodyBytes = maxAuthBodyBytes
	mux.HandleFunc("/backstage/events/{eventID}/control", route, handlers.control)
	mux.HandleFunc(
		"/backstage/events/{eventID}/control/emergency-alerts",
		route,
		handlers.control,
	)
}

func (handlers controlHandlers) control(
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
	event, err := handlers.events.CrewEvent(request.Context(), actor, eventID)
	switch {
	case errors.Is(err, events.ErrEventAccessDenied):
		http.Error(response, "Event crew authority required", http.StatusForbidden)
		return
	case errors.Is(err, events.ErrEventNotFound):
		http.NotFound(response, request)
		return
	case err != nil:
		handlers.browser.frontendError(response, request, "read Event", err)
		return
	case !actor.CanOperateEvent(eventID):
		http.Error(response, "Program and Override authority required", http.StatusForbidden)
		return
	}
	csrfToken, err := handlers.browser.csrfToken(response, request)
	if err != nil {
		handlers.browser.frontendError(response, request, "create CSRF proof", err)
		return
	}
	response.Header().Set(clientBuildHeader, handlers.buildVersion)
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		handlers.renderControl(
			response, request, actor, event, csrfToken,
			http.StatusOK, nil, nil, "", frontend.OverrideForm{},
		)
	case http.MethodPost:
		if !handlers.browser.validForm(response, request) {
			return
		}
		if request.Form.Get("build_version") != handlers.buildVersion {
			form, _ := overrideForm(request)
			action := request.Form.Get("action")
			previewAction, activating := strings.CutPrefix(action, "activate-")
			var preview *frontend.OverridePreview
			formErrors := frontend.FormErrors{{
				Message: "The control page changed; reload required.",
			}}
			if activating {
				var previewErr error
				preview, previewErr = handlers.previewForAction(
					request, actor, event.ID, action, form,
				)
				if previewErr != nil {
					status, message, known := controlError(previewErr)
					if !known {
						handlers.browser.frontendError(
							response,
							request,
							"refresh Display Override preview",
							previewErr,
						)
						return
					}
					handlers.renderControl(
						response, request, actor, event, csrfToken, status,
						frontend.FormErrors{{Message: message}}, nil,
						"preview-"+previewAction, form,
					)
					return
				}
				action = "preview-" + previewAction
				formErrors = frontend.FormErrors{{
					FieldID: "override-confirmed",
					Label:   "Confirmation",
					Message: "The control page changed; review and confirm this refreshed preview.",
				}}
			}
			handlers.renderControl(
				response,
				request,
				actor,
				event,
				csrfToken,
				http.StatusConflict,
				formErrors,
				preview,
				action,
				form,
			)
			return
		}
		handlers.submitControl(response, request, actor, event, csrfToken)
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
	}
}

func (handlers controlHandlers) submitControl(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	event events.Event,
	csrfToken string,
) {
	form, err := overrideForm(request)
	var preview *frontend.OverridePreview
	action := request.Form.Get("action")
	previewAction, activating := strings.CutPrefix(action, "activate-")
	fieldErrors := make(map[string]string)
	var inputErr *controlInputError
	if errors.As(err, &inputErr) {
		fieldErrors[inputErr.Field] = inputErr.Message
		err = errInvalidControlInput
	}
	if err == nil {
		fieldErrors = validateControlForm(action, form)
		if len(fieldErrors) != 0 {
			err = errInvalidControlInput
		}
	}
	if err == nil &&
		(activating || action == "clear") &&
		request.Form.Get("confirmed") != "true" {
		fieldErrors["confirmed"] = "Confirm this action."
		err = errInvalidControlInput
	}
	if err == nil {
		switch action {
		case "preview-stage-message":
			preview, err = handlers.previewStageMessage(request, actor, event.ID, form)
		case "preview-technical-difficulties":
			preview, err = handlers.previewTechnicalDifficulties(request, actor, event.ID, form)
		case "preview-urgent-notice":
			preview, err = handlers.previewUrgentNotice(request, actor, event.ID, form)
		case "activate-stage-message":
			_, err = handlers.overrides.SendStageMessage(
				request.Context(), actor, stageMessageInput(event.ID, form, request.Form.Get("command_id")),
			)
		case "activate-technical-difficulties":
			_, err = handlers.overrides.ActivateTechnicalDifficulties(
				request.Context(), actor, technicalDifficultiesInput(
					event.ID, form, request.Form.Get("command_id"),
				),
			)
		case "activate-urgent-notice":
			_, err = handlers.overrides.ActivateUrgentNotice(
				request.Context(), actor, priorityInput(
					event.ID, form, request,
				),
			)
		case "clear":
			err = handlers.clearOverride(request, actor, event.ID)
		default:
			err = errInvalidControlInput
		}
	}
	if err != nil {
		status, message, known := controlError(err)
		if !known {
			handlers.browser.frontendError(response, request, "apply Display Override", err)
			return
		}
		if preview == nil && activating {
			preview, err = handlers.previewForAction(request, actor, event.ID, action, form)
			if err != nil {
				status, message, known = controlError(err)
				if !known {
					handlers.browser.frontendError(
						response,
						request,
						"refresh Display Override preview",
						err,
					)
					return
				}
				fieldErrors = nil
			}
		}
		submittedAction := action
		if activating {
			submittedAction = "preview-" + previewAction
		}
		handlers.renderControl(
			response, request, actor, event, csrfToken, status,
			controlFormErrors(message, action, request.Form, fieldErrors),
			preview, submittedAction, form,
		)
		return
	}
	if preview != nil {
		handlers.renderControl(
			response, request, actor, event, csrfToken,
			http.StatusOK, nil, preview, "", frontend.OverrideForm{},
		)
		return
	}
	http.Redirect(response, request, controlPath(event.ID), http.StatusSeeOther)
}

func (handlers controlHandlers) previewForAction(
	request *http.Request,
	actor auth.Account,
	eventID int,
	action string,
	form frontend.OverrideForm,
) (*frontend.OverridePreview, error) {
	action, _ = strings.CutPrefix(action, "activate-")
	switch action {
	case "stage-message":
		return handlers.previewStageMessage(request, actor, eventID, form)
	case "technical-difficulties":
		return handlers.previewTechnicalDifficulties(request, actor, eventID, form)
	case "urgent-notice":
		return handlers.previewUrgentNotice(request, actor, eventID, form)
	default:
		return nil, errInvalidControlInput
	}
}

func controlFormErrors(
	message string,
	action string,
	values url.Values,
	fields map[string]string,
) frontend.FormErrors {
	if len(fields) == 0 {
		return frontend.FormErrors{{Message: message}}
	}
	result := make(frontend.FormErrors, 0, len(fields))
	keys := make([]string, 0, len(fields))
	for field := range fields {
		keys = append(keys, field)
	}
	slices.Sort(keys)
	for _, field := range keys {
		fieldID := frontend.ControlFieldID(action, field)
		if action == "clear" && field == "confirmed" {
			overrideID, _ := strconv.Atoi(values.Get("override_id"))
			fieldID = frontend.ControlClearFieldID(overrideID)
		}
		result = append(result, frontend.FormError{
			FieldID: fieldID,
			Label:   controlFieldLabel(field),
			Message: fields[field],
		})
	}
	return result
}

func controlFieldLabel(field string) string {
	switch field {
	case "text":
		return "Message"
	case "target_group_key":
		return "Display Group"
	case "target_type":
		return "Target"
	case "target_id":
		return "Target numeric ID"
	case "target_key":
		return "Target key"
	case "presentation":
		return "Presentation"
	case "duration_seconds":
		return "Duration"
	case "emphasis":
		return "Emphasis"
	case "confirmed":
		return "Confirmation"
	default:
		return field
	}
}

var errInvalidControlInput = errors.New("invalid control input")

type controlInputError struct {
	Field   string
	Message string
}

func (err *controlInputError) Error() string {
	return err.Field + ": " + err.Message
}

func overrideForm(request *http.Request) (frontend.OverrideForm, error) {
	form := frontend.OverrideForm{
		Text: request.Form.Get("text"), TargetGroupKey: request.Form.Get("target_group_key"),
		TargetType: request.Form.Get("target_type"),
		TargetKey:  request.Form.Get("target_key"), Presentation: request.Form.Get("presentation"),
		TargetIDRaw: request.Form.Get("target_id"), DurationRaw: request.Form.Get("duration_seconds"),
		UntilCleared: request.Form.Get("until_cleared") == "true", Emphasis: request.Form.Get("emphasis"),
	}
	duration, err := strconv.Atoi(request.Form.Get("duration_seconds"))
	if request.Form.Get("duration_seconds") == "" {
		duration = 0
		err = nil
	}
	if err != nil || duration < 0 {
		return form, &controlInputError{
			Field: "duration_seconds", Message: "Enter a non-negative duration.",
		}
	}
	targetID, err := strconv.Atoi(request.Form.Get("target_id"))
	if request.Form.Get("target_id") == "" {
		targetID = 0
		err = nil
	}
	if err != nil || targetID < 0 {
		form.DurationSeconds = duration
		return form, &controlInputError{
			Field: "target_id", Message: "Enter a non-negative target ID.",
		}
	}
	form.DurationSeconds = duration
	form.TargetID = targetID
	return form, nil
}

func validateControlForm(action string, form frontend.OverrideForm) map[string]string {
	fields := make(map[string]string)
	action = strings.TrimPrefix(strings.TrimPrefix(action, "preview-"), "activate-")
	if action != "clear" && strings.TrimSpace(form.Text) == "" {
		fields["text"] = "Enter a message."
	}
	switch action {
	case "stage-message":
		if strings.TrimSpace(form.TargetGroupKey) == "" {
			fields["target_group_key"] = "Enter a Display Group."
		}
		switch form.Emphasis {
		case string(overrides.Normal), string(overrides.Attention), string(overrides.Urgent):
		default:
			fields["emphasis"] = "Choose a supported emphasis."
		}
	case "technical-difficulties":
		if strings.TrimSpace(form.TargetGroupKey) == "" {
			fields["target_group_key"] = "Enter a Display Group."
		}
		if !form.UntilCleared && form.DurationSeconds == 0 {
			fields["duration_seconds"] = "Enter a duration or choose Until cleared."
		}
	case "urgent-notice":
		switch form.Presentation {
		case "Overlay", "Replace":
		default:
			fields["presentation"] = "Choose Overlay or Replace."
		}
		validateControlTarget(form, fields)
		if !form.UntilCleared && form.DurationSeconds == 0 {
			fields["duration_seconds"] = "Enter a duration or choose Until cleared."
		}
	}
	return fields
}

func validateControlTarget(form frontend.OverrideForm, fields map[string]string) {
	switch overrides.TargetType(form.TargetType) {
	case "Event", "Public", "Crew":
		if form.TargetID != 0 {
			fields["target_id"] = "Leave the numeric ID empty for this target."
		}
		if strings.TrimSpace(form.TargetKey) != "" {
			fields["target_key"] = "Leave the Display Group key empty for this target."
		}
	case "Location", "Lane", "ProgramChannel", "Display":
		if form.TargetID <= 0 {
			fields["target_id"] = "Enter the target's positive numeric ID."
		}
		if strings.TrimSpace(form.TargetKey) != "" {
			fields["target_key"] = "Leave the Display Group key empty for this target."
		}
	case "DisplayGroup":
		if strings.TrimSpace(form.TargetKey) == "" {
			fields["target_key"] = "Enter the Display Group key."
		}
		if form.TargetID != 0 {
			fields["target_id"] = "Leave the numeric ID empty for a Display Group."
		}
	default:
		fields["target_type"] = "Choose a supported target."
	}
}

func stageMessageInput(
	eventID int,
	form frontend.OverrideForm,
	commandID string,
) overrides.SendStageMessageInput {
	return overrides.SendStageMessageInput{
		EventID: eventID, Text: form.Text, TargetGroupKey: form.TargetGroupKey,
		DurationSeconds: form.DurationSeconds, UntilCleared: form.UntilCleared,
		Emphasis: overrides.Emphasis(form.Emphasis), CommandID: commandID,
	}
}

func technicalDifficultiesInput(
	eventID int,
	form frontend.OverrideForm,
	commandID string,
) overrides.TechnicalDifficultiesInput {
	return overrides.TechnicalDifficultiesInput{
		EventID: eventID, Text: form.Text, TargetGroupKey: form.TargetGroupKey,
		DurationSeconds: form.DurationSeconds, UntilCleared: form.UntilCleared,
		CommandID: commandID,
	}
}

func priorityInput(
	eventID int,
	form frontend.OverrideForm,
	request *http.Request,
) overrides.PriorityInput {
	return overrides.PriorityInput{
		EventID: eventID, Text: form.Text,
		Target: overrides.Target{
			Type: overrides.TargetType(form.TargetType),
			ID:   form.TargetID,
			Key:  form.TargetKey,
		},
		Presentation: form.Presentation, DurationSeconds: form.DurationSeconds,
		UntilCleared: form.UntilCleared, CommandID: request.Form.Get("command_id"),
		PreviewFingerprint: request.Form.Get("preview_fingerprint"),
	}
}

func (handlers controlHandlers) previewStageMessage(
	request *http.Request,
	actor auth.Account,
	eventID int,
	form frontend.OverrideForm,
) (*frontend.OverridePreview, error) {
	resolved, err := handlers.overrides.PreviewStageMessage(
		request.Context(), actor, stageMessageInput(eventID, form, ""),
	)
	return handlers.controlPreview("stage-message", "Stage Message", resolved, "", form, err)
}

func (handlers controlHandlers) previewTechnicalDifficulties(
	request *http.Request,
	actor auth.Account,
	eventID int,
	form frontend.OverrideForm,
) (*frontend.OverridePreview, error) {
	resolved, err := handlers.overrides.PreviewTechnicalDifficulties(
		request.Context(), actor, technicalDifficultiesInput(eventID, form, ""),
	)
	return handlers.controlPreview(
		"technical-difficulties", "Technical Difficulties", resolved, "", form, err,
	)
}

func (handlers controlHandlers) previewUrgentNotice(
	request *http.Request,
	actor auth.Account,
	eventID int,
	form frontend.OverrideForm,
) (*frontend.OverridePreview, error) {
	resolved, err := handlers.overrides.PreviewUrgentNotice(
		request.Context(), actor, priorityInput(eventID, form, request),
	)
	return handlers.controlPreview("urgent-notice", "Urgent Notice", resolved.Preview, "", form, err)
}

func (handlers controlHandlers) controlPreview(
	action string,
	label string,
	resolved overrides.Preview,
	fingerprint string,
	form frontend.OverrideForm,
	err error,
) (*frontend.OverridePreview, error) {
	if err != nil {
		return nil, err
	}
	commandID, err := planningCommandID(handlers.browser.random)
	if err != nil {
		return nil, err
	}
	return &frontend.OverridePreview{
		Action: action, Label: label, Resolved: resolved,
		Fingerprint: fingerprint, Form: form, CommandID: commandID,
	}, nil
}

func (handlers controlHandlers) clearOverride(
	request *http.Request,
	actor auth.Account,
	eventID int,
) error {
	overrideID, err := strconv.Atoi(request.Form.Get("override_id"))
	if err != nil {
		return errInvalidControlInput
	}
	revision, err := strconv.Atoi(request.Form.Get("expected_revision"))
	if err != nil {
		return errInvalidControlInput
	}
	_, err = handlers.overrides.Clear(request.Context(), actor, overrides.ClearInput{
		EventID: eventID, OverrideID: overrideID, ExpectedRevision: revision,
		CommandID:          request.Form.Get("command_id"),
		Confirmed:          request.Form.Get("confirmed") == "true",
		ConfirmationMethod: "Keyboard",
	})
	return err
}

func controlError(err error) (int, string, bool) {
	switch {
	case errors.Is(err, overrides.ErrProducerRequired),
		errors.Is(err, overrides.ErrScopeDenied):
		return http.StatusForbidden, "Display target authority required.", true
	case errors.Is(err, overrides.ErrNotFound):
		return http.StatusNotFound, "Display Override not found.", true
	case errors.Is(err, overrides.ErrRevision),
		errors.Is(err, overrides.ErrConfigurationRevision),
		errors.Is(err, overrides.ErrCommandConflict):
		return http.StatusConflict, "Display Override changed; preview again.", true
	case errors.Is(err, errInvalidControlInput),
		errors.Is(err, overrides.ErrInvalidInput),
		errors.Is(err, overrides.ErrEventNotActive),
		errors.Is(err, command.ErrInvalidID):
		return http.StatusUnprocessableEntity, "Check the Override details and try again.", true
	default:
		return 0, "", false
	}
}

func (handlers controlHandlers) renderControl(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	event events.Event,
	csrfToken string,
	status int,
	formErrors frontend.FormErrors,
	preview *frontend.OverridePreview,
	submittedAction string,
	form frontend.OverrideForm,
) {
	var state rundown.CrewRundown
	var err error
	if canReadBackstageRundown(actor, event.ID) {
		state, err = handlers.rundown.CrewRundown(request.Context(), actor, event.ID)
		if err != nil {
			handlers.browser.frontendError(response, request, "read Program Channels", err)
			return
		}
	}
	sessions := make([]rundown.CrewSession, 0, len(state.Sessions))
	for _, session := range state.Sessions {
		if canAccessCompetition(actor, event.ID, session) {
			sessions = append(sessions, session)
		}
	}
	active, err := handlers.overrides.ListActive(request.Context(), actor, event.ID)
	if errors.Is(err, overrides.ErrEventNotActive) {
		active = nil
	} else if err != nil {
		handlers.browser.frontendError(response, request, "read active Display Overrides", err)
		return
	}
	clearCommandIDs := make(map[int]string, len(active))
	for _, item := range active {
		clearCommandIDs[item.ID], err = planningCommandID(handlers.browser.random)
		if err != nil {
			handlers.browser.frontendError(
				response, request, "create Display Override command identity", err,
			)
			return
		}
	}
	handlers.browser.render(response, request, status, frontend.Control(frontend.ControlPage{
		AccountName: actor.Name, CSRFToken: csrfToken, BuildVersion: handlers.buildVersion,
		ReducedEffects: reducedEffectsCookie(request),
		Navigation:     backstageNavigation(actor, request.URL.Path), Event: event,
		CanControl:        canUseBackstageControl(actor, event.ID),
		CanEmergencyAlert: canUseEmergencyAlert(actor, event.ID),
		Sessions:          sessions, Active: active, ClearCommandIDs: clearCommandIDs,
		Preview: preview, SubmittedAction: submittedAction, Form: form,
		Errors: formErrors,
	}))
}

func controlPath(eventID int) string {
	return "/backstage/events/" + strconv.Itoa(eventID) + "/control"
}
