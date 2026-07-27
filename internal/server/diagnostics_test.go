package server

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"

	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/displaystream"
	"github.com/dotwaffle/beamers/internal/operations"
)

func TestDiagnosticsAreLocalOrAdministratorOnly(t *testing.T) {
	application, session, _, _ := newDiagnosticsTestApplication(
		t,
		&net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 8080},
	)

	public := httptest.NewRecorder()
	application.ServeHTTP(
		public,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/diagnostics", http.NoBody),
	)
	if public.Code != http.StatusNotFound {
		t.Fatalf("public diagnostics status = %d, want 404", public.Code)
	}

	unauthenticated := httptest.NewRecorder()
	plaintext := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, "/admin/diagnostics", http.NoBody,
	)
	application.ServeHTTP(unauthenticated, plaintext)
	if unauthenticated.Code != http.StatusForbidden {
		t.Fatalf("plaintext diagnostics status = %d, want 403", unauthenticated.Code)
	}

	unauthenticated = httptest.NewRecorder()
	secure := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, "/admin/diagnostics", http.NoBody,
	)
	secure.TLS = &tls.ConnectionState{}
	application.ServeHTTP(
		unauthenticated,
		secure,
	)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated diagnostics status = %d, want 401", unauthenticated.Code)
	}

	request := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, "/admin/diagnostics", http.NoBody,
	)
	request.TLS = &tls.ConnectionState{}
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	response := httptest.NewRecorder()
	application.ServeHTTP(response, request)
	assertDiagnosticsResponse(t, response)
}

func TestDiagnosticsRetainIndependentComponentsWhenStorageFails(t *testing.T) {
	found := normalDiagnostics(
		nil,
		errors.New("storage failed"),
		operations.Capacity{},
		errors.New("capacity unavailable"),
		auth.SessionCounts{},
		errors.New("session counts unavailable"),
		2,
		2,
		3,
		4,
		scheduleStreamMetricSnapshot{Connections: 5, Resnapshots: 6},
		true,
		nil,
	)
	if found.Storage.Status != "unavailable" ||
		found.Backup.Status != "unavailable" ||
		found.Replication.Status != "disabled" ||
		found.Displays.Status != "unavailable" ||
		found.Capacity.Status != "unavailable" ||
		found.Authentication.Status != "unavailable" ||
		found.Streams.Display.Subscribers != 2 ||
		found.Streams.Program.Subscribers != 3 ||
		found.Streams.Schedule.Subscribers != 4 ||
		found.Streams.Schedule.Connections != 5 ||
		found.Streams.Schedule.Resnapshots != 6 ||
		found.Telemetry.Status != "enabled" {
		t.Fatalf("diagnostics = %+v", found)
	}
}

func TestDiagnosticsReportScheduleReadersSeparately(t *testing.T) {
	application, _, _, displayStream := newDiagnosticsTestApplication(
		t,
		&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080},
	)
	display := displayStream.SubscribeDisplay(0, 7)
	schedule := application.config.ScheduleStream.Subscribe(0)
	t.Cleanup(display.Close)
	t.Cleanup(schedule.Close)
	scheduleCursor := application.config.ScheduleStream.Cursor()
	displayStream.Notify()
	if current := application.config.ScheduleStream.Cursor(); current != scheduleCursor {
		t.Fatalf("Display invalidation advanced Schedule stream: %+v", current)
	}

	response := httptest.NewRecorder()
	application.ServeHTTP(
		response,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/diagnostics", http.NoBody),
	)
	var found normalDiagnosticsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &found); err != nil {
		t.Fatalf("decode diagnostics: %v", err)
	}
	if found.Streams.Display.Subscribers != 1 ||
		found.Streams.Schedule.Subscribers != 1 {
		t.Fatalf("stream diagnostics = %+v", found.Streams)
	}
}

func TestDiagnosticsWarnBeyondTestedCapacityWithoutRefusal(t *testing.T) {
	found := normalDiagnostics(
		nil,
		nil,
		operations.Capacity{
			Locations: 65,
			Lanes:     64,
			Sessions:  2,
			Entries:   4_999,
			Displays:  251,
		},
		nil,
		auth.SessionCounts{},
		nil,
		252,
		251,
		0,
		0,
		scheduleStreamMetricSnapshot{},
		false,
		nil,
	)
	if found.Capacity.Status != "warning" {
		t.Fatalf("capacity status = %q, want warning", found.Capacity.Status)
	}
	want := map[string]bool{
		"lanes_or_locations":   false,
		"sessions_and_entries": false,
		"displays":             false,
	}
	for _, warning := range found.Capacity.Warnings {
		if _, known := want[warning.Code]; known {
			want[warning.Code] = true
		}
		switch warning.Code {
		case "lanes_or_locations":
			if warning.TestedMax != 64 {
				t.Errorf("Location or Lane tested max = %d, want 64", warning.TestedMax)
			}
		case "sessions_and_entries":
			if warning.TestedMax != 5_000 {
				t.Errorf("Session and Entry tested max = %d, want 5000", warning.TestedMax)
			}
		case "displays":
			if warning.TestedMax != 250 {
				t.Errorf("Display tested max = %d, want 250", warning.TestedMax)
			}
		}
	}
	for code, seen := range want {
		if !seen {
			t.Errorf("capacity warnings = %+v, want %q", found.Capacity.Warnings, code)
		}
	}
}

