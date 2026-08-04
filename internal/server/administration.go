package server

import (
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dotwaffle/beamers/internal/activation"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/frontend"
	"github.com/dotwaffle/beamers/internal/rundown"
)

type administrationHandlers struct {
	browser    frontendHandlers
	events     *events.Service
	activation *activation.Service
	rundown    *rundown.Queries
}

var (
	errInvalidAdministrationInput  = errors.New("invalid administration input")
	errUnknownAdministrationAction = errors.New("unknown administration action")
)

func registerAdministrationRoutes(
	mux *routeMux,
	authentication *auth.Service,
	eventService *events.Service,
	activationService *activation.Service,
	rundownQueries *rundown.Queries,
	logger *slog.Logger,
) {
	handlers := administrationHandlers{
		browser: frontendHandlers{
			authentication: authentication,
			logger:         logger,
			random:         rand.Reader,
		},
		events:     eventService,
		activation: activationService,
		rundown:    rundownQueries,
	}
	route := backstagePageRoute()
	route.maxBodyBytes = maxAuthBodyBytes
	mux.HandleFunc("/backstage/administration", route, handlers.administration)
}

func (handlers administrationHandlers) administration(
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
	csrfToken, err := handlers.browser.csrfToken(response, request)
	if err != nil {
		handlers.browser.frontendError(response, request, "create CSRF proof", err)
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		handlers.render(response, request, actor, csrfToken, http.StatusOK, nil, nil, nil)
	case http.MethodPost:
		if !handlers.browser.validForm(response, request) {
			return
		}
		handlers.submit(response, request, actor, csrfToken)
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
	}
}

func (handlers administrationHandlers) submit(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	csrfToken string,
) {
	action := request.Form.Get("action")
	var (
		preflight     *activation.Preflight
		recoveryToken *auth.RecoveryToken
		err           error
	)
	switch action {
	case "create-account":
		err = handlers.createAccount(request, actor)
	case "grant":
		err = handlers.createGrant(request, actor)
	case "disable-account":
		err = handlers.disableAccount(request, actor)
	case "issue-recovery-token":
		recoveryToken, err = handlers.issueRecoveryToken(request, actor)
	case "prune-event-slug-alias":
		err = handlers.pruneEventSlugAlias(request, actor)
	case "preflight":
		preflight, err = handlers.preflight(request, actor)
	case "activate":
		err = handlers.activate(request, actor)
	default:
		err = errUnknownAdministrationAction
	}
	if err == nil {
		if preflight != nil || recoveryToken != nil {
			handlers.render(
				response,
				request,
				actor,
				csrfToken,
				http.StatusOK,
				nil,
				preflight,
				recoveryToken,
			)
			return
		}
		http.Redirect(response, request, "/backstage/administration", http.StatusSeeOther)
		return
	}
	status, message, known := administrationError(err)
	if action == "issue-recovery-token" && errors.Is(err, auth.ErrLastAdministrator) {
		status, message, known = http.StatusConflict,
			"The last Administrator requires host recovery.",
			true
	}
	if !known {
		handlers.browser.frontendError(
			response,
			request,
			"submit Backstage administration action "+action,
			err,
		)
		return
	}
	if action == "activate" {
		current, preflightErr := handlers.preflight(request, actor)
		if preflightErr != nil {
			handlers.browser.frontendError(
				response,
				request,
				"refresh Activation Preflight",
				preflightErr,
			)
			return
		}
		preflight = current
	}
	handlers.render(
		response,
		request,
		actor,
		csrfToken,
		status,
		administrationFormErrors(err, request.Form, message),
		preflight,
		nil,
	)
}

func (handlers administrationHandlers) pruneEventSlugAlias(
	request *http.Request,
	actor auth.Account,
) error {
	aliasID, err := positiveAdministrationFormID(request, "alias_id")
	if err != nil {
		return err
	}
	_, err = handlers.events.PruneEventSlugAlias(
		request.Context(),
		actor,
		aliasID,
		request.Form.Get("confirmed") == "true",
		request.Form.Get("command_id"),
	)
	return err
}

