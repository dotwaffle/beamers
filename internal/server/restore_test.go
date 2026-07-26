package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	releaseRead := make(chan struct{})
	applyStarted := make(chan struct{}, 2)
	releaseApply := make(chan struct{})
	restoreResult := make(chan error, 2)
	app := &application{
		accepting: true,
		cancels:   make(map[uint64]context.CancelCauseFunc),
		drained:   closedChannel(),
	}
	handler := http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/admin/restores/apply" {
			applyStarted <- struct{}{}
			<-releaseApply
			ctx, cancel := context.WithTimeout(
				context.WithoutCancel(request.Context()),
				time.Second,
			)
			defer cancel()
			_, err := app.beginRestore(ctx)
			restoreResult <- err
			return
		}
		close(started)
		<-request.Context().Done()
		close(canceled)
		<-releaseRead
	})
	app.handler = testRouteMux(handler, map[string]routeContract{
		"/admin/restores/apply": {
			kind: crewInterface, timeout: restoreOperationTimeout, recoveryLimit: 5,
		},
		"/storage-read": crewRoute(),
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.ServeHTTP(
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
	for range 2 {
		go app.ServeHTTP(
			httptest.NewRecorder(),
			httptest.NewRequestWithContext(
				t.Context(),
				http.MethodPost,
				"/admin/restores/apply",
				http.NoBody,
			),
		)
	}
	<-applyStarted
	<-applyStarted
	close(releaseApply)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("active read was not canceled before Restore")
	}
	if err := <-restoreResult; !errors.Is(err, errRestoreInProgress) {
		t.Fatalf("competing Restore error = %v", err)
	}
	close(releaseRead)
	if err := <-restoreResult; err != nil {
		t.Fatalf("drain active read: %v", err)
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
	configuration, err := backup.LoadConfiguration(dataDir, "")
	if err != nil {
		t.Fatalf("load Backup configuration: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	if _, err = installation.CreateBackup(ctx, backup.CreateInput{
		DataDir:       dataDir,
		OutputPath:    archivePath,
		Mode:          backup.Sanitized,
		Configuration: configuration,
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
	application, err := newApplication(t.Context(), applicationConfig{
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

	cancelWithoutAcknowledgment, err := json.Marshal(map[string]any{
		"password": "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("encode unacknowledged cancellation: %v", err)
	}
	cancelRequest := httptest.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"/admin/restores/cancel",
		bytes.NewReader(cancelWithoutAcknowledgment),
	)
	cancelRequest.Header.Set("Content-Type", "application/json")
	cancelRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.Token})
	cancelResponse := httptest.NewRecorder()
	application.ServeHTTP(cancelResponse, cancelRequest)
	if cancelResponse.Code != http.StatusUnprocessableEntity {
		t.Fatalf(
			"unacknowledged cancellation response = %d: %s",
			cancelResponse.Code,
			cancelResponse.Body.String(),
		)
	}

	cancelApproval, err := json.Marshal(map[string]any{
		"password":                             "correct horse battery staple",
		"acknowledge_abandon_prepared_restore": true,
	})
	if err != nil {
		t.Fatalf("encode cancellation approval: %v", err)
	}
	cancelRequest = httptest.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"/admin/restores/cancel",
		bytes.NewReader(cancelApproval),
	)
	cancelRequest.Header.Set("Content-Type", "application/json")
	cancelRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.Token})
	cancelResponse = httptest.NewRecorder()
	application.ServeHTTP(cancelResponse, cancelRequest)
	if cancelResponse.Code != http.StatusOK {
		t.Fatalf(
			"Restore cancellation response = %d: %s",
			cancelResponse.Code,
			cancelResponse.Body.String(),
		)
	}

	previewRequest = httptest.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"/admin/restores/preview",
		bytes.NewReader(archive),
	)
	previewRequest.Header.Set("Content-Type", "application/zip")
	previewRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.Token})
	previewResponse = httptest.NewRecorder()
	application.ServeHTTP(previewResponse, previewRequest)
	if previewResponse.Code != http.StatusOK {
		t.Fatalf(
			"Restore preview after cancellation response = %d: %s",
			previewResponse.Code,
			previewResponse.Body.String(),
		)
	}
	if err = json.Unmarshal(previewResponse.Body.Bytes(), &plan); err != nil {
		t.Fatalf("decode Restore plan after cancellation: %v", err)
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
