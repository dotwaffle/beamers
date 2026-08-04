package server

import (
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/dotwaffle/beamers/internal/displaystream"
	"github.com/dotwaffle/beamers/internal/operations"
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

// TestPprofIsDisabledByDefault confirms pprof stays off the routing table
// unless EnablePprof is set, per ADR 0035's opt-in requirement, even on a
// loopback listener where the loopback-or-authenticated guard would
// otherwise allow it.
func TestPprofIsDisabledByDefault(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "installation")
	if err := operations.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	installation, err := operations.OpenInstallation(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open installation: %v", err)
	}
	t.Cleanup(func() {
		_ = installation.Close()
	})
	displayStream, err := displaystream.NewProcess(displaySubscriberQueueCapacity)
	if err != nil {
		t.Fatalf("create Display stream: %v", err)
	}
	programStream, err := displaystream.NewProcess(displaySubscriberQueueCapacity)
	if err != nil {
		t.Fatalf("create Program Output stream: %v", err)
	}
	application, err := newApplication(t.Context(), applicationConfig{
		Config: Config{
			DataDir: dataDir, AttachmentsDir: filepath.Join(dataDir, "attachments"),
			BuildVersion: "test", Logger: slog.New(slog.DiscardHandler),
			TracerProvider: tracenoop.NewTracerProvider(),
			MeterProvider:  metricnoop.NewMeterProvider(),
			Propagator:     propagation.TraceContext{},
			EnablePprof:    false,
		},
		Installation:    installation,
		ListenerAddress: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080},
		DisplayStream:   displayStream, ProgramStream: programStream,
	})
	if err != nil {
		t.Fatalf("create application: %v", err)
	}

	response := httptest.NewRecorder()
	application.ServeHTTP(
		response,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/debug/pprof/", http.NoBody),
	)
	if response.Code != http.StatusNotFound {
		t.Fatalf("pprof status with EnablePprof=false = %d, want 404", response.Code)
	}
}