func (handlers administrationHandlers) createAccount(
	request *http.Request,
	actor auth.Account,
) error {
	_, err := handlers.browser.authentication.CreateAccountWithDisplayName(
		request.Context(),
		actor,
		request.Form.Get("account_handle"),
		request.Form.Get("display_name"),
		request.Form.Get("password"),
		request.Form.Get("command_id"),
	)
	return err
}

func (handlers administrationHandlers) createGrant(
	request *http.Request,
	actor auth.Account,
) error {
	accountID, err := positiveAdministrationFormID(request, "account_id")
	if err != nil {
		return err
	}
	eventID, err := positiveAdministrationFormID(request, "event_id")
	if err != nil {
		return err
	}
	laneIDs, err := positiveFormIDs(request.Form["lane_ids"])
	if err != nil {
		return errInvalidAdministrationInput
	}
	_, err = handlers.events.GrantScopedEventAccess(
		request.Context(),
		actor,
		eventID,
		events.GrantInput{
			AccountID:        accountID,
			Role:             request.Form.Get("role"),
			LaneIDs:          laneIDs,
			DisplayGroupKeys: commaSeparatedValues(request.Form.Get("display_group_keys")),
			Capabilities:     request.Form["capability"],
			CommandID:        request.Form.Get("command_id"),
		},
	)
	return err
}

func (handlers administrationHandlers) disableAccount(
	request *http.Request,
	actor auth.Account,
) error {
	accountID, err := positiveAdministrationFormID(request, "account_id")
	if err != nil {
		return err
	}
	return handlers.browser.authentication.DisableAccount(
		request.Context(),
		actor,
		accountID,
		request.Form.Get("command_id"),
		request.Form.Get("reason"),
	)
}

func (handlers administrationHandlers) issueRecoveryToken(
	request *http.Request,
	actor auth.Account,
) (*auth.RecoveryToken, error) {
	accountID, err := positiveAdministrationFormID(request, "account_id")
	if err != nil {
		return nil, err
	}
	token, err := handlers.browser.authentication.IssueRecoveryToken(
		request.Context(),
		actor,
		accountID,
		request.Form.Get("reason"),
		request.Form.Get("command_id"),
	)
	if err != nil {
		return nil, err
	}
	return &token, nil
}

func (handlers administrationHandlers) preflight(
	request *http.Request,
	actor auth.Account,
) (*activation.Preflight, error) {
	eventID, err := positiveAdministrationFormID(request, "event_id")
	if err != nil {
		return nil, err
	}
	found, err := handlers.activation.Preflight(request.Context(), actor, eventID)
	if err != nil {
		return nil, err
	}
	return &found, nil
}

func (handlers administrationHandlers) activate(
	request *http.Request,
	actor auth.Account,
) error {
	eventID, err := positiveAdministrationFormID(request, "event_id")
	if err != nil {
		return err
	}
	eventRevision, err := positiveAdministrationFormID(request, "event_revision")
	if err != nil {
		return err
	}
	publishedRevision, err := positiveAdministrationFormID(request, "published_revision")
	if err != nil {
		return err
	}
	activationGeneration, err := strconv.Atoi(request.Form.Get("activation_generation"))
	if err != nil || activationGeneration < 0 {
		return errInvalidAdministrationInput
	}
	_, err = handlers.activation.Activate(request.Context(), actor, activation.ActivateInput{
		EventID:   eventID,
		CommandID: request.Form.Get("command_id"),
		Confirmation: activation.Confirmation{
			EventRevision:        eventRevision,
			PublishedRevision:    publishedRevision,
			ActivationGeneration: activationGeneration,
			Fingerprint:          request.Form.Get("fingerprint"),
		},
	})
	return err
}

func positiveAdministrationFormID(request *http.Request, name string) (int, error) {
	id, err := strconv.Atoi(request.Form.Get(name))
	if err != nil || id <= 0 {
		return 0, errInvalidAdministrationInput
	}
	return id, nil
}

