package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dotwaffle/beamers/internal/attachments"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/displayviews"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/frontend"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/store"
)

type planningHandlers struct {
	browser     frontendHandlers
	events      *events.Service
	attachments *attachments.Service
	commands    *rundown.Commands
	queries     *rundown.Queries
}

func registerPlanningRoutes(
	mux *routeMux,
	authentication *auth.Service,
	eventService *events.Service,
	attachmentService *attachments.Service,
	commands *rundown.Commands,
	queries *rundown.Queries,
	logger *slog.Logger,
) {
	handlers := planningHandlers{
		browser: frontendHandlers{
			authentication: authentication,
			logger:         logger,
			random:         rand.Reader,
		},
		events:      eventService,
		attachments: attachmentService,
		commands:    commands,
		queries:     queries,
	}
	route := backstagePageRoute()
	route.maxBodyBytes = maxRundownRPCBodyBytes
	mux.HandleFunc("/backstage/events/{eventID}", route, handlers.overview)
	mux.HandleFunc(
		"/backstage/events/{eventID}/sessions",
		route,
		handlers.sessions,
	)
	mux.HandleFunc("/backstage/events/{eventID}/settings", route, handlers.settings)
	mux.HandleFunc(
		"/backstage/events/{eventID}/display-settings",
		route,
		handlers.displaySettings,
	)
	mux.HandleFunc("/backstage/events/{eventID}/planning", route, handlers.planning)
	mux.HandleFunc("/backstage/events/new", route, handlers.newEvent)
}

func (handlers planningHandlers) sessions(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !frontendReadAllowed(response, request) {
		return
	}
	actor, ok := handlers.browser.browserAccount(response, request)
	if !ok {
		return
	}
	eventID, err := positivePathID(request, "eventID")
	role, granted := actor.EventRoles[eventID]
	if err != nil || !granted {
		http.NotFound(response, request)
		return
	}
	event, err := handlers.events.CrewEvent(request.Context(), actor, eventID)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Event", err)
		return
	}
	var state rundown.CrewRundown
	if canReadBackstageRundown(actor, eventID) {
		state, err = handlers.queries.CrewRundown(request.Context(), actor, eventID)
		if errors.Is(err, store.ErrEventNotFound) {
			state = rundown.CrewRundown{}
		} else if err != nil {
			handlers.browser.frontendError(response, request, "read Sessions", err)
			return
		}
	}
	manage := make(map[int]bool)
	for _, session := range state.Sessions {
		if session.Type == rundown.SessionCompetition &&
			canAccessCompetition(actor, eventID, session) {
			manage[session.ID] = true
		}
	}
	prologue, ok := handlers.browser.backstagePagePrologue(response, request, actor)
	if !ok {
		return
	}
	handlers.browser.render(response, request, http.StatusOK, frontend.BackstageSessions(
		frontend.BackstageSessionsPage{
			AccountName: actor.Name, CSRFToken: prologue.CSRFToken,
			ReducedEffects: prologue.ReducedEffects,
			Navigation:     prologue.Navigation,
			Event:          event, Role: string(role), Sessions: state.Sessions,
			Manage: manage, Producer: actor.CanProduceEvent(eventID),
		},
	))
}

func (handlers planningHandlers) overview(response http.ResponseWriter, request *http.Request) {
	if !frontendReadAllowed(response, request) {
		return
	}
	actor, ok := handlers.browser.browserAccount(response, request)
	if !ok {
		return
	}
	eventID, err := positivePathID(request, "eventID")
	role, granted := actor.EventRoles[eventID]
	if err != nil || !granted {
		http.NotFound(response, request)
		return
	}
	overview, err := handlers.events.CrewEventOverview(request.Context(), actor, eventID)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Event overview", err)
		return
	}
	preview, err := handlers.queries.PublishPreview(
		request.Context(),
		actor,
		rundown.PublishPreviewInput{EventID: eventID},
	)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Rundown readiness", err)
		return
	}
	prologue, ok := handlers.browser.backstagePagePrologue(response, request, actor)
	if !ok {
		return
	}
	handlers.browser.render(response, request, http.StatusOK, frontend.EventOverview(
		frontend.EventOverviewPage{
			AccountName: actor.Name, CSRFToken: prologue.CSRFToken,
			ReducedEffects: prologue.ReducedEffects,
			Navigation:     prologue.Navigation,
			Event:          overview.Event, Active: overview.Active,
			AttachmentRelease: overview.AttachmentRelease,
			Rundown:           preview, Role: string(role),
			Producer: actor.CanProduceEvent(eventID), Administrator: actor.Administrator,
		},
	))
}

func (handlers planningHandlers) settings(response http.ResponseWriter, request *http.Request) {
	actor, ok := handlers.browser.browserAccount(response, request)
	if !ok {
		return
	}
	eventID, err := positivePathID(request, "eventID")
	if err != nil || !actor.CanProduceEvent(eventID) {
		http.NotFound(response, request)
		return
	}
	prologue, ok := handlers.browser.backstagePagePrologue(response, request, actor)
	if !ok {
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		handlers.renderSettings(response, request, actor, eventID, prologue, http.StatusOK, nil)
	case http.MethodPost:
		if !handlers.browser.validForm(response, request) {
			return
		}
		var inputErr error
		action := request.Form.Get("action")
		switch action {
		case "configure-attachment-release":
			var revision, cueSessionID int
			revision, inputErr = nonnegativeFormInt(request, "expected_release_revision")
			if inputErr == nil {
				cueSessionID, inputErr = optionalPositiveFormInt(request, "cue_session_id")
			}
			if inputErr == nil {
				_, inputErr = handlers.attachments.ConfigureEventRelease(
					request.Context(),
					actor,
					attachments.ConfigureEventReleaseInput{
						EventID: eventID, ExpectedRevision: revision,
						Policy:       attachments.ReleasePolicy(request.Form.Get("release_policy")),
						CueSessionID: cueSessionID, CommandID: request.Form.Get("command_id"),
					},
				)
			}
		case "fire-attachment-release-cue":
			var revision int
			revision, inputErr = nonnegativeFormInt(request, "expected_release_revision")
			if inputErr == nil {
				_, inputErr = handlers.attachments.FireReleaseCue(
					request.Context(),
					actor,
					attachments.FireReleaseCueInput{
						EventID: eventID, ExpectedRevision: revision,
						PreviewFingerprint: request.Form.Get("preview_fingerprint"),
						Confirmed:          request.Form.Get("confirmed") == "true",
						CommandID:          request.Form.Get("command_id"),
					},
				)
			}
		default:
			var input events.CreateInput
			input, inputErr = eventFormInput(request)
			if inputErr == nil {
				_, inputErr = handlers.events.Update(request.Context(), actor, eventID, input)
			}
		}
		if inputErr != nil {
			status, formErrors := eventSettingsErrors(inputErr, request.Form.Get("action"))
			handlers.renderSettings(response, request, actor, eventID, prologue, status, formErrors)
			return
		}
		http.Redirect(
			response,
			request,
			"/backstage/events/"+strconv.Itoa(eventID)+"/settings",
			http.StatusSeeOther,
		)
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
	}
}

func (handlers planningHandlers) renderSettings(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	eventID int,
	prologue backstagePrologue,
	status int,
	formErrors frontend.FormErrors,
) {
	event, err := handlers.events.CrewEvent(request.Context(), actor, eventID)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Event settings", err)
		return
	}
	release, err := handlers.attachments.PreviewReleaseCue(request.Context(), actor, eventID)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Attachment release settings", err)
		return
	}
	crew, err := handlers.queries.CrewRundown(request.Context(), actor, eventID)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Attachment release ceremonies", err)
		return
	}
	ceremonies := make([]rundown.CrewSession, 0)
	for _, session := range crew.Sessions {
		if session.Type == rundown.SessionCeremony {
			ceremonies = append(ceremonies, session)
		}
	}
	commandID, err := planningCommandID(handlers.browser.random)
	if err != nil {
		handlers.browser.frontendError(response, request, "create Event command identity", err)
		return
	}
	handlers.browser.render(response, request, status, frontend.EventSettings(
		frontend.EventSettingsPage{
			AccountName: actor.Name, CSRFToken: prologue.CSRFToken,
			ReducedEffects: prologue.ReducedEffects,
			Navigation:     prologue.Navigation,
			Event:          event, Release: release, Ceremonies: ceremonies,
			CommandID: commandID, SubmittedAction: request.Form.Get("action"),
			Form: request.Form, Errors: formErrors,
		},
	))
}

