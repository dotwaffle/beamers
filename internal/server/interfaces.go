package server

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"
)

const (
	defaultRequestTimeout = 10 * time.Second
	uploadRequestTimeout  = 5 * time.Minute
)

type interfaceKind uint8

const (
	crewInterface interfaceKind = iota
	displayInterface
	uploadInterface
	publicInterface
	probeInterface
)

type interfacePolicy struct {
	listenerAddress      net.Addr
	trustedProxies       []netip.Prefix
	allowInsecureCrew    bool
	allowInsecureDisplay bool
	publicOnly           bool
}

type interfaceRequestContextKey struct{}

type interfaceRequest struct {
	secure         bool
	clientAddress  string
	allowPlaintext bool
}

func protectInterfaces(next http.Handler, policy interfacePolicy) http.Handler {
	recoveryLimiter := newAuthFailureLimiter(time.Now)
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		kind := requestInterface(request.URL.Path)
		if policy.publicOnly && kind != publicInterface {
			http.NotFound(response, request)
			return
		}

		peer := remoteAddress(request.RemoteAddr)
		trustedPeer := addressInPrefixes(peer, policy.trustedProxies)
		secure := request.TLS != nil
		clientAddress := peer.String()
		if !peer.IsValid() {
			clientAddress = request.RemoteAddr
		}
		if trustedPeer {
			secure = forwardedSchemeIsHTTPS(request.Header.Get("X-Forwarded-Proto"))
			clientAddress = forwardedClientAddress(request.Header.Values("X-Forwarded-For"), peer, policy.trustedProxies)
		}

		allowPlaintext := listenerIsLoopback(policy.listenerAddress) ||
			kind == publicInterface ||
			kind == probeInterface ||
			kind == crewInterface && policy.allowInsecureCrew ||
			kind == displayInterface && policy.allowInsecureDisplay
		if !secure && !allowPlaintext {
			setBrowserProtectionHeaders(response, false)
			http.Error(response, "secure transport required", http.StatusForbidden)
			return
		}

		warnings := insecureWarnings(kind, policy)
		details := interfaceRequest{
			secure: secure, clientAddress: clientAddress,
			allowPlaintext: allowPlaintext,
		}
		requestContext := context.WithValue(
			request.Context(),
			interfaceRequestContextKey{},
			details,
		)
		cancel := func() {}
		if timeout := requestTimeout(request.URL.Path); timeout > 0 {
			requestContext, cancel = context.WithTimeout(requestContext, timeout)
		}
		defer cancel()
		request = request.WithContext(requestContext)
		setBrowserProtectionHeaders(response, secure)
		if deadline, ok := requestContext.Deadline(); ok {
			controller := http.NewResponseController(response)
			if err := controller.SetReadDeadline(deadline); err != nil &&
				!errors.Is(err, http.ErrNotSupported) {
				http.Error(response, "request deadline unavailable", http.StatusInternalServerError)
				return
			}
			if err := controller.SetWriteDeadline(deadline); err != nil &&
				!errors.Is(err, http.ErrNotSupported) {
				http.Error(response, "request deadline unavailable", http.StatusInternalServerError)
				return
			}
			defer func() {
				// A later request on this keep-alive connection owns its deadline.
				_ = controller.SetReadDeadline(time.Time{})
				_ = controller.SetWriteDeadline(time.Time{})
			}()
		}
		if warnings != "" {
			response.Header().Set("Warning", `299 Beamers "`+warnings+`"`)
			response.Header().Set("X-Beamers-Insecure-Mode", warnings)
		}
		var recoveryResponse *statusResponse
		if key, limited := recoveryLimitKey(request); limited &&
			request.Method == http.MethodPost &&
			sameOrigin(request) {
			if retryAfter, blocked := recoveryLimiter.reserve(key); blocked {
				writeAuthRateLimit(response, retryAfter)
				return
			}
			recoveryResponse = &statusResponse{
				ResponseWriter: response,
				status:         http.StatusOK,
			}
			response = recoveryResponse
			defer func() {
				if recoveryResponse.status != http.StatusUnauthorized &&
					recoveryResponse.status != http.StatusForbidden {
					recoveryLimiter.release(key)
				}
			}()
		}
		request.Body = http.MaxBytesReader(response, request.Body, requestBodyLimit(request.URL.Path))
		if warnings != "" && crewWarningPage(request.URL.Path) {
			warningResponse := &crewWarningResponse{
				ResponseWriter: response,
				status:         http.StatusOK,
			}
			next.ServeHTTP(warningResponse, request)
			warningResponse.flush(warnings)
			return
		}
		next.ServeHTTP(response, request)
	})
}