func (handlers administrationHandlers) render(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	csrfToken string,
	status int,
	formErrors frontend.FormErrors,
	preflight *activation.Preflight,
	recoveryToken *auth.RecoveryToken,
) {
	accounts, err := handlers.browser.authentication.ListAccounts(request.Context(), actor)
	if err != nil {
		handlers.browser.frontendError(response, request, "list Accounts", err)
		return
	}
	foundEvents, err := handlers.events.List(request.Context(), actor)
	if err != nil {
		handlers.browser.frontendError(response, request, "list Events", err)
		return
	}
	grants, err := handlers.events.ListGrants(request.Context(), actor)
	if err != nil {
		handlers.browser.frontendError(response, request, "list Event Grants", err)
		return
	}
	slugAliases, err := handlers.events.ListEventSlugAliases(request.Context(), actor)
	if err != nil {
		handlers.browser.frontendError(response, request, "list Event Slug Aliases", err)
		return
	}
	auditEntries, err := handlers.browser.authentication.ListAuditEntries(
		request.Context(),
		actor,
	)
	if err != nil {
		handlers.browser.frontendError(response, request, "list Audit Entries", err)
		return
	}
	activeEvent, err := handlers.activation.ActiveEvent(request.Context(), actor)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Active Event", err)
		return
	}
	accountCommandID, err := planningCommandID(handlers.browser.random)
	if err != nil {
		handlers.browser.frontendError(response, request, "create Account command identity", err)
		return
	}
	grantCommandID, err := planningCommandID(handlers.browser.random)
	if err != nil {
		handlers.browser.frontendError(response, request, "create Event Grant command identity", err)
		return
	}
	disableCommandIDs := make(map[int]string, len(accounts))
	recoveryCommandIDs := make(map[int]string, len(accounts))
	for _, account := range accounts {
		disableCommandIDs[account.ID], err = planningCommandID(handlers.browser.random)
		if err != nil {
			handlers.browser.frontendError(response, request, "create Disable Account command identity", err)
			return
		}
		recoveryCommandIDs[account.ID], err = planningCommandID(handlers.browser.random)
		if err != nil {
			handlers.browser.frontendError(
				response,
				request,
				"create Account recovery command identity",
				err,
			)
			return
		}
	}
	pruneAliasCommandIDs := make(map[int]string, len(slugAliases))
	for _, alias := range slugAliases {
		pruneAliasCommandIDs[alias.ID], err = planningCommandID(handlers.browser.random)
		if err != nil {
			handlers.browser.frontendError(
				response,
				request,
				"create Event Slug Alias command identity",
				err,
			)
			return
		}
	}
	activationCommandID, err := planningCommandID(handlers.browser.random)
	if err != nil {
		handlers.browser.frontendError(response, request, "create Activate Event command identity", err)
		return
	}
	eventLanes := make(map[int][]rundown.CrewLane, len(foundEvents))
	for _, event := range foundEvents {
		lanes, laneErr := handlers.rundown.AdministrationLanes(request.Context(), actor, event.ID)
		if laneErr != nil {
			handlers.browser.frontendError(response, request, "list Event Lanes", laneErr)
			return
		}
		eventLanes[event.ID] = lanes
	}
	handlers.browser.render(
		response,
		request,
		status,
		frontend.Administration(frontend.AdministrationPage{
			AccountName: actor.Name, CSRFToken: csrfToken,
			ReducedEffects:       reducedEffectsCookie(request),
			Navigation:           backstageNavigation(actor, request.URL.Path),
			Accounts:             accounts,
			Events:               foundEvents,
			EventLanes:           eventLanes,
			Grants:               grants,
			AccountCommandID:     accountCommandID,
			GrantCommandID:       grantCommandID,
			DisableCommandIDs:    disableCommandIDs,
			RecoveryCommandIDs:   recoveryCommandIDs,
			RecoveryToken:        recoveryToken,
			SlugAliases:          slugAliases,
			PruneAliasCommandIDs: pruneAliasCommandIDs,
			AuditEntries:         auditEntries,
			ActiveEvent:          activeEvent,
			Preflight:            preflight,
			ActivationCommandID:  activationCommandID,
			SubmittedAction:      administrationSubmittedAction(request, formErrors, preflight),
			Form:                 administrationSubmittedForm(request, formErrors, preflight),
			Errors:               formErrors,
		}),
	)
}

func administrationSubmittedAction(
	request *http.Request,
	formErrors frontend.FormErrors,
	preflight *activation.Preflight,
) string {
	if len(formErrors) > 0 {
		return request.Form.Get("action")
	}
	if preflight != nil {
		return "preflight"
	}
	return ""
}

