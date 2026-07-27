package server

import (
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/dotwaffle/beamers/internal/activation"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/frontend"
)

type administrationHandlers struct {
	browser        frontendHandlers
	events         *events.Service
	activation     *activation.Service
	notifyDisplays func()
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
	notifyDisplays func(),
	logger *slog.Logger,
) {
	handlers := administrationHandlers{
		browser: frontendHandlers{
			authentication: authentication,
			logger:         logger,
			random:         rand.Reader,
		},
		events:         eventService,
		activation:     activationService,
		notifyDisplays: notifyDisplays,
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
		handlers.render(response, request, actor, csrfToken, http.StatusOK, "", nil)
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
		preflight *activation.Preflight
		err       error
	)
	switch action {
	case "create-account":
		err = handlers.createAccount(request, actor)
	case "grant":
		err = handlers.createGrant(request, actor)
	case "disable-account":
		err = handlers.disableAccount(request, actor)
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
		if preflight != nil {
			handlers.render(response, request, actor, csrfToken, http.StatusOK, "", preflight)
			return
		}
		http.Redirect(response, request, "/backstage/administration", http.StatusSeeOther)
		return
	}
	status, message, known := administrationError(err)
	if !known {
		handlers.browser.frontendError(
			response,
			request,
			"submit Backstage administration action "+action,
			err,
		)
		return
	}
	handlers.render(response, request, actor, csrfToken, status, message, preflight)
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
	laneIDs, err := positiveFormIDs(
		commaSeparatedValues(request.Form.Get("lane_ids")),
	)
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
	if err == nil {
		handlers.notifyDisplays()
	}
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
	message string,
	preflight *activation.Preflight,
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
	for _, account := range accounts {
		disableCommandIDs[account.ID], err = planningCommandID(handlers.browser.random)
		if err != nil {
			handlers.browser.frontendError(response, request, "create Disable Account command identity", err)
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
	handlers.browser.render(
		response,
		request,
		status,
		frontend.Administration(frontend.AdministrationPage{
			AccountName: actor.Name, CSRFToken: csrfToken,
			ReducedEffects:       reducedEffectsCookie(request),
			Navigation:           backstageNavigation(actor),
			Accounts:             accounts,
			Events:               foundEvents,
			Grants:               grants,
			AccountCommandID:     accountCommandID,
			GrantCommandID:       grantCommandID,
			DisableCommandIDs:    disableCommandIDs,
			SlugAliases:          slugAliases,
			PruneAliasCommandIDs: pruneAliasCommandIDs,
			AuditEntries:         auditEntries,
			ActiveEvent:          activeEvent,
			Preflight:            preflight,
			ActivationCommandID:  activationCommandID,
			Error:                message,
		}),
	)
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