// eventSettingsFormErrorRules is the classify-error-to-field table for
// eventSettingsErrors' common shape. Adding a new validation rule means
// adding one row here.
var eventSettingsFormErrorRules = []formErrorRule{
	ruleValidation(func(validation *events.ValidationError) (string, string, string) {
		field, label := eventSettingsField(validation.Field)
		return field, label, validation.Message
	}),
	ruleValidation(func(validation *rundown.ValidationError) (string, string, string) {
		field, label := eventSettingsField(validation.Field)
		return field, label, validation.Message
	}),
	rule(matchErr(events.ErrEventSlugUnavailable),
		"public-slug", "Current Event Slug", ""),
	rule(matchErr(attachments.ErrReleasePolicy),
		"release-policy", "Event Release Policy", ""),
	rule(matchAll(matchErr(attachments.ErrInvalidInput), matchAction("configure-attachment-release")),
		"cue-session", "Optional release Ceremony", ""),
	rule(matchAll(matchErr(attachments.ErrInvalidInput), matchAction("fire-attachment-release-cue")),
		"release-cue-confirmed", "Release confirmation", ""),
}

func eventSettingsErrors(err error, action string) (int, frontend.FormErrors) {
	status, message := planningError(err)
	field, label, fieldMessage, _ := resolveFormErrorField(eventSettingsFormErrorRules, err, action)
	return status, formErrorResult(field, label, fieldMessage, action, message,
		func(field, _ string) string { return field },
	)
}

func eventSettingsField(field string) (string, string) {
	fields := map[string][2]string{
		"name":                              {"event-name", "Event name"},
		"public":                            {"event-public", "Public visibility"},
		"public_slug":                       {"public-slug", "Current Event Slug"},
		"planned_start_date":                {"planned-start-date", "Planned start"},
		"planned_end_date":                  {"planned-end-date", "Planned end"},
		"timezone":                          {"event-timezone", "Timezone"},
		"event_locale":                      {"event-locale", "Event locale"},
		"content_language":                  {"content-language", "Content language"},
		"event_day_boundary":                {"event-day-boundary", "Event day boundary"},
		"entry_default_disposition":         {"entry-default-disposition", "New Competition Entry disposition"},
		"submission_eligibility":            {"submission-eligibility", "New Competition Submission Eligibility"},
		"voting_method":                     {"voting-method", "Competition Voting Method"},
		"self_vote_policy":                  {"self-vote-policy", "Competition Self-Vote Policy"},
		"target_adjustment_presets_seconds": {"target-adjustment-presets", "Target adjustment presets"},
	}
	return fields[field][0], fields[field][1]
}

func (handlers planningHandlers) displaySettings(
	response http.ResponseWriter,
	request *http.Request,
) {
	actor, ok := handlers.browser.browserAccount(response, request)
	if !ok {
		return
	}
	eventID, err := positivePathID(request, "eventID")
	if err != nil || !actor.CanProduceEvent(eventID) {
		http.NotFound(response, request)
		return
	}
	event, err := handlers.events.CrewEvent(request.Context(), actor, eventID)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Event", err)
		return
	}
	configuration, err := handlers.events.DisplayConfiguration(
		request.Context(),
		actor,
		eventID,
	)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Display configuration", err)
		return
	}
	draft, err := handlers.queries.DraftRundown(request.Context(), actor, eventID)
	if err != nil {
		handlers.browser.frontendError(response, request, "read named Sessions", err)
		return
	}
	prologue, ok := handlers.browser.backstagePagePrologue(response, request, actor)
	if !ok {
		return
	}
	form := displaySettingsFormFromConfiguration(configuration.Configuration, draft)
	switch request.Method {
	case http.MethodGet, http.MethodHead:
	case http.MethodPost:
		if !handlers.browser.validForm(response, request) {
			return
		}
		form = displaySettingsFormFromValues(request.Form, draft)
		input, inputErr := displaySettingsInput(request.Form, configuration.Configuration, draft)
		if inputErr == nil {
			_, inputErr = handlers.events.ConfigureDisplays(
				request.Context(),
				actor,
				eventID,
				input,
			)
		}
		if inputErr != nil {
			status, formErrors, errorSection, known := displaySettingsError(inputErr)
			if !known {
				handlers.browser.frontendError(
					response,
					request,
					"configure Event Displays",
					inputErr,
				)
				return
			}
			handlers.renderDisplaySettings(
				response,
				request,
				actor,
				event,
				configuration.EventRevision,
				draft,
				prologue,
				form,
				status,
				formErrors,
				errorSection,
			)
			return
		}
		http.Redirect(
			response,
			request,
			"/backstage/events/"+strconv.Itoa(eventID)+"/display-settings",
			http.StatusSeeOther,
		)
		return
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
		return
	}
	handlers.renderDisplaySettings(
		response,
		request,
		actor,
		event,
		configuration.EventRevision,
		draft,
		prologue,
		form,
		http.StatusOK,
		nil,
		"",
	)
}

func (handlers planningHandlers) renderDisplaySettings(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	event events.Event,
	eventRevision int,
	draft rundown.DraftRundown,
	prologue backstagePrologue,
	form frontend.DisplaySettingsForm,
	status int,
	formErrors frontend.FormErrors,
	errorSection string,
) {
	commandID, err := planningCommandID(handlers.browser.random)
	if err != nil {
		handlers.browser.frontendError(response, request, "create Display command identity", err)
		return
	}
	handlers.browser.render(response, request, status, frontend.DisplaySettings(
		frontend.DisplaySettingsPage{
			AccountName: actor.Name, CSRFToken: prologue.CSRFToken,
			ReducedEffects: prologue.ReducedEffects,
			Navigation:     prologue.Navigation,
			Event:          event, EventRevision: eventRevision, Draft: draft,
			SessionTypes: displaySessionTypes(), Form: form,
			CommandID: commandID, Errors: formErrors, ErrorSection: errorSection,
		},
	))
}

type displaySettingsInputError struct {
	fieldID string
	field   string
	message string
	section string
}

func (err *displaySettingsInputError) Error() string {
	return err.field + ": " + err.message
}

func displaySettingsError(
	err error,
) (int, frontend.FormErrors, string, bool) {
	var inputError *displaySettingsInputError
	if errors.As(err, &inputError) {
		return http.StatusUnprocessableEntity, frontend.FormErrors{{
			FieldID: inputError.fieldID, Label: inputError.field,
			Message: inputError.message,
		}}, inputError.section, true
	}
	status, message := planningError(err)
	if status == http.StatusInternalServerError {
		return status, nil, "", false
	}
	var validation *events.ValidationError
	if errors.As(err, &validation) && validation.Field == "rotation_seconds" {
		return status, frontend.FormErrors{{
			FieldID: "rotation_seconds", Label: "Rotation interval",
			Message: validation.Message,
		}}, "", true
	}
	return status, frontend.FormErrors{{Message: message}}, "", true
}