func administrationSubmittedForm(
	request *http.Request,
	formErrors frontend.FormErrors,
	preflight *activation.Preflight,
) url.Values {
	if len(formErrors) > 0 || preflight != nil {
		return request.Form
	}
	return nil
}

func commaSeparatedValues(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	values := strings.Split(value, ",")
	for index := range values {
		values[index] = strings.TrimSpace(values[index])
	}
	return values
}

func administrationFormErrors(
	err error,
	values url.Values,
	message string,
) frontend.FormErrors {
	action := values.Get("action")
	field, label, fieldMessage := "", "", message
	var validation *events.ValidationError
	switch {
	case errors.Is(err, auth.ErrInvalidAccountDetails):
		return administrationAccountFormErrors(values)
	case errors.As(err, &validation):
		field = validation.Field
		label, fieldMessage = administrationFieldLabel(field), validation.Message
	case errors.Is(err, auth.ErrAccountExists):
		field, label = "account_handle", "Account Handle"
	case errors.Is(err, events.ErrGrantRoleRequired):
		field, label = "role", "Role"
	case errors.Is(err, events.ErrEventGrantLaneMismatch):
		field, label, fieldMessage = "lane_ids", "Lane IDs",
			"Checked Lanes must belong to the selected Event."
	case errors.Is(err, auth.ErrDisableReasonRequired):
		field, label = "reason", "Reason"
	case errors.Is(err, auth.ErrRecoveryReasonRequired):
		field, label = "reason", "Offline verification"
	case errors.Is(err, events.ErrEventSlugPruneConfirmationRequired):
		field, label = "confirmed", "Pruning confirmation"
	case errors.Is(err, errInvalidAdministrationInput):
		field, label = administrationInvalidField(values)
	}
	if field == "" && action == "activate" {
		field, label = "confirmation", "Activation confirmation"
	}
	if field == "" {
		return frontend.FormErrors{{Message: message}}
	}
	targetID := administrationTargetID(action, values)
	return frontend.FormErrors{{
		FieldID: frontend.AdministrationFieldID(action, targetID, field),
		Label:   label,
		Message: fieldMessage,
	}}
}

func administrationAccountFormErrors(values url.Values) frontend.FormErrors {
	var formErrors frontend.FormErrors
	for _, field := range []struct {
		name, label, message string
		valid                bool
	}{
		{
			name: "account_handle", label: "Account Handle",
			message: "Enter an Account Handle of 1 to 200 characters.",
			valid:   validAdministrationText(values.Get("account_handle")),
		},
		{
			name: "display_name", label: "Display Name",
			message: "Enter a Display Name of 1 to 200 characters.",
			valid:   validAdministrationText(values.Get("display_name")),
		},
		{
			name: "password", label: "Password",
			message: "Enter a password of 12 to 1024 characters.",
			valid: func() bool {
				password := values.Get("password")
				length := utf8.RuneCountInString(password)
				return utf8.ValidString(password) && length >= 12 && length <= 1024
			}(),
		},
	} {
		if !field.valid {
			formErrors = append(formErrors, frontend.FormError{
				FieldID: frontend.AdministrationFieldID(
					"create-account", 0, field.name,
				),
				Label: field.label, Message: field.message,
			})
		}
	}
	if len(formErrors) == 0 {
		return frontend.FormErrors{{Message: "Check the Account details and try again."}}
	}
	return formErrors
}

