package server

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestRoutePolicyMatchesRegisteredRoutesAndFailsClosed(t *testing.T) {
	routes := newRouteMux()
	routes.HandleFunc(
		"/results/{eventID}",
		publicRoute(),
		func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(http.StatusNoContent)
		},
	)
	handler := protectInterfaces(routes, interfacePolicy{
		listenerAddress: &net.TCPAddr{IP: net.IPv4zero, Port: 8081},
		publicOnly:      true,
	})

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/results/7",
		http.NoBody,
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("registered route = %d, want %d", response.Code, http.StatusNoContent)
	}

	request = httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/unregistered",
		http.NoBody,
	)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unregistered route = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestInterfacePolicyKeepsInsecureCrewAndDisplaySeparate(t *testing.T) {
	listener := &net.TCPAddr{IP: net.IPv4zero, Port: 8080}
	handler := protectTestRoutes(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}), map[string]routeContract{
		"/display":      displayRoute(),
		"/auth/sign-in": crewRoute(),
	}, interfacePolicy{
		listenerAddress:      listener,
		allowInsecureDisplay: true,
	})

	display := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"http://beamers.test/display",
		http.NoBody,
	)
	display.RemoteAddr = "192.0.2.10:1234"
	displayResponse := httptest.NewRecorder()
	handler.ServeHTTP(displayResponse, display)
	if displayResponse.Code != http.StatusNoContent ||
		!strings.Contains(displayResponse.Header().Get("Warning"), "insecure Display") {
		t.Fatalf(
			"insecure Display response = %d, headers %v",
			displayResponse.Code,
			displayResponse.Header(),
		)
	}

	crew := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"http://beamers.test/auth/sign-in",
		http.NoBody,
	)
	crew.RemoteAddr = "192.0.2.10:1234"
	crewResponse := httptest.NewRecorder()
	handler.ServeHTTP(crewResponse, crew)
	if crewResponse.Code != http.StatusForbidden {
		t.Fatalf("insecure Crew response = %d, want %d", crewResponse.Code, http.StatusForbidden)
	}
}

func TestInterfacePolicyTrustsForwardingOnlyFromConfiguredProxy(t *testing.T) {
	listener := &net.TCPAddr{IP: net.IPv4zero, Port: 8080}
	trusted := netip.MustParsePrefix("192.0.2.0/24")
	handler := protectTestRoutes(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !requestIsSecure(request) {
			t.Error("trusted proxy request is not secure")
		}
		if got := requestClientAddress(request); got != "198.51.100.9" {
			t.Errorf("trusted proxy client = %q, want 198.51.100.9", got)
		}
		http.SetCookie(response, &http.Cookie{Name: "test", Value: "value", Secure: requestIsSecure(request)})
	}), map[string]routeContract{
		"/auth/session": crewRoute(),
	}, interfacePolicy{listenerAddress: listener, trustedProxies: []netip.Prefix{trusted}})

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"http://beamers.test/auth/session",
		http.NoBody,
	)
	request.RemoteAddr = "192.0.2.4:1234"
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("X-Forwarded-For", "198.51.100.9, 192.0.2.3")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Header().Get("Set-Cookie"), "Secure") {
		t.Fatalf("trusted proxy response = %d, headers %v", response.Code, response.Header())
	}

	untrusted := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"http://beamers.test/auth/session",
		http.NoBody,
	)
	untrusted.RemoteAddr = "203.0.113.5:1234"
	untrusted.Header.Set("X-Forwarded-Proto", "https")
	untrustedResponse := httptest.NewRecorder()
	handler.ServeHTTP(untrustedResponse, untrusted)
	if untrustedResponse.Code != http.StatusForbidden {
		t.Fatalf(
			"untrusted proxy response = %d, want %d",
			untrustedResponse.Code,
			http.StatusForbidden,
		)
	}
}

func TestPublicListenerOnlyServesAllowlistedRoutes(t *testing.T) {
	publicRoutes := make(map[string]routeContract)
	for _, path := range []string{
		"/schedule",
		"/results/events/1/prizegiving/2",
		"/results/events/1/event-awards",
		"/public/attachments",
		"/public/attachments/42",
	} {
		publicRoutes[path] = publicRoute()
	}
	handler := protectTestRoutes(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}), publicRoutes, interfacePolicy{
		listenerAddress: &net.TCPAddr{IP: net.IPv4zero, Port: 8081},
		publicOnly:      true,
	})
	for _, path := range []string{
		"/schedule",
		"/results/events/1/prizegiving/2",
		"/results/events/1/event-awards",
		"/public/attachments",
		"/public/attachments/42",
	} {
		request := httptest.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			"http://beamers.test"+path,
			http.NoBody,
		)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Errorf("GET %s = %d, want %d", path, response.Code, http.StatusNoContent)
		}
	}
	for _, path := range []string{"/auth/sign-in", "/display", "/livez", "/upload/token"} {
		request := httptest.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			"http://beamers.test"+path,
			http.NoBody,
		)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want %d", path, response.Code, http.StatusNotFound)
		}
	}
}

func TestInterfacePolicyBoundsOrdinaryRequestsAndSetsBrowserHeaders(t *testing.T) {
	handler := protectTestRoutes(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if _, ok := request.Context().Deadline(); !ok {
			t.Error("ordinary request has no deadline")
		}
		_, err := io.ReadAll(request.Body)
		if err == nil {
			t.Error("oversized ordinary request was readable")
		}
	}), map[string]routeContract{"/auth/sign-in": crewRoute()}, interfacePolicy{
		listenerAddress: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080},
	})
	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"http://localhost/auth/sign-in",
		strings.NewReader(strings.Repeat("x", (1<<20)+1)),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	for _, name := range []string{
		"Content-Security-Policy",
		"Permissions-Policy",
		"Referrer-Policy",
		"X-Content-Type-Options",
		"X-Frame-Options",
	} {
		if response.Header().Get(name) == "" {
			t.Errorf("response is missing %s", name)
		}
	}
}

