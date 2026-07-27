package server

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/frontend"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/viewer"
)

const (
	csrfCookieName           = "beamers_csrf"
	reducedEffectsCookieName = "beamers_reduced_effects"
	csrfTokenBytes           = 32
	reducedEffectsMaxAge     = 365 * 24 * 60 * 60
)

type frontendHandlers struct {
	authentication *auth.Service
	logger         *slog.Logger
	limiter        *authFailureLimiter
	random         io.Reader
	rundown        *rundown.Queries
	events         *events.Service
}

func registerFrontendRoutes(
	mux *routeMux,
	authentication *auth.Service,
	limiter *authFailureLimiter,
	rundownQueries *rundown.Queries,
	eventService *events.Service,
	logger *slog.Logger,
) error {
	handlers := frontendHandlers{
		authentication: authentication,
		logger:         logger,
		limiter:        limiter,
		random:         rand.Reader,
		rundown:        rundownQueries,
		events:         eventService,
	}
	formRoute := browserPageRoute()
	formRoute.maxBodyBytes = maxAuthBodyBytes
	mux.HandleFunc("/", browserPageRoute(), handlers.root)
	mux.HandleFunc("/setup", formRoute, handlers.setup)
	mux.HandleFunc("/register", formRoute, handlers.register)
	mux.HandleFunc("/sign-in", formRoute, handlers.signIn)
	mux.HandleFunc("/recover", formRoute, handlers.recover)
	mux.HandleFunc("/sign-out", formRoute, handlers.signOut)
	mux.HandleFunc("/effects", formRoute, handlers.effects)
	mux.HandleFunc("/profile", formRoute, handlers.profile)
	mux.HandleFunc("/people/{handle}", browserPageRoute(), handlers.publicProfile)
	mux.HandleFunc("/events/{slug}", browserPageRoute(), handlers.publicEvent)
	mux.HandleFunc("/backstage", backstagePageRoute(), handlers.backstage)
	backstageFormRoute := backstagePageRoute()
	backstageFormRoute.maxBodyBytes = maxAuthBodyBytes
	mux.HandleFunc("/admin/registration", backstageFormRoute, handlers.registrationPolicy)
	for _, path := range []string{
		frontend.StylesheetPath,
		frontend.ChakraRegularPath,
		frontend.ChakraBoldPath,
		frontend.OpenSansPath,
		frontend.HTMXPath,
		frontend.SSEPath,
		frontend.EventTimePath,
		frontend.WebAuthnPath,
	} {
		handler, err := handlers.asset(path)
		if err != nil {
			return err
		}
		mux.HandleFunc(path, publicRoute(), handler)
	}
	return nil
}

func (handlers frontendHandlers) register(response http.ResponseWriter, request *http.Request) {
	setupRequired, err := handlers.authentication.SetupRequired(request.Context())
	if err != nil {
		handlers.frontendError(response, request, "read setup state", err)
		return
	}
	if setupRequired {
		http.Redirect(response, request, "/setup", http.StatusSeeOther)
		return
	}
	open, err := handlers.authentication.RegistrationOpen(request.Context())
	if err != nil {
		handlers.frontendError(response, request, "read Registration Policy", err)
		return
	}
	csrfToken, err := handlers.csrfToken(response, request)
	if err != nil {
		handlers.frontendError(response, request, "create CSRF proof", err)
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		handlers.render(
			response,
			request,
			http.StatusOK,
			frontend.Register(csrfToken, "", "", "", open, reducedEffectsCookie(request)),
		)
		return
	case http.MethodPost:
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
		return
	}
	if !handlers.validForm(response, request) {
		return
	}
	handle := request.Form.Get("handle")
	displayName := request.Form.Get("display_name")
	clientKey, accountKey := registrationFailureKeys(request, handle)
	if retryAfter, blocked := handlers.limiter.reserve(clientKey, accountKey); blocked {
		writeAuthRateLimit(response, retryAfter)
		return
	}
	_, err = handlers.authentication.Register(
		request.Context(),
		handle,
		displayName,
		request.Form.Get("password"),
	)
	status := 0
	message := ""
	renderOpen := open
	switch {
	case errors.Is(err, auth.ErrRegistrationClosed):
		status, message, renderOpen = http.StatusForbidden, "Registration is closed.", false
	case errors.Is(err, auth.ErrAccountExists):
		status, message = http.StatusConflict, "That Account Handle is already in use."
	case errors.Is(err, auth.ErrInvalidAccountDetails):
		status, message = http.StatusBadRequest, "Check the Account details and try again."
	case errors.Is(err, auth.ErrAuthenticationBusy):
		handlers.limiter.release(clientKey, accountKey)
		writeAuthRateLimit(response, time.Second)
	case err != nil:
		handlers.limiter.release(clientKey, accountKey)
		handlers.frontendError(response, request, "register Account", err)
	default:
		http.Redirect(response, request, "/sign-in", http.StatusSeeOther)
	}
	if message != "" {
		handlers.render(
			response, request, status,
			frontend.Register(
				csrfToken, message, handle, displayName, renderOpen,
				reducedEffectsCookie(request),
			),
		)
	}
}

