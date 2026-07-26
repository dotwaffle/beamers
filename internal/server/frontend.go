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
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/frontend"
)

const (
	csrfCookieName = "beamers_csrf"
	csrfTokenBytes = 32
)

type frontendHandlers struct {
	authentication *auth.Service
	logger         *slog.Logger
	limiter        *authFailureLimiter
	random         io.Reader
}

func registerFrontendRoutes(
	mux *routeMux,
	authentication *auth.Service,
	limiter *authFailureLimiter,
	logger *slog.Logger,
) error {
	handlers := frontendHandlers{
		authentication: authentication,
		logger:         logger,
		limiter:        limiter,
		random:         rand.Reader,
	}
	formRoute := publicRoute()
	formRoute.maxBodyBytes = maxAuthBodyBytes
	mux.HandleFunc("/", publicRoute(), handlers.root)
	mux.HandleFunc("/setup", formRoute, handlers.setup)
	mux.HandleFunc("/sign-in", formRoute, handlers.signIn)
	mux.HandleFunc("/sign-out", formRoute, handlers.signOut)
	for _, path := range []string{
		frontend.StylesheetPath,
		frontend.HTMXPath,
		frontend.SSEPath,
	} {
		handler, err := handlers.asset(path)
		if err != nil {
			return err
		}
		mux.HandleFunc(path, publicRoute(), handler)
	}
	return nil
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
	if cookie, cookieErr := request.Cookie(sessionCookieName); cookieErr == nil {
		account, authenticateErr := handlers.authentication.Authenticate(
			request.Context(),
			cookie.Value,
		)
		switch {
		case authenticateErr == nil:
			accountName = account.Name
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
	handlers.render(
		response,
		request,
		http.StatusOK,
		frontend.Root(setupRequired, accountName, csrfToken),
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
		handlers.render(response, request, http.StatusOK, frontend.Setup(csrfToken, ""))
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
		handlers.render(response, request, http.StatusBadRequest, frontend.Setup(csrfToken, "Check the Account details and try again."))
	case errors.Is(err, auth.ErrAuthenticationFailed):
		handlers.render(response, request, http.StatusUnauthorized, frontend.Setup(csrfToken, "Setup token is invalid or expired."))
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
				http.Redirect(response, request, "/", http.StatusSeeOther)
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
		handlers.render(response, request, http.StatusOK, frontend.SignIn(csrfToken, "", ""))
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
		handlers.render(response, request, http.StatusUnauthorized, frontend.SignIn(csrfToken, "Sign-in failed.", handle))
	case err != nil:
		handlers.limiter.release(clientKey, accountKey)
		handlers.frontendError(response, request, "sign in Account", err)
	default:
		handlers.limiter.release(clientKey, accountKey)
		handlers.limiter.reset(accountKey)
		setSessionCookie(response, request, session)
		http.Redirect(response, request, "/", http.StatusSeeOther)
	}
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
	cookie, err := request.Cookie(csrfCookieName)
	provided := request.Form.Get("csrf_token")
	if err != nil || !validCSRFToken(cookie.Value) || !validCSRFToken(provided) ||
		subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(provided)) != 1 {
		http.Error(response, "invalid CSRF proof", http.StatusForbidden)
		return false
	}
	return true
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
