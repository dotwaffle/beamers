package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/displays"
	"github.com/dotwaffle/beamers/internal/displaystream"
)

const (
	displayCookieName           = "beamers_display"
	displayEnrollmentCookieName = "beamers_display_enrollment"
	displayClaimRecoveryCookie  = "beamers_display_claim_recovery"
	displayConnectCookiePath    = "/beamers.display.v1.DisplayService"
)

type displayHandlers struct {
	authentication        *auth.Service
	service               *displays.Service
	stream                *displaystream.Hub
	logger                *slog.Logger
	allowPlaintextDisplay bool
	claimOrigin           string
	buildVersion          string
}

func registerDisplayRoutes(
	mux *http.ServeMux,
	authentication *auth.Service,
	service *displays.Service,
	stream *displaystream.Hub,
	buildVersion string,
	logger *slog.Logger,
	listenerAddress net.Addr,
) {
	handlers := displayHandlers{
		authentication: authentication, service: service, stream: stream, logger: logger,
		allowPlaintextDisplay: listenerIsLoopback(listenerAddress),
		claimOrigin:           "http://" + listenerAddress.String(),
		buildVersion:          buildVersion,
	}
	mux.HandleFunc("/display", handlers.display)
	mux.HandleFunc("/display/client.js", handlers.clientJavaScript)
	mux.HandleFunc("/display/assets/", handlers.clientJavaScript)
	mux.HandleFunc("/display/events", handlers.events)
	mux.HandleFunc("/admin/displays", handlers.list)
	mux.HandleFunc("/admin/displays/enroll", handlers.enroll)
	mux.HandleFunc("/admin/displays/{displayID}/assign", handlers.assign)
}

func (handlers displayHandlers) display(response http.ResponseWriter, request *http.Request) {
	if !requestAllowed(response, request, http.MethodGet, handlers.allowPlaintextDisplay) {
		return
	}
	code := cookieValue(request, displayEnrollmentCookieName)
	credential := cookieValue(request, displayCookieName)
	snapshot, currentErr := handlers.service.Current(request.Context(), credential)
	if currentErr == nil {
		clearDisplayEnrollmentCookie(response, request)
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("X-Beamers-Display-Asset", snapshot.AssetVersion)
		response.Header().Set("X-Beamers-Display-Protocol", snapshot.ProtocolVersion)
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		page := displays.DisplayPage(snapshot)
		if err := page.Render(request.Context(), response); err != nil {
			handlers.logger.ErrorContext(request.Context(), "write Display page", "error", err)
		}
		return
	}
	if !errors.Is(currentErr, displays.ErrDisplayAuthentication) {
		handlers.logger.ErrorContext(request.Context(), "Display authentication failed", "error", currentErr)
		http.Error(response, "Display unavailable", http.StatusInternalServerError)
		return
	}
	enrollment, err := handlers.service.EnrollmentForBrowser(request.Context(), code, credential)
	if err != nil {
		handlers.logger.ErrorContext(request.Context(), "Display Enrollment failed", "error", err)
		http.Error(response, "Display Enrollment unavailable", http.StatusInternalServerError)
		return
	}
	claimURL := url.URL{
		Path:     "/admin/displays/enroll",
		RawQuery: url.Values{"code": []string{enrollment.Code}}.Encode(),
	}
	qrCode, err := displays.EnrollmentQRCodeDataURL(handlers.claimOrigin + claimURL.String())
	if err != nil {
		handlers.logger.ErrorContext(request.Context(), "Display Enrollment QR rendering failed", "error", err)
		http.Error(response, "Display Enrollment unavailable", http.StatusInternalServerError)
		return
	}
	setDisplayCookies(response, request, enrollment)
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := displays.EnrollmentPage(enrollment.Code, qrCode).Render(request.Context(), response); err != nil {
		handlers.logger.ErrorContext(request.Context(), "write Display Enrollment page", "error", err)
	}
}

func (handlers displayHandlers) clientJavaScript(response http.ResponseWriter, request *http.Request) {
	if !requestAllowed(response, request, http.MethodGet, handlers.allowPlaintextDisplay) {
		return
	}
	if request.URL.Path != "/display/client.js" &&
		request.URL.Path != displays.ClientJavaScriptPath() {
		http.NotFound(response, request)
		return
	}
	cacheControl := "no-cache"
	if request.URL.Path == displays.ClientJavaScriptPath() {
		cacheControl = "public, max-age=31536000, immutable"
	}
	response.Header().Set("Cache-Control", cacheControl)
	response.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	if _, err := response.Write(displays.ClientJavaScript); err != nil {
		handlers.logger.ErrorContext(request.Context(), "write Display client", "error", err)
	}
}