func (handlers frontendHandlers) profile(response http.ResponseWriter, request *http.Request) {
	actor, ok := handlers.browserAccount(response, request)
	if !ok {
		return
	}
	csrfToken, err := handlers.csrfToken(response, request)
	if err != nil {
		handlers.frontendError(response, request, "create CSRF proof", err)
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
	case http.MethodPost:
		if !handlers.validForm(response, request) {
			return
		}
		switch request.Form.Get("action") {
		case "replace-recovery-codes":
			codes, replaceErr := handlers.authentication.ReplaceRecoveryCodes(
				request.Context(),
				actor,
				request.Form.Get("command_id"),
			)
			switch {
			case errors.Is(replaceErr, auth.ErrRecoveryCodesAlreadyReplaced):
				http.Error(
					response,
					"Recovery Codes were already replaced and cannot be shown again.",
					http.StatusConflict,
				)
				return
			case errors.Is(replaceErr, auth.ErrInvalidAccountDetails):
				http.Error(response, "invalid command identity", http.StatusBadRequest)
				return
			case replaceErr != nil:
				handlers.frontendError(response, request, "replace Recovery Codes", replaceErr)
				return
			}
			handlers.renderProfile(response, request, actor, csrfToken, codes)
			return
		case "remove-password":
			err = handlers.authentication.RemovePassword(
				request.Context(),
				actor,
				request.Form.Get("command_id"),
			)
			switch {
			case errors.Is(err, auth.ErrFinalCredential):
				http.Error(response, "final active Credential cannot be removed", http.StatusConflict)
			case errors.Is(err, auth.ErrCommandConflict):
				http.Error(response, "Credential command conflict", http.StatusConflict)
			case errors.Is(err, auth.ErrInvalidAccountDetails):
				http.Error(response, "invalid Credential command", http.StatusBadRequest)
			case err != nil:
				handlers.frontendError(response, request, "remove password Credential", err)
			default:
				http.Redirect(response, request, "/profile", http.StatusSeeOther)
			}
			return
		case "revoke-webauthn":
			credentialID, parseErr := strconv.Atoi(request.Form.Get("credential_id"))
			if parseErr != nil || credentialID <= 0 {
				http.Error(response, "invalid WebAuthn Credential", http.StatusBadRequest)
				return
			}
			err = handlers.authentication.RevokeWebAuthnCredential(
				request.Context(),
				actor,
				credentialID,
				request.Form.Get("command_id"),
			)
			switch {
			case errors.Is(err, auth.ErrFinalCredential):
				http.Error(response, "final active Credential cannot be removed", http.StatusConflict)
			case errors.Is(err, auth.ErrInvalidSession):
				http.Error(response, "WebAuthn Credential not found", http.StatusNotFound)
			case errors.Is(err, auth.ErrCommandConflict):
				http.Error(response, "Credential command conflict", http.StatusConflict)
			case errors.Is(err, auth.ErrInvalidAccountDetails):
				http.Error(response, "invalid Credential command", http.StatusBadRequest)
			case err != nil:
				handlers.frontendError(response, request, "revoke WebAuthn Credential", err)
			default:
				http.Redirect(response, request, "/profile", http.StatusSeeOther)
			}
			return
		}
		entryIDs, parseErr := positiveFormIDs(request.Form["entry_id"])
		if parseErr != nil {
			http.Error(response, "invalid Profile Entry", http.StatusBadRequest)
			return
		}
		err = handlers.authentication.UpdateProfile(
			request.Context(),
			actor,
			request.Form.Get("display_name"),
			request.Form.Get("published") == "true",
			entryIDs,
		)
		switch {
		case errors.Is(err, auth.ErrInvalidAccountDetails),
			errors.Is(err, auth.ErrProfileEntryUnavailable):
			http.Error(response, "invalid Profile", http.StatusBadRequest)
		case err != nil:
			handlers.frontendError(response, request, "update Profile", err)
		default:
			http.Redirect(response, request, "/profile", http.StatusSeeOther)
		}
		return
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
		return
	}
	handlers.renderProfile(response, request, actor, csrfToken, nil)
}

