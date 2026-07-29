package server

import (
	"bytes"
	"context"
	"errors"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dotwaffle/beamers/internal/frontend"
)

const (
	defaultRequestTimeout = 10 * time.Second
	uploadRequestTimeout  = 5 * time.Minute
	defaultRequestBytes   = 1 << 20
	maxUploadRequestBytes = (64 << 20) + (1 << 20)
)

type interfaceKind uint8

const (
	unspecifiedInterface interfaceKind = iota
	crewInterface
	displayInterface
	publicInterface
	probeInterface
)

type interfacePolicy struct {
	listenerAddress      net.Addr
	trustedProxies       []netip.Prefix
	allowInsecureCrew    bool
	allowInsecureDisplay bool
	publicOnly           bool
	demo                 bool
}

type routeContract struct {
	kind               interfaceKind
	timeout            time.Duration
	maxBodyBytes       int64
	persistent         bool
	mutatesOnRead      bool
	browserWarningPage bool
	recoveryLimit      int
}

func crewRoute() routeContract {
	return routeContract{kind: crewInterface}
}

func displayRoute() routeContract {
	return routeContract{kind: displayInterface}
}

func publicRoute() routeContract {
	return routeContract{kind: publicInterface}
}

func browserPageRoute() routeContract {
	return routeContract{kind: publicInterface, browserWarningPage: true}
}

func backstagePageRoute() routeContract {
	return routeContract{kind: crewInterface, browserWarningPage: true}
}

func probeRoute() routeContract {
	return routeContract{kind: probeInterface}
}

type routeMux struct {
	mux       *http.ServeMux
	handler   http.Handler
	contracts map[string]routeContract
}

func newRouteMux() *routeMux {
	mux := http.NewServeMux()
	return &routeMux{
		mux:       mux,
		handler:   mux,
		contracts: make(map[string]routeContract),
	}
}

func (routes *routeMux) Handle(
	pattern string,
	contract routeContract,
	handler http.Handler,
) {
	routes.register(pattern, contract)
	routes.mux.Handle(pattern, handler)
}

func (routes *routeMux) HandleFunc(
	pattern string,
	contract routeContract,
	handler http.HandlerFunc,
) {
	routes.register(pattern, contract)
	routes.mux.HandleFunc(pattern, handler)
}

func (routes *routeMux) register(pattern string, contract routeContract) {
	if pattern == "" || contract.timeout < 0 || contract.maxBodyBytes < 0 ||
		contract.persistent && contract.timeout != 0 ||
		contract.recoveryLimit < 0 ||
		contract.kind <= unspecifiedInterface || contract.kind > probeInterface {
		panic("invalid route contract")
	}
	if _, exists := routes.contracts[pattern]; exists {
		panic("duplicate route contract")
	}
	routes.contracts[pattern] = contract
}

func (routes *routeMux) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	routes.handler.ServeHTTP(response, request)
}

func (routes *routeMux) contract(request *http.Request) (routeContract, bool) {
	_, pattern := routes.mux.Handler(request)
	contract, ok := routes.contracts[pattern]
	return contract, ok
}

type interfaceRequestContextKey struct{}

type interfaceRequest struct {
	secure         bool
	clientAddress  string
	allowPlaintext bool
	publicOnly     bool
}

type routeHandler interface {
	http.Handler
	contract(*http.Request) (routeContract, bool)
}

