package server

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
)

const (
	sessionCookieName    = "beamers_session"
	maxAuthBodyBytes     = 8 << 10
	maxWebAuthnBodyBytes = 64 << 10
)

type authenticationHandlers struct {
	service            *auth.Service
	logger             *slog.Logger
	limiter            *authFailureLimiter
	allowPlaintextCrew bool
}

func registerAuthenticationRoutes(
	mux *routeMux,
	service *auth.Service,
	logger *slog.Logger,
	listenerAddress net.Addr,
	limiter *authFailureLimiter,
) {
	handlers := authenticationHandlers{
		service:            service,
		logger:             logger,
		limiter:            limiter,
		allowPlaintextCrew: listenerIsLoopback(listenerAddress),
	}
	mux.HandleFunc("/auth/bootstrap", crewRoute(), handlers.bootstrap)
	mux.HandleFunc("/auth/sign-in", crewRoute(), handlers.signIn)
	mux.HandleFunc("/auth/session", crewRoute(), handlers.session)
	mux.HandleFunc("/auth/sign-out", crewRoute(), handlers.signOut)
	webAuthnRoute := browserPageRoute()
	webAuthnRoute.maxBodyBytes = maxWebAuthnBodyBytes
	mux.HandleFunc("/auth/webauthn/register/begin", webAuthnRoute, handlers.beginWebAuthnRegistration)
	mux.HandleFunc("/auth/webauthn/register/finish", webAuthnRoute, handlers.finishWebAuthnRegistration)
	mux.HandleFunc("/auth/webauthn/sign-in/begin", webAuthnRoute, handlers.beginWebAuthnSignIn)
	mux.HandleFunc("/auth/webauthn/sign-in/finish", webAuthnRoute, handlers.finishWebAuthnSignIn)
}

func (handlers authenticationHandlers) bootstrap(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !handlers.authRequestAllowed(response, request, http.MethodPost) {
		return
	}
	var input struct {
		BootstrapToken string `json:"bootstrap_token"`
		Name           string `json:"name"`
		Password       string `json:"password"`
	}
	if err := decodeAuthJSON(response, request, &input); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	clientKey, bootstrapKey := bootstrapFailureKeys(request)
	if retryAfter, blocked := handlers.limiter.reserve(clientKey, bootstrapKey); blocked {
		writeAuthRateLimit(response, retryAfter)
		return
	}
	session, err := handlers.service.BootstrapAdministrator(
		request.Context(),
		input.BootstrapToken,
		input.Name,
		input.Password,
	)
	switch {
	case errors.Is(err, auth.ErrAuthenticationBusy):
		handlers.limiter.release(clientKey, bootstrapKey)
		writeAuthRateLimit(response, time.Second)
		return
	case errors.Is(err, auth.ErrInvalidAccountDetails):
		http.Error(response, "invalid Account details", http.StatusBadRequest)
		return
	case errors.Is(err, auth.ErrAuthenticationFailed):
		http.Error(response, "authentication failed", http.StatusUnauthorized)
		return
	case err != nil:
		handlers.limiter.release(clientKey, bootstrapKey)
		handlers.logger.ErrorContext(
			request.Context(),
			"Administrator bootstrap failed",
			"component", "authentication",
			"error", err,
		)
		http.Error(response, "authentication unavailable", http.StatusInternalServerError)
		return
	}
	handlers.limiter.release(clientKey, bootstrapKey)
	handlers.limiter.reset(bootstrapKey)
	setSessionCookie(response, request, session)
	response.WriteHeader(http.StatusCreated)
}

func (handlers authenticationHandlers) signIn(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !handlers.authRequestAllowed(response, request, http.MethodPost) {
		return
	}
	var input struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := decodeAuthJSON(response, request, &input); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	clientKey, accountKey := signInFailureKeys(request, input.Name)
	if retryAfter, blocked := handlers.limiter.reserve(clientKey, accountKey); blocked {
		writeAuthRateLimit(response, retryAfter)
		return
	}
	session, err := handlers.service.SignIn(request.Context(), input.Name, input.Password)
	if errors.Is(err, auth.ErrAuthenticationBusy) {
		handlers.limiter.release(clientKey, accountKey)
		writeAuthRateLimit(response, time.Second)
		return
	}
	if errors.Is(err, auth.ErrAuthenticationFailed) {
		http.Error(response, "authentication failed", http.StatusUnauthorized)
		return
	}
	if err != nil {
		handlers.limiter.release(clientKey, accountKey)
		handlers.logger.ErrorContext(
			request.Context(),
			"Account sign-in failed",
			"component", "authentication",
			"error", err,
		)
		http.Error(response, "authentication unavailable", http.StatusInternalServerError)
		return
	}
	handlers.limiter.release(clientKey, accountKey)
	handlers.limiter.reset(accountKey)
	setSessionCookie(response, request, session)
	response.WriteHeader(http.StatusNoContent)
}

