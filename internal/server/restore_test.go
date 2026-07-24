package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	_ "github.com/dotwaffle/beamers/ent/runtime"
	"github.com/dotwaffle/beamers/internal/backup"
	"github.com/dotwaffle/beamers/internal/displaystream"
	"github.com/dotwaffle/beamers/internal/operations"
)

func TestRestoreMaintenanceCancelsAndDrainsActiveReads(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	application := &application{
		handler: http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			close(started)
			<-request.Context().Done()
			close(canceled)
		}),
		accepting: true,
		cancels:   make(map[uint64]context.CancelCauseFunc),
		drained:   closedChannel(),
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		application.ServeHTTP(
			httptest.NewRecorder(),
			httptest.NewRequestWithContext(
				t.Context(),
				http.MethodGet,
				"/storage-read",
				http.NoBody,
			),
		)
	}()
	<-started
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := application.beginRestore(ctx); err != nil {
		t.Fatalf("drain active read: %v", err)
	}
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("active read was not canceled before Restore")
	}
	<-done
}

func TestHealthyAdministratorRestoresThroughMaintenanceMode(t *testing.T) {
	ctx := t.Context()
	dataDir := filepath.Join(t.TempDir(), "installation")
	if err := operations.Initialize(ctx, dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	installation, err := operations.OpenInstallation(ctx, dataDir)
	if err != nil {
		t.Fatalf("open installation: %v", err)
	}
	bootstrapToken, err := installation.Authentication().IssueBootstrap(ctx)
	if err != nil {
		t.Fatalf("issue bootstrap: %v", err)
	}
	session, err := installation.Authentication().BootstrapAdministrator(
		ctx,
		bootstrapToken,
		"Administrator",
		"correct horse battery staple",
	)
	if err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	if _, err = installation.CreateBackup(ctx, backup.CreateInput{
		DataDir:    dataDir,
		OutputPath: archivePath,
		Mode:       backup.Sanitized,
	}); err != nil {
		t.Fatalf("create Backup: %v", err)
	}
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read Backup: %v", err)
	}
	displayStream, err := displaystream.NewProcess(displaySubscriberQueueCapacity)
	if err != nil {
		t.Fatalf("create Display stream: %v", err)
	}
	programStream, err := displaystream.NewProcess(displaySubscriberQueueCapacity)
	if err != nil {
		t.Fatalf("create Program Output stream: %v", err)
	}
	logger := slog.New(slog.DiscardHandler)
	application, err := newApplication(applicationConfig{
		Config: Config{
			DataDir: dataDir, AttachmentsDir: filepath.Join(dataDir, "attachments"),
			BuildVersion: "test", Logger: logger,
			TracerProvider: tracenoop.NewTracerProvider(),
			MeterProvider:  noop.NewMeterProvider(),
			Propagator:     propagation.TraceContext{},
		},
		Installation:    installation,
		ListenerAddress: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080},
		DisplayStream:   displayStream,
		ProgramStream:   programStream,
	})
	if err != nil {
		_ = installation.Close()
		t.Fatalf("build application: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := application.Close(); closeErr != nil {
			t.Errorf("close application: %v", closeErr)
		}
	})

	previewRequest := httptest.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"/admin/restores/preview",
		bytes.NewReader(archive),
	)
	previewRequest.Header.Set("Content-Type", "application/zip")
	previewRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.Token})
	previewResponse := httptest.NewRecorder()
	application.ServeHTTP(previewResponse, previewRequest)
	if previewResponse.Code != http.StatusOK {
		t.Fatalf(
			"Restore preview response = %d: %s",
			previewResponse.Code,
			previewResponse.Body.String(),
		)
	}
	var plan backup.RestorePlan
	if err = json.Unmarshal(previewResponse.Body.Bytes(), &plan); err != nil {
		t.Fatalf("decode Restore plan: %v", err)
	}
	if plan.DataQuarantine == "" || plan.Manifest.Mode != backup.Sanitized {
		t.Fatalf("Restore plan = %+v", plan)
	}

	approval, err := json.Marshal(map[string]any{
		"password":                "correct horse battery staple",
		"acknowledge_replacement": true,
	})
	if err != nil {
		t.Fatalf("encode Restore approval: %v", err)
	}
	applyRequest := httptest.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"/admin/restores/apply",
		bytes.NewReader(approval),
	)
	applyRequest.Header.Set("Content-Type", "application/json")
	applyRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.Token})
	applyResponse := httptest.NewRecorder()
	application.ServeHTTP(applyResponse, applyRequest)
	if applyResponse.Code != http.StatusOK {
		t.Fatalf(
			"Restore apply response = %d: %s",
			applyResponse.Code,
			applyResponse.Body.String(),
		)
	}

	readyRequest := httptest.NewRequestWithContext(ctx, http.MethodGet, "/readyz", http.NoBody)
	readyResponse := httptest.NewRecorder()
	application.ServeHTTP(readyResponse, readyRequest)
	if readyResponse.Code != http.StatusOK {
		t.Fatalf("readiness after Restore = %d: %s", readyResponse.Code, readyResponse.Body.String())
	}
}