type statusResponse struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (response *statusResponse) WriteHeader(status int) {
	if response.wroteHeader {
		return
	}
	response.status = status
	response.wroteHeader = true
	response.ResponseWriter.WriteHeader(status)
}

func (response *statusResponse) Write(content []byte) (int, error) {
	if !response.wroteHeader {
		response.WriteHeader(http.StatusOK)
	}
	return response.ResponseWriter.Write(content)
}

type crewWarningResponse struct {
	http.ResponseWriter
	body        bytes.Buffer
	status      int
	wroteHeader bool
}

func (response *crewWarningResponse) WriteHeader(status int) {
	if response.wroteHeader {
		return
	}
	response.status = status
	response.wroteHeader = true
}

func (response *crewWarningResponse) Write(content []byte) (int, error) {
	response.wroteHeader = true
	return response.body.Write(content)
}

func (response *crewWarningResponse) flush(warnings string) {
	content := response.body.Bytes()
	if strings.HasPrefix(response.Header().Get("Content-Type"), "text/html") {
		if bodyStart := bytes.Index(content, []byte("<body")); bodyStart >= 0 {
			if tagEnd := bytes.IndexByte(content[bodyStart:], '>'); tagEnd >= 0 {
				insertAt := bodyStart + tagEnd + 1
				banner := []byte(`<aside role="alert" style="padding:1rem;color:#fff;` +
					`background:#8b0000;font:700 1rem/1.4 system-ui">` +
					`Security warning: ` + warnings +
					`; network peers may observe credentials, sessions, or Display content.` +
					`</aside>`)
				content = append(content[:insertAt], append(banner, content[insertAt:]...)...)
				response.Header().Del("Content-Length")
			}
		}
	}
	response.ResponseWriter.WriteHeader(response.status)
	_, _ = response.ResponseWriter.Write(content)
}

func crewWarningPage(path string) bool {
	if path == "/admin/displays/enroll" || strings.HasSuffix(path, "/confirmation") {
		return true
	}
	program, found := strings.CutPrefix(path, "/crew/program/")
	return found && program != "" && !strings.ContainsRune(program, '/')
}

func recoveryLimitKey(request *http.Request) (authFailureKey, bool) {
	var limit int
	switch request.URL.Path {
	case "/admin/restores/preview":
		limit = 10
	case "/admin/restores/apply", "/admin/upgrade/apply":
		limit = 5
	default:
		return authFailureKey{}, false
	}
	return authFailureKey{
		value: "recovery|" + request.URL.Path + "|" + requestClientAddress(request),
		limit: limit,
	}, true
}

func requestInterface(path string) interfaceKind {
	switch {
	case path == "/livez" || path == "/readyz":
		return probeInterface
	case path == "/schedule" ||
		strings.HasPrefix(path, "/schedule/") ||
		path == "/assets/schedule.css" ||
		strings.HasPrefix(path, "/results/"):
		return publicInterface
	case path == "/display" ||
		strings.HasPrefix(path, "/display/") ||
		strings.HasPrefix(path, displayConnectCookiePath+"/"):
		return displayInterface
	case strings.HasPrefix(path, "/upload/"):
		return uploadInterface
	default:
		return crewInterface
	}
}