func TestRecoveryEntryPointsHaveBuiltInRateLimits(t *testing.T) {
	fail := false
	handler := protectTestRoutes(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if fail {
			http.Error(response, "authentication required", http.StatusUnauthorized)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	}), map[string]routeContract{
		"/admin/restores/apply": {
			kind: crewInterface, timeout: restoreOperationTimeout, recoveryLimit: 5,
		},
	}, interfacePolicy{
		listenerAddress: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080},
	})
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for range 10 {
			request := httptest.NewRequestWithContext(
				t.Context(),
				method,
				"http://localhost/admin/restores/apply",
				http.NoBody,
			)
			request.RemoteAddr = "127.0.0.1:1234"
			if method == http.MethodPost {
				request.Header.Set("Origin", "http://attacker.test")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusNoContent {
				t.Fatalf("non-sensitive recovery request = %d, want %d", response.Code, http.StatusNoContent)
			}
		}
	}
	for range 10 {
		request := httptest.NewRequestWithContext(
			t.Context(),
			http.MethodPost,
			"http://localhost/admin/restores/apply",
			http.NoBody,
		)
		request.RemoteAddr = "127.0.0.1:1234"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("successful recovery request = %d, want %d", response.Code, http.StatusNoContent)
		}
	}
	fail = true
	for attempt := range 6 {
		request := httptest.NewRequestWithContext(
			t.Context(),
			http.MethodPost,
			"http://localhost/admin/restores/apply",
			http.NoBody,
		)
		request.RemoteAddr = "127.0.0.1:1234"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		want := http.StatusUnauthorized
		if attempt == 5 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("recovery attempt %d = %d, want %d", attempt+1, response.Code, want)
		}
	}
}

func TestInsecureModesRenderCrewHTMLWarning(t *testing.T) {
	handler := protectTestRoutes(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = response.Write([]byte("<!doctype html><html><body><main>Control</main></body></html>"))
	}), map[string]routeContract{
		"/crew/program/1": {
			kind: crewInterface, mutatesOnRead: true, browserWarningPage: true,
		},
	}, interfacePolicy{
		listenerAddress:      &net.TCPAddr{IP: net.IPv4zero, Port: 8080},
		allowInsecureCrew:    true,
		allowInsecureDisplay: true,
	})
	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"http://beamers.test/crew/program/1",
		http.NoBody,
	)
	request.RemoteAddr = "192.0.2.10:1234"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if !strings.Contains(body, `role="alert"`) ||
		!strings.Contains(body, "insecure Crew mode enabled") ||
		!strings.Contains(body, "insecure Display mode enabled") {
		t.Fatalf("Crew HTML warning = %q", body)
	}
}

func TestDemoWarningDoesNotBufferPersistentStreams(t *testing.T) {
	handler := protectTestRoutes(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = response.Write([]byte("data: ready\n\n"))
		_ = http.NewResponseController(response).Flush()
		<-request.Context().Done()
	}), map[string]routeContract{
		"/events": {kind: displayInterface, persistent: true},
	}, interfacePolicy{
		listenerAddress:      &net.TCPAddr{IP: net.IPv4zero, Port: 8080},
		allowInsecureDisplay: true,
		demo:                 true,
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(t.Context())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/events", http.NoBody)
	if err != nil {
		t.Fatalf("create stream request: %v", err)
	}
	type streamResult struct {
		content string
		err     error
	}
	streamReady := make(chan streamResult, 1)
	go func() {
		response, requestErr := server.Client().Do(request)
		if requestErr != nil {
			streamReady <- streamResult{err: requestErr}
			return
		}
		defer func() {
			_ = response.Body.Close()
		}()
		content := make([]byte, len("data: ready\n\n"))
		_, requestErr = io.ReadFull(response.Body, content)
		streamReady <- streamResult{content: string(content), err: requestErr}
	}()
	select {
	case result := <-streamReady:
		if result.err != nil {
			t.Fatalf("open stream: %v", result.err)
		}
		if result.content != "data: ready\n\n" {
			t.Fatalf("stream prefix = %q", result.content)
		}
		cancel()
	case <-time.After(time.Second):
		cancel()
		t.Fatal("demo warning buffered persistent stream")
	}
}

func protectTestRoutes(
	handler http.Handler,
	contracts map[string]routeContract,
	policy interfacePolicy,
) http.Handler {
	routes := testRouteMux(handler, contracts)
	return protectInterfaces(routes, policy)
}

func testRouteMux(
	handler http.Handler,
	contracts map[string]routeContract,
) *routeMux {
	routes := newRouteMux()
	for pattern, contract := range contracts {
		routes.Handle(pattern, contract, handler)
	}
	return routes
}

func TestUnsafeMethodsRequireSameOrigin(t *testing.T) {
	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPatch,
		"https://beamers.test/crew/events/1",
		http.NoBody,
	)
	request.Header.Set("Origin", "https://attacker.test")
	request.TLS = &tls.ConnectionState{}
	response := httptest.NewRecorder()
	if requestAllowed(response, request, http.MethodPatch, false) {
		t.Fatal("cross-origin PATCH was allowed")
	}
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin PATCH = %d, want %d", response.Code, http.StatusForbidden)
	}
}