func (handlers frontendHandlers) renderProfile(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	csrfToken string,
	recoveryCodes []string,
) {
	found, err := handlers.authentication.Profile(request.Context(), actor)
	if err != nil {
		handlers.frontendError(response, request, "read Profile", err)
		return
	}
	recoveryCommandID, err := planningCommandID(handlers.random)
	if err != nil {
		handlers.frontendError(response, request, "create Recovery Code command identity", err)
		return
	}
	credentialCommandID, err := planningCommandID(handlers.random)
	if err != nil {
		handlers.frontendError(response, request, "create Credential command identity", err)
		return
	}
	passwordActive, err := handlers.authentication.PasswordActive(request.Context(), actor)
	if err != nil {
		handlers.frontendError(response, request, "read password Credential", err)
		return
	}
	credentials, err := handlers.authentication.WebAuthnCredentials(request.Context(), actor)
	if err != nil {
		handlers.frontendError(response, request, "read WebAuthn Credentials", err)
		return
	}
	canRemovePassword := false
	for _, credential := range credentials {
		canRemovePassword = canRemovePassword || !credential.Revoked
	}
	handlers.render(
		response,
		request,
		http.StatusOK,
		frontend.ProfilePage(
			csrfToken,
			found,
			passwordActive,
			canRemovePassword,
			credentials,
			recoveryCodes,
			recoveryCommandID,
			credentialCommandID,
			reducedEffectsCookie(request),
			backstageAccessible(request) &&
				backstageAvailable(backstageNavigation(actor)),
		),
	)
}

func (handlers frontendHandlers) recover(response http.ResponseWriter, request *http.Request) {
	csrfToken, err := handlers.csrfToken(response, request)
	if err != nil {
		handlers.frontendError(response, request, "create CSRF proof", err)
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		commandID, commandErr := planningCommandID(handlers.random)
		if commandErr != nil {
			handlers.frontendError(response, request, "create Account recovery command identity", commandErr)
			return
		}
		handlers.render(
			response,
			request,
			http.StatusOK,
			frontend.Recover(csrfToken, "", "", commandID, reducedEffectsCookie(request)),
		)
		return
	case http.MethodPost:
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
		return
	}
	if !handlers.validForm(response, request) {
		return
	}
	handle := request.Form.Get("handle")
	clientKey, accountKey := recoveryFailureKeys(request, handle)
	if retryAfter, blocked := handlers.limiter.reserve(clientKey, accountKey); blocked {
		writeAuthRateLimit(response, retryAfter)
		return
	}
	session, err := handlers.authentication.Recover(
		request.Context(),
		handle,
		request.Form.Get("credential"),
		request.Form.Get("password"),
		request.Form.Get("command_id"),
	)
	switch {
	case errors.Is(err, auth.ErrAuthenticationBusy):
		handlers.limiter.release(clientKey, accountKey)
		writeAuthRateLimit(response, time.Second)
	case errors.Is(err, auth.ErrAuthenticationFailed):
		handlers.render(
			response,
			request,
			http.StatusUnauthorized,
			frontend.Recover(
				csrfToken,
				"Recovery failed.",
				handle,
				request.Form.Get("command_id"),
				reducedEffectsCookie(request),
			),
		)
	case errors.Is(err, auth.ErrInvalidAccountDetails):
		handlers.limiter.release(clientKey, accountKey)
		handlers.render(
			response,
			request,
			http.StatusUnprocessableEntity,
			frontend.Recover(
				csrfToken,
				"Choose a password of at least 12 characters.",
				handle,
				request.Form.Get("command_id"),
				reducedEffectsCookie(request),
			),
		)
	case errors.Is(err, auth.ErrRecoveryAlreadyCompleted):
		handlers.limiter.release(clientKey, accountKey)
		http.Error(
			response,
			"Account recovery already completed; sign in with the new password.",
			http.StatusConflict,
		)
	case errors.Is(err, auth.ErrCommandConflict):
		handlers.limiter.release(clientKey, accountKey)
		http.Error(response, "command identity conflict", http.StatusConflict)
	case err != nil:
		handlers.limiter.release(clientKey, accountKey)
		handlers.frontendError(response, request, "recover Account", err)
	default:
		handlers.limiter.release(clientKey, accountKey)
		handlers.limiter.reset(accountKey)
		setSessionCookie(response, request, session)
		http.Redirect(response, request, "/", http.StatusSeeOther)
	}
}

func (handlers frontendHandlers) publicProfile(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !frontendReadAllowed(response, request) {
		return
	}
	found, ok, err := handlers.authentication.PublicProfile(
		request.Context(),
		request.PathValue("handle"),
	)
	if err != nil {
		handlers.frontendError(response, request, "read Public Profile", err)
		return
	}
	if !ok {
		http.NotFound(response, request)
		return
	}
	handlers.render(response, request, http.StatusOK, frontend.PublicProfile(found))
}