func displaySettingsInput(
	values url.Values,
	current displayviews.Configuration,
	draft rundown.DraftRundown,
) (events.DisplayConfigurationInput, error) {
	rotation, err := strconv.Atoi(strings.TrimSpace(values.Get("rotation_seconds")))
	if err != nil {
		return events.DisplayConfigurationInput{}, &displaySettingsInputError{
			fieldID: "rotation_seconds", field: "Rotation interval",
			message: "must be an integer",
		}
	}
	revision, err := strconv.Atoi(values.Get("expected_event_revision"))
	if err != nil || revision <= 0 {
		return events.DisplayConfigurationInput{}, &events.ValidationError{
			Field: "expected_event_revision", Message: "must be a positive Event revision",
		}
	}
	thresholds, err := displayThresholds(displayThresholdInput{
		values:     values,
		prefix:     "timer_threshold",
		fieldLabel: "Default timer threshold",
	})
	if err != nil {
		return events.DisplayConfigurationInput{}, err
	}
	current.RotationSeconds = rotation
	current.ReducedEffects = values.Get("reduced_effects") == "true"
	current.TimerThresholds = thresholds
	current.SessionTypeTimerThresholds = maps.Clone(current.SessionTypeTimerThresholds)
	if current.SessionTypeTimerThresholds == nil {
		current.SessionTypeTimerThresholds = make(map[string][]displayviews.TimerThreshold)
	}
	for _, sessionType := range displaySessionTypes() {
		prefix := "session_type." + sessionType + ".threshold"
		if values.Get(prefix+"_override") != "true" {
			delete(current.SessionTypeTimerThresholds, sessionType)
			continue
		}
		thresholds, thresholdErr := displayThresholds(displayThresholdInput{
			values:     values,
			prefix:     prefix,
			fieldLabel: sessionType + " Session type timer threshold",
			section:    "session-type",
		})
		if thresholdErr != nil {
			return events.DisplayConfigurationInput{}, thresholdErr
		}
		current.SessionTypeTimerThresholds[sessionType] = thresholds
	}
	current.SessionTimerThresholds = maps.Clone(current.SessionTimerThresholds)
	if current.SessionTimerThresholds == nil {
		current.SessionTimerThresholds = make(map[int][]displayviews.TimerThreshold)
	}
	for _, session := range draft.Sessions {
		prefix := "session." + strconv.Itoa(session.ID) + ".threshold"
		if values.Get(prefix+"_override") != "true" {
			delete(current.SessionTimerThresholds, session.ID)
			continue
		}
		thresholds, thresholdErr := displayThresholds(displayThresholdInput{
			values:     values,
			prefix:     prefix,
			fieldLabel: session.Title + " (" + string(session.Type) + ") Session timer threshold",
			section:    "session",
		})
		if thresholdErr != nil {
			return events.DisplayConfigurationInput{}, thresholdErr
		}
		current.SessionTimerThresholds[session.ID] = thresholds
	}
	return events.DisplayConfigurationInput{
		Configuration:         current,
		ExpectedEventRevision: revision,
		CommandID:             values.Get("command_id"),
	}, nil
}

type displayThresholdInput struct {
	values     url.Values
	prefix     string
	fieldLabel string
	section    string
}

// displayThresholds parses the submitted rows and leaves every threshold rule
// to the Display views domain, so the form and the Event command agree on what
// a valid threshold set is.
func displayThresholds(
	input displayThresholdInput,
) ([]displayviews.TimerThreshold, error) {
	secondsValues := input.values[input.prefix+"_seconds"]
	emphasisValues := input.values[input.prefix+"_emphasis"]
	count := max(len(secondsValues), len(emphasisValues))
	result := make([]displayviews.TimerThreshold, 0, count)
	rows := make([]int, 0, count)
	for index := range count {
		secondsValue := strings.TrimSpace(formValue(secondsValues, index))
		emphasis := formValue(emphasisValues, index)
		if secondsValue == "" && emphasis == "" {
			continue
		}
		seconds, err := strconv.Atoi(secondsValue)
		if err != nil {
			return nil, displayThresholdError(input, displayviews.ThresholdError{
				Index:   index,
				Aspect:  displayviews.ThresholdSeconds,
				Message: "must be a whole number of seconds",
			})
		}
		rows = append(rows, index)
		result = append(result, displayviews.TimerThreshold{
			RemainingSeconds: seconds,
			Emphasis:         emphasis,
		})
	}
	if err := displayviews.ValidateTimerThresholds(input.prefix, result); err != nil {
		var violation *displayviews.ThresholdError
		if !errors.As(err, &violation) {
			return nil, err
		}
		submitted := *violation
		if submitted.Index < len(rows) {
			submitted.Index = rows[submitted.Index]
		}
		return nil, displayThresholdError(input, submitted)
	}
	return result, nil
}

// displayThresholdError points the operator at the row they submitted. The
// domain reports positions within the parsed set, which skips blank rows, so
// callers translate that position back to the submitted row first.
func displayThresholdError(
	input displayThresholdInput,
	violation displayviews.ThresholdError,
) error {
	kind := string(violation.Aspect)
	field := input.fieldLabel
	row := strconv.Itoa(violation.Index + 1)
	switch violation.Aspect {
	case displayviews.ThresholdSeconds:
		field += " remaining seconds row " + row
	case displayviews.ThresholdEmphasis:
		field += " emphasis row " + row
	case displayviews.ThresholdSet:
		kind = string(displayviews.ThresholdSeconds)
	}
	return &displaySettingsInputError{
		fieldID: displayThresholdFieldID(input.prefix, kind, violation.Index),
		field:   field,
		message: violation.Message,
		section: input.section,
	}
}

func displaySettingsFormFromConfiguration(
	configuration displayviews.Configuration,
	draft rundown.DraftRundown,
) frontend.DisplaySettingsForm {
	form := frontend.DisplaySettingsForm{
		RotationSeconds: strconv.Itoa(configuration.RotationSeconds),
		ReducedEffects:  configuration.ReducedEffects,
		TimerThresholds: displayThresholdFields(configuration.TimerThresholds),
		SessionTypeOverrides: make(
			map[string]bool,
			len(displaySessionTypes()),
		),
		SessionTypeThresholds: make(
			map[string][]frontend.DisplayThresholdFields,
			len(displaySessionTypes()),
		),
		SessionOverrides: make(
			map[int]bool,
			len(draft.Sessions),
		),
		SessionThresholds: make(
			map[int][]frontend.DisplayThresholdFields,
			len(draft.Sessions),
		),
	}
	for _, sessionType := range displaySessionTypes() {
		_, form.SessionTypeOverrides[sessionType] =
			configuration.SessionTypeTimerThresholds[sessionType]
		form.SessionTypeThresholds[sessionType] = displayThresholdFields(
			configuration.SessionTypeTimerThresholds[sessionType],
		)
	}
	for _, session := range draft.Sessions {
		_, form.SessionOverrides[session.ID] =
			configuration.SessionTimerThresholds[session.ID]
		form.SessionThresholds[session.ID] = displayThresholdFields(
			configuration.SessionTimerThresholds[session.ID],
		)
	}
	return form
}

func displaySettingsFormFromValues(
	values url.Values,
	draft rundown.DraftRundown,
) frontend.DisplaySettingsForm {
	form := frontend.DisplaySettingsForm{
		RotationSeconds: values.Get("rotation_seconds"),
		ReducedEffects:  values.Get("reduced_effects") == "true",
		TimerThresholds: displayThresholdFieldsFromValues(values, "timer_threshold"),
		SessionTypeOverrides: make(
			map[string]bool,
			len(displaySessionTypes()),
		),
		SessionTypeThresholds: make(
			map[string][]frontend.DisplayThresholdFields,
			len(displaySessionTypes()),
		),
		SessionOverrides: make(
			map[int]bool,
			len(draft.Sessions),
		),
		SessionThresholds: make(
			map[int][]frontend.DisplayThresholdFields,
			len(draft.Sessions),
		),
	}
	for _, sessionType := range displaySessionTypes() {
		form.SessionTypeOverrides[sessionType] =
			values.Get("session_type."+sessionType+".threshold_override") == "true"
		form.SessionTypeThresholds[sessionType] = displayThresholdFieldsFromValues(
			values,
			"session_type."+sessionType+".threshold",
		)
	}
	for _, session := range draft.Sessions {
		form.SessionOverrides[session.ID] = values.Get(
			"session."+strconv.Itoa(session.ID)+".threshold_override",
		) == "true"
		form.SessionThresholds[session.ID] = displayThresholdFieldsFromValues(
			values,
			"session."+strconv.Itoa(session.ID)+".threshold",
		)
	}
	return form
}

func displayThresholdFields(
	thresholds []displayviews.TimerThreshold,
) []frontend.DisplayThresholdFields {
	result := make([]frontend.DisplayThresholdFields, 0, len(thresholds)+1)
	for _, threshold := range thresholds {
		result = append(result, frontend.DisplayThresholdFields{
			Seconds:  strconv.Itoa(threshold.RemainingSeconds),
			Emphasis: threshold.Emphasis,
		})
	}
	return append(result, frontend.DisplayThresholdFields{})
}

func displayThresholdFieldsFromValues(
	values url.Values,
	prefix string,
) []frontend.DisplayThresholdFields {
	secondsValues := values[prefix+"_seconds"]
	emphasisValues := values[prefix+"_emphasis"]
	count := max(len(secondsValues), len(emphasisValues))
	result := make([]frontend.DisplayThresholdFields, 0, count+1)
	for index := range count {
		result = append(result, frontend.DisplayThresholdFields{
			Seconds:  formValue(secondsValues, index),
			Emphasis: formValue(emphasisValues, index),
		})
	}
	return append(result, frontend.DisplayThresholdFields{})
}

