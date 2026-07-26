package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/backup"
	"github.com/dotwaffle/beamers/internal/operations"
	"github.com/dotwaffle/beamers/internal/store/storetest"
)

func TestServeCommandExportsItsTerminalError(t *testing.T) {
	exported := make(chan struct{}, 1)
	collector := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path == "/v1/logs" {
			exported <- struct{}{}
		}
		response.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	var stdout, stderr bytes.Buffer
	if code := run(
		t.Context(),
		[]string{
			"serve",
			"--otlp-endpoint", collector.URL,
			"--shutdown-timeout", "0",
		},
		&stdout,
		&stderr,
	); code != 1 {
		t.Fatalf("serve exit = %d, want 1; stderr = %s", code, stderr.String())
	}
	select {
	case <-exported:
	default:
		t.Fatal("serve terminal error was not exported")
	}
}

func TestBackupCommandCreatesAndVerifiesSanitizedArchive(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "installation")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run(
		t.Context(),
		[]string{"init", "--data-dir", dataDir},
		&stdout,
		&stderr,
	); code != 0 {
		t.Fatalf("initialize exit = %d, stderr = %s", code, stderr.String())
	}
	installation, err := operations.OpenInstallation(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open installation: %v", err)
	}
	bootstrapToken, err := installation.Authentication().IssueBootstrap(t.Context())
	if err != nil {
		t.Fatalf("issue Administrator bootstrap: %v", err)
	}
	if _, err = installation.Authentication().BootstrapAdministrator(
		t.Context(),
		bootstrapToken,
		"Administrator",
		"correct horse battery staple",
	); err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	if err = installation.Close(); err != nil {
		t.Fatalf("close installation: %v", err)
	}
	listenConfig := net.ListenConfig{}
	listener, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve native listener: %v", err)
	}
	listenAddress := listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatalf("release native listener: %v", err)
	}
	replicaURL := "file://" + filepath.Join(t.TempDir(), "replica")
	serveContext, cancelServe := context.WithCancel(t.Context())
	defer cancelServe()
	serveDone := make(chan int, 1)
	var serveStderr bytes.Buffer
	go func() {
		serveDone <- run(
			serveContext,
			[]string{
				"serve",
				"--data-dir", dataDir,
				"--listen", listenAddress,
				"--trusted-proxy", "127.0.0.1",
				"--insecure-crew",
				"--replica-url", replicaURL,
				"--shutdown-timeout", "15s",
			},
			&bytes.Buffer{},
			&serveStderr,
		)
	}()
	waitForNativeReadiness(t, listenAddress, serveDone, &serveStderr)
	cancelServe()
	if code := <-serveDone; code != 0 {
		t.Fatalf("serve exit = %d, stderr = %s", code, serveStderr.String())
	}

	archivePath := filepath.Join(t.TempDir(), "installation.beamers-backup")
	stdout.Reset()
	stderr.Reset()
	if code := run(
		t.Context(),
		[]string{
			"backup",
			"--data-dir", dataDir,
			"--output", archivePath,
		},
		&stdout,
		&stderr,
	); code != 0 {
		t.Fatalf("backup exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "created Sanitized Backup") ||
		!strings.Contains(stdout.String(), archivePath) {
		t.Fatalf("backup output = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(
		t.Context(),
		[]string{"backup", "verify", "--input", archivePath},
		&stdout,
		&stderr,
	); code != 0 {
		t.Fatalf("verify Backup exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "verified Sanitized Backup") ||
		!strings.Contains(stdout.String(), "format 2") {
		t.Fatalf("verify Backup output = %q", stdout.String())
	}
	verified, err := backup.Verify(t.Context(), archivePath)
	if err != nil {
		t.Fatalf("read verified Backup manifest: %v", err)
	}
	if verified.Configuration.ListenAddress != listenAddress ||
		verified.Configuration.ShutdownTimeout != 15*time.Second ||
		!verified.Configuration.InsecureCrew ||
		!verified.Configuration.ReplicationEnabled ||
		verified.Configuration.ReplicaURL != replicaURL ||
		len(verified.Configuration.TrustedProxies) != 1 {
		t.Fatalf("effective native configuration = %+v", verified.Configuration)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(
		t.Context(),
		[]string{
			"backup",
			"--data-dir", dataDir,
			"--output", archivePath,
		},
		&stdout,
		&stderr,
	); code == 0 {
		t.Fatal("second Backup unexpectedly replaced its output")
	}

	restoredDataDir := filepath.Join(t.TempDir(), "restored")
	stdout.Reset()
	stderr.Reset()
	if code := run(
		t.Context(),
		[]string{
			"restore",
			"--input", archivePath,
			"--data-dir", restoredDataDir,
			"--acknowledge-configuration-differences",
		},
		&stdout,
		&stderr,
	); code != 0 {
		t.Fatalf("Restore exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "restored Sanitized Backup") {
		t.Fatalf("Restore output = %q", stdout.String())
	}
	restored, err := operations.OpenInstallation(t.Context(), restoredDataDir)
	if err != nil {
		t.Fatalf("reopen restored installation: %v", err)
	}
	if err = restored.Ready(t.Context()); err != nil {
		t.Fatalf("restored installation readiness: %v", err)
	}
	if _, err = restored.Authentication().SignIn(
		t.Context(),
		"Administrator",
		"correct horse battery staple",
	); !errors.Is(err, auth.ErrAuthenticationFailed) {
		t.Fatalf("restored old credential error = %v", err)
	}
	restoredBootstrap, err := restored.Authentication().IssueBootstrap(t.Context())
	if err != nil {
		t.Fatalf("issue restored bootstrap: %v", err)
	}
	if _, err = restored.Authentication().BootstrapAdministrator(
		t.Context(),
		restoredBootstrap,
		"Administrator",
		"new correct horse battery staple",
	); err != nil {
		t.Fatalf("re-bootstrap restored Administrator: %v", err)
	}
	if err = restored.Close(); err != nil {
		t.Fatalf("close restored installation: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(
		t.Context(),
		[]string{
			"restore",
			"--input", archivePath,
			"--data-dir", restoredDataDir,
		},
		&stdout,
		&stderr,
	); code == 0 {
		t.Fatal("second Restore unexpectedly replaced its target")
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(
		t.Context(),
		[]string{
			"restore", "preview",
			"--input", archivePath,
			"--data-dir", restoredDataDir,
		},
		&stdout,
		&stderr,
	); code != 0 {
		t.Fatalf("preview replacement exit = %d, stderr = %s", code, stderr.String())
	}
	var plan backup.RestorePlan
	if err = json.Unmarshal(stdout.Bytes(), &plan); err != nil {
		t.Fatalf("decode replacement plan: %v", err)
	}
	if plan.JournalPath == "" ||
		plan.DataQuarantine == "" ||
		!plan.RequiresConfigurationAcknowledgment {
		t.Fatalf("replacement plan = %+v", plan)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(
		t.Context(),
		[]string{"restore", "cancel", "--journal", plan.JournalPath},
		&stdout,
		&stderr,
	); code == 0 {
		t.Fatal("unacknowledged prepared Restore unexpectedly canceled")
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(
		t.Context(),
		[]string{
			"restore", "cancel",
			"--journal", plan.JournalPath,
			"--acknowledge-abandon-prepared",
		},
		&stdout,
		&stderr,
	); code != 0 {
		t.Fatalf("cancel prepared Restore exit = %d, stderr = %s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(
		t.Context(),
		[]string{
			"restore", "preview",
			"--input", archivePath,
			"--data-dir", restoredDataDir,
		},
		&stdout,
		&stderr,
	); code != 0 {
		t.Fatalf("preview after cancellation exit = %d, stderr = %s", code, stderr.String())
	}
	if err = json.Unmarshal(stdout.Bytes(), &plan); err != nil {
		t.Fatalf("decode replacement plan after cancellation: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(
		t.Context(),
		[]string{"restore", "apply", "--journal", plan.JournalPath},
		&stdout,
		&stderr,
	); code == 0 {
		t.Fatal("unacknowledged replacement unexpectedly applied")
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(
		t.Context(),
		[]string{
			"restore", "apply",
			"--journal", plan.JournalPath,
			"--acknowledge-replacement",
			"--acknowledge-configuration-differences",
		},
		&stdout,
		&stderr,
	); code != 0 {
		t.Fatalf("apply replacement exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "restored Sanitized Backup") {
		t.Fatalf("replacement output = %q", stdout.String())
	}
}

func waitForNativeReadiness(
	t *testing.T,
	address string,
	serveDone <-chan int,
	stderr *bytes.Buffer,
) {
	t.Helper()
	client := &http.Client{Timeout: 100 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case code := <-serveDone:
			t.Fatalf("serve exited before readiness with %d: %s", code, stderr.String())
		default:
		}
		request, err := http.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			"http://"+address+"/readyz",
			http.NoBody,
		)
		if err != nil {
			t.Fatalf("create readiness request: %v", err)
		}
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("serve did not become ready")
}

func TestUpgradeCommandPreviewsAndAppliesKnownSafeMigration(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "installation")
	var stdout, stderr bytes.Buffer
	if code := run(
		t.Context(),
		[]string{"init", "--data-dir", dataDir},
		&stdout,
		&stderr,
	); code != 0 {
		t.Fatalf("initialize exit = %d, stderr = %s", code, stderr.String())
	}
	if err := storetest.DowngradeBeforeUpgradeContracts(
		t.Context(),
		filepath.Join(dataDir, "beamers.db"),
	); err != nil {
		t.Fatalf("prepare schema 47 fixture: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(
		t.Context(),
		[]string{"upgrade", "preview", "--data-dir", dataDir},
		&stdout,
		&stderr,
	); code != 0 {
		t.Fatalf("upgrade preview exit = %d, stderr = %s", code, stderr.String())
	}
	var plan operations.UpgradePlan
	if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil {
		t.Fatalf("decode upgrade plan: %v", err)
	}
	if !plan.RequiresApproval ||
		plan.Migration.FromVersion != 47 ||
		plan.Migration.ToVersion != 50 ||
		plan.PreviewDigest == "" {
		t.Fatalf("upgrade plan = %+v", plan)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(
		t.Context(),
		[]string{
			"upgrade", "apply",
			"--data-dir", dataDir,
			"--approve-consequences",
			"--acknowledge-no-down-migration",
			"--preview-digest", plan.PreviewDigest,
		},
		&stdout,
		&stderr,
	); code != 0 {
		t.Fatalf("upgrade apply exit = %d, stderr = %s", code, stderr.String())
	}
	var result operations.UpgradeResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode upgrade result: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(filepath.Dir(result.BackupPath))
	})
	if result.FromVersion != 47 ||
		result.ToVersion != 50 ||
		result.Manifest.Mode != backup.FullFidelity {
		t.Fatalf("upgrade result = %+v", result)
	}
}
