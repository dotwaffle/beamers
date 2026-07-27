package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/federation"
)

const (
	federationCookieName = "beamers_federation"
	federationTokenBytes = 32
	federationFlowTTL    = 10 * time.Minute
	maxFederationFlows   = 1024
)

type federationFlow struct {
	bindingHash [sha256.Size]byte
	expiresAt   time.Time
	accountID   int
	returnTo    string
}

type federationHandlers struct {
	authentication *auth.Service
	sceneID        *federation.SceneID
	logger         *slog.Logger
	random         io.Reader
	now            func() time.Time

	mu    sync.Mutex
	flows map[string]federationFlow
}

func registerFederationRoutes(
	mux *routeMux,
	authentication *auth.Service,
	sceneID *federation.SceneID,
	logger *slog.Logger,
) {
	if sceneID == nil {
		return
	}
	handlers := &federationHandlers{
		authentication: authentication,
		sceneID:        sceneID,
		logger:         logger,
		random:         rand.Reader,
		now:            time.Now,
		flows:          make(map[string]federationFlow),
	}
	route := browserPageRoute()
	route.maxBodyBytes = maxAuthBodyBytes
	mux.HandleFunc("/auth/federation/providers", browserPageRoute(), handlers.providers)
	mux.HandleFunc("/auth/federation/sceneid/begin", route, handlers.begin)
	mux.HandleFunc("/auth/federation/sceneid/callback", browserPageRoute(), handlers.callback)
}

func (handlers *federationHandlers) providers(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !frontendReadAllowed(response, request) {
		return
	}
	setAuthHeaders(response)
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode([]federation.PublicProvider{
		handlers.sceneID.Public(),
	}); err != nil {
		handlers.logger.ErrorContext(
			request.Context(),
			"write federation providers",
			"component", "http",
			"error", err,
		)
	}
}

