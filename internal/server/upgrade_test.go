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
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/dotwaffle/beamers/internal/displaystream"
	"github.com/dotwaffle/beamers/internal/operations"
	"github.com/dotwaffle/beamers/internal/store/storetest"
)

func TestUpgradeMaintenanceRejectsUnregisteredRoutes(t *testing.T) {
	application := newUpgradeApplication(applicationConfig{}, nil)
	for _, path := range []string{
		"/admin/events?event=1",
		"/beamers.display.v1.DisplayService/GetSnapshot",
		"/unknown",
	} {
		response := httptest.NewRecorder()
		application.ServeHTTP(
			response,
			httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody),
		)
		if response.Code != http.StatusNotFound ||
			response.Header().Get("X-Beamers-Maintenance") != "" {
			t.Errorf(
				"GET %s = %d, headers %v; want unclassified 404",
				path,
				response.Code,
				response.Header(),
			)
		}
	}
}

func TestServerHoldsRollbackIncompatibleUpgradeForApproval(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "installation")
	if err := operations.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	if err := storetest.DowngradeBeforeUpgradeContracts(
		t.Context(),
		filepath.Join(dataDir, "beamers.db"),
	); err != nil {
		t.Fatalf("prepare schema 47 fixture: %v", err)
	}
	listenConfig := net.ListenConfig{}
	reserved, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve listener: %v", err)
	}
	address := reserved.Addr().String()
	if err = reserved.Close(); err != nil {
		t.Fatalf("release listener: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		result <- Run(ctx, Config{
			DataDir: dataDir, ListenAddress: address, BuildVersion: "test",
			ShutdownTimeout: time.Second, Logger: slog.New(slog.DiscardHandler),
			TracerProvider: tracenoop.NewTracerProvider(),
			MeterProvider:  noop.NewMeterProvider(),
			Propagator:     propagation.TraceContext{},
		})
	}()
	client := &http.Client{Timeout: 100 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	var maintenance *http.Response
	for {
		request, requestErr := http.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			"http://"+address+"/readyz",
			http.NoBody,
		)
		if requestErr != nil {
			cancel()
			t.Fatalf("create maintenance probe: %v", requestErr)
		}
		response, requestErr := client.Do(request)
		if requestErr == nil {
			maintenance = response
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("server did not enter maintenance: %v", requestErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if closeErr := maintenance.Body.Close(); closeErr != nil {
		t.Fatalf("close maintenance readiness: %v", closeErr)
	}
	if maintenance.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("upgrade readiness = %d, want 503", maintenance.StatusCode)
	}
	cancel()
	if err = <-result; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("stop upgraded server: %v", err)
	}
	backups, err := filepath.Glob(
		filepath.Join(filepath.Dir(dataDir), ".installation.beamers-upgrade-backup-*", "backup.zip"),
	)
	if err != nil || len(backups) != 0 {
		t.Fatalf("unapproved upgrade Backups = %v, %v", backups, err)
	}
}

func TestAdministratorApprovesForceLiveUpgradeInMaintenance(t *testing.T) {
	ctx := t.Context()
	dataDir := filepath.Join(t.TempDir(), "installation")
	if err := operations.Initialize(ctx, dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	installation, err := operations.OpenInstallation(ctx, dataDir)
	if err != nil {
		t.Fatalf("open installation: %v", err)
	}
	token, err := installation.Authentication().IssueBootstrap(ctx)
	if err != nil {
		t.Fatalf("issue bootstrap: %v", err)
	}
	session, err := installation.Authentication().BootstrapAdministrator(
		ctx,
		token,
		"Administrator",
		"correct horse battery staple",
	)
	if err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	if err = installation.Close(); err != nil {
		t.Fatalf("close installation: %v", err)
	}
	databasePath := filepath.Join(dataDir, "beamers.db")
	if err = storetest.AddLiveSession(ctx, databasePath); err != nil {
		t.Fatalf("add live state: %v", err)
	}
	if err = storetest.DowngradeBeforeUpgradeContracts(ctx, databasePath); err != nil {
		t.Fatalf("prepare schema 47 fixture: %v", err)
	}
	upgrade, err := operations.PrepareUpgrade(ctx, operations.OpenConfig{DataDir: dataDir})
	if err != nil {
		t.Fatalf("prepare upgrade: %v", err)
	}
	if !upgrade.Plan().RequiresApproval ||
		len(upgrade.Plan().LiveBlockers) != 1 ||
		upgrade.Plan().LiveBlockers[0] != "Live Session" {
		t.Fatalf("force-live plan = %+v", upgrade.Plan())
	}
	displayStream, err := displaystream.NewProcess(displaySubscriberQueueCapacity)
	if err != nil {
		t.Fatalf("create Display stream: %v", err)
	}
	programStream, err := displaystream.NewProcess(displaySubscriberQueueCapacity)
	if err != nil {
		t.Fatalf("create Program stream: %v", err)
	}
	application := newUpgradeApplication(applicationConfig{
		Config: Config{
			DataDir: dataDir, BuildVersion: "test",
			Logger:         slog.New(slog.DiscardHandler),
			TracerProvider: tracenoop.NewTracerProvider(),
			MeterProvider:  noop.NewMeterProvider(),
			Propagator:     propagation.TraceContext{},
		},
		ListenerAddress: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080},
		DisplayStream:   displayStream, ProgramStream: programStream,
	}, upgrade)
	t.Cleanup(func() {
		if closeErr := application.Close(); closeErr != nil {
			t.Errorf("close application: %v", closeErr)
		}
	})

	previewRequest := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/admin/upgrade",
		http.NoBody,
	)
	previewRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.Token})
	previewResponse := httptest.NewRecorder()
	application.ServeHTTP(previewResponse, previewRequest)
	if previewResponse.Code != http.StatusOK {
		t.Fatalf(
			"upgrade preview response = %d: %s",
			previewResponse.Code,
			previewResponse.Body.String(),
		)
	}
	var preview operations.UpgradePlan
	if err = json.Unmarshal(previewResponse.Body.Bytes(), &preview); err != nil ||
		preview.PreviewDigest == "" {
		t.Fatalf("decode upgrade preview = %+v, %v", preview, err)
	}

	approval, err := json.Marshal(map[string]any{
		"password":                      "correct horse battery staple",
		"approve_consequences":          true,
		"acknowledge_no_down_migration": true,
		"force_live":                    true,
		"reason":                        "urgent data-visibility fix",
		"preview_digest":                preview.PreviewDigest,
	})
	if err != nil {
		t.Fatalf("encode approval: %v", err)
	}
	applyRequest := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"/admin/upgrade/apply",
		bytes.NewReader(approval),
	)
	applyRequest.Header.Set("Content-Type", "application/json")
	applyRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.Token})
	applyResponse := httptest.NewRecorder()
	application.ServeHTTP(applyResponse, applyRequest)
	if applyResponse.Code != http.StatusOK {
		t.Fatalf(
			"upgrade apply response = %d: %s",
			applyResponse.Code,
			applyResponse.Body.String(),
		)
	}
	readyResponse := httptest.NewRecorder()
	application.ServeHTTP(
		readyResponse,
		httptest.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			"/readyz",
			http.NoBody,
		),
	)
	if readyResponse.Code != http.StatusOK {
		t.Fatalf("readiness after upgrade = %d", readyResponse.Code)
	}
	audits, err := storetest.CountUpgradeAudits(ctx, databasePath)
	if err != nil || audits != 1 {
		t.Fatalf("upgrade Audit Entries = %d, %v", audits, err)
	}
}