func protectInterfaces(
	next routeHandler,
	policy interfacePolicy,
) http.Handler {
	recoveryLimiter := newAuthFailureLimiter(time.Now)
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
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
		setBrowserProtectionHeaders(response, secure)

		contract, found := next.contract(request)
		if !found {
			if unmatchedBrowserRequest(request) {
				serveBrowserRecovery(response, request, http.StatusNotFound)
			} else {
				http.NotFound(response, request)
			}
			return
		}
		kind := contract.kind
		if policy.publicOnly && kind != publicInterface {
			if contract.browserWarningPage && acceptsHTML(request) {
				serveBrowserRecovery(response, request, http.StatusNotFound)
			} else {
				http.NotFound(response, request)
			}
			return
		}

		allowPlaintext := listenerIsLoopback(policy.listenerAddress) ||
			kind == publicInterface ||
			kind == probeInterface ||
			kind == crewInterface && policy.allowInsecureCrew ||
			kind == displayInterface && policy.allowInsecureDisplay
		if !secure && !allowPlaintext {
			serveRouteError(
				response,
				request,
				contract,
				http.StatusForbidden,
				"secure transport required",
			)
			return
		}

		warnings := insecureWarnings(kind, policy)
		details := interfaceRequest{
			secure: secure, clientAddress: clientAddress,
			allowPlaintext: allowPlaintext, publicOnly: policy.publicOnly,
		}
		requestContext := context.WithValue(
			request.Context(),
			interfaceRequestContextKey{},
			details,
		)
		cancel := func() {}
		if timeout := contract.requestTimeout(); timeout > 0 {
			requestContext, cancel = context.WithTimeout(requestContext, timeout)
		}
		defer cancel()
		request = request.WithContext(requestContext)
		if deadline, ok := requestContext.Deadline(); ok {
			controller := http.NewResponseController(response)
			if err := controller.SetReadDeadline(deadline); err != nil &&
				!errors.Is(err, http.ErrNotSupported) {
				serveRouteError(
					response,
					request,
					contract,
					http.StatusInternalServerError,
					"request deadline unavailable",
				)
				return
			}
			if err := controller.SetWriteDeadline(deadline); err != nil &&
				!errors.Is(err, http.ErrNotSupported) {
				serveRouteError(
					response,
					request,
					contract,
					http.StatusInternalServerError,
					"request deadline unavailable",
				)
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
		if key, limited := recoveryLimitKey(request, contract); limited &&
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
		request.Body = http.MaxBytesReader(response, request.Body, contract.requestBodyLimit())
		if contract.browserWarningPage && !contract.persistent && acceptsHTML(request) {
			recoveryResponse := &browserRecoveryResponse{
				ResponseWriter: response,
				request:        request,
			}
			response = recoveryResponse
			defer recoveryResponse.flush()
		}
		if warnings != "" && contract.browserWarningPage {
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

func serveRouteError(
	response http.ResponseWriter,
	request *http.Request,
	contract routeContract,
	status int,
	message string,
) {
	if contract.browserWarningPage && acceptsHTML(request) && browserRecoveryStatus(status) {
		serveBrowserRecovery(response, request, status)
		return
	}
	http.Error(response, message, status)
}

func unmatchedBrowserRequest(request *http.Request) bool {
	if !acceptsHTML(request) {
		return false
	}
	if strings.HasPrefix(request.URL.Path, "/results/") &&
		(strings.HasSuffix(request.URL.Path, ".json") ||
			strings.HasSuffix(request.URL.Path, ".txt")) {
		return false
	}
	for _, prefix := range []string{
		"/admin/",
		"/api/",
		"/assets/",
		"/auth/",
		"/beamers.",
		"/crew/",
		"/diagnostics",
		"/display",
		"/livez",
		"/public/",
		"/readyz",
		"/schedule/events",
		"/upload/",
	} {
		if strings.HasPrefix(request.URL.Path, prefix) {
			return false
		}
	}
	return true
}

func acceptsHTML(request *http.Request) bool {
	for _, value := range request.Header.Values("Accept") {
		for item := range strings.SplitSeq(value, ",") {
			mediaType, parameters, err := mime.ParseMediaType(strings.TrimSpace(item))
			if err != nil || mediaType != "text/html" {
				continue
			}
			quality := 1.0
			if value := parameters["q"]; value != "" {
				quality, err = strconv.ParseFloat(value, 64)
			}
			if err == nil && quality > 0 {
				return true
			}
		}
	}
	return false
}

type browserRecoveryResponse struct {
	http.ResponseWriter
	request     *http.Request
	status      int
	wroteHeader bool
	recovering  bool
}

func (response *browserRecoveryResponse) WriteHeader(status int) {
	if response.wroteHeader {
		return
	}
	response.status = status
	response.wroteHeader = true
	response.recovering = browserRecoveryStatus(status)
	if !response.recovering {
		response.ResponseWriter.WriteHeader(status)
	}
}

func (response *browserRecoveryResponse) Write(content []byte) (int, error) {
	if !response.wroteHeader {
		response.WriteHeader(http.StatusOK)
	}
	if response.recovering {
		return len(content), nil
	}
	return response.ResponseWriter.Write(content)
}

func (response *browserRecoveryResponse) flush() {
	if response.recovering {
		serveBrowserRecovery(response.ResponseWriter, response.request, response.status)
	}
}

func browserRecoveryStatus(status int) bool {
	return status == http.StatusForbidden ||
		status == http.StatusNotFound ||
		status == http.StatusInternalServerError
}

func serveBrowserRecovery(
	response http.ResponseWriter,
	request *http.Request,
	status int,
) {
	title, message := browserRecoveryCopy(status)
	var content bytes.Buffer
	if err := frontend.RecoveryPage(title, message).Render(request.Context(), &content); err != nil {
		http.Error(response, http.StatusText(status), status)
		return
	}
	response.Header().Del("Content-Length")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.WriteHeader(status)
	if request.Method != http.MethodHead {
		_, _ = response.Write(content.Bytes())
	}
}

func browserRecoveryCopy(status int) (string, string) {
	switch status {
	case http.StatusForbidden:
		return "Access denied", "You do not have access to this page."
	case http.StatusNotFound:
		return "Page not found", "The page could not be found."
	default:
		return "Something went wrong", "Beamers could not load this page."
	}
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

func recoveryLimitKey(
	request *http.Request,
	contract routeContract,
) (authFailureKey, bool) {
	if contract.recoveryLimit == 0 {
		return authFailureKey{}, false
	}
	return authFailureKey{
		value: "recovery|" + request.URL.Path + "|" + requestClientAddress(request),
		limit: contract.recoveryLimit,
	}, true
}

func insecureWarnings(kind interfaceKind, policy interfacePolicy) string {
	var warnings []string
	if policy.demo {
		warnings = append(warnings, "demo mode uses plaintext transport and shared credentials")
	}
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
	response.Header().Set("Referrer-Policy", "same-origin")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("X-Frame-Options", "DENY")
	if secure {
		response.Header().Set("Strict-Transport-Security", "max-age=31536000")
	}
}

func (contract routeContract) requestBodyLimit() int64 {
	if contract.maxBodyBytes > 0 {
		return contract.maxBodyBytes
	}
	return defaultRequestBytes
}

func (contract routeContract) requestTimeout() time.Duration {
	switch {
	case contract.persistent:
		return 0
	case contract.timeout > 0:
		return contract.timeout
	}
	return defaultRequestTimeout
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
