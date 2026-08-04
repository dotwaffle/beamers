package server

import (
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/eventthemes"
	"github.com/dotwaffle/beamers/internal/frontend"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/themes"
	"github.com/dotwaffle/beamers/internal/themevalue"
)

var errInvalidThemeForm = errors.New("invalid Theme form")

type themeHandlers struct {
	browser frontendHandlers
	themes  *themes.Service
	events  *eventthemes.Service
}

func registerThemeRoutes(
	mux *routeMux,
	authentication *auth.Service,
	service *themes.Service,
	eventService *eventthemes.Service,
	logger *slog.Logger,
) {
	handlers := themeHandlers{
		browser: frontendHandlers{
			authentication: authentication,
			logger:         logger,
			random:         rand.Reader,
		},
		themes: service,
		events: eventService,
	}
	mux.HandleFunc(frontend.InstallationThemePath, publicRoute(), handlers.stylesheet)
	mux.HandleFunc(
		"/assets/events/{eventID}/theme.css",
		publicRoute(),
		handlers.eventStylesheet,
	)
	route := backstagePageRoute()
	route.maxBodyBytes = maxAuthBodyBytes
	mux.HandleFunc("/backstage/themes", route, handlers.administration)
	mux.HandleFunc(
		"/backstage/events/{eventID}/theme",
		route,
		eventThemeHandlers{
			browser: handlers.browser, themes: eventService,
		}.administration,
	)
}