func (handlers frontendHandlers) registrationPolicy(
	response http.ResponseWriter,
	request *http.Request,
) {
	actor, ok := handlers.browserAccount(response, request)
	if !ok {
		return
	}
	if !actor.Administrator {
		http.Error(response, "Administrator authority required", http.StatusForbidden)
		return
	}
	csrfToken, err := handlers.csrfToken(response, request)
	if err != nil {
		handlers.frontendError(response, request, "create CSRF proof", err)
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
	case http.MethodPost:
		if !handlers.validForm(response, request) {
			return
		}
		err = handlers.authentication.SetRegistrationOpen(
			request.Context(),
			actor,
			request.Form.Get("registration_open") == "true",
		)
		if err != nil {
			handlers.frontendError(response, request, "set Registration Policy", err)
			return
		}
		http.Redirect(response, request, "/admin/registration", http.StatusSeeOther)
		return
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
		return
	}
	open, err := handlers.authentication.RegistrationOpen(request.Context())
	if err != nil {
		handlers.frontendError(response, request, "read Registration Policy", err)
		return
	}
	handlers.render(
		response,
		request,
		http.StatusOK,
		frontend.RegistrationPolicy(
			csrfToken,
			open,
			reducedEffectsCookie(request),
			actor.Name,
			backstageNavigation(actor),
		),
	)
}

func (handlers frontendHandlers) backstage(response http.ResponseWriter, request *http.Request) {
	if !frontendReadAllowed(response, request) {
		return
	}
	actor, ok := handlers.browserAccount(response, request)
	if !ok {
		return
	}
	navigation := backstageNavigation(actor)
	if !backstageAvailable(navigation) {
		http.NotFound(response, request)
		return
	}
	navigation, err := handlers.backstageCompetitionNavigation(request, actor, navigation)
	if err != nil {
		handlers.frontendError(response, request, "read Competition navigation", err)
		return
	}
	csrfToken, err := handlers.csrfToken(response, request)
	if err != nil {
		handlers.frontendError(response, request, "create CSRF proof", err)
		return
	}
	handlers.render(
		response,
		request,
		http.StatusOK,
		frontend.Backstage(
			actor.Name,
			csrfToken,
			reducedEffectsCookie(request),
			navigation,
		),
	)
}

type backstageNavigationModel = frontend.BackstageNavigation
type backstageEventNavigation = frontend.BackstageEvent

func (handlers frontendHandlers) backstageCompetitionNavigation(
	request *http.Request,
	account auth.Account,
	navigation backstageNavigationModel,
) (backstageNavigationModel, error) {
	for eventIndex := range navigation.Events {
		event := &navigation.Events[eventIndex]
		if account.EventRoles[event.ID] != viewer.Operator {
			continue
		}
		sectionIndex := slices.IndexFunc(event.Sections, func(section frontend.BackstageSection) bool {
			return section.ID == "event-"+strconv.Itoa(event.ID)+"-entries"
		})
		if sectionIndex < 0 {
			continue
		}
		state, err := handlers.rundown.CrewRundown(request.Context(), account, event.ID)
		if err != nil {
			return backstageNavigationModel{}, err
		}
		sections := append([]frontend.BackstageSection(nil), event.Sections[:sectionIndex]...)
		for _, session := range state.Sessions {
			if session.Type != rundown.SessionCompetition ||
				!canAccessCompetition(account, event.ID, session) {
				continue
			}
			sections = append(sections, frontend.BackstageSection{
				ID:    "event-" + strconv.Itoa(event.ID) + "-competition-" + strconv.Itoa(session.ID),
				Label: session.Title + " Entries and Attachments",
				Href:  competitionEntriesPath(event.ID, session.ID),
			})
		}
		event.Sections = append(sections, event.Sections[sectionIndex+1:]...)
	}
	return navigation, nil
}

func canAccessCompetition(
	account auth.Account,
	eventID int,
	session rundown.CrewSession,
) bool {
	identity := viewer.Identity{
		EventRoles:  account.EventRoles,
		EventScopes: account.EventScopes,
	}
	if identity.CanProduceEvent(eventID) {
		return true
	}
	if len(session.LaneIDs) == 0 {
		return false
	}
	for _, laneID := range session.LaneIDs {
		if !identity.CanOperateLane(eventID, laneID) {
			return false
		}
	}
	return true
}

