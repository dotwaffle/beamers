package server

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/eventthemes"
	"github.com/dotwaffle/beamers/internal/frontend"
	"github.com/dotwaffle/beamers/internal/themevalue"
)

type eventThemeHandlers struct {
	browser frontendHandlers
	themes  *eventthemes.Service
}

func (handlers eventThemeHandlers) administration(
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
	csrfToken, err := handlers.browser.csrfToken(response, request)
	if err != nil {
		handlers.browser.frontendError(response, request, "create CSRF proof", err)
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		active, activeErr := handlers.themes.Active(request.Context(), eventID, "")
		if activeErr != nil {
			handlers.browser.frontendError(response, request, "read active Event Theme", activeErr)
			return
		}
		selected := active.Revision.ID
		if value := request.URL.Query().Get("revision"); value != "" {
			selected, err = nonnegativeThemeID(value)
			if err != nil {
				handlers.render(
					response, request, actor, eventID, csrfToken, 0,
					http.StatusBadRequest, "Theme Revision must be a non-negative integer.",
				)
				return
			}
		}
		handlers.render(response, request, actor, eventID, csrfToken, selected, http.StatusOK, "")
	case http.MethodPost:
		if !handlers.browser.validForm(response, request) {
			return
		}
		if err = validateEventThemeForm(request.Form); err != nil {
			handlers.render(
				response, request, actor, eventID, csrfToken, 0,
				http.StatusBadRequest, "An undocumented Theme control was rejected.",
			)
			return
		}
		handlers.submit(response, request, actor, eventID, csrfToken)
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
	}
}

func (handlers eventThemeHandlers) submit(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	eventID int,
	csrfToken string,
) {
	switch request.Form.Get("action") {
	case "create-draft":
		created, err := handlers.themes.CreateDraft(
			request.Context(),
			actor,
			eventID,
			eventthemes.CreateDraftInput{
				Config: eventThemeConfig(request.Form), CommandID: request.Form.Get("command_id"),
			},
		)
		if err != nil {
			status, message, known := eventThemeError(err)
			if !known {
				handlers.browser.frontendError(response, request, "create Event Theme Revision", err)
				return
			}
			handlers.render(response, request, actor, eventID, csrfToken, 0, status, message)
			return
		}
		http.Redirect( //nolint:gosec // The redirect path contains parsed positive integer IDs only.
			response,
			request,
			eventThemeAdministrationPath(eventID, created.ID),
			http.StatusSeeOther,
		)
	case "activate":
		revisionID, revisionErr := nonnegativeThemeID(request.Form.Get("revision_id"))
		expectedID, expectedErr := nonnegativeThemeID(
			request.Form.Get("expected_active_revision_id"),
		)
		if revisionErr != nil || expectedErr != nil {
			handlers.render(
				response, request, actor, eventID, csrfToken, 0,
				http.StatusBadRequest, "Check the Theme Revision details and try again.",
			)
			return
		}
		_, err := handlers.themes.Activate(
			request.Context(),
			actor,
			eventID,
			eventthemes.ActivateInput{
				RevisionID: revisionID, ExpectedActiveRevisionID: expectedID,
				CommandID: request.Form.Get("command_id"),
			},
		)
		if err != nil {
			status, message, known := eventThemeError(err)
			if !known {
				handlers.browser.frontendError(response, request, "activate Event Theme Revision", err)
				return
			}
			handlers.render(
				response, request, actor, eventID, csrfToken, revisionID, status, message,
			)
			return
		}
		http.Redirect( //nolint:gosec // The redirect path contains parsed non-negative integer IDs only.
			response,
			request,
			eventThemeAdministrationPath(eventID, revisionID),
			http.StatusSeeOther,
		)
	default:
		handlers.render(
			response, request, actor, eventID, csrfToken, 0,
			http.StatusBadRequest, "Check the Theme action and try again.",
		)
	}
}

