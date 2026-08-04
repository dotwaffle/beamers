package telemetry

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/proto"
)

func TestRuntimeExportsAllSignalsAndKeepsStderrAuthoritative(t *testing.T) {
	var (
		mu         sync.Mutex
		paths      = make(map[string]int)
		logRequest collectorpb.ExportLogsServiceRequest
	)
	collector := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		_ = request.Body.Close()
		mu.Lock()
		paths[request.URL.Path]++
		if request.URL.Path == "/v1/logs" {
			_ = proto.Unmarshal(body, &logRequest)
		}
		mu.Unlock()
		response.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	var stderr bytes.Buffer
	runtime, err := New(t.Context(), Config{
		Endpoint: collector.URL, ServiceVersion: "test", Stderr: &stderr,
		SampleRatio: 1, ExportTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("create telemetry runtime: %v", err)
	}
	ctx, span := runtime.TracerProvider().Tracer("test").Start(t.Context(), "operation")
	counter, err := runtime.MeterProvider().Meter("test").Int64Counter("test.commands")
	if err != nil {
		t.Fatalf("create counter: %v", err)
	}
	counter.Add(ctx, 1)
	runtime.Logger().InfoContext(
		ctx,
		"authoritative log",
		"component", "test",
		"event_id", 42,
		"command_id", "secret-command",
		"error", errors.New("private failure"),
	)
	span.End()

	shutdownContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = runtime.Shutdown(shutdownContext); err != nil {
		t.Fatalf("shutdown telemetry: %v", err)
	}
	if !strings.Contains(stderr.String(), `"msg":"authoritative log"`) {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), `"event_id":42`) ||
		!strings.Contains(stderr.String(), `"command_id":"secret-command"`) {
		t.Fatalf("stderr lost authoritative attributes: %q", stderr.String())
	}
	mu.Lock()
	defer mu.Unlock()
	for _, path := range []string{"/v1/traces", "/v1/metrics", "/v1/logs"} {
		if paths[path] == 0 {
			t.Errorf("collector requests %v, missing %s", paths, path)
		}
	}
	foundComponent, foundError := false, false
	for _, resourceLogs := range logRequest.GetResourceLogs() {
		for _, scopeLogs := range resourceLogs.GetScopeLogs() {
			for _, record := range scopeLogs.GetLogRecords() {
				for _, attribute := range record.GetAttributes() {
					switch attribute.GetKey() {
					case "component":
						foundComponent = true
					case "error":
						foundError = true
						if got := attribute.GetValue().GetStringValue(); got != "private failure" {
							t.Errorf("exported error attribute = %q, want %q", got, "private failure")
						}
					case "event_id", "command_id":
						t.Errorf("unsafe OTLP log attribute exported: %s", attribute.GetKey())
					}
				}
			}
		}
	}
	if !foundComponent {
		t.Error("bounded component attribute was not exported")
	}
	if !foundError {
		t.Error("bounded error attribute was not exported")
	}
}

func TestBoundedLogAttributeExportsRollbackBackupPath(t *testing.T) {
	attribute, ok := boundedLogAttribute(slog.String("rollback_backup", "/data/.beamers-upgrade-backup-1/backup.zip"))
	if !ok {
		t.Fatal("rollback_backup attribute was unexpectedly dropped")
	}
	if attribute.Value.Kind() != slog.KindString {
		t.Fatalf("rollback_backup kind = %v, want string", attribute.Value.Kind())
	}
	if attribute.Value.String() != "/data/.beamers-upgrade-backup-1/backup.zip" {
		t.Fatalf("rollback_backup value = %q", attribute.Value.String())
	}
}

func TestBoundedLogAttributeTruncatesOverlongError(t *testing.T) {
	longMessage := strings.Repeat("x", maxBoundedAttributeRunes+100)
	attribute, ok := boundedLogAttribute(slog.Any("error", errors.New(longMessage)))
	if !ok {
		t.Fatal("error attribute was unexpectedly dropped")
	}
	if runeLength := len([]rune(attribute.Value.String())); runeLength > maxBoundedAttributeRunes+len("…(truncated)") {
		t.Fatalf("truncated error attribute length = %d, want <= %d", runeLength, maxBoundedAttributeRunes)
	}
	if !strings.HasSuffix(attribute.Value.String(), "…(truncated)") {
		t.Fatalf("truncated error attribute missing marker: %q", attribute.Value.String())
	}
}

func TestBoundedLogAttributeDropsUnknownKeys(t *testing.T) {
	if _, ok := boundedLogAttribute(slog.String("event_id", "42")); ok {
		t.Fatal("event_id attribute was unexpectedly exported")
	}
	if _, ok := boundedLogAttribute(slog.Int("rollback_backup", 1)); ok {
		t.Fatal("non-string rollback_backup attribute was unexpectedly exported")
	}
}

func TestRuntimeRejectsUnsafeEndpoint(t *testing.T) {
	for _, endpoint := range []string{
		"collector.example.test",
		"ftp://collector.example.test",
		"https://user:secret@collector.example.test",
		"https://collector.example.test/path?token=secret",
	} {
		_, err := New(t.Context(), Config{
			Endpoint: endpoint, ServiceVersion: "test", Stderr: io.Discard,
			SampleRatio: 1, ExportTimeout: time.Second,
		})
		if err == nil {
			t.Errorf("New endpoint %q unexpectedly succeeded", endpoint)
		}
	}
}

func TestRuntimeRejectsNaNSampleRatio(t *testing.T) {
	_, err := New(t.Context(), Config{
		Endpoint: "https://collector.example.test", ServiceVersion: "test",
		Stderr: io.Discard, SampleRatio: math.NaN(), ExportTimeout: time.Second,
	})
	if err == nil {
		t.Fatal("NaN sample ratio unexpectedly accepted")
	}
}