func validAdministrationText(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" &&
		utf8.ValidString(value) &&
		utf8.RuneCountInString(value) <= 200 &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func administrationInvalidField(values url.Values) (string, string) {
	switch values.Get("action") {
	case "grant":
		if id, _ := strconv.Atoi(values.Get("account_id")); id <= 0 {
			return "account_id", "Account"
		}
		if id, _ := strconv.Atoi(values.Get("event_id")); id <= 0 {
			return "event_id", "Event"
		}
		return "lane_ids", "Lane IDs"
	case "preflight":
		return "event_id", "Event"
	default:
		return "", ""
	}
}

func administrationTargetID(action string, values url.Values) int {
	field := ""
	switch action {
	case "disable-account", "issue-recovery-token":
		field = "account_id"
	case "activate":
		field = "event_id"
	case "prune-event-slug-alias":
		field = "alias_id"
	}
	id, _ := strconv.Atoi(values.Get(field))
	return id
}

func administrationFieldLabel(field string) string {
	switch field {
	case "account_id":
		return "Account"
	case "event_id":
		return "Event"
	case "lane_ids":
		return "Lane IDs"
	case "display_group_keys":
		return "Display group keys"
	case "role":
		return "Role"
	default:
		return strings.ReplaceAll(field, "_", " ")
	}
}

func administrationError(err error) (int, string, bool) {
	var validation *events.ValidationError
	switch {
	case errors.As(err, &validation):
		return http.StatusUnprocessableEntity, validation.Error(), true
	case errors.Is(err, errInvalidAdministrationInput),
		errors.Is(err, errUnknownAdministrationAction):
		return http.StatusBadRequest, "Check the administration details and try again.", true
	case errors.Is(err, auth.ErrInvalidAccountDetails):
		return http.StatusUnprocessableEntity, "Check the Account details and try again.", true
	case errors.Is(err, command.ErrInvalidID):
		return http.StatusUnprocessableEntity, "A valid command identity is required.", true
	case errors.Is(err, events.ErrEventSlugAliasNotFound):
		return http.StatusNotFound, "Event Slug Alias not found.", true
	case errors.Is(err, events.ErrEventSlugPruneConfirmationRequired):
		return http.StatusUnprocessableEntity, "Confirm the Event Slug Alias pruning warning.", true
	case errors.Is(err, auth.ErrAccountExists):
		return http.StatusConflict, "That Account Handle is already in use.", true
	case errors.Is(err, auth.ErrAdministratorRequired),
		errors.Is(err, events.ErrAdministratorRequired),
		errors.Is(err, activation.ErrAdministratorRequired):
		return http.StatusForbidden, "Administrator authority required.", true
	case errors.Is(err, auth.ErrDisableReasonRequired), errors.Is(err, auth.ErrDisableAccountNotFound):
		return http.StatusUnprocessableEntity, "Check the Disable Account details and try again.", true
	case errors.Is(err, auth.ErrRecoveryReasonRequired),
		errors.Is(err, auth.ErrRecoveryAccountNotFound):
		return http.StatusUnprocessableEntity, "Check the Account recovery details and try again.", true
	case errors.Is(err, auth.ErrRecoveryTokenAlreadyIssued):
		return http.StatusConflict, "That Recovery Token was already issued and cannot be shown again.", true
	case errors.Is(err, auth.ErrLastAdministrator):
		return http.StatusConflict, "The last Administrator cannot be disabled.", true
	case errors.Is(err, auth.ErrAuthenticationBusy):
		return http.StatusServiceUnavailable, "Account creation is busy; try again.", true
	case errors.Is(err, auth.ErrStorageDegraded):
		return http.StatusServiceUnavailable, "Administration is temporarily unavailable.", true
	case errors.Is(err, events.ErrEventGrantExists):
		return http.StatusConflict, "That Account already has an Event Grant.", true
	case errors.Is(err, events.ErrGrantRoleRequired):
		return http.StatusUnprocessableEntity, "Choose a valid Event role.", true
	case errors.Is(err, events.ErrAccountNotFound), errors.Is(err, events.ErrEventNotFound):
		return http.StatusNotFound, "The Account or Event was not found.", true
	case errors.Is(err, events.ErrEventGrantLaneMismatch):
		return http.StatusUnprocessableEntity, "Checked Lanes must belong to the selected Event.", true
	case errors.Is(err, activation.ErrPreflightBlocked):
		return http.StatusConflict, "Activation Preflight is blocked.", true
	case errors.Is(err, activation.ErrStalePreflight):
		return http.StatusConflict, "Activation Preflight is stale; run it again.", true
	case errors.Is(err, auth.ErrCommandConflict),
		errors.Is(err, events.ErrCommandConflict),
		errors.Is(err, activation.ErrCommandConflict):
		return http.StatusConflict, "The command was already used for different work.", true
	default:
		return 0, "", false
	}
}
