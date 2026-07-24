package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
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
}
