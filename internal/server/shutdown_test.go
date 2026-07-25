package server

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/operations"
	"github.com/dotwaffle/beamers/internal/telemetry"
)

func TestTelemetryShutdownTimeoutIsReportedAfterLiveWorkStops(t *testing.T) {
	collector := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		_ *http.Request,
	) {
		time.Sleep(250 * time.Millisecond)
		response.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	var logs bytes.Buffer
	telemetryRuntime, err := telemetry.New(t.Context(), telemetry.Config{
		Endpoint: collector.URL, ServiceVersion: "test", Stderr: &logs,
		SampleRatio: 1, ExportTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("create telemetry runtime: %v", err)
	}
	dataDir := filepath.Join(t.TempDir(), "installation")
	if err = operations.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	listenConfig := net.ListenConfig{}
	listener, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve listen address: %v", err)
	}
	address := listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatalf("release listen address: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		result <- Run(ctx, Config{
			DataDir: dataDir, ListenAddress: address, BuildVersion: "test",
			ShutdownTimeout: 500 * time.Millisecond,
			Logger:          telemetryRuntime.Logger(),
			TracerProvider:  telemetryRuntime.TracerProvider(),
			MeterProvider:   telemetryRuntime.MeterProvider(),
			Propagator:      telemetryRuntime.Propagator(),
			Telemetry:       telemetryRuntime,
		})
	}()
	waitForReady(t, address)
	cancel()
	if err = <-result; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("server shutdown error = %v, want telemetry deadline", err)
	}
	for _, fragment := range []string{
		`"phase":"readiness"`,
		`"phase":"traffic_drain"`,
		`"phase":"in_flight"`,
		`"status":"skipped"`,
		`"finalizer":"http"`,
		`"finalizer":"storage"`,
		`"finalizer":"telemetry"`,
		`"status":"timeout"`,
		`"budget_ms":500`,
		`"elapsed_ms":`,
		`"remaining_ms":`,
	} {
		if !strings.Contains(logs.String(), fragment) {
			t.Errorf("shutdown logs missing %q:\n%s", fragment, logs.String())
		}
	}
}

func TestShutdownFinalizersRunSynchronizationBeforeStorage(t *testing.T) {
	replicationErr := errors.New("replication failed")
	telemetryErr := errors.New("telemetry failed")
	storageErr := errors.New("storage failed")
	var mu sync.Mutex
	var order []string
	record := func(name string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, name)
	}
	statuses := make(map[string]string)

	timedOut, err := finalizeShutdown(
		t.Context(),
		shutdownFinalizers{
			http: func(context.Context) error {
				record("http")
				return nil
			},
			replication: func(context.Context) error {
				record("replication")
				return replicationErr
			},
			telemetry: func(context.Context) error {
				record("telemetry")
				return telemetryErr
			},
			storage: func(context.Context) error {
				record("storage")
				return storageErr
			},
		},
		func(result finalizerResult) {
			statuses[result.name] = result.status()
		},
	)
	if timedOut ||
		!errors.Is(err, replicationErr) ||
		!errors.Is(err, storageErr) ||
		!errors.Is(err, telemetryErr) {
		t.Fatalf("timedOut=%t error=%v", timedOut, err)
	}
	if len(order) != 4 || order[len(order)-1] != "storage" {
		t.Fatalf("finalizer order = %v, want storage last", order)
	}
	if statuses["http"] != "complete" ||
		statuses["replication"] != "error" ||
		statuses["telemetry"] != "error" ||
		statuses["storage"] != "error" {
		t.Fatalf("finalizer statuses = %v", statuses)
	}
}

func TestShutdownFinalizersSkipStorageAfterSynchronizationDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	stalled := make(chan struct{})
	t.Cleanup(func() {
		close(stalled)
	})
	storageStarted := false
	found := make(map[string]string)
	started := time.Now()
	timedOut, err := finalizeShutdown(
		ctx,
		shutdownFinalizers{
			http: func(context.Context) error {
				return nil
			},
			replication: func(context.Context) error {
				<-stalled
				return nil
			},
			storage: func(context.Context) error {
				storageStarted = true
				return nil
			},
		},
		func(result finalizerResult) {
			found[result.name] = result.status()
		},
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("collect finalizers error = %v, want deadline exceeded", err)
	}
	if !timedOut ||
		found["http"] != "complete" ||
		found["replication"] != "timeout" ||
		found["storage"] != "skipped" ||
		storageStarted ||
		time.Since(started) > 200*time.Millisecond {
		t.Fatalf("timedOut=%t found=%v elapsed=%s", timedOut, found, time.Since(started))
	}
}

