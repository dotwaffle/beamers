package server

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/dotwaffle/beamers/internal/auth"
)

func (handlers authenticationHandlers) beginWebAuthnRegistration(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !handlers.authRequestAllowed(response, request, http.MethodPost) {
		return
	}
	actor, ok := handlers.webAuthnAccount(response, request)
	if !ok {
		return
	}
	rp, err := requestRelyingParty(request)
	if err != nil {
		http.Error(response, "WebAuthn requires a secure origin", http.StatusForbidden)
		return
	}
	registration, err := handlers.service.BeginWebAuthnRegistration(
		request.Context(),
		actor,
		rp,
	)
	if err != nil {
		handlers.webAuthnError(response, request, "begin WebAuthn registration", err)
		return
	}
	handlers.writeWebAuthnJSON(response, request, registration)
}

func (handlers authenticationHandlers) finishWebAuthnRegistration(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !handlers.authRequestAllowed(response, request, http.MethodPost) {
		return
	}
	actor, ok := handlers.webAuthnAccount(response, request)
	if !ok {
		return
	}
	var input struct {
		CeremonyID string          `json:"ceremony_id"`
		Name       string          `json:"name"`
		Credential json.RawMessage `json:"credential"`
	}
	if err := decodeAuthJSONLimit(
		response,
		request,
		&input,
		maxWebAuthnBodyBytes,
	); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	rp, err := requestRelyingParty(request)
	if err != nil {
		http.Error(response, "WebAuthn requires a secure origin", http.StatusForbidden)
		return
	}
	credential, err := handlers.service.FinishWebAuthnRegistration(
		request.Context(),
		actor,
		rp,
		input.CeremonyID,
		input.Name,
		input.Credential,
	)
	switch {
	case errors.Is(err, auth.ErrAuthenticationFailed):
		http.Error(response, "WebAuthn registration failed", http.StatusUnauthorized)
	case errors.Is(err, auth.ErrAccountExists):
		http.Error(response, "WebAuthn Credential already registered", http.StatusConflict)
	case errors.Is(err, auth.ErrCommandConflict):
		http.Error(response, "WebAuthn registration conflict", http.StatusConflict)
	case errors.Is(err, auth.ErrInvalidAccountDetails):
		http.Error(response, "invalid Credential name", http.StatusBadRequest)
	case err != nil:
		handlers.webAuthnError(response, request, "finish WebAuthn registration", err)
	default:
		handlers.writeWebAuthnJSON(response, request, credential)
	}
}

func (handlers authenticationHandlers) beginWebAuthnSignIn(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !handlers.authRequestAllowed(response, request, http.MethodPost) {
		return
	}
	var input struct {
		Handle string `json:"handle"`
	}
	if err := decodeAuthJSON(response, request, &input); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	clientKey, accountKey := signInFailureKeys(
		request,
		strings.ToLower(strings.TrimSpace(input.Handle)),
	)
	if retryAfter, blocked := handlers.limiter.reserve(clientKey, accountKey); blocked {
		writeAuthRateLimit(response, retryAfter)
		return
	}
	rp, err := requestRelyingParty(request)
	if err != nil {
		handlers.limiter.release(clientKey, accountKey)
		http.Error(response, "WebAuthn requires a secure origin", http.StatusForbidden)
		return
	}
	signIn, err := handlers.service.BeginWebAuthnSignIn(request.Context(), input.Handle, rp)
	if errors.Is(err, auth.ErrAuthenticationFailed) {
		http.Error(response, "authentication failed", http.StatusUnauthorized)
		return
	}
	if err != nil {
		handlers.limiter.release(clientKey, accountKey)
		handlers.webAuthnError(response, request, "begin WebAuthn sign-in", err)
		return
	}
	handlers.writeWebAuthnJSON(response, request, signIn)
}

func (handlers authenticationHandlers) finishWebAuthnSignIn(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !handlers.authRequestAllowed(response, request, http.MethodPost) {
		return
	}
	var input struct {
		CeremonyID string          `json:"ceremony_id"`
		Credential json.RawMessage `json:"credential"`
	}
	if err := decodeAuthJSONLimit(
		response,
		request,
		&input,
		maxWebAuthnBodyBytes,
	); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	rp, err := requestRelyingParty(request)
	if err != nil {
		http.Error(response, "WebAuthn requires a secure origin", http.StatusForbidden)
		return
	}
	session, err := handlers.service.FinishWebAuthnSignIn(
		request.Context(),
		rp,
		input.CeremonyID,
		input.Credential,
	)
	if errors.Is(err, auth.ErrAuthenticationFailed) {
		http.Error(response, "authentication failed", http.StatusUnauthorized)
		return
	}
	if err != nil {
		handlers.webAuthnError(response, request, "finish WebAuthn sign-in", err)
		return
	}
	clientKey, accountKey := signInFailureKeys(request, session.Account.Handle)
	handlers.limiter.release(clientKey)
	handlers.limiter.reset(accountKey)
	setSessionCookie(response, request, session)
	response.WriteHeader(http.StatusNoContent)
}

func (handlers authenticationHandlers) webAuthnAccount(
	response http.ResponseWriter,
	request *http.Request,
) (auth.Account, bool) {
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil {
		http.Error(response, "authentication required", http.StatusUnauthorized)
		return auth.Account{}, false
	}
	account, err := handlers.service.Authenticate(request.Context(), cookie.Value)
	if errors.Is(err, auth.ErrInvalidSession) {
		http.Error(response, "authentication required", http.StatusUnauthorized)
		return auth.Account{}, false
	}
	if err != nil {
		handlers.webAuthnError(response, request, "authenticate WebAuthn Account", err)
		return auth.Account{}, false
	}
	return account, true
}

func (handlers authenticationHandlers) webAuthnError(
	response http.ResponseWriter,
	request *http.Request,
	action string,
	err error,
) {
	handlers.logger.ErrorContext(
		request.Context(),
		action+" failed",
		"component", "authentication",
		"error", err,
	)
	http.Error(response, "authentication unavailable", http.StatusInternalServerError)
}

func requestRelyingParty(request *http.Request) (auth.WebAuthnRelyingParty, error) {
	scheme := "http"
	if requestIsSecure(request) {
		scheme = "https"
	}
	origin, err := url.Parse(scheme + "://" + request.Host)
	if err != nil || origin.Hostname() == "" || origin.User != nil ||
		net.ParseIP(origin.Hostname()) != nil {
		return auth.WebAuthnRelyingParty{}, errors.New("invalid WebAuthn origin")
	}
	if scheme != "https" && !strings.EqualFold(origin.Hostname(), "localhost") {
		return auth.WebAuthnRelyingParty{}, errors.New("insecure WebAuthn origin")
	}
	return auth.WebAuthnRelyingParty{
		ID: origin.Hostname(), Origin: origin.String(), DisplayName: "Beamers",
	}, nil
}

func (handlers authenticationHandlers) writeWebAuthnJSON(
	response http.ResponseWriter,
	request *http.Request,
	value any,
) {
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(value); err != nil {
		handlers.logger.ErrorContext(
			request.Context(),
			"write WebAuthn response failed",
			"component", "authentication",
			"error", err,
		)
	}
}