func TestCapacityWarningsUseConnectedDisplayIdentities(t *testing.T) {
	if warnings := capacityWarnings(operations.Capacity{Displays: 1_000}, 250); slices.ContainsFunc(
		warnings,
		func(warning capacityWarning) bool { return warning.Code == "displays" },
	) {
		t.Fatalf("offline Display enrollments produced a capacity warning: %+v", warnings)
	}
	warnings := capacityWarnings(operations.Capacity{Displays: 250}, 251)
	if !slices.ContainsFunc(warnings, func(warning capacityWarning) bool {
		return warning.Code == "displays" && warning.Observed == 251
	}) {
		t.Fatalf("251 connected Display identities produced no warning: %+v", warnings)
	}
}

func TestDiagnosticsAreAvailableOnLoopback(t *testing.T) {
	application, _, _, _ := newDiagnosticsTestApplication(
		t,
		&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080},
	)

	response := httptest.NewRecorder()
	application.ServeHTTP(
		response,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/diagnostics", http.NoBody),
	)
	assertDiagnosticsResponse(t, response)
}

func assertDiagnosticsResponse(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("diagnostics status = %d, want 200; body %q", response.Code, response.Body.String())
	}
	if value := response.Header().Get("Cache-Control"); value != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", value)
	}
	var found struct {
		Mode      string `json:"mode"`
		Readiness struct {
			Status string `json:"status"`
		} `json:"readiness"`
		Storage struct {
			Status string `json:"status"`
		} `json:"storage"`
		Backup struct {
			Status string `json:"status"`
		} `json:"backup"`
		Authentication struct {
			Status          string `json:"status"`
			Active          int    `json:"active"`
			Cached          int    `json:"cached"`
			Stored          int    `json:"stored"`
			PerAccountLimit int    `json:"per_account_limit"`
		} `json:"authentication"`
		Streams struct {
			Display struct {
				Status      string `json:"status"`
				Subscribers int    `json:"subscribers"`
			} `json:"display"`
			Program struct {
				Status      string `json:"status"`
				Subscribers int    `json:"subscribers"`
			} `json:"program"`
		} `json:"streams"`
		Displays struct {
			Total    int            `json:"total"`
			Delivery map[string]int `json:"delivery"`
		} `json:"displays"`
		Telemetry struct {
			Status string `json:"status"`
		} `json:"telemetry"`
	}
	if err := json.NewDecoder(response.Body).Decode(&found); err != nil {
		t.Fatalf("decode diagnostics: %v", err)
	}
	if found.Mode != "normal" ||
		found.Readiness.Status != "ready" ||
		found.Storage.Status != "ready" ||
		found.Backup.Status != "available" ||
		found.Authentication.Status != "bounded" ||
		found.Authentication.Active != 1 ||
		found.Authentication.Cached != 1 ||
		found.Authentication.Stored != 1 ||
		found.Authentication.PerAccountLimit != 8 ||
		found.Streams.Display.Status != "ready" ||
		found.Streams.Program.Status != "ready" ||
		found.Displays.Total != 0 ||
		found.Telemetry.Status != "disabled" {
		t.Fatalf("diagnostics = %+v", found)
	}
}

func newDiagnosticsTestApplication(
	t *testing.T,
	listenerAddress net.Addr,
) (*application, string, *operations.Installation, *displaystream.Hub) {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "installation")
	if err := operations.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	installation, err := operations.OpenInstallation(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open installation: %v", err)
	}
	bootstrapToken, err := installation.Authentication().IssueBootstrap(t.Context())
	if err != nil {
		_ = installation.Close()
		t.Fatalf("issue bootstrap: %v", err)
	}
	session, err := installation.Authentication().BootstrapAdministrator(
		t.Context(),
		bootstrapToken,
		"Administrator",
		"correct horse battery staple",
	)
	if err != nil {
		_ = installation.Close()
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	displayStream, err := displaystream.NewProcess(displaySubscriberQueueCapacity)
	if err != nil {
		_ = installation.Close()
		t.Fatalf("create Display stream: %v", err)
	}
	programStream, err := displaystream.NewProcess(displaySubscriberQueueCapacity)
	if err != nil {
		_ = installation.Close()
		t.Fatalf("create Program Output stream: %v", err)
	}
	found, err := newApplication(t.Context(), applicationConfig{
		Config: Config{
			DataDir: dataDir, AttachmentsDir: filepath.Join(dataDir, "attachments"),
			BuildVersion: "test", Logger: slog.New(slog.DiscardHandler),
			TracerProvider: tracenoop.NewTracerProvider(),
			MeterProvider:  metricnoop.NewMeterProvider(),
			Propagator:     propagation.TraceContext{},
		},
		Installation: installation, ListenerAddress: listenerAddress,
		DisplayStream: displayStream, ProgramStream: programStream,
	})
	if err != nil {
		_ = installation.Close()
		t.Fatalf("build application: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := found.Close(); closeErr != nil {
			t.Errorf("close application: %v", closeErr)
		}
	})
	return found, session.Token, installation, displayStream
}