func displaySessionTypes() []string {
	return displayviews.SessionTypes()
}

func formValue(values []string, index int) string {
	if index >= len(values) {
		return ""
	}
	return values[index]
}

func displayThresholdFieldID(prefix, kind string, index int) string {
	return prefix + "-" + kind + "-" + strconv.Itoa(index)
}

func (handlers planningHandlers) newEvent(response http.ResponseWriter, request *http.Request) {
	actor, ok := handlers.browser.browserAccount(response, request)
	if !ok {
		return
	}
	if !actor.Administrator {
		http.NotFound(response, request)
		return
	}
	prologue, ok := handlers.browser.backstagePagePrologue(response, request, actor)
	if !ok {
		return
	}
	commandID, err := planningCommandID(handlers.browser.random)
	if err != nil {
		handlers.browser.frontendError(response, request, "create Event command identity", err)
		return
	}
	grantCommandID, err := planningCommandID(handlers.browser.random)
	if err != nil {
		handlers.browser.frontendError(response, request, "create Event Grant command identity", err)
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		handlers.renderNewEvent(
			response, request, actor, prologue, commandID, grantCommandID,
			http.StatusOK, "",
		)
	case http.MethodPost:
		if !handlers.browser.validForm(response, request) {
			return
		}
		commandID = request.Form.Get("command_id")
		grantCommandID = request.Form.Get("grant_command_id")
		input, inputErr := newEventFormInput(request)
		if inputErr != nil {
			status, message := planningError(inputErr)
			handlers.renderNewEvent(
				response, request, actor, prologue, commandID, grantCommandID,
				status, message,
			)
			return
		}
		created, createErr := handlers.events.Create(request.Context(), actor, input)
		if createErr == nil && request.Form.Get("grant_self") == "true" {
			_, createErr = handlers.events.GrantEventAccess(
				request.Context(),
				actor,
				created.ID,
				actor.ID,
				"Producer",
				grantCommandID,
			)
		}
		if createErr != nil {
			status, message := planningError(createErr)
			handlers.renderNewEvent(
				response, request, actor, prologue, commandID, grantCommandID,
				status, message,
			)
			return
		}
		target := "/backstage"
		if request.Form.Get("grant_self") == "true" {
			target = "/backstage/events/" + strconv.Itoa(created.ID) + "/planning"
		}
		http.Redirect(response, request, target, http.StatusSeeOther)
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
	}
}

func (handlers planningHandlers) renderNewEvent(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	prologue backstagePrologue,
	commandID, grantCommandID string,
	status int,
	message string,
) {
	existingEvents, err := handlers.events.List(request.Context(), actor)
	if err != nil {
		handlers.browser.frontendError(response, request, "list Events", err)
		return
	}
	handlers.browser.render(response, request, status, frontend.NewEvent(frontend.NewEventPage{
		AccountName: actor.Name, CSRFToken: prologue.CSRFToken,
		ReducedEffects: prologue.ReducedEffects,
		Navigation:     prologue.Navigation,
		CommandID:      commandID, GrantCommandID: grantCommandID,
		GrantSelfDefault: len(existingEvents) == 0,
		Error:            message,
	}))
}

func (handlers planningHandlers) planning(response http.ResponseWriter, request *http.Request) {
	actor, ok := handlers.browser.browserAccount(response, request)
	if !ok {
		return
	}
	eventID, err := positivePathID(request, "eventID")
	if err != nil || !actor.CanProduceEvent(eventID) {
		http.NotFound(response, request)
		return
	}
	prologue, ok := handlers.browser.backstagePagePrologue(response, request, actor)
	if !ok {
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		handlers.render(response, request, actor, eventID, prologue, http.StatusOK, nil, nil, nil, nil, "", "", "", "")
	case http.MethodPost:
		if !handlers.browser.validForm(response, request) {
			return
		}
		handlers.submit(response, request, actor, eventID, prologue)
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
	}
}

func (handlers planningHandlers) submit(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	eventID int,
	prologue backstagePrologue,
) {
	var (
		publishPreview   *rundown.PublishPreview
		csvPreview       *rundown.CSVImportPreview
		icalendarPreview *rundown.ICalendarImportPreview
		err              error
		mutated          bool
	)
	switch request.Form.Get("action") {
	case "draft":
		var input rundown.EditDraftInput
		var event events.Event
		event, err = handlers.events.CrewEvent(request.Context(), actor, eventID)
		if err == nil {
			var draft rundown.DraftRundown
			draft, err = handlers.queries.DraftRundown(request.Context(), actor, eventID)
			if err == nil {
				input, err = draftFormInput(request, eventID, event.Timezone, draft)
			}
		}
		if err == nil {
			_, err = handlers.commands.EditDraft(request.Context(), actor, input)
			mutated = err == nil
		}
	case "publish-preview":
		var preview rundown.PublishPreview
		var changeIDs []int
		changeIDs, err = positiveFormIDs(request.Form["change_id"])
		if err == nil {
			preview, err = handlers.queries.PublishPreview(request.Context(), actor, rundown.PublishPreviewInput{
				EventID: eventID, ChangeIDs: changeIDs,
			})
			publishPreview = &preview
		}
	case "publish":
		var input rundown.PublishInput
		input, err = publishFormInput(request, eventID)
		if err == nil {
			_, err = handlers.commands.Publish(request.Context(), actor, input)
			mutated = err == nil
			if !mutated && errors.Is(err, rundown.ErrStalePreview) {
				var preview rundown.PublishPreview
				preview, previewErr := handlers.queries.PublishPreview(
					request.Context(),
					actor,
					rundown.PublishPreviewInput{
						EventID: eventID, ChangeIDs: input.Confirmation.ChangeIDs,
					},
				)
				if previewErr == nil {
					publishPreview = &preview
				}
			}
		}
	case "csv-preview":
		var mappings []rundown.CSVFieldMapping
		mappings, err = csvFormMappings(request.Form.Get("csv_mappings"))
		if err == nil {
			preview, previewErr := handlers.queries.PreviewCSVImport(
				request.Context(),
				actor,
				rundown.CSVImportPreviewInput{
					EventID:  eventID,
					CSVData:  request.Form.Get("csv_data"),
					Mappings: mappings,
				},
			)
			if previewErr != nil {
				var validation *rundown.ValidationError
				if !errors.As(previewErr, &validation) {
					previewErr = formValidationError("csv_data", previewErr.Error())
				}
			}
			err, csvPreview = previewErr, &preview
		}
	case "csv-import":
		var input rundown.CSVImportInput
		input, err = csvImportFormInput(request, eventID)
		if err == nil {
			_, err = handlers.commands.ImportCSV(request.Context(), actor, input)
			mutated = err == nil
			if err != nil {
				var preview rundown.CSVImportPreview
				preview, previewErr := handlers.queries.PreviewCSVImport(
					request.Context(),
					actor,
					rundown.CSVImportPreviewInput{
						EventID: eventID, CSVData: input.CSVData, Mappings: input.Mappings,
					},
				)
				if previewErr == nil {
					csvPreview = &preview
				}
			}
		}
	case "icalendar-preview":
		var choices []rundown.ICalendarOccurrenceChoice
		choices, err = icalendarFormChoices(request.Form.Get("icalendar_choices"))
		if err == nil {
			preview, previewErr := handlers.queries.PreviewICalendarImport(
				request.Context(),
				actor,
				rundown.ICalendarImportPreviewInput{
					EventID: eventID,
					Data:    request.Form.Get("icalendar_data"),
					Choices: choices,
				},
			)
			if previewErr != nil {
				var validation *rundown.ValidationError
				if !errors.As(previewErr, &validation) {
					previewErr = formValidationError("icalendar_data", previewErr.Error())
				}
			}
			err, icalendarPreview = previewErr, &preview
		}
	case "icalendar-import":
		var input rundown.ICalendarImportInput
		input, err = icalendarImportFormInput(request, eventID)
		if err == nil {
			_, err = handlers.commands.ImportICalendar(request.Context(), actor, input)
			mutated = err == nil
			if err != nil {
				var preview rundown.ICalendarImportPreview
				preview, previewErr := handlers.queries.PreviewICalendarImport(
					request.Context(),
					actor,
					rundown.ICalendarImportPreviewInput{
						EventID: eventID, Data: input.Data, Choices: input.Choices,
					},
				)
				if previewErr == nil {
					icalendarPreview = &preview
				}
			}
		}
	default:
		err = formValidationError("action", "must identify a planning action")
	}
	if mutated {
		http.Redirect(
			response,
			request,
			"/backstage/events/"+strconv.Itoa(eventID)+"/planning",
			http.StatusSeeOther,
		)
		return
	}
	status, formErrors := planningFormErrors(err, request.Form)
	handlers.render(
		response,
		request,
		actor,
		eventID,
		prologue,
		status,
		formErrors,
		publishPreview,
		csvPreview,
		icalendarPreview,
		request.Form.Get("csv_data"),
		request.Form.Get("csv_mappings"),
		request.Form.Get("icalendar_data"),
		request.Form.Get("icalendar_choices"),
	)
}

