package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/operations"
)

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
		!strings.Contains(stdout.String(), "format 1") {
		t.Fatalf("verify Backup output = %q", stdout.String())
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
}