func backstageNavigation(account auth.Account) backstageNavigationModel {
	eventIDs := make([]int, 0, len(account.EventRoles))
	for eventID := range account.EventRoles {
		eventIDs = append(eventIDs, eventID)
	}
	slices.Sort(eventIDs)
	navigation := backstageNavigationModel{
		Administrator: account.Administrator,
		Events:        make([]backstageEventNavigation, 0, len(eventIDs)),
	}
	for _, eventID := range eventIDs {
		role := account.EventRoles[eventID]
		sections := []frontend.BackstageSection{
			backstageSection(eventID, "overview", "Event overview"),
		}
		if role == viewer.Producer {
			sections = append(sections,
				backstageSection(eventID, "planning", "Plan and publish"),
			)
		}
		scope := account.EventScopes[eventID]
		if role == viewer.Producer ||
			role == viewer.Operator &&
				(len(scope.LaneIDs) != 0 || len(scope.DisplayGroupKeys) != 0) {
			sections = append(sections,
				backstageSection(eventID, "operation", "Sessions and Displays"),
			)
		}
		if role == viewer.Producer ||
			role == viewer.Operator &&
				(len(scope.LaneIDs) != 0 ||
					len(scope.DisplayGroupKeys) != 0) {
			sections = append(sections,
				backstageSection(eventID, "control", "Program Output and Overrides"),
			)
		}
		if account.HasCapability(eventID, viewer.EmergencyAlert) &&
			(role == viewer.Producer ||
				len(scope.LaneIDs) != 0 ||
				len(scope.DisplayGroupKeys) != 0) {
			sections = append(sections,
				backstageSection(eventID, "emergency", "Emergency Alerts"),
			)
		}
		if role == viewer.Producer ||
			role == viewer.Operator && len(scope.LaneIDs) != 0 {
			sections = append(sections,
				backstageSection(eventID, "entries", "Competition Entries and Attachments"),
			)
		}
		if role == viewer.Producer ||
			account.HasCapability(eventID, viewer.ViewResults) ||
			account.HasCapability(eventID, viewer.ManageResults) {
			sections = append(sections,
				backstageSection(eventID, "results", "Results and Prizegiving"),
			)
		}
		navigation.Events = append(navigation.Events, backstageEventNavigation{
			ID: eventID, Role: string(role), Sections: sections,
		})
	}
	return navigation
}

func backstageSection(eventID int, fragment, label string) frontend.BackstageSection {
	id := "event-" + strconv.Itoa(eventID) + "-" + fragment
	href := "/backstage#" + id
	if fragment == "planning" {
		href = "/backstage/events/" + strconv.Itoa(eventID) + "/planning"
	}
	if fragment == "operation" {
		href = "/backstage/events/" + strconv.Itoa(eventID) + "/operations"
	}
	if fragment == "control" {
		href = "/backstage/events/" + strconv.Itoa(eventID) + "/control"
	}
	if fragment == "entries" {
		href = "/backstage/events/" + strconv.Itoa(eventID) +
			"/planning#competition-entries"
	}
	if fragment == "results" {
		href = "/backstage/events/" + strconv.Itoa(eventID) + "/results"
	}
	return frontend.BackstageSection{
		ID:    id,
		Label: label,
		Href:  href,
	}
}

func backstageAvailable(navigation backstageNavigationModel) bool {
	return navigation.Administrator || len(navigation.Events) != 0
}

func backstageAccessible(request *http.Request) bool {
	details, ok := request.Context().Value(
		interfaceRequestContextKey{},
	).(interfaceRequest)
	return !ok || !details.publicOnly
}

func (handlers frontendHandlers) browserAccount(
	response http.ResponseWriter,
	request *http.Request,
) (auth.Account, bool) {
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil {
		http.Redirect(response, request, "/sign-in", http.StatusSeeOther)
		return auth.Account{}, false
	}
	found, err := handlers.authentication.Authenticate(request.Context(), cookie.Value)
	if errors.Is(err, auth.ErrInvalidSession) {
		clearSessionCookie(response, request)
		http.Redirect(response, request, "/sign-in", http.StatusSeeOther)
		return auth.Account{}, false
	}
	if err != nil {
		handlers.frontendError(response, request, "authenticate Frontend session", err)
		return auth.Account{}, false
	}
	return found, true
}