func (handlers planningHandlers) render(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	eventID int,
	prologue backstagePrologue,
	status int,
	formErrors frontend.FormErrors,
	publishPreview *rundown.PublishPreview,
	csvPreview *rundown.CSVImportPreview,
	icalendarPreview *rundown.ICalendarImportPreview,
	csvData string,
	csvMappings string,
	icalendarData string,
	icalendarChoices string,
) {
	event, err := handlers.events.CrewEvent(request.Context(), actor, eventID)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Event planning state", err)
		return
	}
	crew, err := handlers.queries.CrewRundown(request.Context(), actor, eventID)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Published Rundown", err)
		return
	}
	draft, err := handlers.queries.DraftRundown(request.Context(), actor, eventID)
	if err != nil {
		handlers.browser.frontendError(response, request, "read materialized Draft Rundown", err)
		return
	}
	current, err := handlers.queries.PublishPreview(
		request.Context(),
		actor,
		rundown.PublishPreviewInput{EventID: eventID},
	)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Draft review", err)
		return
	}
	commandID, err := planningCommandID(handlers.browser.random)
	if err != nil {
		handlers.browser.frontendError(response, request, "create command identity", err)
		return
	}
	sessionBases := make(map[int]string, len(draft.Sessions))
	for _, session := range draft.Sessions {
		encoded, encodeErr := json.Marshal(session)
		if encodeErr != nil {
			handlers.browser.frontendError(response, request, "encode viewed Draft Session", encodeErr)
			return
		}
		sessionBases[session.ID] = string(encoded)
	}
	handlers.browser.render(response, request, status, frontend.Planning(frontend.PlanningPage{
		AccountName: actor.Name, CSRFToken: prologue.CSRFToken,
		ReducedEffects: prologue.ReducedEffects,
		Navigation:     prologue.Navigation,
		Event:          event, Rundown: crew, Draft: draft, SessionBases: sessionBases, CurrentPreview: current,
		PublishPreview: publishPreview, CSVPreview: csvPreview,
		ICalendarPreview: icalendarPreview, CSVData: csvData, CSVMappings: csvMappings,
		ICalendarData: icalendarData, ICalendarChoices: icalendarChoices,
		CommandID: commandID, SubmittedAction: request.Form.Get("action"),
		Form: request.Form, Errors: formErrors,
	}))
}

func planningFormErrors(err error, values url.Values) (int, frontend.FormErrors) {
	status, message := planningError(err)
	if err == nil {
		return status, nil
	}
	var validation *rundown.ValidationError
	if !errors.As(err, &validation) {
		return status, frontend.FormErrors{{Message: message}}
	}
	field, label := planningValidationField(validation.Field)
	if field == "" {
		return status, frontend.FormErrors{{Message: validation.Error()}}
	}
	target, id := "draft", 0
	switch {
	case strings.HasPrefix(field, "csv_"):
		return status, frontend.FormErrors{{
			FieldID: strings.ReplaceAll(field, "_", "-"), Label: label,
			Message: validation.Message,
		}}
	case strings.HasPrefix(field, "icalendar_"):
		return status, frontend.FormErrors{{
			FieldID: strings.ReplaceAll(field, "_", "-"), Label: label,
			Message: validation.Message,
		}}
	case field == "change_id":
		return status, frontend.FormErrors{{
			FieldID: "publish-changes", Label: label, Message: validation.Message,
		}}
	case field == "publish_note":
		return status, frontend.FormErrors{{
			FieldID: "publish-note", Label: label, Message: validation.Message,
		}}
	case field == "proposal_ids":
		importType := strings.TrimSuffix(values.Get("action"), "-import")
		return status, frontend.FormErrors{{
			FieldID: importType + "-proposal-ids", Label: label, Message: validation.Message,
		}}
	}
	for _, candidate := range []string{"location", "lane", "track", "session"} {
		if parsed, parseErr := strconv.Atoi(values.Get(candidate + "_id")); parseErr == nil && parsed > 0 {
			target, id = candidate, parsed
			break
		}
	}
	if target == "draft" {
		switch field {
		case "session_lane_ids", "session_location_ids", "session_track_ids":
			field = strings.TrimSuffix(field, "s")
		}
	}
	return status, frontend.FormErrors{{
		FieldID: frontend.PlanningFieldID(target, id, field), Label: label,
		Message: validation.Message,
	}}
}

func planningValidationField(field string) (string, string) {
	fields := map[string][2]string{
		"locations.name":                     {"location_name", "Location name"},
		"lanes.name":                         {"lane_name", "Lane name"},
		"lanes.location":                     {"lane_location_id", "Lane Location"},
		"tracks.name":                        {"track_name", "Track name"},
		"sessions.title":                     {"session_title", "Session title"},
		"sessions.speaker":                   {"session_speaker", "Speaker"},
		"sessions.type":                      {"session_type", "Session type"},
		"sessions.audience_visibility":       {"audience_visibility", "Audience visibility"},
		"sessions.public_details":            {"public_details", "Public details"},
		"sessions.crew_notes":                {"crew_notes", "Crew notes"},
		"sessions.planned_end":               {"planned_end", "Planned end"},
		"sessions.timing_policy":             {"timing_policy", "Timing policy"},
		"sessions.minimum_duration":          {"minimum_duration", "Minimum duration"},
		"sessions.start_boundary":            {"start_boundary", "Start boundary"},
		"sessions.end_boundary":              {"end_boundary", "End boundary"},
		"sessions.boundary":                  {"start_boundary", "Start boundary"},
		"sessions.upload_deadline":           {"upload_deadline", "Presentation upload deadline"},
		"sessions.submission_deadline":       {"submission_deadline", "Competition submission deadline"},
		"sessions.entry_default_disposition": {"session_entry_default_disposition", "New Entry disposition"},
		"sessions.lanes":                     {"session_lane_ids", "Lanes"},
		"sessions.locations":                 {"session_location_ids", "Locations"},
		"sessions.tracks":                    {"session_track_ids", "Tracks"},
		"location_name":                      {"location_name", "Location name"},
		"lane_name":                          {"lane_name", "Lane name"},
		"lane_location_id":                   {"lane_location_id", "Lane Location"},
		"track_name":                         {"track_name", "Track name"},
		"session_title":                      {"session_title", "Session title"},
		"session_speaker":                    {"session_speaker", "Speaker"},
		"session_type":                       {"session_type", "Session type"},
		"audience_visibility":                {"audience_visibility", "Audience visibility"},
		"public_details":                     {"public_details", "Public details"},
		"crew_notes":                         {"crew_notes", "Crew notes"},
		"planned_start":                      {"planned_start", "Planned start"},
		"planned_end":                        {"planned_end", "Planned end"},
		"timing_policy":                      {"timing_policy", "Timing policy"},
		"minimum_duration":                   {"minimum_duration", "Minimum duration"},
		"start_boundary":                     {"start_boundary", "Start boundary"},
		"end_boundary":                       {"end_boundary", "End boundary"},
		"session_lane_id":                    {"session_lane_id", "Current Draft Lane ID"},
		"session_location_id":                {"session_location_id", "Current Draft Location ID"},
		"session_track_id":                   {"session_track_id", "Current Draft Track ID"},
		"session_lane_ids":                   {"session_lane_ids", "Lanes"},
		"session_location_ids":               {"session_location_ids", "Locations"},
		"session_track_ids":                  {"session_track_ids", "Tracks"},
		"upload_deadline":                    {"upload_deadline", "Presentation upload deadline"},
		"submission_deadline":                {"submission_deadline", "Competition submission deadline"},
		"session_entry_default_disposition":  {"session_entry_default_disposition", "New Entry disposition"},
		"proposal_ids":                       {"proposal_ids", "Proposals to apply"},
		"csv_mappings":                       {"csv_mappings", "Field mappings"},
		"csv_data":                           {"csv_data", "CSV data"},
		"icalendar_choices":                  {"icalendar_choices", "Occurrence choices"},
		"icalendar_data":                     {"icalendar_data", "iCalendar data"},
		"change_id":                          {"change_id", "Draft changes"},
		"publish_note":                       {"publish_note", "Publish note"},
	}
	if found, ok := fields[field]; ok {
		return found[0], found[1]
	}
	return "", ""
}