func (handlers authenticationHandlers) session(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !handlers.authRequestAllowed(response, request, http.MethodGet) {
		return
	}
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil {
		http.Error(response, "authentication required", http.StatusUnauthorized)
		return
	}
	account, err := handlers.service.Authenticate(request.Context(), cookie.Value)
	if errors.Is(err, auth.ErrInvalidSession) {
		http.Error(response, "authentication required", http.StatusUnauthorized)
		return
	}
	if err != nil {
		handlers.logger.ErrorContext(
			request.Context(),
			"Account session lookup failed",
			"component", "authentication",
			"error", err,
		)
		http.Error(response, "authentication unavailable", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(struct {
		Name          string `json:"name"`
		Administrator bool   `json:"administrator"`
	}{Name: account.Name, Administrator: account.Administrator}); err != nil {
		handlers.logger.ErrorContext(
			request.Context(),
			"write Account session response",
			"component", "http",
			"error", err,
		)
	}
}

func (handlers authenticationHandlers) signOut(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !handlers.authRequestAllowed(response, request, http.MethodPost) {
		return
	}
	cookie, err := request.Cookie(sessionCookieName)
	if err == nil {
		if err := handlers.service.SignOut(request.Context(), cookie.Value); err != nil {
			handlers.logger.ErrorContext(
				request.Context(),
				"Account sign-out failed",
				"component", "authentication",
				"error", err,
			)
			http.Error(response, "authentication unavailable", http.StatusInternalServerError)
			return
		}
	}
	clearSessionCookie(response, request)
	response.WriteHeader(http.StatusNoContent)
}

func (handlers authenticationHandlers) authRequestAllowed(
	response http.ResponseWriter,
	request *http.Request,
	method string,
) bool {
	return requestAllowed(response, request, method, handlers.allowPlaintextCrew)
}

func requestAllowed(
	response http.ResponseWriter,
	request *http.Request,
	method string,
	allowPlaintextCrew bool,
) bool {
	setAuthHeaders(response)
	if request.Method != method {
		response.Header().Set("Allow", method)
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if !requestIsSecure(request) &&
		!allowPlaintextCrew &&
		!requestPlaintextAllowed(request) {
		http.Error(response, "secure transport required", http.StatusForbidden)
		return false
	}
	if method != http.MethodGet &&
		method != http.MethodHead &&
		method != http.MethodOptions &&
		!sameOrigin(request) {
		http.Error(response, "cross-origin request rejected", http.StatusForbidden)
		return false
	}
	return true
}

func decodeAuthJSON(response http.ResponseWriter, request *http.Request, destination any) error {
	return decodeAuthJSONLimit(response, request, destination, maxAuthBodyBytes)
}

func decodeAuthJSONLimit(
	response http.ResponseWriter,
	request *http.Request,
	destination any,
	limit int64,
) error {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("authentication request must be JSON")
	}
	request.Body = http.MaxBytesReader(response, request.Body, limit)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("authentication request must contain one JSON value")
	}
	return nil
}

func sameOrigin(request *http.Request) bool {
	if strings.EqualFold(request.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return false
	}
	origin := request.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	wantScheme := "http"
	if requestIsSecure(request) {
		wantScheme = "https"
	}
	return parsed.Scheme == wantScheme && parsed.Host == request.Host
}

func setSessionCookie(response http.ResponseWriter, request *http.Request, session auth.Session) {
	// Plaintext cookies exist only on a loopback listener, where browsers need
	// them for the documented local HTTP exception. Every TLS cookie is Secure.
	//nolint:gosec // G124 cannot infer the listener-level loopback restriction.
	http.SetCookie(response, &http.Cookie{
		Name:     sessionCookieName,
		Value:    session.Token,
		Path:     "/",
		Expires:  session.ExpiresAt,
		HttpOnly: true,
		Secure:   requestIsSecure(request),
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(response http.ResponseWriter, request *http.Request) {
	// Match the creation attributes so browsers remove loopback and TLS cookies.
	//nolint:gosec // G124 cannot infer the listener-level loopback restriction.
	http.SetCookie(response, &http.Cookie{
		Name:     sessionCookieName,
		Path:     "/",
		Expires:  time.Unix(1, 0).UTC(),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   requestIsSecure(request),
		SameSite: http.SameSiteLaxMode,
	})
}

func setAuthHeaders(response http.ResponseWriter) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
}

func writeAuthRateLimit(response http.ResponseWriter, retryAfter time.Duration) {
	seconds := max(1, int(retryAfter.Round(time.Second)/time.Second))
	response.Header().Set("Retry-After", strconv.Itoa(seconds))
	http.Error(response, "authentication failed", http.StatusTooManyRequests)
}

func listenerIsLoopback(address net.Addr) bool {
	tcpAddress, ok := address.(*net.TCPAddr)
	return ok && tcpAddress.IP.IsLoopback()
}