func (handlers *federationHandlers) begin(
	response http.ResponseWriter,
	request *http.Request,
) {
	setAuthHeaders(response)
	var accountID int
	switch request.Method {
	case http.MethodGet:
		if request.URL.Query().Get("mode") != "" {
			http.Error(response, "invalid federation mode", http.StatusBadRequest)
			return
		}
	case http.MethodPost:
		if !validFormSubmission(response, request) {
			return
		}
		if request.Form.Get("mode") != "link" {
			http.Error(response, "invalid federation mode", http.StatusBadRequest)
			return
		}
		cookie, err := request.Cookie(sessionCookieName)
		if err != nil {
			http.Error(response, "authentication required", http.StatusUnauthorized)
			return
		}
		actor, err := handlers.authentication.Authenticate(request.Context(), cookie.Value)
		if errors.Is(err, auth.ErrInvalidSession) {
			http.Error(response, "authentication required", http.StatusUnauthorized)
			return
		}
		if err != nil {
			handlers.failure(response, request, "authenticate federation link", err)
			return
		}
		accountID = actor.ID
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodPost)
		return
	}
	state, err := handlers.newToken()
	if err != nil {
		handlers.failure(response, request, "create federation state", err)
		return
	}
	binding, err := handlers.newToken()
	if err != nil {
		handlers.failure(response, request, "create federation binding", err)
		return
	}
	authorizationURL, err := handlers.sceneID.AuthorizationURL(federation.Authorization{
		State: state,
	})
	if err != nil {
		http.Error(response, "SceneID requires HTTPS or loopback", http.StatusForbidden)
		return
	}
	handlers.rememberFlow(state, federationFlow{
		bindingHash: sha256.Sum256([]byte(binding)),
		expiresAt:   handlers.now().UTC().Add(federationFlowTTL),
		accountID:   accountID,
		returnTo:    safeFrontendReturnTo(request.URL.Query().Get("return_to"), "/"),
	})
	//nolint:gosec // Plaintext cookies are confined to the loopback exception.
	http.SetCookie(response, &http.Cookie{
		Name: federationCookieName, Value: binding,
		Path:     "/auth/federation/sceneid/callback",
		Expires:  handlers.now().UTC().Add(federationFlowTTL),
		HttpOnly: true, Secure: requestIsSecure(request), SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(response, request, authorizationURL, http.StatusSeeOther)
}

func (handlers *federationHandlers) callback(
	response http.ResponseWriter,
	request *http.Request,
) {
	setAuthHeaders(response)
	if request.Method != http.MethodGet {
		frontendMethodNotAllowed(response, http.MethodGet)
		return
	}
	binding, err := request.Cookie(federationCookieName)
	if err != nil {
		http.Error(response, "SceneID authentication failed", http.StatusUnauthorized)
		return
	}
	flow, ok := handlers.takeFlow(request.URL.Query().Get("state"), binding.Value)
	clearFederationCookie(response, request)
	if !ok || request.URL.Query().Get("error") != "" ||
		request.URL.Query().Get("code") == "" {
		http.Error(response, "SceneID authentication failed", http.StatusUnauthorized)
		return
	}
	identity, err := handlers.sceneID.Identity(request.Context(), federation.Callback{
		Code: request.URL.Query().Get("code"),
	})
	if err != nil {
		http.Error(response, "SceneID authentication failed", http.StatusUnauthorized)
		return
	}
	if flow.accountID != 0 {
		handlers.finishLink(response, request, flow, identity)
		return
	}
	session, _, err := handlers.authentication.FederatedSignIn(
		request.Context(),
		"sceneid",
		identity.Subject,
		identity.DisplayName,
		handlers.sceneID.Public().AllowAccountCreation,
	)
	switch {
	case errors.Is(err, auth.ErrRegistrationClosed):
		http.Error(response, "SceneID Account creation is disabled", http.StatusForbidden)
	case errors.Is(err, auth.ErrAuthenticationFailed):
		http.Error(response, "SceneID authentication failed", http.StatusUnauthorized)
	case err != nil:
		handlers.failure(response, request, "complete SceneID sign-in", err)
	default:
		setSessionCookie(response, request, session)
		http.Redirect(response, request, flow.returnTo, http.StatusSeeOther)
	}
}

func (handlers *federationHandlers) finishLink(
	response http.ResponseWriter,
	request *http.Request,
	flow federationFlow,
	identity federation.Identity,
) {
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil {
		http.Error(response, "authentication required", http.StatusUnauthorized)
		return
	}
	actor, err := handlers.authentication.Authenticate(request.Context(), cookie.Value)
	if errors.Is(err, auth.ErrInvalidSession) || err == nil && actor.ID != flow.accountID {
		http.Error(response, "authentication required", http.StatusUnauthorized)
		return
	}
	if err != nil {
		handlers.failure(response, request, "authenticate SceneID link callback", err)
		return
	}
	_, err = handlers.authentication.LinkFederatedIdentity(
		request.Context(),
		actor,
		"sceneid",
		identity.Subject,
		"link-sceneid-"+base64.RawURLEncoding.EncodeToString(flow.bindingHash[:]),
	)
	switch {
	case errors.Is(err, auth.ErrAccountExists):
		http.Error(response, "SceneID identity is linked to another Account", http.StatusConflict)
	case err != nil:
		handlers.failure(response, request, "link SceneID identity", err)
	default:
		http.Redirect(response, request, "/profile", http.StatusSeeOther)
	}
}

func (handlers *federationHandlers) newToken() (string, error) {
	content := make([]byte, federationTokenBytes)
	if _, err := io.ReadFull(handlers.random, content); err != nil {
		return "", errors.New("generate federation proof")
	}
	return base64.RawURLEncoding.EncodeToString(content), nil
}

func (handlers *federationHandlers) rememberFlow(state string, flow federationFlow) {
	now := handlers.now().UTC()
	handlers.mu.Lock()
	defer handlers.mu.Unlock()
	var oldestState string
	var oldestExpiry time.Time
	for candidate, existing := range handlers.flows {
		if !existing.expiresAt.After(now) {
			delete(handlers.flows, candidate)
			continue
		}
		if oldestState == "" || existing.expiresAt.Before(oldestExpiry) {
			oldestState, oldestExpiry = candidate, existing.expiresAt
		}
	}
	if len(handlers.flows) >= maxFederationFlows {
		delete(handlers.flows, oldestState)
	}
	handlers.flows[state] = flow
}

func (handlers *federationHandlers) takeFlow(state, binding string) (federationFlow, bool) {
	bindingHash := sha256.Sum256([]byte(binding))
	handlers.mu.Lock()
	defer handlers.mu.Unlock()
	flow, ok := handlers.flows[state]
	if !ok ||
		!flow.expiresAt.After(handlers.now().UTC()) ||
		subtle.ConstantTimeCompare(flow.bindingHash[:], bindingHash[:]) != 1 {
		return federationFlow{}, false
	}
	delete(handlers.flows, state)
	return flow, true
}

func clearFederationCookie(response http.ResponseWriter, request *http.Request) {
	//nolint:gosec // Plaintext cookies are confined to the loopback exception.
	http.SetCookie(response, &http.Cookie{
		Name:    federationCookieName,
		Path:    "/auth/federation/sceneid/callback",
		Expires: time.Unix(1, 0).UTC(), MaxAge: -1,
		HttpOnly: true, Secure: requestIsSecure(request), SameSite: http.SameSiteLaxMode,
	})
}

func (handlers *federationHandlers) failure(
	response http.ResponseWriter,
	request *http.Request,
	operation string,
	err error,
) {
	handlers.logger.ErrorContext(
		request.Context(),
		operation,
		"component", "authentication",
		"error", err,
	)
	http.Error(response, "SceneID unavailable", http.StatusInternalServerError)
}