func insecureWarnings(kind interfaceKind, policy interfacePolicy) string {
	var warnings []string
	if policy.allowInsecureCrew && kind == crewInterface {
		warnings = append(warnings, "insecure Crew mode enabled")
	}
	if policy.allowInsecureDisplay && (kind == displayInterface || kind == crewInterface) {
		warnings = append(warnings, "insecure Display mode enabled")
	}
	return strings.Join(warnings, "; ")
}

func setBrowserProtectionHeaders(response http.ResponseWriter, secure bool) {
	response.Header().Set(
		"Content-Security-Policy",
		"default-src 'self'; base-uri 'none'; connect-src 'self'; "+
			"font-src 'self'; form-action 'self'; frame-ancestors 'none'; "+
			"img-src 'self' data:; object-src 'none'; script-src 'self'; "+
			"style-src 'self' 'unsafe-inline'",
	)
	response.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	response.Header().Set("Referrer-Policy", "no-referrer")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("X-Frame-Options", "DENY")
	if secure {
		response.Header().Set("Strict-Transport-Security", "max-age=31536000")
	}
}

func requestBodyLimit(path string) int64 {
	switch {
	case path == "/admin/restores/preview":
		return maxRestoreUploadBytes
	case strings.HasPrefix(path, "/upload/") ||
		strings.HasSuffix(path, "/attachments"):
		return (64 << 20) + (1 << 20)
	default:
		return 1 << 20
	}
}

func requestTimeout(path string) time.Duration {
	switch {
	case persistentRequest(path):
		return 0
	case path == "/admin/restores/preview" || path == "/admin/restores/apply":
		return restoreOperationTimeout
	case strings.HasPrefix(path, "/upload/") ||
		strings.HasSuffix(path, "/attachments"):
		return uploadRequestTimeout
	default:
		return defaultRequestTimeout
	}
}

func requestIsSecure(request *http.Request) bool {
	if details, ok := request.Context().Value(interfaceRequestContextKey{}).(interfaceRequest); ok {
		return details.secure
	}
	return request.TLS != nil
}

func requestPlaintextAllowed(request *http.Request) bool {
	details, ok := request.Context().Value(interfaceRequestContextKey{}).(interfaceRequest)
	return ok && details.allowPlaintext
}

func requestClientAddress(request *http.Request) string {
	if details, ok := request.Context().Value(interfaceRequestContextKey{}).(interfaceRequest); ok &&
		details.clientAddress != "" {
		return details.clientAddress
	}
	return authClientAddressFromRemote(request.RemoteAddr)
}

func forwardedSchemeIsHTTPS(value string) bool {
	parts := strings.Split(value, ",")
	return len(parts) == 1 && strings.EqualFold(strings.TrimSpace(parts[0]), "https")
}

func forwardedClientAddress(values []string, peer netip.Addr, trusted []netip.Prefix) string {
	addresses := make([]netip.Addr, 0, len(values)+1)
	for _, value := range values {
		for part := range strings.SplitSeq(value, ",") {
			address, err := netip.ParseAddr(strings.TrimSpace(part))
			if err != nil {
				return peer.String()
			}
			addresses = append(addresses, address.Unmap())
		}
	}
	addresses = append(addresses, peer)
	for _, address := range slices.Backward(addresses) {
		if !addressInPrefixes(address, trusted) {
			return address.String()
		}
	}
	if len(addresses) > 0 {
		return addresses[0].String()
	}
	return peer.String()
}

func remoteAddress(value string) netip.Addr {
	host, _, err := net.SplitHostPort(value)
	if err == nil {
		value = host
	}
	address, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}
	}
	return address.Unmap()
}

func addressInPrefixes(address netip.Addr, prefixes []netip.Prefix) bool {
	if !address.IsValid() {
		return false
	}
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