func (handlers eventThemeHandlers) render(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	eventID int,
	csrfToken string,
	selectedRevisionID int,
	status int,
	message string,
) {
	active, err := handlers.themes.Active(request.Context(), eventID, "")
	if err != nil {
		handlers.browser.frontendError(response, request, "read active Event Theme", err)
		return
	}
	revisions, err := handlers.themes.List(request.Context(), actor, eventID)
	if errors.Is(err, eventthemes.ErrProducerRequired) {
		http.Error(response, "Producer authority required", http.StatusForbidden)
		return
	}
	if err != nil {
		handlers.browser.frontendError(response, request, "list Event Theme Revisions", err)
		return
	}
	preview, err := handlers.themes.Preview(
		request.Context(), actor, eventID, selectedRevisionID,
	)
	if errors.Is(err, eventthemes.ErrRevisionNotFound) {
		status, message = http.StatusNotFound, "Theme Revision not found."
		preview, err = handlers.themes.Preview(
			request.Context(), actor, eventID, active.Revision.ID,
		)
	}
	if err != nil {
		handlers.browser.frontendError(response, request, "preview Event Theme", err)
		return
	}
	createCommandID, err := planningCommandID(handlers.browser.random)
	if err != nil {
		handlers.browser.frontendError(response, request, "create Event Theme command identity", err)
		return
	}
	activateCommandID, err := planningCommandID(handlers.browser.random)
	if err != nil {
		handlers.browser.frontendError(response, request, "create Event Theme activation identity", err)
		return
	}
	summaries := make([]frontend.ThemeRevisionSummary, len(revisions))
	for index, revision := range revisions {
		summaries[index] = frontend.ThemeRevisionSummary{
			ID: revision.ID, CreatedAt: revision.CreatedAt,
			Active: revision.ID == active.Revision.ID,
		}
	}
	handlers.browser.render(
		response,
		request,
		status,
		frontend.EventThemeAdministration(frontend.EventThemePage{
			EventID: eventID, AccountName: actor.Name, CSRFToken: csrfToken,
			ReducedEffects:   reducedEffectsCookie(request),
			Navigation:       backstageNavigation(actor, request.URL.Path),
			ActiveRevisionID: active.Revision.ID, Revisions: summaries,
			RevisionID: preview.Revision.ID, Config: preview.Revision.Config,
			Resolved: preview.Resolved, Variants: preview.Variants,
			Findings:        preview.Findings,
			CreateCommandID: createCommandID, ActivateCommandID: activateCommandID,
			Form: request.Form, Errors: themeFormErrors(request.Form, message, true),
		}),
	)
}

func validateEventThemeForm(form url.Values) error {
	allowed := map[string]bool{
		"csrf_token": true, "action": true, "command_id": true,
		"revision_id": true, "expected_active_revision_id": true,
		"brand_asset": true, "background_color": true, "surface_color": true,
		"border_color": true, "text_color": true, "muted_color": true,
		"accent_color": true, "link_color": true, "focus_color": true,
		"live_color": true, "warning_color": true,
		"danger_color": true, "success_color": true,
		"background": true, "typeface": true, "transition": true,
		"effect": true, "motion": true,
	}
	for _, variant := range eventThemeVariantNames() {
		allowed[variant+"_accent_color"] = true
		allowed[variant+"_motion"] = true
	}
	for name, values := range form {
		if !allowed[name] || len(values) != 1 {
			return errInvalidThemeForm
		}
	}
	return nil
}

func eventThemeConfig(form url.Values) themevalue.EventConfig {
	config := themevalue.EventConfig{Overrides: themeConfig(form)}
	for _, variant := range eventThemeVariantNames() {
		overrides := themevalue.Config{
			AccentColor: form.Get(variant + "_accent_color"),
			Motion:      form.Get(variant + "_motion"),
		}
		if overrides.AccentColor != "" || overrides.Motion != "" {
			if config.Variants == nil {
				config.Variants = make(map[string]themevalue.Config)
			}
			config.Variants[variant] = overrides
		}
	}
	return config
}

func eventThemeVariantNames() []string {
	return []string{
		themevalue.VariantCompetitionOutput,
		themevalue.VariantEventOverview,
		themevalue.VariantLocationSignage,
		themevalue.VariantStandby,
	}
}

func eventThemeValidation(form url.Values) (string, *themevalue.ValidationError) {
	config := eventThemeConfig(form)
	if _, err := themevalue.ResolveEvent(themevalue.DefaultConfig(), config, ""); err != nil {
		var validation *themevalue.ValidationError
		if errors.As(err, &validation) {
			return validation.Field, validation
		}
	}
	for _, variant := range eventThemeVariantNames() {
		if _, configured := config.Variants[variant]; !configured {
			continue
		}
		if _, err := themevalue.ResolveEvent(
			themevalue.DefaultConfig(),
			config,
			variant,
		); err != nil {
			var validation *themevalue.ValidationError
			if errors.As(err, &validation) {
				return variant + "_" + validation.Field, validation
			}
		}
	}
	return "", nil
}

func eventThemeAdministrationPath(eventID, revisionID int) string {
	return "/backstage/events/" + strconv.Itoa(eventID) +
		"/theme?revision=" + strconv.Itoa(revisionID)
}

func eventThemeError(err error) (int, string, bool) {
	var validation *themevalue.ValidationError
	switch {
	case errors.As(err, &validation):
		return http.StatusBadRequest, "Check " + validation.Field + ": " + validation.Message + ".", true
	case errors.Is(err, eventthemes.ErrActivationBlocked):
		return http.StatusConflict, "Activation is blocked by inherited accessibility checks.", true
	case errors.Is(err, eventthemes.ErrStaleActiveRevision):
		return http.StatusConflict, "The active Event Theme changed. Review it and try again.", true
	case errors.Is(err, eventthemes.ErrRevisionNotFound):
		return http.StatusNotFound, "Theme Revision not found.", true
	case errors.Is(err, eventthemes.ErrProducerRequired):
		return http.StatusForbidden, "Producer authority required.", true
	case errors.Is(err, eventthemes.ErrCommandConflict):
		return http.StatusConflict, "Theme command identity conflict.", true
	default:
		return 0, "", false
	}
}
