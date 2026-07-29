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
	"unicode"
	"unicode/utf8"

	"github.com/a-h/templ"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/federation"
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
	sceneID        *federation.PublicProvider
}

func registerFrontendRoutes(
	mux *routeMux,
	authentication *auth.Service,
	limiter *authFailureLimiter,
	rundownQueries *rundown.Queries,
	eventService *events.Service,
	logger *slog.Logger,
	sceneID *federation.PublicProvider,
) error {
	handlers := frontendHandlers{
		authentication: authentication,
		logger:         logger,
		limiter:        limiter,
		random:         rand.Reader,
		rundown:        rundownQueries,
		events:         eventService,
		sceneID:        sceneID,
	}
	formRoute := browserPageRoute()
	formRoute.maxBodyBytes = maxAuthBodyBytes
	mux.HandleFunc("/{$}", browserPageRoute(), handlers.root)
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
			frontend.Register(
				csrfToken,
				"",
				"",
				nil,
				open,
				handlers.sceneID != nil && handlers.sceneID.AllowAccountCreation,
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
	var formErrors frontend.FormErrors
	renderOpen := open
	renderFailure := false
	switch {
	case errors.Is(err, auth.ErrRegistrationClosed):
		status, renderOpen, renderFailure = http.StatusForbidden, false, true
	case errors.Is(err, auth.ErrAccountExists):
		status, renderFailure = http.StatusConflict, true
		formErrors = frontend.FormErrors{{
			FieldID: "register-handle",
			Label:   "Account Handle",
			Message: "That Account Handle is already in use.",
		}}
	case errors.Is(err, auth.ErrInvalidAccountDetails):
		status, renderFailure = http.StatusBadRequest, true
		formErrors = accountDetailErrors(
			"register",
			handle,
			displayName,
			request.Form.Get("password"),
		)
	case errors.Is(err, auth.ErrAuthenticationBusy):
		handlers.limiter.release(clientKey, accountKey)
		writeAuthRateLimit(response, time.Second)
	case err != nil:
		handlers.limiter.release(clientKey, accountKey)
		handlers.frontendError(response, request, "register Account", err)
	default:
		http.Redirect(response, request, "/sign-in", http.StatusSeeOther)
	}
	if renderFailure {
		handlers.render(
			response, request, status,
			frontend.Register(
				csrfToken, handle, displayName, formErrors, renderOpen,
				handlers.sceneID != nil && handlers.sceneID.AllowAccountCreation,
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
				handlers.renderProfileMessage(
					response, request, actor, csrfToken,
					http.StatusConflict,
					"Recovery Codes were already replaced and cannot be shown again.",
				)
				return
			case errors.Is(replaceErr, auth.ErrInvalidAccountDetails):
				handlers.renderProfileMessage(
					response, request, actor, csrfToken,
					http.StatusBadRequest, "Invalid Recovery Code request.",
				)
				return
			case replaceErr != nil:
				handlers.frontendError(response, request, "replace Recovery Codes", replaceErr)
				return
			}
			handlers.renderProfile(
				response,
				request,
				actor,
				csrfToken,
				codes,
				http.StatusOK,
				false,
				nil,
			)
			return
		case "remove-password":
			err = handlers.authentication.RemovePassword(
				request.Context(),
				actor,
				request.Form.Get("command_id"),
			)
			switch {
			case errors.Is(err, auth.ErrFinalCredential):
				handlers.renderProfileMessage(
					response, request, actor, csrfToken,
					http.StatusConflict, "The final active Credential cannot be removed.",
				)
			case errors.Is(err, auth.ErrCommandConflict):
				handlers.renderProfileMessage(
					response, request, actor, csrfToken,
					http.StatusConflict, "Credential state changed. Try again.",
				)
			case errors.Is(err, auth.ErrInvalidAccountDetails):
				handlers.renderProfileMessage(
					response, request, actor, csrfToken,
					http.StatusBadRequest, "Invalid Credential request.",
				)
			case err != nil:
				handlers.frontendError(response, request, "remove password Credential", err)
			default:
				http.Redirect(response, request, "/profile", http.StatusSeeOther)
			}
			return
		case "revoke-webauthn":
			credentialID, parseErr := strconv.Atoi(request.Form.Get("credential_id"))
			if parseErr != nil || credentialID <= 0 {
				handlers.renderProfileMessage(
					response, request, actor, csrfToken,
					http.StatusBadRequest, "Invalid WebAuthn Credential request.",
				)
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
				handlers.renderProfileMessage(
					response, request, actor, csrfToken,
					http.StatusConflict, "The final active Credential cannot be removed.",
				)
			case errors.Is(err, auth.ErrInvalidSession):
				handlers.renderProfileMessage(
					response, request, actor, csrfToken,
					http.StatusNotFound, "WebAuthn Credential unavailable.",
				)
			case errors.Is(err, auth.ErrCommandConflict):
				handlers.renderProfileMessage(
					response, request, actor, csrfToken,
					http.StatusConflict, "Credential state changed. Try again.",
				)
			case errors.Is(err, auth.ErrInvalidAccountDetails):
				handlers.renderProfileMessage(
					response, request, actor, csrfToken,
					http.StatusBadRequest, "Invalid Credential request.",
				)
			case err != nil:
				handlers.frontendError(response, request, "revoke WebAuthn Credential", err)
			default:
				http.Redirect(response, request, "/profile", http.StatusSeeOther)
			}
			return
		}
		entryIDs, parseErr := positiveFormIDs(request.Form["entry_id"])
		if parseErr != nil {
			handlers.renderProfile(
				response,
				request,
				actor,
				csrfToken,
				nil,
				http.StatusBadRequest,
				true,
				frontend.FormErrors{{
					FieldID: "profile-entries",
					Label:   "Public Profile",
					Message: "Choose only available Entries.",
				}},
			)
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
		case errors.Is(err, auth.ErrInvalidAccountDetails):
			handlers.renderProfile(
				response,
				request,
				actor,
				csrfToken,
				nil,
				http.StatusBadRequest,
				true,
				frontend.FormErrors{{
					FieldID: "profile-display-name",
					Label:   "Display Name",
					Message: "Enter a Display Name.",
				}},
			)
		case errors.Is(err, auth.ErrProfileEntryUnavailable):
			handlers.renderProfile(
				response,
				request,
				actor,
				csrfToken,
				nil,
				http.StatusBadRequest,
				true,
				frontend.FormErrors{{
					FieldID: "profile-entries",
					Label:   "Public Profile",
					Message: "Choose only available Entries.",
				}},
			)
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
	handlers.renderProfile(
		response,
		request,
		actor,
		csrfToken,
		nil,
		http.StatusOK,
		false,
		nil,
	)
}

func (handlers frontendHandlers) renderProfileMessage(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	csrfToken string,
	status int,
	message string,
) {
	handlers.renderProfile(
		response, request, actor, csrfToken, nil, status, false,
		frontend.FormErrors{{Message: message}},
	)
}

func (handlers frontendHandlers) renderProfile(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	csrfToken string,
	recoveryCodes []string,
	status int,
	submitted bool,
	formErrors frontend.FormErrors,
) {
	found, err := handlers.authentication.Profile(request.Context(), actor)
	if err != nil {
		handlers.frontendError(response, request, "read Profile", err)
		return
	}
	if submitted {
		found.DisplayName = request.Form.Get("display_name")
		found.Published = request.Form.Get("published") == "true"
		selectedIDs := make(map[string]struct{}, len(request.Form["entry_id"]))
		for _, entryID := range request.Form["entry_id"] {
			selectedIDs[entryID] = struct{}{}
		}
		for index := range found.AvailableEntries {
			entryID := strconv.Itoa(found.AvailableEntries[index].ID)
			_, found.AvailableEntries[index].Selected = selectedIDs[entryID]
		}
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
	federated, err := handlers.authentication.ListFederatedIdentities(request.Context(), actor)
	if err != nil {
		handlers.frontendError(response, request, "read Federated Identities", err)
		return
	}
	canRemovePassword := false
	for _, credential := range credentials {
		canRemovePassword = canRemovePassword || !credential.Revoked
	}
	for _, identity := range federated {
		canRemovePassword = canRemovePassword || !identity.Revoked
	}
	handlers.render(
		response,
		request,
		status,
		frontend.ProfilePage(
			csrfToken,
			found,
			formErrors,
			passwordActive,
			canRemovePassword,
			credentials,
			handlers.sceneID != nil,
			recoveryCodes,
			recoveryCommandID,
			credentialCommandID,
			reducedEffectsCookie(request),
			backstageAccessible(request) &&
				backstageAvailable(backstageNavigation(actor, "")),
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
			frontend.Recover(csrfToken, "", commandID, nil, reducedEffectsCookie(request)),
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
				handle,
				request.Form.Get("command_id"),
				frontend.FormErrors{
					{
						FieldID: "recover-handle",
						Label:   "Account Handle",
						Message: "Recovery failed.",
					},
					{
						FieldID: "recover-credential",
						Label:   "Recovery Code or Administrator Recovery Token",
						Message: "Recovery failed.",
					},
				},
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
				handle,
				request.Form.Get("command_id"),
				frontend.FormErrors{{
					FieldID: "recover-password",
					Label:   "New Password",
					Message: "Enter a password of 12 to 1024 characters.",
				}},
				reducedEffectsCookie(request),
			),
		)
	case errors.Is(err, auth.ErrRecoveryAlreadyCompleted):
		handlers.limiter.release(clientKey, accountKey)
		handlers.renderRecoverFailure(
			response, request, csrfToken, handle,
			http.StatusConflict,
			"Account recovery already completed; sign in with the new password.",
		)
	case errors.Is(err, auth.ErrCommandConflict):
		handlers.limiter.release(clientKey, accountKey)
		handlers.renderRecoverFailure(
			response, request, csrfToken, handle,
			http.StatusConflict, "Recovery request changed. Try again.",
		)
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

func (handlers frontendHandlers) renderRecoverFailure(
	response http.ResponseWriter,
	request *http.Request,
	csrfToken, handle string,
	status int,
	message string,
) {
	commandID, err := planningCommandID(handlers.random)
	if err != nil {
		handlers.frontendError(response, request, "create Account recovery command identity", err)
		return
	}
	handlers.render(
		response,
		request,
		status,
		frontend.Recover(
			csrfToken,
			handle,
			commandID,
			frontend.FormErrors{{Message: message}},
			reducedEffectsCookie(request),
		),
	)
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
	csrfToken, err := handlers.csrfToken(response, request)
	if err != nil {
		handlers.frontendError(response, request, "create CSRF proof", err)
		return
	}
	handlers.render(
		response,
		request,
		http.StatusOK,
		frontend.PublicProfile(found, csrfToken, reducedEffectsCookie(request)),
	)
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
			backstageNavigation(actor, request.URL.Path),
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
	navigation := backstageNavigation(actor, request.URL.Path)
	if !backstageAvailable(navigation) {
		http.NotFound(response, request)
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

func backstageNavigation(
	account auth.Account,
	activePath string,
) backstageNavigationModel {
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
				backstageSection(eventID, "settings", "Event settings"),
				backstageSection(eventID, "display-settings", "Event Displays"),
			)
		}
		sections = append(
			sections,
			backstageSection(eventID, "sessions", "Sessions and Competitions"),
		)
		if role == viewer.Producer {
			sections = append(sections,
				backstageSection(eventID, "planning", "Plan and publish"),
				backstageSection(eventID, "theme", "Event Theme"),
				backstageSection(eventID, "voting", "Voting Keys"),
			)
		}
		canControl := canUseBackstageControl(account, eventID)
		if canControl {
			sections = append(sections,
				backstageSection(eventID, "operation", "Live operations"),
			)
		} else if role == viewer.Operator {
			sections = append(sections, backstageUnavailableSection(
				eventID,
				"operation",
				"Live operations",
				"No Lane or Display Group scope assigned.",
			))
		}
		if canControl {
			sections = append(sections,
				backstageSection(eventID, "control", "Program Output and Overrides"),
			)
		} else if role == viewer.Operator {
			sections = append(sections, backstageUnavailableSection(
				eventID,
				"control",
				"Program Output and Overrides",
				"No Lane or Display Group scope assigned.",
			))
		}
		if canUseEmergencyAlert(account, eventID) {
			sections = append(sections,
				backstageSection(eventID, "emergency", "Emergency Alerts"),
			)
		} else if account.HasCapability(eventID, viewer.EmergencyAlert) {
			sections = append(sections, backstageUnavailableSection(
				eventID,
				"emergency",
				"Emergency Alerts",
				"No Lane or Display Group scope assigned.",
			))
		}
		if role == viewer.Producer ||
			account.HasCapability(eventID, viewer.ViewResults) ||
			account.HasCapability(eventID, viewer.ManageResults) {
			sections = append(sections,
				backstageSection(eventID, "results", "Results and Prizegiving"),
			)
		}
		if account.Administrator {
			sections = append(
				sections,
				backstageSection(eventID, "final-files", "Final Files Export"),
			)
		}
		navigation.Events = append(navigation.Events, backstageEventNavigation{
			ID: eventID, Name: account.EventNames[eventID],
			Role: string(role), Sections: sections,
		})
	}
	if activePath != "" {
		markBackstageLocation(&navigation, activePath)
	}
	return navigation
}

func canUseBackstageControl(account auth.Account, eventID int) bool {
	if account.CanProduceEvent(eventID) {
		return true
	}
	if account.EventRoles[eventID] != viewer.Operator {
		return false
	}
	scope := account.EventScopes[eventID]
	return len(scope.LaneIDs) != 0 || len(scope.DisplayGroupKeys) != 0
}

func canReadBackstageRundown(account auth.Account, eventID int) bool {
	role := account.EventRoles[eventID]
	return role == viewer.Producer ||
		role == viewer.Observer ||
		role == viewer.Operator && len(account.EventScopes[eventID].LaneIDs) != 0
}

func canUseEmergencyAlert(account auth.Account, eventID int) bool {
	return account.HasCapability(eventID, viewer.EmergencyAlert) &&
		canUseBackstageControl(account, eventID)
}

func backstageSection(eventID int, fragment, label string) frontend.BackstageSection {
	id := "event-" + strconv.Itoa(eventID) + "-" + fragment
	href := "/backstage#" + id
	if fragment == "overview" {
		href = "/backstage/events/" + strconv.Itoa(eventID)
	}
	if fragment == "settings" {
		href = "/backstage/events/" + strconv.Itoa(eventID) + "/settings"
	}
	if fragment == "display-settings" {
		href = "/backstage/events/" + strconv.Itoa(eventID) + "/display-settings"
	}
	if fragment == "sessions" {
		href = "/backstage/events/" + strconv.Itoa(eventID) + "/sessions"
	}
	if fragment == "final-files" {
		href = "/backstage/events/" + strconv.Itoa(eventID) + "/final-files"
	}
	if fragment == "planning" {
		href = "/backstage/events/" + strconv.Itoa(eventID) + "/planning"
	}
	if fragment == "operation" {
		href = "/backstage/events/" + strconv.Itoa(eventID) + "/operations"
	}
	if fragment == "control" {
		href = "/backstage/events/" + strconv.Itoa(eventID) + "/control"
	}
	if fragment == "emergency" {
		href = "/backstage/events/" + strconv.Itoa(eventID) +
			"/control/emergency-alerts#emergency-alerts"
	}
	if fragment == "voting" {
		href = "/backstage/events/" + strconv.Itoa(eventID) + "/voting-keys"
	}
	if fragment == "theme" {
		href = "/backstage/events/" + strconv.Itoa(eventID) + "/theme"
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

func backstageUnavailableSection(
	eventID int,
	fragment string,
	label string,
	reason string,
) frontend.BackstageSection {
	section := backstageSection(eventID, fragment, label)
	section.Href = ""
	section.UnavailableReason = reason
	return section
}

func markBackstageLocation(
	navigation *backstageNavigationModel,
	activePath string,
) {
	if activePath == "/backstage" {
		navigation.Breadcrumbs = []frontend.Breadcrumb{{Label: "Backstage"}}
		return
	}
	navigation.Breadcrumbs = []frontend.Breadcrumb{{
		Label: "Backstage",
		Href:  "/backstage",
	}}
	for eventIndex := range navigation.Events {
		event := &navigation.Events[eventIndex]
		eventPath := "/backstage/events/" + strconv.Itoa(event.ID)
		if activePath != eventPath &&
			!strings.HasPrefix(activePath, eventPath+"/") {
			continue
		}
		eventCrumb := frontend.Breadcrumb{Label: event.Name}
		if activePath != eventPath {
			eventCrumb.Href = eventPath
		}
		navigation.Breadcrumbs = append(navigation.Breadcrumbs, eventCrumb)
		for sectionIndex := range event.Sections {
			section := &event.Sections[sectionIndex]
			sectionPath, _, _ := strings.Cut(section.Href, "#")
			current := activePath == sectionPath
			if strings.HasSuffix(section.ID, "-planning") &&
				strings.HasPrefix(activePath, eventPath+"/presentations/") {
				current = true
			}
			if strings.HasSuffix(section.ID, "-sessions") &&
				strings.HasPrefix(activePath, eventPath+"/competitions/") {
				current = true
			}
			if !current || section.UnavailableReason != "" {
				continue
			}
			section.Current = true
			if activePath != eventPath {
				navigation.Breadcrumbs = append(
					navigation.Breadcrumbs,
					frontend.Breadcrumb{Label: section.Label},
				)
			}
			return
		}
		return
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

type frontendShell struct {
	accountName    string
	backstage      bool
	reducedEffects bool
}

func (handlers frontendHandlers) shell(
	response http.ResponseWriter,
	request *http.Request,
) (frontendShell, bool) {
	shell := frontendShell{reducedEffects: reducedEffectsCookie(request)}
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil {
		return shell, true
	}
	account, err := handlers.authentication.Authenticate(request.Context(), cookie.Value)
	switch {
	case err == nil:
		shell.accountName = account.Name
		shell.backstage = backstageAccessible(request) &&
			backstageAvailable(backstageNavigation(account, ""))
		shell.reducedEffects, err = handlers.authentication.ReducedEffects(
			request.Context(),
			cookie.Value,
		)
		if err != nil {
			handlers.frontendError(response, request, "read Account Reduced Effects", err)
			return frontendShell{}, false
		}
		setReducedEffectsCookie(response, request, shell.reducedEffects)
	case errors.Is(err, auth.ErrInvalidSession):
		clearSessionCookie(response, request)
	default:
		handlers.frontendError(response, request, "authenticate Frontend session", err)
		return frontendShell{}, false
	}
	return shell, true
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
	shell, ok := handlers.shell(response, request)
	if !ok {
		return
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
			shell.accountName,
			csrfToken,
			shell.reducedEffects,
			shell.backstage,
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
	shell, ok := handlers.shell(response, request)
	if !ok {
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
		frontend.PublicEventHub(
			found,
			shell.accountName,
			csrfToken,
			shell.reducedEffects,
			shell.backstage,
		),
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
			frontend.Setup(csrfToken, "", "", nil, reducedEffectsCookie(request)),
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
		handlers.render(
			response,
			request,
			http.StatusBadRequest,
			frontend.Setup(
				csrfToken,
				request.Form.Get("handle"),
				request.Form.Get("display_name"),
				accountDetailErrors(
					"setup",
					request.Form.Get("handle"),
					request.Form.Get("display_name"),
					request.Form.Get("password"),
				),
				reducedEffectsCookie(request),
			),
		)
	case errors.Is(err, auth.ErrAuthenticationFailed):
		handlers.render(
			response,
			request,
			http.StatusUnauthorized,
			frontend.Setup(
				csrfToken,
				request.Form.Get("handle"),
				request.Form.Get("display_name"),
				frontend.FormErrors{{
					FieldID: "setup-token",
					Label:   "One-time setup token",
					Message: "Setup token is invalid or expired.",
				}},
				reducedEffectsCookie(request),
			),
		)
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
				safeFrontendReturnTo(request.URL.Query().Get("return_to"), "/"),
				nil,
				handlers.sceneID != nil,
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
				handle,
				safeFrontendReturnTo(request.Form.Get("return_to"), "/"),
				frontend.FormErrors{
					{
						FieldID: "sign-in-handle",
						Label:   "Account Handle",
						Message: "Sign-in failed.",
					},
					{
						FieldID: "sign-in-password",
						Label:   "Password",
						Message: "Sign-in failed.",
					},
				},
				handlers.sceneID != nil,
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

func accountDetailErrors(
	prefix string,
	handle string,
	displayName string,
	password string,
) frontend.FormErrors {
	var result frontend.FormErrors
	if !validBrowserAccountName(handle) {
		result = append(result, frontend.FormError{
			FieldID: prefix + "-handle",
			Label:   "Account Handle",
			Message: "Enter an Account Handle.",
		})
	}
	if !validBrowserAccountName(displayName) {
		result = append(result, frontend.FormError{
			FieldID: prefix + "-display-name",
			Label:   "Display Name",
			Message: "Enter a Display Name.",
		})
	}
	passwordLength := utf8.RuneCountInString(password)
	if !utf8.ValidString(password) || passwordLength < 12 || passwordLength > 1024 {
		result = append(result, frontend.FormError{
			FieldID: prefix + "-password",
			Label:   "Password",
			Message: "Enter a password of 12 to 1024 characters.",
		})
	}
	return result
}

func validBrowserAccountName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > 200 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
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
	returnTo := safeFrontendReturnTo(request.FormValue("return_to"), "/")
	if request.Method == http.MethodGet || request.Method == http.MethodHead {
		csrfToken, err := handlers.csrfToken(response, request)
		if err != nil {
			handlers.frontendError(response, request, "create CSRF proof", err)
			return
		}
		handlers.render(
			response,
			request,
			http.StatusOK,
			frontend.Effects(csrfToken, reducedEffectsCookie(request), returnTo),
		)
		return
	}
	if request.Method != http.MethodPost {
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
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
	http.Redirect( //nolint:gosec // safeFrontendReturnTo restricts redirects to local absolute paths.
		response,
		request,
		returnTo,
		http.StatusSeeOther,
	)
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

func reducedEffectsPreferenceCookie(request *http.Request) bool {
	_, err := request.Cookie(reducedEffectsCookieName)
	return err == nil
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
	return validFormSubmission(response, request)
}

func validFormSubmission(
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