func positiveFormIDs(values []string) ([]int, error) {
	ids := make([]int, 0, len(values))
	for _, value := range values {
		id, err := strconv.Atoi(value)
		if err != nil || id <= 0 {
			return nil, errors.New("invalid ID")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (handlers frontendHandlers) root(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/" {
		http.NotFound(response, request)
		return
	}
	if !frontendReadAllowed(response, request) {
		return
	}
	setupRequired, err := handlers.authentication.SetupRequired(request.Context())
	if err != nil {
		handlers.frontendError(response, request, "read setup state", err)
		return
	}
	accountName := ""
	backstage := false
	reducedEffects := reducedEffectsCookie(request)
	if cookie, cookieErr := request.Cookie(sessionCookieName); cookieErr == nil {
		account, authenticateErr := handlers.authentication.Authenticate(
			request.Context(),
			cookie.Value,
		)
		switch {
		case authenticateErr == nil:
			accountName = account.Name
			backstage = backstageAccessible(request) &&
				backstageAvailable(backstageNavigation(account))
			reducedEffects, authenticateErr = handlers.authentication.ReducedEffects(
				request.Context(),
				cookie.Value,
			)
			if authenticateErr != nil {
				handlers.frontendError(
					response,
					request,
					"read Account Reduced Effects",
					authenticateErr,
				)
				return
			}
			setReducedEffectsCookie(response, request, reducedEffects)
		case errors.Is(authenticateErr, auth.ErrInvalidSession):
			clearSessionCookie(response, request)
		default:
			handlers.frontendError(response, request, "authenticate Frontend session", authenticateErr)
			return
		}
	}
	csrfToken, err := handlers.csrfToken(response, request)
	if err != nil {
		handlers.frontendError(response, request, "create CSRF proof", err)
		return
	}
	var publicEvents []events.PublicEvent
	if !setupRequired {
		publicEvents, err = handlers.events.PublicListing(request.Context())
		if err != nil {
			handlers.frontendError(response, request, "list public Events", err)
			return
		}
	}
	handlers.render(
		response,
		request,
		http.StatusOK,
		frontend.Root(
			setupRequired,
			accountName,
			csrfToken,
			reducedEffects,
			backstage,
			publicEvents,
		),
	)
}

func (handlers frontendHandlers) publicEvent(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !frontendReadAllowed(response, request) {
		return
	}
	found, alias, err := handlers.events.PublicEvent(
		request.Context(),
		request.PathValue("slug"),
	)
	if errors.Is(err, events.ErrEventNotFound) {
		http.NotFound(response, request)
		return
	}
	if err != nil {
		handlers.frontendError(response, request, "read public Event", err)
		return
	}
	if alias {
		http.Redirect(
			response,
			request,
			"/events/"+found.Slug,
			http.StatusFound,
		)
		return
	}
	handlers.render(
		response,
		request,
		http.StatusOK,
		frontend.PublicEvent(found),
	)
}

func (handlers frontendHandlers) setup(response http.ResponseWriter, request *http.Request) {
	setupRequired, err := handlers.authentication.SetupRequired(request.Context())
	if err != nil {
		handlers.frontendError(response, request, "read setup state", err)
		return
	}
	if !setupRequired {
		http.NotFound(response, request)
		return
	}
	csrfToken, err := handlers.csrfToken(response, request)
	if err != nil {
		handlers.frontendError(response, request, "create CSRF proof", err)
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		handlers.render(
			response,
			request,
			http.StatusOK,
			frontend.Setup(csrfToken, "", reducedEffectsCookie(request)),
		)
		return
	case http.MethodPost:
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
		return
	}
	if !handlers.validForm(response, request) {
		return
	}
	clientKey, bootstrapKey := bootstrapFailureKeys(request)
	if retryAfter, blocked := handlers.limiter.reserve(clientKey, bootstrapKey); blocked {
		writeAuthRateLimit(response, retryAfter)
		return
	}
	session, err := handlers.authentication.BootstrapFirstAccount(
		request.Context(),
		request.Form.Get("bootstrap_token"),
		request.Form.Get("handle"),
		request.Form.Get("display_name"),
		request.Form.Get("password"),
	)
	switch {
	case errors.Is(err, auth.ErrAuthenticationBusy):
		handlers.limiter.release(clientKey, bootstrapKey)
		writeAuthRateLimit(response, time.Second)
	case errors.Is(err, auth.ErrInvalidAccountDetails):
		handlers.render(response, request, http.StatusBadRequest, frontend.Setup(csrfToken, "Check the Account details and try again.", reducedEffectsCookie(request)))
	case errors.Is(err, auth.ErrAuthenticationFailed):
		handlers.render(response, request, http.StatusUnauthorized, frontend.Setup(csrfToken, "Setup token is invalid or expired.", reducedEffectsCookie(request)))
	case err != nil:
		handlers.limiter.release(clientKey, bootstrapKey)
		handlers.frontendError(response, request, "bootstrap first Account", err)
	default:
		handlers.limiter.release(clientKey, bootstrapKey)
		handlers.limiter.reset(bootstrapKey)
		setSessionCookie(response, request, session)
		http.Redirect(response, request, "/", http.StatusSeeOther)
	}
}

func (handlers frontendHandlers) signIn(response http.ResponseWriter, request *http.Request) {
	setupRequired, err := handlers.authentication.SetupRequired(request.Context())
	if err != nil {
		handlers.frontendError(response, request, "read setup state", err)
		return
	}
	if setupRequired {
		http.Redirect(response, request, "/setup", http.StatusSeeOther)
		return
	}
	if request.Method == http.MethodGet || request.Method == http.MethodHead {
		if cookie, cookieErr := request.Cookie(sessionCookieName); cookieErr == nil {
			_, authenticateErr := handlers.authentication.Authenticate(
				request.Context(),
				cookie.Value,
			)
			switch {
			case authenticateErr == nil:
				//nolint:gosec // The helper accepts only same-origin absolute paths.
				http.Redirect(
					response,
					request,
					safeFrontendReturnTo(request.URL.Query().Get("return_to"), "/"),
					http.StatusSeeOther,
				)
				return
			case errors.Is(authenticateErr, auth.ErrInvalidSession):
				clearSessionCookie(response, request)
			default:
				handlers.frontendError(response, request, "authenticate Frontend session", authenticateErr)
				return
			}
		}
	}
	csrfToken, err := handlers.csrfToken(response, request)
	if err != nil {
		handlers.frontendError(response, request, "create CSRF proof", err)
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		handlers.render(
			response,
			request,
			http.StatusOK,
			frontend.SignIn(
				csrfToken,
				"",
				"",
				safeFrontendReturnTo(request.URL.Query().Get("return_to"), "/"),
				reducedEffectsCookie(request),
			),
		)
		return
	case http.MethodPost:
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
		return
	}
	if !handlers.validForm(response, request) {
		return
	}
	handle := request.Form.Get("handle")
	clientKey, accountKey := signInFailureKeys(request, handle)
	if retryAfter, blocked := handlers.limiter.reserve(clientKey, accountKey); blocked {
		writeAuthRateLimit(response, retryAfter)
		return
	}
	session, err := handlers.authentication.SignIn(
		request.Context(),
		handle,
		request.Form.Get("password"),
	)
	switch {
	case errors.Is(err, auth.ErrAuthenticationBusy):
		handlers.limiter.release(clientKey, accountKey)
		writeAuthRateLimit(response, time.Second)
	case errors.Is(err, auth.ErrAuthenticationFailed):
		handlers.render(
			response,
			request,
			http.StatusUnauthorized,
			frontend.SignIn(
				csrfToken,
				"Sign-in failed.",
				handle,
				safeFrontendReturnTo(request.Form.Get("return_to"), "/"),
				reducedEffectsCookie(request),
			),
		)
	case err != nil:
		handlers.limiter.release(clientKey, accountKey)
		handlers.frontendError(response, request, "sign in Account", err)
	default:
		handlers.limiter.release(clientKey, accountKey)
		handlers.limiter.reset(accountKey)
		setSessionCookie(response, request, session)
		//nolint:gosec // The helper accepts only same-origin absolute paths.
		http.Redirect(
			response,
			request,
			safeFrontendReturnTo(request.Form.Get("return_to"), "/"),
			http.StatusSeeOther,
		)
	}
}

func safeFrontendReturnTo(value, fallback string) string {
	parsed, err := url.ParseRequestURI(value)
	if err != nil ||
		!strings.HasPrefix(parsed.Path, "/") ||
		strings.HasPrefix(parsed.Path, "//") ||
		strings.Contains(parsed.Path, `\`) ||
		parsed.IsAbs() ||
		parsed.Host != "" {
		return fallback
	}
	return value
}

func (handlers frontendHandlers) effects(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		frontendMethodNotAllowed(response, http.MethodPost)
		return
	}
	if !handlers.validForm(response, request) {
		return
	}
	enabled := request.Form.Get("reduce_effects") == "true"
	if cookie, err := request.Cookie(sessionCookieName); err == nil {
		if err = handlers.authentication.SetReducedEffects(
			request.Context(),
			cookie.Value,
			enabled,
		); errors.Is(err, auth.ErrInvalidSession) {
			clearSessionCookie(response, request)
		} else if err != nil {
			handlers.frontendError(response, request, "set Account Reduced Effects", err)
			return
		}
	}
	setReducedEffectsCookie(response, request, enabled)
	http.Redirect(response, request, "/", http.StatusSeeOther)
}

func (handlers frontendHandlers) signOut(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		frontendMethodNotAllowed(response, http.MethodPost)
		return
	}
	if !handlers.validForm(response, request) {
		return
	}
	if cookie, err := request.Cookie(sessionCookieName); err == nil {
		if err = handlers.authentication.SignOut(request.Context(), cookie.Value); err != nil {
			handlers.frontendError(response, request, "sign out Account", err)
			return
		}
	}
	clearSessionCookie(response, request)
	http.Redirect(response, request, "/", http.StatusSeeOther)
}

func reducedEffectsCookie(request *http.Request) bool {
	cookie, err := request.Cookie(reducedEffectsCookieName)
	return err == nil && cookie.Value == "true"
}

func setReducedEffectsCookie(
	response http.ResponseWriter,
	request *http.Request,
	enabled bool,
) {
	//nolint:gosec // Plaintext cookies are limited by the listener policy.
	cookie := &http.Cookie{
		Name:     reducedEffectsCookieName,
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsSecure(request),
		SameSite: http.SameSiteLaxMode,
	}
	if enabled {
		cookie.Value = "true"
		cookie.MaxAge = reducedEffectsMaxAge
	} else {
		cookie.MaxAge = -1
	}
	http.SetCookie(response, cookie)
}

func (handlers frontendHandlers) validForm(
	response http.ResponseWriter,
	request *http.Request,
) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" ||
		!sameOrigin(request) ||
		request.ParseForm() != nil {
		http.Error(response, "invalid form submission", http.StatusBadRequest)
		return false
	}
	if !validCSRFProof(request) {
		http.Error(response, "invalid CSRF proof", http.StatusForbidden)
		return false
	}
	return true
}

func (handlers frontendHandlers) validMultipartForm(
	response http.ResponseWriter,
	request *http.Request,
) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" ||
		!sameOrigin(request) ||
		request.ParseMultipartForm(64<<20) != nil { //nolint:gosec // Route bytes are bounded.
		http.Error(response, "invalid form submission", http.StatusBadRequest)
		return false
	}
	if !validCSRFProof(request) {
		http.Error(response, "invalid CSRF proof", http.StatusForbidden)
		return false
	}
	return true
}

func validCSRFProof(request *http.Request) bool {
	cookie, err := request.Cookie(csrfCookieName)
	provided := request.FormValue("csrf_token")
	return err == nil && validCSRFToken(cookie.Value) && validCSRFToken(provided) &&
		subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(provided)) == 1
}

func (handlers frontendHandlers) csrfToken(
	response http.ResponseWriter,
	request *http.Request,
) (string, error) {
	if cookie, err := request.Cookie(csrfCookieName); err == nil && validCSRFToken(cookie.Value) {
		return cookie.Value, nil
	}
	contents := make([]byte, csrfTokenBytes)
	if _, err := io.ReadFull(handlers.random, contents); err != nil {
		return "", errors.New("generate CSRF token")
	}
	token := base64.RawURLEncoding.EncodeToString(contents)
	//nolint:gosec // Plaintext cookies are limited by the listener policy.
	http.SetCookie(response, &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsSecure(request),
		SameSite: http.SameSiteLaxMode,
	})
	return token, nil
}

func validCSRFToken(token string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == csrfTokenBytes &&
		base64.RawURLEncoding.EncodeToString(decoded) == token
}

func (handlers frontendHandlers) asset(path string) (http.HandlerFunc, error) {
	content, err := frontend.Asset(path)
	if err != nil {
		return nil, err
	}
	contentType := "text/javascript; charset=utf-8"
	cacheControl := "public, max-age=31536000, immutable"
	if strings.HasSuffix(path, ".css") {
		cacheControl = "public, max-age=3600"
		contentType = "text/css; charset=utf-8"
	} else if strings.HasSuffix(path, ".ttf") {
		contentType = "font/ttf"
	}
	return func(response http.ResponseWriter, request *http.Request) {
		if !frontendReadAllowed(response, request) {
			return
		}
		response.Header().Set("Cache-Control", cacheControl)
		response.Header().Set("Content-Type", contentType)
		if request.Method != http.MethodHead {
			_, _ = response.Write(content)
		}
	}, nil
}

func (handlers frontendHandlers) render(
	response http.ResponseWriter,
	request *http.Request,
	status int,
	component templ.Component,
) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	var content bytes.Buffer
	if err := component.Render(request.Context(), &content); err != nil {
		handlers.frontendError(response, request, "render Frontend page", err)
		return
	}
	response.WriteHeader(status)
	if request.Method != http.MethodHead {
		_, _ = response.Write(content.Bytes())
	}
}

func (handlers frontendHandlers) frontendError(
	response http.ResponseWriter,
	request *http.Request,
	operation string,
	err error,
) {
	handlers.logger.ErrorContext(request.Context(), operation, "error", err)
	http.Error(response, "Frontend unavailable", http.StatusInternalServerError)
}

func frontendReadAllowed(response http.ResponseWriter, request *http.Request) bool {
	if request.Method == http.MethodGet || request.Method == http.MethodHead {
		return true
	}
	frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead)
	return false
}

func frontendMethodNotAllowed(response http.ResponseWriter, allowed string) {
	response.Header().Set("Allow", allowed)
	http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
}