func (handlers displayHandlers) list(response http.ResponseWriter, request *http.Request) {
	if !requestAllowed(response, request, http.MethodGet, handlers.allowPlaintextDisplay) {
		return
	}
	actor, ok := handlers.authenticate(response, request)
	if !ok {
		return
	}
	statuses, err := handlers.service.List(request.Context(), actor, handlers.stream.Cursor())
	if errors.Is(err, displays.ErrCrewRequired) {
		http.Error(response, "Active Event crew authority required", http.StatusForbidden)
		return
	}
	if err != nil {
		handlers.logger.ErrorContext(request.Context(), "list Displays failed", "error", err)
		http.Error(response, "Display list unavailable", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(statuses); err != nil {
		handlers.logger.ErrorContext(request.Context(), "write Display list", "error", err)
	}
}

func (handlers displayHandlers) assign(response http.ResponseWriter, request *http.Request) {
	if !requestAllowed(response, request, http.MethodPost, handlers.allowPlaintextDisplay) {
		return
	}
	actor, ok := handlers.authenticate(response, request)
	if !ok {
		return
	}
	displayID, err := positivePathID(request, "displayID")
	if err != nil {
		http.Error(response, "Display not found", http.StatusNotFound)
		return
	}
	var input struct {
		EventID          int      `json:"event_id"`
		LocationID       int      `json:"location_id"`
		ViewKey          string   `json:"view_key"`
		DisplayGroupKeys []string `json:"display_group_keys"`
		CommandID        string   `json:"command_id"`
	}
	if decodeErr := decodeAuthJSON(response, request, &input); decodeErr != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	assigned, err := handlers.service.Assign(request.Context(), actor, displays.AssignInput{
		DisplayID: displayID, EventID: input.EventID, LocationID: input.LocationID,
		ViewKey: input.ViewKey, DisplayGroupKeys: input.DisplayGroupKeys,
		CommandID: input.CommandID,
	})
	switch {
	case errors.Is(err, displays.ErrAdministratorRequired):
		http.Error(response, "Administrator authority required", http.StatusForbidden)
		return
	case errors.Is(err, displays.ErrInvalidDisplay), errors.Is(err, displays.ErrAssignmentReference):
		http.Error(response, "valid Event, Location, View, and command_id are required", http.StatusUnprocessableEntity)
		return
	case errors.Is(err, displays.ErrDisplayNotFound):
		http.Error(response, "Display not found", http.StatusNotFound)
		return
	case errors.Is(err, displays.ErrCommandConflict):
		http.Error(response, err.Error(), http.StatusConflict)
		return
	case err != nil:
		handlers.logger.ErrorContext(request.Context(), "assign Display failed", "error", err)
		http.Error(response, "Display Assignment unavailable", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(assigned); err != nil {
		handlers.logger.ErrorContext(request.Context(), "write Display Assignment", "error", err)
	}
	handlers.stream.Notify()
}

func (handlers displayHandlers) enroll(response http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		handlers.enrollmentClaimPage(response, request)
	case http.MethodPost:
		handlers.claimEnrollment(response, request)
	default:
		response.Header().Set("Allow", "GET, POST")
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (handlers displayHandlers) enrollmentClaimPage(response http.ResponseWriter, request *http.Request) {
	if !requestAllowed(response, request, http.MethodGet, handlers.allowPlaintextDisplay) {
		return
	}
	actor, ok := handlers.authenticate(response, request)
	if !ok {
		return
	}
	if !actor.Administrator {
		http.Error(response, "Administrator authority required", http.StatusForbidden)
		return
	}
	code := request.URL.Query().Get("code")
	commandID := "enroll-display-" + strings.ToLower(strings.ReplaceAll(code, "-", ""))
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	initialName := displayClaimRecoveryName(request, code)
	page := displays.EnrollmentClaimPage(code, commandID, handlers.buildVersion, initialName)
	if err := page.Render(request.Context(), response); err != nil {
		handlers.logger.ErrorContext(request.Context(), "write Display claim page", "error", err)
	}
}

func (handlers displayHandlers) claimEnrollment(response http.ResponseWriter, request *http.Request) {
	if !requestAllowed(response, request, http.MethodPost, handlers.allowPlaintextDisplay) {
		return
	}
	actor, ok := handlers.authenticate(response, request)
	if !ok {
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, maxAuthBodyBytes)
	if err := request.ParseForm(); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	if request.PostForm.Get("build_version") != handlers.buildVersion {
		recoveryCode := request.PostForm.Get("code")
		if len(recoveryCode) > 20 {
			recoveryCode = ""
		}
		setDisplayClaimRecoveryCookie(
			response,
			request,
			recoveryCode,
			request.PostForm.Get("name"),
		)
		reloadURL := "/admin/displays/enroll?" + url.Values{
			"code": []string{recoveryCode},
		}.Encode()
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		response.WriteHeader(http.StatusConflict)
		page := displays.EnrollmentClaimReloadPage(reloadURL)
		if err := page.Render(request.Context(), response); err != nil {
			handlers.logger.ErrorContext(request.Context(), "write reload-required page", "error", err)
		}
		return
	}
	created, err := handlers.service.ClaimEnrollment(request.Context(), actor, displays.ClaimInput{
		Code: request.PostForm.Get("code"), Name: request.PostForm.Get("name"),
		CommandID: request.PostForm.Get("command_id"),
	})
	switch {
	case errors.Is(err, displays.ErrAdministratorRequired):
		http.Error(response, "Administrator authority required", http.StatusForbidden)
		return
	case errors.Is(err, displays.ErrInvalidDisplay):
		http.Error(response, "valid Display code, name, and command_id are required", http.StatusUnprocessableEntity)
		return
	case errors.Is(err, displays.ErrEnrollmentUnavailable):
		http.Error(response, "Display Enrollment is unavailable", http.StatusConflict)
		return
	case errors.Is(err, displays.ErrCommandConflict):
		http.Error(response, err.Error(), http.StatusConflict)
		return
	case err != nil:
		handlers.logger.ErrorContext(request.Context(), "claim Display Enrollment failed", "error", err)
		http.Error(response, "Display Enrollment unavailable", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	clearDisplayClaimRecoveryCookie(response, request)
	response.WriteHeader(http.StatusCreated)
	if err := displays.EnrollmentClaimedPage(created).Render(request.Context(), response); err != nil {
		handlers.logger.ErrorContext(request.Context(), "write enrolled Display page", "error", err)
	}
}

func setDisplayClaimRecoveryCookie(
	response http.ResponseWriter,
	request *http.Request,
	code string,
	name string,
) {
	if len(code) > 20 {
		code = ""
	}
	nameRunes := []rune(name)
	if len(nameRunes) > 200 {
		name = string(nameRunes[:200])
	}
	value := base64.RawURLEncoding.EncodeToString([]byte(code + "\n" + name))
	//nolint:gosec // G124 cannot infer the listener-level loopback restriction.
	http.SetCookie(response, &http.Cookie{
		Name: displayClaimRecoveryCookie, Value: value,
		Path: "/admin/displays/enroll", MaxAge: 60, HttpOnly: true,
		Secure: request.TLS != nil, SameSite: http.SameSiteStrictMode,
	})
}

func displayClaimRecoveryName(request *http.Request, code string) string {
	cookie, err := request.Cookie(displayClaimRecoveryCookie)
	if err != nil {
		return ""
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return ""
	}
	storedCode, name, found := strings.Cut(string(decoded), "\n")
	if !found || storedCode != code {
		return ""
	}
	return name
}

func clearDisplayClaimRecoveryCookie(response http.ResponseWriter, request *http.Request) {
	//nolint:gosec // G124 cannot infer the listener-level loopback restriction.
	http.SetCookie(response, &http.Cookie{
		Name: displayClaimRecoveryCookie, Path: "/admin/displays/enroll",
		MaxAge: -1, HttpOnly: true,
		Secure: request.TLS != nil, SameSite: http.SameSiteStrictMode,
	})
}

func (handlers displayHandlers) authenticate(
	response http.ResponseWriter,
	request *http.Request,
) (auth.Account, bool) {
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil {
		http.Error(response, "authentication required", http.StatusUnauthorized)
		return auth.Account{}, false
	}
	actor, err := handlers.authentication.Authenticate(request.Context(), cookie.Value)
	if errors.Is(err, auth.ErrInvalidSession) {
		http.Error(response, "authentication required", http.StatusUnauthorized)
		return auth.Account{}, false
	}
	if err != nil {
		handlers.logger.ErrorContext(request.Context(), "Account session lookup failed", "error", err)
		http.Error(response, "authentication unavailable", http.StatusInternalServerError)
		return auth.Account{}, false
	}
	return actor, true
}

func cookieValue(request *http.Request, name string) string {
	cookie, err := request.Cookie(name)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func setDisplayCookies(response http.ResponseWriter, request *http.Request, enrollment displays.Enrollment) {
	for _, cookie := range []*http.Cookie{
		//nolint:gosec // G124 cannot infer the listener-level loopback restriction.
		{
			Name: displayCookieName, Value: enrollment.Credential, Path: "/display",
			Expires: enrollment.CredentialExpires, HttpOnly: true,
			Secure: request.TLS != nil, SameSite: http.SameSiteStrictMode,
		},
		//nolint:gosec // G124 cannot infer the listener-level loopback restriction.
		{
			Name: displayCookieName, Value: enrollment.Credential, Path: displayConnectCookiePath,
			Expires: enrollment.CredentialExpires, HttpOnly: true,
			Secure: request.TLS != nil, SameSite: http.SameSiteStrictMode,
		},
		//nolint:gosec // G124 cannot infer the listener-level loopback restriction.
		{
			Name: displayEnrollmentCookieName, Value: enrollment.Code, Path: "/display",
			Expires: enrollment.ExpiresAt, HttpOnly: true,
			Secure: request.TLS != nil, SameSite: http.SameSiteStrictMode,
		},
	} {
		http.SetCookie(response, cookie)
	}
}

func clearDisplayEnrollmentCookie(response http.ResponseWriter, request *http.Request) {
	// Match the creation attributes so the browser retains only its persistent credential.
	http.SetCookie(response, &http.Cookie{ //nolint:gosec // G124 cannot infer the listener-level loopback restriction.
		Name: displayEnrollmentCookieName, Path: "/display", Expires: time.Unix(1, 0).UTC(), MaxAge: -1,
		HttpOnly: true, Secure: request.TLS != nil, SameSite: http.SameSiteStrictMode,
	})
}