func planningCommandID(random io.Reader) (string, error) {
	value := make([]byte, 18)
	if _, err := io.ReadFull(random, value); err != nil {
		return "", err
	}
	return "browser-" + base64.RawURLEncoding.EncodeToString(value), nil
}

func planningError(err error) (int, string) {
	if err == nil {
		return http.StatusOK, ""
	}
	var eventValidation *events.ValidationError
	var rundownValidation *rundown.ValidationError
	switch {
	case errors.As(err, &eventValidation):
		return http.StatusUnprocessableEntity, eventValidation.Error()
	case errors.As(err, &rundownValidation):
		return http.StatusUnprocessableEntity, rundownValidation.Error()
	case errors.Is(err, events.ErrRevisionConflict):
		return http.StatusConflict, "Event changed. Reload, review the latest revision, and try again."
	case errors.Is(err, attachments.ErrReleaseCuePreviewChanged):
		return http.StatusConflict, "Release impact changed. Reload and review the current preview."
	case errors.Is(err, attachments.ErrReleaseCueBlocked):
		return http.StatusConflict, "Release is blocked by unresolved Entries. Resolve them and review again."
	case errors.Is(err, attachments.ErrReleaseRevision):
		return http.StatusConflict, "Attachment release changed. Reload and review the latest state."
	case errors.Is(err, attachments.ErrInvalidInput),
		errors.Is(err, attachments.ErrReleasePolicy):
		return http.StatusUnprocessableEntity, "Check the Attachment release settings and confirmation."
	case errors.Is(err, events.ErrEventSlugUnavailable):
		return http.StatusConflict, "Event Slug is already in use."
	case errors.Is(err, rundown.ErrDraftRevisionConflict):
		return http.StatusConflict, "Draft changed. Reload and review the latest revision."
	case errors.Is(err, rundown.ErrStalePreview):
		return http.StatusConflict, "Publish Preview is stale. Preview the current Draft again."
	case errors.Is(err, events.ErrCommandConflict),
		errors.Is(err, rundown.ErrCommandConflict),
		errors.Is(err, attachments.ErrCommandConflict):
		return http.StatusConflict, "Command identity was already used for different work."
	case errors.Is(err, events.ErrEventAccessDenied),
		errors.Is(err, rundown.ErrEventAccessDenied),
		errors.Is(err, attachments.ErrProducerRequired),
		errors.Is(err, attachments.ErrUploadTargetNotFound):
		return http.StatusNotFound, "Event not found."
	default:
		return http.StatusInternalServerError, "Planning action failed."
	}
}

func eventFormInput(request *http.Request) (events.CreateInput, error) {
	revision, err := nonnegativeFormInt(request, "expected_event_revision")
	if err != nil || revision == 0 {
		return events.CreateInput{}, formValidationError("expected_event_revision", "must be positive")
	}
	input, err := eventConfigurationFormInput(request)
	if err != nil {
		return events.CreateInput{}, err
	}
	input.ExpectedRevision = revision
	return input, nil
}

func newEventFormInput(request *http.Request) (events.CreateInput, error) {
	return eventConfigurationFormInput(request)
}

func eventConfigurationFormInput(request *http.Request) (events.CreateInput, error) {
	presets, err := commaSeparatedInts(request.Form.Get("target_adjustment_presets_seconds"))
	if err != nil {
		return events.CreateInput{}, formValidationError(
			"target_adjustment_presets_seconds",
			"must contain comma-separated integers",
		)
	}
	return events.CreateInput{
		Name:                           request.Form.Get("event_name"),
		Public:                         request.Form.Get("public") == "true",
		PublicSlug:                     request.Form.Get("public_slug"),
		PlannedStartDate:               request.Form.Get("planned_start_date"),
		PlannedEndDate:                 request.Form.Get("planned_end_date"),
		Timezone:                       request.Form.Get("timezone"),
		EventLocale:                    request.Form.Get("event_locale"),
		ContentLanguage:                request.Form.Get("content_language"),
		EventDayBoundary:               request.Form.Get("event_day_boundary"),
		EntryDefaultDisposition:        request.Form.Get("entry_default_disposition"),
		SubmissionEligibility:          request.Form.Get("submission_eligibility"),
		VotingMethod:                   request.Form.Get("voting_method"),
		SelfVotePolicy:                 request.Form.Get("self_vote_policy"),
		TargetAdjustmentPresetsSeconds: presets,
		CommandID:                      request.Form.Get("command_id"),
	}, nil
}

func draftFormInput(
	request *http.Request,
	eventID int,
	timezone string,
	draft rundown.DraftRundown,
) (rundown.EditDraftInput, error) {
	revision, err := nonnegativeFormInt(request, "expected_draft_revision")
	if err != nil {
		return rundown.EditDraftInput{}, err
	}
	input := rundown.EditDraftInput{
		EventID: eventID, CommandID: request.Form.Get("command_id"),
		ExpectedDraftRevision: revision,
	}
	locationID, err := optionalPositiveFormInt(request, "location_id")
	if err != nil {
		return rundown.EditDraftInput{}, err
	}
	locationName := strings.TrimSpace(request.Form.Get("location_name"))
	if locationID != 0 || locationName != "" {
		location := rundown.LocationDraftInput{ID: locationID, Name: locationName}
		if locationID == 0 {
			location.Ref = "browser-location"
		} else {
			if _, ok := draftLocation(draft, locationID); !ok {
				return rundown.EditDraftInput{}, formValidationError(
					"location_id",
					"must identify current Draft structure",
				)
			}
			if locationName != request.Form.Get("base_location_name") {
				location.UpdateFields = []string{"name"}
			}
		}
		input.Locations = append(input.Locations, location)
	}
	laneID, err := optionalPositiveFormInt(request, "lane_id")
	if err != nil {
		return rundown.EditDraftInput{}, err
	}
	laneName := strings.TrimSpace(request.Form.Get("lane_name"))
	if laneID != 0 || laneName != "" {
		lane := rundown.LaneDraftInput{ID: laneID, Name: laneName}
		if laneID == 0 {
			locationTarget, targetErr := formTarget(
				request,
				"lane_location_id",
				locationName != "" && locationID == 0,
				"browser-location",
			)
			if targetErr != nil {
				return rundown.EditDraftInput{}, targetErr
			}
			lane.Ref = "browser-lane"
			lane.Location = locationTarget
		} else {
			if _, ok := draftLane(draft, laneID); !ok {
				return rundown.EditDraftInput{}, formValidationError(
					"lane_id",
					"must identify current Draft structure",
				)
			}
			if laneName != "" {
				if laneName != request.Form.Get("base_lane_name") {
					lane.UpdateFields = append(lane.UpdateFields, "name")
				}
			}
			if request.Form.Get("lane_location_id") != "" {
				locationTarget, targetErr := formTarget(
					request,
					"lane_location_id",
					false,
					"",
				)
				if targetErr != nil {
					return rundown.EditDraftInput{}, targetErr
				}
				baseLocationID, baseErr := strconv.Atoi(request.Form.Get("base_lane_location_id"))
				if baseErr != nil || baseLocationID <= 0 {
					return rundown.EditDraftInput{}, formValidationError(
						"base_lane_location_id",
						"must identify the viewed Draft Location",
					)
				}
				if locationTarget.ID != baseLocationID {
					lane.Location = locationTarget
					lane.UpdateFields = append(lane.UpdateFields, "location")
				}
			}
		}
		input.Lanes = append(input.Lanes, lane)
	}
	trackID, err := optionalPositiveFormInt(request, "track_id")
	if err != nil {
		return rundown.EditDraftInput{}, err
	}
	trackName := strings.TrimSpace(request.Form.Get("track_name"))
	if trackID != 0 || trackName != "" {
		track := rundown.TrackDraftInput{ID: trackID, Name: trackName}
		if trackID == 0 {
			track.Ref = "browser-track"
		} else {
			if _, ok := draftTrack(draft, trackID); !ok {
				return rundown.EditDraftInput{}, formValidationError(
					"track_id",
					"must identify current Draft structure",
				)
			}
			if trackName != request.Form.Get("base_track_name") {
				track.UpdateFields = []string{"name"}
			}
		}
		input.Tracks = append(input.Tracks, track)
	}
	sessionID, err := optionalPositiveFormInt(request, "session_id")
	if err != nil {
		return rundown.EditDraftInput{}, err
	}
	sessionTitle := strings.TrimSpace(request.Form.Get("session_title"))
	if sessionID != 0 || sessionTitle != "" {
		var current, base *rundown.CrewSession
		if sessionID != 0 {
			found, ok := draftSession(draft, sessionID)
			if !ok {
				return rundown.EditDraftInput{}, formValidationError(
					"session_id",
					"must identify current Draft structure",
				)
			}
			current = &found
			var viewed rundown.CrewSession
			if decodeErr := json.Unmarshal(
				[]byte(request.Form.Get("base_session")),
				&viewed,
			); decodeErr != nil || viewed.ID != sessionID {
				return rundown.EditDraftInput{}, formValidationError(
					"base_session",
					"must identify the viewed Draft Session",
				)
			}
			base = &viewed
		}
		session, sessionErr := sessionFormInput(
			request,
			sessionID,
			timezone,
			current,
			base,
			locationName != "" && locationID == 0,
			laneName != "" && laneID == 0,
			trackName != "" && trackID == 0,
		)
		if sessionErr != nil {
			return rundown.EditDraftInput{}, sessionErr
		}
		input.Sessions = append(input.Sessions, session)
	}
	if len(input.Locations)+len(input.Lanes)+len(input.Tracks)+len(input.Sessions) == 0 {
		return rundown.EditDraftInput{}, formValidationError("draft", "must include a structural edit")
	}
	return input, nil
}