func TestShutdownFinalizersUseReserveAfterSynchronizationCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	storageStarted := false
	_, err := finalizeShutdown(
		ctx,
		shutdownFinalizers{
			http: func(context.Context) error {
				return nil
			},
			replication: func(syncContext context.Context) error {
				<-syncContext.Done()
				return context.Cause(syncContext)
			},
			storage: func(context.Context) error {
				storageStarted = true
				return nil
			},
		},
		func(finalizerResult) {},
	)
	if !errors.Is(err, context.DeadlineExceeded) || !storageStarted {
		t.Fatalf("error=%v storageStarted=%t, want sync timeout and storage closure", err, storageStarted)
	}
}

func TestServerRejectsNonpositiveShutdownBudget(t *testing.T) {
	err := Run(t.Context(), Config{BuildVersion: "test"})
	if err == nil || !strings.Contains(err.Error(), "shutdown timeout") {
		t.Fatalf("Run error = %v, want shutdown timeout validation", err)
	}
}

func TestTrafficDrainUsesOnlyLongProfileWithActiveWork(t *testing.T) {
	open := make(chan struct{})
	if status := waitForTrafficDrain(t.Context(), 10*time.Second, 1, open); status != "skipped" {
		t.Fatalf("ten-second drain status = %q, want skipped", status)
	}
	if status := waitForTrafficDrain(t.Context(), 30*time.Second, 0, open); status != "skipped" {
		t.Fatalf("idle drain status = %q, want skipped", status)
	}
	closed := make(chan struct{})
	close(closed)
	if status := waitForTrafficDrain(t.Context(), 30*time.Second, 1, closed); status != "complete" {
		t.Fatalf("completed drain status = %q, want complete", status)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if status := waitForTrafficDrain(ctx, 30*time.Second, 1, open); status != "canceled" {
		t.Fatalf("canceled drain status = %q, want canceled", status)
	}
	deadlineContext, cancelDeadline := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancelDeadline()
	started := time.Now()
	if status := waitForTrafficDrain(
		deadlineContext,
		30*time.Second,
		1,
		open,
	); status != "timeout" || time.Since(started) > 200*time.Millisecond {
		t.Fatalf(
			"deadline drain status = %q elapsed=%s, want bounded timeout",
			status,
			time.Since(started),
		)
	}
}

func TestInFlightDrainCancelsStreamsButPreservesActiveWork(t *testing.T) {
	streamContext, cancelStream := context.WithCancelCause(t.Context())
	defer cancelStream(nil)
	workContext, cancelWork := context.WithCancelCause(t.Context())
	defer cancelWork(nil)
	drained := make(chan struct{})
	application := &application{
		active: 2,
		handler: http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(http.StatusNoContent)
		}),
		cancels: map[uint64]context.CancelCauseFunc{
			1: cancelStream,
			2: cancelWork,
		},
		persistent: map[uint64]bool{1: true},
		drained:    drained,
	}

	active, waiting := application.beginInFlightDrain()
	if active != 2 || waiting != drained ||
		!application.rejectMutations ||
		!application.rejectStreams {
		t.Fatalf(
			"in-flight drain active=%d waiting=%p rejectMutations=%t rejectStreams=%t",
			active,
			waiting,
			application.rejectMutations,
			application.rejectStreams,
		)
	}
	if context.Cause(streamContext) == nil {
		t.Error("persistent stream was not canceled")
	}
	if context.Cause(workContext) != nil {
		t.Errorf("in-flight work was canceled: %v", context.Cause(workContext))
	}
	for _, request := range []*http.Request{
		httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/command", http.NoBody),
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/display", http.NoBody),
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/crew/program/42", http.NoBody),
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/display/events", http.NoBody),
	} {
		response := httptest.NewRecorder()
		application.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s status = %d, want 503", request.Method, request.URL.Path, response.Code)
		}
	}
	response := httptest.NewRecorder()
	application.ServeHTTP(
		response,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/schedule", http.NoBody),
	)
	if response.Code != http.StatusNoContent {
		t.Errorf("GET /schedule status = %d, want 204", response.Code)
	}
	waitContext, cancelWait := context.WithCancel(t.Context())
	cancelWait()
	if status := waitForInFlightDrain(
		waitContext,
		time.Now().Add(time.Second),
		active,
		waiting,
	); status != "canceled" {
		t.Fatalf("in-flight wait status = %q, want canceled", status)
	}
	if status := waitForInFlightDrain(
		t.Context(),
		time.Now().Add(-time.Millisecond),
		active,
		waiting,
	); status != "timeout" {
		t.Fatalf("reserve-expired in-flight status = %q, want timeout", status)
	}
}

func waitForReady(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get("http://" + address + "/readyz") //nolint:noctx // bounded by deadline.
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server did not become ready")
}