func (handlers themeHandlers) eventStylesheet(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !frontendReadAllowed(response, request) {
		return
	}
	eventID, err := positivePathID(request, "eventID")
	if err != nil {
		http.NotFound(response, request)
		return
	}
	active, err := handlers.events.Active(request.Context(), eventID, "")
	if errors.Is(err, store.ErrEventNotFound) {
		http.NotFound(response, request)
		return
	}
	if err != nil {
		handlers.browser.frontendError(response, request, "read active Event Theme", err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "text/css; charset=utf-8")
	if request.Method == http.MethodHead {
		return
	}
	stylesheet, err := themevalue.Stylesheet(active.Resolved)
	if err != nil {
		handlers.browser.frontendError(response, request, "render active Event Theme", err)
		return
	}
	_, _ = response.Write([]byte(stylesheet))
}

func (handlers themeHandlers) stylesheet(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !frontendReadAllowed(response, request) {
		return
	}
	active, err := handlers.themes.Active(request.Context())
	if err != nil {
		handlers.browser.frontendError(response, request, "read active Installation Theme", err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "text/css; charset=utf-8")
	if request.Method != http.MethodHead {
		stylesheet, renderErr := themevalue.Stylesheet(active.Config)
		if renderErr != nil {
			handlers.browser.frontendError(
				response,
				request,
				"render active Installation Theme",
				renderErr,
			)
			return
		}
		_, _ = response.Write([]byte(stylesheet))
	}
}

func (handlers themeHandlers) administration(
	response http.ResponseWriter,
	request *http.Request,
) {
	actor, ok := handlers.browser.browserAccount(response, request)
	if !ok {
		return
	}
	if !actor.Administrator {
		http.Error(response, "Administrator authority required", http.StatusForbidden)
		return
	}
	prologue, ok := handlers.browser.backstagePagePrologue(response, request, actor)
	if !ok {
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		selected, selectErr := nonnegativeThemeID(request.URL.Query().Get("revision"))
		if request.URL.Query().Get("revision") == "" {
			active, activeErr := handlers.themes.Active(actor.Context(request.Context()))
			if activeErr != nil {
				handlers.browser.frontendError(
					response,
					request,
					"read active Installation Theme",
					activeErr,
				)
				return
			}
			selected = active.ID
		} else if selectErr != nil {
			handlers.render(
				response,
				request,
				actor,
				prologue,
				0,
				http.StatusBadRequest,
				"Theme Revision must be a non-negative integer.",
			)
			return
		}
		handlers.render(response, request, actor, prologue, selected, http.StatusOK, "")
	case http.MethodPost:
		if !handlers.browser.validForm(response, request) {
			return
		}
		if err := validateThemeForm(request.Form); err != nil {
			handlers.render(
				response,
				request,
				actor,
				prologue,
				0,
				http.StatusBadRequest,
				"An undocumented Theme control was rejected.",
			)
			return
		}
		handlers.submit(response, request, actor, prologue)
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
	}
}

func (handlers themeHandlers) submit(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	prologue backstagePrologue,
) {
	switch request.Form.Get("action") {
	case "create-draft":
		created, err := handlers.themes.CreateDraft(
			request.Context(),
			actor,
			themes.CreateDraftInput{
				Config:    themeConfig(request.Form),
				CommandID: request.Form.Get("command_id"),
			},
		)
		if err != nil {
			status, message, known := themeError(err)
			if !known {
				handlers.browser.frontendError(response, request, "create Theme Revision", err)
				return
			}
			handlers.render(response, request, actor, prologue, 0, status, message)
			return
		}
		http.Redirect(
			response,
			request,
			"/backstage/themes?revision="+strconv.Itoa(created.ID),
			http.StatusSeeOther,
		)
	case "load-preset":
		config, ok := themevalue.PresetConfig(request.Form.Get("preset"))
		if !ok {
			handlers.render(
				response,
				request,
				actor,
				prologue,
				0,
				http.StatusBadRequest,
				"Select a bundled Theme Preset and try again.",
			)
			return
		}
		// Selection is an authoring convenience, not a separate presentation
		// mode: the values land in the Draft form, every one stays editable, and
		// nothing is stored until the Producer saves. See ADR 0057.
		applyThemeConfig(request.Form, config)
		handlers.render(response, request, actor, prologue, 0, http.StatusOK, "")
	case "activate":
		revisionID, revisionErr := nonnegativeThemeID(request.Form.Get("revision_id"))
		expectedID, expectedErr := nonnegativeThemeID(
			request.Form.Get("expected_active_revision_id"),
		)
		if revisionErr != nil || expectedErr != nil {
			handlers.render(
				response,
				request,
				actor,
				prologue,
				0,
				http.StatusBadRequest,
				"Check the Theme Revision details and try again.",
			)
			return
		}
		_, err := handlers.themes.Activate(
			request.Context(),
			actor,
			themes.ActivateInput{
				RevisionID: revisionID, ExpectedActiveRevisionID: expectedID,
				CommandID: request.Form.Get("command_id"),
			},
		)
		if err != nil {
			status, message, known := themeError(err)
			if !known {
				handlers.browser.frontendError(response, request, "activate Theme Revision", err)
				return
			}
			handlers.render(
				response,
				request,
				actor,
				prologue,
				revisionID,
				status,
				message,
			)
			return
		}
		http.Redirect(
			response,
			request,
			"/backstage/themes?revision="+strconv.Itoa(revisionID),
			http.StatusSeeOther,
		)
	default:
		handlers.render(
			response,
			request,
			actor,
			prologue,
			0,
			http.StatusBadRequest,
			"Check the Theme action and try again.",
		)
	}
}

func (handlers themeHandlers) render(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	prologue backstagePrologue,
	selectedRevisionID int,
	status int,
	message string,
) {
	active, err := handlers.themes.Active(actor.Context(request.Context()))
	if err != nil {
		handlers.browser.frontendError(response, request, "read active Installation Theme", err)
		return
	}
	revisions, err := handlers.themes.List(request.Context(), actor)
	if err != nil {
		handlers.browser.frontendError(response, request, "list Installation Theme Revisions", err)
		return
	}
	preview, err := handlers.themes.Preview(request.Context(), actor, selectedRevisionID)
	if errors.Is(err, themes.ErrRevisionNotFound) {
		status, message = http.StatusNotFound, "Theme Revision not found."
		preview, err = handlers.themes.Preview(request.Context(), actor, active.ID)
	}
	if err != nil {
		handlers.browser.frontendError(response, request, "preview Installation Theme", err)
		return
	}
	createCommandID, err := planningCommandID(handlers.browser.random)
	if err != nil {
		handlers.browser.frontendError(response, request, "create Theme command identity", err)
		return
	}
	activateCommandID, err := planningCommandID(handlers.browser.random)
	if err != nil {
		handlers.browser.frontendError(response, request, "create Theme activation identity", err)
		return
	}
	summaries := make([]frontend.ThemeRevisionSummary, len(revisions))
	for index, revision := range revisions {
		summaries[index] = frontend.ThemeRevisionSummary{
			ID: revision.ID, CreatedAt: revision.CreatedAt, Active: revision.ID == active.ID,
		}
	}
	handlers.browser.render(
		response,
		request,
		status,
		frontend.ThemeAdministration(frontend.ThemePage{
			AccountName: actor.Name, CSRFToken: prologue.CSRFToken,
			ReducedEffects:   prologue.ReducedEffects,
			Navigation:       prologue.Navigation,
			ActiveRevisionID: active.ID,
			Revisions:        summaries,
			Preview: frontend.ThemePreview{
				RevisionID: preview.Revision.ID,
				Config:     preview.Revision.Config,
				Findings:   preview.Findings,
			},
			CreateCommandID:   createCommandID,
			ActivateCommandID: activateCommandID,
			Form:              request.Form,
			Errors:            themeFormErrors(request.Form, message, false),
		}),
	)
}

func themeFormErrors(
	form url.Values,
	message string,
	event bool,
) frontend.FormErrors {
	if message == "" {
		return nil
	}
	if form.Get("action") == "activate" {
		fieldID := frontend.ThemeFieldID("activation_confirmation")
		if event {
			fieldID = frontend.EventThemeFieldID("activation_confirmation")
		}
		return frontend.FormErrors{{
			FieldID: fieldID,
			Label:   "Activation confirmation",
			Message: message,
		}}
	}
	var validation *themevalue.ValidationError
	field := ""
	if form.Get("action") == "create-draft" {
		if event {
			field, validation = eventThemeValidation(form)
		} else {
			err := themevalue.ValidateDraft(themeConfig(form))
			if errors.As(err, &validation) {
				field = validation.Field
			}
		}
	}
	if validation != nil {
		prefix := "theme-"
		if event {
			prefix = "event-theme-"
		}
		return frontend.FormErrors{{
			FieldID: prefix + strings.ReplaceAll(field, "_", "-"),
			Label:   strings.ReplaceAll(field, "_", " "),
			Message: validation.Message,
		}}
	}
	return frontend.FormErrors{{Message: message}}
}

// themeFormField binds one Draft form field to its Config field, so the
// allowed set, decode, and encode cannot drift apart.
type themeFormField struct {
	name   string
	access func(*themevalue.Config) *string
}

// themeFormFields is the complete Draft form vocabulary for a Config. Every
// Config field appears exactly once; TestThemeFormFieldsCoverConfig enforces
// coverage.
var themeFormFields = []themeFormField{
	{"brand_asset", func(config *themevalue.Config) *string { return &config.BrandAsset }},
	{"background_color", func(config *themevalue.Config) *string { return &config.BackgroundColor }},
	{"surface_color", func(config *themevalue.Config) *string { return &config.SurfaceColor }},
	{"border_color", func(config *themevalue.Config) *string { return &config.BorderColor }},
	{"text_color", func(config *themevalue.Config) *string { return &config.TextColor }},
	{"muted_color", func(config *themevalue.Config) *string { return &config.MutedColor }},
	{"accent_color", func(config *themevalue.Config) *string { return &config.AccentColor }},
	{"link_color", func(config *themevalue.Config) *string { return &config.LinkColor }},
	{"focus_color", func(config *themevalue.Config) *string { return &config.FocusColor }},
	{"live_color", func(config *themevalue.Config) *string { return &config.LiveColor }},
	{"warning_color", func(config *themevalue.Config) *string { return &config.WarningColor }},
	{"danger_color", func(config *themevalue.Config) *string { return &config.DangerColor }},
	{"success_color", func(config *themevalue.Config) *string { return &config.SuccessColor }},
	{"background", func(config *themevalue.Config) *string { return &config.Background }},
	{"typeface", func(config *themevalue.Config) *string { return &config.Typeface }},
	{"transition", func(config *themevalue.Config) *string { return &config.Transition }},
	{"effect", func(config *themevalue.Config) *string { return &config.Effect }},
	{"motion", func(config *themevalue.Config) *string { return &config.Motion }},
}

// themeControlFields carry command state rather than Config values.
var themeControlFields = []string{
	"csrf_token", "action", "command_id",
	"revision_id", "expected_active_revision_id", "preset",
}

// allowedThemeFields returns a fresh map on every call; callers may extend it
// with their own additional fields.
func allowedThemeFields() map[string]bool {
	allowed := make(map[string]bool, len(themeControlFields)+len(themeFormFields))
	for _, name := range themeControlFields {
		allowed[name] = true
	}
	for _, field := range themeFormFields {
		allowed[field.name] = true
	}
	return allowed
}

func validateAllowedForm(form url.Values, allowed map[string]bool) error {
	for name, values := range form {
		if !allowed[name] || len(values) != 1 {
			return errInvalidThemeForm
		}
	}
	return nil
}

func validateThemeForm(form url.Values) error {
	return validateAllowedForm(form, allowedThemeFields())
}

// applyThemeConfig writes a Config into the Draft form fields. It is the inverse
// of themeConfig, so a populated form round-trips to the same Config.
func applyThemeConfig(form url.Values, config themevalue.Config) {
	for _, field := range themeFormFields {
		form.Set(field.name, *field.access(&config))
	}
}

func themeConfig(form url.Values) themevalue.Config {
	var config themevalue.Config
	for _, field := range themeFormFields {
		*field.access(&config) = form.Get(field.name)
	}
	return config
}

func nonnegativeThemeID(value string) (int, error) {
	id, err := strconv.Atoi(value)
	if err != nil || id < 0 {
		return 0, errInvalidThemeForm
	}
	return id, nil
}

func themeError(err error) (int, string, bool) {
	var validation *themevalue.ValidationError
	switch {
	case errors.As(err, &validation):
		return http.StatusBadRequest, validation.Error(), true
	case errors.Is(err, themes.ErrActivationBlocked):
		return http.StatusUnprocessableEntity, "Theme activation is blocked.", true
	case errors.Is(err, themes.ErrStaleActiveRevision):
		return http.StatusConflict, "The active Theme changed; preview and try again.", true
	case errors.Is(err, themes.ErrRevisionNotFound):
		return http.StatusNotFound, "Theme Revision not found.", true
	case errors.Is(err, themes.ErrAdministratorRequired):
		return http.StatusForbidden, "Administrator authority required.", true
	case errors.Is(err, command.ErrInvalidID):
		return http.StatusUnprocessableEntity, "A valid command identity is required.", true
	case errors.Is(err, themes.ErrCommandConflict):
		return http.StatusConflict, "That Theme command identity was already used.", true
	default:
		return 0, "", false
	}
}