func sessionFormInput(
	request *http.Request,
	sessionID int,
	timezone string,
	current *rundown.CrewSession,
	base *rundown.CrewSession,
	newLocation bool,
	newLane bool,
	newTrack bool,
) (rundown.SessionDraftInput, error) {
	location, err := formTarget(request, "session_location_id", newLocation, "browser-location")
	if err != nil && sessionID == 0 {
		return rundown.SessionDraftInput{}, err
	}
	lane, err := formTarget(request, "session_lane_id", newLane, "browser-lane")
	if err != nil && sessionID == 0 {
		return rundown.SessionDraftInput{}, err
	}
	track, trackErr := formTarget(request, "session_track_id", newTrack, "browser-track")
	if trackErr != nil && strings.TrimSpace(request.Form.Get("session_track_id")) != "" {
		return rundown.SessionDraftInput{}, trackErr
	}
	plannedStart, err := planningFormTime(
		request.Form.Get("planned_start"), timezone,
		request.Form.Get("planned_start_occurrence"), currentSessionTime(base, "start"),
	)
	if err != nil {
		return rundown.SessionDraftInput{}, formValidationError("planned_start", err.Error())
	}
	plannedEnd, err := planningFormTime(
		request.Form.Get("planned_end"), timezone,
		request.Form.Get("planned_end_occurrence"), currentSessionTime(base, "end"),
	)
	if err != nil {
		return rundown.SessionDraftInput{}, formValidationError("planned_end", err.Error())
	}
	minimumDuration, err := time.ParseDuration(request.Form.Get("minimum_duration"))
	if err != nil {
		return rundown.SessionDraftInput{}, formValidationError("minimum_duration", "must be a duration such as 15m")
	}
	uploadDeadline, err := optionalPlanningFormTime(
		request.Form.Get("upload_deadline"),
		timezone,
		request.Form.Get("upload_deadline_occurrence"),
		currentSessionTime(base, "upload"),
	)
	if err != nil {
		return rundown.SessionDraftInput{}, formValidationError("upload_deadline", err.Error())
	}
	submissionDeadline, err := optionalPlanningFormTime(
		request.Form.Get("submission_deadline"),
		timezone,
		request.Form.Get("submission_deadline_occurrence"),
		currentSessionTime(base, "submission"),
	)
	if err != nil {
		return rundown.SessionDraftInput{}, formValidationError("submission_deadline", err.Error())
	}
	session := rundown.SessionDraftInput{
		ID: sessionID, Title: request.Form.Get("session_title"),
		Speaker:            request.Form.Get("session_speaker"),
		Type:               rundown.SessionType(request.Form.Get("session_type")),
		AudienceVisibility: rundown.AudienceVisibility(request.Form.Get("audience_visibility")),
		PublicDetails:      request.Form.Get("public_details"), CrewNotes: request.Form.Get("crew_notes"),
		PlannedStart: plannedStart, PlannedEnd: plannedEnd,
		TimingPolicy:    rundown.TimingPolicy(request.Form.Get("timing_policy")),
		MinimumDuration: minimumDuration,
		StartBoundary:   rundown.Boundary(request.Form.Get("start_boundary")),
		EndBoundary:     rundown.Boundary(request.Form.Get("end_boundary")),
		UploadDeadline:  uploadDeadline, SubmissionDeadline: submissionDeadline,
		EntryDefault: rundown.EntryDisposition(request.Form.Get("session_entry_default_disposition")),
	}
	if session.Type != rundown.SessionPresentation {
		session.UploadDeadline = time.Time{}
	}
	if session.Type != rundown.SessionCompetition {
		session.SubmissionDeadline = time.Time{}
		session.EntryDefault = ""
	}
	if sessionID == 0 {
		session.Ref = "browser-session"
		session.Lanes = []rundown.TargetRef{lane}
		session.Locations = []rundown.TargetRef{location}
		if track.ID != 0 || track.Ref != "" {
			session.Tracks = []rundown.TargetRef{track}
		}
	} else {
		if current == nil || base == nil {
			return rundown.SessionDraftInput{}, formValidationError("session_id", "must identify current Draft structure")
		}
		for _, field := range []struct {
			name    string
			changed bool
		}{
			{"title", session.Title != base.Title},
			{"speaker", session.Speaker != base.Speaker},
			{"type", session.Type != base.Type},
			{"audience_visibility", session.AudienceVisibility != base.AudienceVisibility},
			{"public_details", session.PublicDetails != base.PublicDetails},
			{"crew_notes", session.CrewNotes != base.CrewNotes},
			{"planned_start", !session.PlannedStart.Equal(base.PlannedStart)},
			{"planned_end", !session.PlannedEnd.Equal(base.PlannedEnd)},
			{"timing_policy", session.TimingPolicy != base.TimingPolicy},
			{"minimum_duration", session.MinimumDuration != base.MinimumDuration},
			{"start_boundary", session.StartBoundary != base.StartBoundary},
			{"end_boundary", session.EndBoundary != base.EndBoundary},
			{"upload_deadline", !session.UploadDeadline.Equal(base.UploadDeadline)},
			{"submission_deadline", !session.SubmissionDeadline.Equal(base.SubmissionDeadline)},
			{"entry_default_disposition", session.EntryDefault != base.EntryDefault},
		} {
			if field.changed {
				session.UpdateFields = append(session.UpdateFields, field.name)
			}
		}
		selectedLanes, idsErr := positiveFormIDs(request.Form["session_lane_ids"])
		if idsErr != nil {
			return rundown.SessionDraftInput{}, formValidationError("session_lane_ids", idsErr.Error())
		}
		selectedLocations, idsErr := positiveFormIDs(request.Form["session_location_ids"])
		if idsErr != nil {
			return rundown.SessionDraftInput{}, formValidationError("session_location_ids", idsErr.Error())
		}
		selectedTracks, idsErr := positiveFormIDs(request.Form["session_track_ids"])
		if idsErr != nil {
			return rundown.SessionDraftInput{}, formValidationError("session_track_ids", idsErr.Error())
		}
		session.AddLanes, session.RemoveLanes = membershipChanges(base.LaneIDs, selectedLanes)
		session.AddLocations, session.RemoveLocations = membershipChanges(base.LocationIDs, selectedLocations)
		session.AddTracks, session.RemoveTracks = membershipChanges(base.TrackIDs, selectedTracks)
		if len(session.AddLanes) != 0 {
			session.UpdateFields = append(session.UpdateFields, "add_lanes")
		}
		if len(session.RemoveLanes) != 0 {
			session.UpdateFields = append(session.UpdateFields, "remove_lanes")
		}
		if len(session.AddLocations) != 0 {
			session.UpdateFields = append(session.UpdateFields, "add_locations")
		}
		if len(session.RemoveLocations) != 0 {
			session.UpdateFields = append(session.UpdateFields, "remove_locations")
		}
		if len(session.AddTracks) != 0 {
			session.UpdateFields = append(session.UpdateFields, "add_tracks")
		}
		if len(session.RemoveTracks) != 0 {
			session.UpdateFields = append(session.UpdateFields, "remove_tracks")
		}
	}
	return session, nil
}

