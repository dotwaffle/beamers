package server

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPprofIsLocalOrAdministratorOnly(t *testing.T) {
	application, session, _, _ := newDiagnosticsTestApplication(
		t,
		&net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 8080},
	)

	unauthenticatedPlaintext := httptest.NewRecorder()
	application.ServeHTTP(
		unauthenticatedPlaintext,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/debug/pprof/", http.NoBody),
	)
	if unauthenticatedPlaintext.Code != http.StatusForbidden {
		t.Fatalf("plaintext pprof status = %d, want 403", unauthenticatedPlaintext.Code)
	}

	unauthenticatedSecure := httptest.NewRecorder()
	secure := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/debug/pprof/", http.NoBody)
	secure.TLS = &tls.ConnectionState{}
	application.ServeHTTP(unauthenticatedSecure, secure)
	if unauthenticatedSecure.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated pprof status = %d, want 401", unauthenticatedSecure.Code)
	}

	authenticated := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/debug/pprof/", http.NoBody)
	request.TLS = &tls.ConnectionState{}
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	application.ServeHTTP(authenticated, request)
	if authenticated.Code != http.StatusOK {
		t.Fatalf("Administrator pprof status = %d, want 200", authenticated.Code)
	}
}

func TestPprofIsUnauthenticatedOnLoopback(t *testing.T) {
	application, _, _, _ := newDiagnosticsTestApplication(
		t,
		&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080},
	)

	response := httptest.NewRecorder()
	application.ServeHTTP(
		response,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/debug/pprof/", http.NoBody),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("loopback pprof status = %d, want 200", response.Code)
	}
}
