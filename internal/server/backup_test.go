package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/dotwaffle/beamers/ent/runtime"
	"github.com/dotwaffle/beamers/internal/backup"
	"github.com/dotwaffle/beamers/internal/operations"
)

func TestAdministratorDownloadsFullFidelityBackupAfterReauthentication(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "installation")
	if err := operations.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	installation, err := operations.OpenInstallation(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open installation: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := installation.Close(); closeErr != nil {
			t.Errorf("close installation: %v", closeErr)
		}
	})
	bootstrapToken, err := installation.Authentication().IssueBootstrap(t.Context())
	if err != nil {
		t.Fatalf("issue bootstrap: %v", err)
	}
	session, err := installation.Authentication().BootstrapAdministrator(
		t.Context(),
		bootstrapToken,
		"Administrator",
		"correct horse battery staple",
	)
	if err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}

	mux := http.NewServeMux()
	registerBackupRoutes(
		mux,
		installation,
		dataDir,
		filepath.Join(dataDir, "attachments"),
		nil,
		slog.New(slog.NewTextHandler(os.Stderr, nil)),
		&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080},
	)
	requestBody, err := json.Marshal(map[string]any{
		"mode":                            backup.FullFidelity,
		"password":                        "correct horse battery staple",
		"acknowledge_unencrypted_secrets": true,
	})
	if err != nil {
		t.Fatalf("encode Backup request: %v", err)
	}
	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"/admin/backups",
		bytes.NewReader(requestBody),
	)
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.Token})
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("Backup response = %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("X-Beamers-Backup-Mode") != string(backup.FullFidelity) {
		t.Fatalf("Backup mode header = %q", response.Header().Get("X-Beamers-Backup-Mode"))
	}
	archivePath := filepath.Join(t.TempDir(), "downloaded.zip")
	if err = os.WriteFile(archivePath, response.Body.Bytes(), 0o600); err != nil {
		t.Fatalf("write downloaded Backup: %v", err)
	}
	manifest, err := backup.Verify(archivePath)
	if err != nil {
		t.Fatalf("verify downloaded Backup: %v", err)
	}
	if manifest.Mode != backup.FullFidelity {
		t.Fatalf("downloaded Backup mode = %q", manifest.Mode)
	}
	restoredDataDir := filepath.Join(t.TempDir(), "restored")
	if _, err = backup.Restore(t.Context(), backup.RestoreInput{
		InputPath: archivePath,
		DataDir:   restoredDataDir,
	}); err != nil {
		t.Fatalf("Restore downloaded Backup: %v", err)
	}
	restored, err := operations.OpenInstallation(t.Context(), restoredDataDir)
	if err != nil {
		t.Fatalf("open Full-Fidelity Restore: %v", err)
	}
	if _, err = restored.Authentication().SignIn(
		t.Context(),
		"Administrator",
		"correct horse battery staple",
	); err != nil {
		_ = restored.Close()
		t.Fatalf("sign in after Full-Fidelity Restore: %v", err)
	}
	if err = restored.Close(); err != nil {
		t.Fatalf("close Full-Fidelity Restore: %v", err)
	}

	requestBody, err = json.Marshal(map[string]any{
		"mode":     backup.FullFidelity,
		"password": "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("encode unacknowledged request: %v", err)
	}
	request = httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"/admin/backups",
		bytes.NewReader(requestBody),
	)
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.Token})
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unacknowledged Backup response = %d: %s", response.Code, response.Body.String())
	}
}