func publishFormInput(request *http.Request, eventID int) (rundown.PublishInput, error) {
	draftRevision, err := nonnegativeFormInt(request, "draft_revision")
	if err != nil {
		return rundown.PublishInput{}, err
	}
	publishedRevision, err := nonnegativeFormInt(request, "published_revision")
	if err != nil {
		return rundown.PublishInput{}, err
	}
	changeIDs, err := positiveFormIDs(request.Form["change_id"])
	if err != nil {
		return rundown.PublishInput{}, formValidationError("change_id", "must identify Draft changes")
	}
	return rundown.PublishInput{
		EventID: eventID, CommandID: request.Form.Get("command_id"),
		Confirmation: rundown.PublishConfirmation{
			DraftRevision: draftRevision, PublishedRevision: publishedRevision,
			ChangeIDs: changeIDs, Fingerprint: request.Form.Get("fingerprint"),
		},
		PublishNote: request.Form.Get("publish_note"),
	}, nil
}

func csvImportFormInput(request *http.Request, eventID int) (rundown.CSVImportInput, error) {
	revision, err := nonnegativeFormInt(request, "expected_draft_revision")
	if err != nil {
		return rundown.CSVImportInput{}, err
	}
	mappings, err := csvFormMappings(request.Form.Get("csv_mappings"))
	if err != nil {
		return rundown.CSVImportInput{}, err
	}
	return rundown.CSVImportInput{
		EventID: eventID, CommandID: request.Form.Get("command_id"),
		ExpectedDraftRevision: revision, CSVData: request.Form.Get("csv_data"),
		Mappings: mappings, Fingerprint: request.Form.Get("fingerprint"),
		ProposalIDs: request.Form["proposal_id"],
	}, nil
}

func icalendarImportFormInput(
	request *http.Request,
	eventID int,
) (rundown.ICalendarImportInput, error) {
	revision, err := nonnegativeFormInt(request, "expected_draft_revision")
	if err != nil {
		return rundown.ICalendarImportInput{}, err
	}
	choices, err := icalendarFormChoices(request.Form.Get("icalendar_choices"))
	if err != nil {
		return rundown.ICalendarImportInput{}, err
	}
	return rundown.ICalendarImportInput{
		EventID: eventID, CommandID: request.Form.Get("command_id"),
		ExpectedDraftRevision: revision, Data: request.Form.Get("icalendar_data"),
		Choices: choices, Fingerprint: request.Form.Get("fingerprint"),
		ProposalIDs: request.Form["proposal_id"],
	}, nil
}

func csvFormMappings(value string) ([]rundown.CSVFieldMapping, error) {
	var mappings []rundown.CSVFieldMapping
	for line := range strings.SplitSeq(strings.TrimSpace(value), "\n") {
		source, target, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found || strings.TrimSpace(source) == "" || strings.TrimSpace(target) == "" {
			return nil, formValidationError("csv_mappings", "must use one source=target mapping per line")
		}
		mappings = append(mappings, rundown.CSVFieldMapping{
			SourceColumn: strings.TrimSpace(source),
			TargetField:  strings.TrimSpace(target),
		})
	}
	if len(mappings) == 0 {
		return nil, formValidationError("csv_mappings", "must include at least one mapping")
	}
	return mappings, nil
}

func icalendarFormChoices(value string) ([]rundown.ICalendarOccurrenceChoice, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	var choices []rundown.ICalendarOccurrenceChoice
	for line := range strings.SplitSeq(strings.TrimSpace(value), "\n") {
		parts := strings.Split(strings.TrimSpace(line), ",")
		if len(parts) != 3 {
			return nil, formValidationError(
				"icalendar_choices",
				"must use one UID,PROPERTY,Earlier|Later choice per line",
			)
		}
		choices = append(choices, rundown.ICalendarOccurrenceChoice{
			UID: strings.TrimSpace(parts[0]), Property: strings.TrimSpace(parts[1]),
			Occurrence: strings.TrimSpace(parts[2]),
		})
	}
	return choices, nil
}

func formTarget(
	request *http.Request,
	field string,
	useRef bool,
	ref string,
) (rundown.TargetRef, error) {
	id, err := optionalPositiveFormInt(request, field)
	if err != nil {
		return rundown.TargetRef{}, err
	}
	if id != 0 {
		return rundown.TargetRef{ID: id}, nil
	}
	if useRef {
		return rundown.TargetRef{Ref: ref}, nil
	}
	return rundown.TargetRef{}, formValidationError(field, "must identify published or new structure")
}

func planningFormTime(
	value, timezone, occurrence string,
	current time.Time,
) (time.Time, error) {
	if !current.IsZero() {
		location, err := time.LoadLocation(timezone)
		if err != nil {
			return time.Time{}, errors.New("event timezone is invalid")
		}
		if occurrence == "" && current.In(location).Format("2006-01-02T15:04") == value {
			return current, nil
		}
	}
	return rundown.ResolveLocalDateTime(value, timezone, occurrence)
}

func membershipChanges(base, selected []int) (additions, removals []rundown.TargetRef) {
	for _, id := range selected {
		if !slices.Contains(base, id) {
			additions = append(additions, rundown.TargetRef{ID: id})
		}
	}
	for _, id := range base {
		if !slices.Contains(selected, id) {
			removals = append(removals, rundown.TargetRef{ID: id})
		}
	}
	return additions, removals
}

func optionalPlanningFormTime(
	value, timezone, occurrence string,
	current time.Time,
) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	return planningFormTime(value, timezone, occurrence, current)
}

func currentSessionTime(session *rundown.CrewSession, field string) time.Time {
	if session == nil {
		return time.Time{}
	}
	switch field {
	case "start":
		return session.PlannedStart
	case "end":
		return session.PlannedEnd
	case "upload":
		return session.UploadDeadline
	case "submission":
		return session.SubmissionDeadline
	default:
		return time.Time{}
	}
}

func draftLocation(draft rundown.DraftRundown, id int) (rundown.CrewLocation, bool) {
	for _, item := range draft.Locations {
		if item.ID == id {
			return item, true
		}
	}
	return rundown.CrewLocation{}, false
}

func draftLane(draft rundown.DraftRundown, id int) (rundown.CrewLane, bool) {
	for _, item := range draft.Lanes {
		if item.ID == id {
			return item, true
		}
	}
	return rundown.CrewLane{}, false
}

func draftTrack(draft rundown.DraftRundown, id int) (rundown.CrewTrack, bool) {
	for _, item := range draft.Tracks {
		if item.ID == id {
			return item, true
		}
	}
	return rundown.CrewTrack{}, false
}

func draftSession(draft rundown.DraftRundown, id int) (rundown.CrewSession, bool) {
	for _, item := range draft.Sessions {
		if item.ID == id {
			return item, true
		}
	}
	return rundown.CrewSession{}, false
}

func optionalPositiveFormInt(request *http.Request, field string) (int, error) {
	value := strings.TrimSpace(request.Form.Get(field))
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, formValidationError(field, "must be a positive integer")
	}
	return parsed, nil
}

func nonnegativeFormInt(request *http.Request, field string) (int, error) {
	value := strings.TrimSpace(request.Form.Get(field))
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0, formValidationError(field, "must be a nonnegative integer")
	}
	return parsed, nil
}

func commaSeparatedInts(value string) ([]int, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	result := make([]int, 0, len(parts))
	for _, part := range parts {
		parsed, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		result = append(result, parsed)
	}
	return result, nil
}

func formValidationError(field, message string) error {
	return &rundown.ValidationError{Field: field, Message: message}
}
