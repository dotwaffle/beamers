package acceptance_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/store/storetest"
)

func validEventInput() map[string]string {
	return map[string]string{
		"name":               "Revision 2026",
		"planned_start_date": "2026-08-21",
		"planned_end_date":   "2026-08-23",
		"timezone":           "Europe/Berlin",
		"event_locale":       "de-DE",
		"content_language":   "en-GB",
		"event_day_boundary": "06:00",
		"command_id":         "create-event-1",
	}
}

func startAuthenticatedAdministrator(t *testing.T) (*http.Client, *runningServer) {
	t.Helper()
	return startAuthenticatedAdministratorWithListeners(t, false)
}

func startAuthenticatedAdministratorWithPublicListener(
	t *testing.T,
) (*http.Client, *runningServer) {
	t.Helper()
	return startAuthenticatedAdministratorWithListeners(t, true)
}

func startAuthenticatedAdministratorWithListeners(
	t *testing.T,
	separatePublic bool,
) (*http.Client, *runningServer) {
	t.Helper()

	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)
	bootstrapToken := strings.TrimSpace(runBeamersOutput(t, bin, "bootstrap", "--data-dir", dataDir))
	client := authenticatedClient(t)
	var server *runningServer
	if separatePublic {
		server = startBeamersWithPublicListener(t, bin, dataDir)
	} else {
		server = startBeamers(t, bin, dataDir)
	}
	assertJSONRequest(
		t,
		client,
		server.address,
		"/auth/bootstrap",
		map[string]string{
			"bootstrap_token": bootstrapToken,
			"name":            "Ada Admin",
			"password":        "correct horse battery staple",
		},
		http.StatusCreated,
		"",
	)
	return client, server
}

func TestSignInFailuresAreGenericAndRateLimited(t *testing.T) {
	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)
	bootstrapToken := strings.TrimSpace(runBeamersOutput(t, bin, "bootstrap", "--data-dir", dataDir))

	client := authenticatedClient(t)
	server := startBeamers(t, bin, dataDir)
	assertJSONRequest(
		t,
		client,
		server.address,
		"/auth/bootstrap",
		map[string]string{
			"bootstrap_token": bootstrapToken,
			"name":            "Ada Admin",
			"password":        "correct horse battery staple",
		},
		http.StatusCreated,
		"",
	)
	assertJSONRequest(t, client, server.address, "/auth/sign-out", nil, http.StatusNoContent, "")

	assertJSONRequest(
		t,
		client,
		server.address,
		"/auth/sign-in",
		map[string]string{"name": "Unknown Account", "password": "wrong password"},
		http.StatusUnauthorized,
		"authentication failed\n",
	)
	for range 5 {
		assertJSONRequest(
			t,
			client,
			server.address,
			"/auth/sign-in",
			map[string]string{"name": "Ada Admin", "password": "wrong password"},
			http.StatusUnauthorized,
			"authentication failed\n",
		)
	}
	assertJSONRequest(
		t,
		client,
		server.address,
		"/auth/sign-in",
		map[string]string{
			"name":     "Ada Admin",
			"password": "correct horse battery staple",
		},
		http.StatusTooManyRequests,
		"authentication failed\n",
	)
	server.stop(t)
}

func TestInvalidDisplayCookiesCannotBypassEnrollmentRateLimit(t *testing.T) {
	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)
	server := startBeamers(t, bin, dataDir)
	client := &http.Client{Timeout: 5 * time.Second}

	for attempt := range 11 {
		request, err := http.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			"http://"+server.address+"/display",
			http.NoBody,
		)
		if err != nil {
			t.Fatalf("create Display Enrollment request: %v", err)
		}
		request.AddCookie(&http.Cookie{Name: "beamers_display", Value: "invalid"})
		request.AddCookie(&http.Cookie{Name: "beamers_display_enrollment", Value: "invalid"})
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("send Display Enrollment request: %v", err)
		}
		if closeErr := response.Body.Close(); closeErr != nil {
			t.Fatalf("close Display Enrollment response: %v", closeErr)
		}
		want := http.StatusOK
		if attempt == 10 {
			want = http.StatusTooManyRequests
		}
		if response.StatusCode != want {
			t.Fatalf(
				"Display Enrollment attempt %d = %d, want %d",
				attempt+1,
				response.StatusCode,
				want,
			)
		}
	}
	server.stop(t)
}

func TestPlaintextNonLoopbackRefusesAuthentication(t *testing.T) {
	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)
	bootstrapToken := strings.TrimSpace(runBeamersOutput(t, bin, "bootstrap", "--data-dir", dataDir))

	server := startBeamersAt(t, bin, dataDir, "0.0.0.0:0")
	_, port, err := net.SplitHostPort(server.address)
	if err != nil {
		t.Fatalf("parse non-loopback listener address: %v", err)
	}
	dialAddress := net.JoinHostPort("127.0.0.1", port)
	assertJSONRequest(
		t,
		authenticatedClient(t),
		dialAddress,
		"/auth/bootstrap",
		map[string]string{
			"bootstrap_token": bootstrapToken,
			"name":            "Ada Admin",
			"password":        "correct horse battery staple",
		},
		http.StatusForbidden,
		"secure transport required\n",
	)
	server.stop(t)
}

func TestInstallationStartsHealthyAndRestarts(t *testing.T) {
	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")

	runBeamers(t, bin, "init", "--data-dir", dataDir)
	databasePath := filepath.Join(dataDir, "beamers.db")
	initialDatabase, err := os.Stat(databasePath)
	if err != nil {
		t.Fatalf("stat initialized database: %v", err)
	}

	first := startBeamers(t, bin, dataDir)
	assertProbe(t, first.address, "/livez", "live\n")
	assertProbe(t, first.address, "/readyz", "ready\n")
	first.stop(t)

	second := startBeamers(t, bin, dataDir)
	assertProbe(t, second.address, "/livez", "live\n")
	assertProbe(t, second.address, "/readyz", "ready\n")
	second.stop(t)
	restartedDatabase, err := os.Stat(databasePath)
	if err != nil {
		t.Fatalf("stat restarted database: %v", err)
	}
	if !os.SameFile(initialDatabase, restartedDatabase) {
		t.Error("restart replaced the initialized database")
	}

	output, err := exec.CommandContext(t.Context(), bin, "init", "--data-dir", dataDir).CombinedOutput()
	if err == nil {
		t.Fatalf("second initialization succeeded; output:\n%s", output)
	}
}

func TestHealthyInstallationExposesBoundedLocalDiagnostics(t *testing.T) {
	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)
	server := startBeamers(t, bin, dataDir)

	response := get(t, authenticatedClient(t), server.address, "/diagnostics")
	defer func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("close diagnostics response: %v", err)
		}
	}()
	var found struct {
		Mode    string `json:"mode"`
		Storage struct {
			Status string `json:"status"`
		} `json:"storage"`
		Backup struct {
			Status     string   `json:"status"`
			AgeSeconds *float64 `json:"age_seconds"`
		} `json:"backup"`
		DiskSpace struct {
			Status       string `json:"status"`
			FreeBytes    uint64 `json:"free_bytes"`
			WarningBytes uint64 `json:"warning_bytes"`
		} `json:"disk_space"`
		StorageSize struct {
			DatabaseBytes    uint64 `json:"database_bytes"`
			AttachmentsBytes uint64 `json:"attachments_bytes"`
		} `json:"storage_size"`
		Replication struct {
			Status string `json:"status"`
		} `json:"replication"`
		Streams map[string]struct {
			Status      string `json:"status"`
			Subscribers int    `json:"subscribers"`
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
	if response.StatusCode != http.StatusOK ||
		response.Header.Get("Cache-Control") != "no-store" ||
		found.Mode != "normal" ||
		found.Storage.Status != "ready" ||
		found.Backup.Status != "available" ||
		found.Backup.AgeSeconds != nil ||
		found.DiskSpace.Status != "ready" ||
		found.DiskSpace.FreeBytes == 0 ||
		found.DiskSpace.WarningBytes == 0 ||
		found.StorageSize.DatabaseBytes == 0 ||
		found.Replication.Status != "disabled" ||
		found.Streams["display"].Status != "ready" ||
		found.Streams["program"].Status != "ready" ||
		found.Displays.Total != 0 ||
		found.Telemetry.Status != "disabled" {
		t.Fatalf("diagnostics = %d %+v, headers %v", response.StatusCode, found, response.Header)
	}
	server.stop(t)
}

func TestServeDoesNotInitializeStorage(t *testing.T) {
	bin := buildBeamers(t)
	missingDataDir := filepath.Join(t.TempDir(), "missing")

	missing := startBeamersAt(t, bin, missingDataDir, "0.0.0.0:0")
	assertRecoveryProbes(t, missing.address)
	assertLoopbackAddress(t, missing.address)
	missing.stop(t)
	if _, err := os.Stat(missingDataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("serve created missing data directory: %v", err)
	}

	uninitializedDataDir := t.TempDir()
	databasePath := filepath.Join(uninitializedDataDir, "beamers.db")
	if err := os.WriteFile(databasePath, nil, 0o600); err != nil {
		t.Fatalf("create uninitialized database: %v", err)
	}
	uninitialized := startBeamers(t, bin, uninitializedDataDir)
	assertRecoveryProbes(t, uninitialized.address)
	uninitialized.stop(t)
	contents, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatalf("read uninitialized database after serve: %v", err)
	}
	if len(contents) != 0 {
		t.Fatalf("serve changed uninitialized database to %d bytes", len(contents))
	}
	entries, err := os.ReadDir(uninitializedDataDir)
	if err != nil {
		t.Fatalf("read uninitialized data directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "beamers.db" {
		t.Fatalf("serve changed uninitialized data directory: %v", entries)
	}
}

func TestServeRefusesUnsupportedSchema(t *testing.T) {
	bin := buildBeamers(t)
	tests := []struct {
		name    string
		prepare func(context.Context, string) error
	}{
		{name: "newer version", prepare: storetest.MarkSchemaNewer},
		{name: "unknown migration", prepare: storetest.ReplaceMigrationChecksum},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataDir := filepath.Join(t.TempDir(), "data")
			runBeamers(t, bin, "init", "--data-dir", dataDir)
			if err := test.prepare(t.Context(), filepath.Join(dataDir, "beamers.db")); err != nil {
				t.Fatalf("prepare unsupported schema: %v", err)
			}
			server := startBeamers(t, bin, dataDir)
			assertRecoveryProbes(t, server.address)
			server.stop(t)
		})
	}
}

func TestUnsafeStartupOnlyServesLocalRecoveryDiagnostics(t *testing.T) {
	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)
	if err := storetest.MarkSchemaNewer(
		t.Context(),
		filepath.Join(dataDir, "beamers.db"),
	); err != nil {
		t.Fatalf("make installation schema unsafe: %v", err)
	}

	server := startBeamersAt(t, bin, dataDir, "0.0.0.0:0")
	assertLoopbackAddress(t, server.address)
	assertRecoveryProbes(t, server.address)
	assertRecoveryDiagnostics(t, server.address, "unsupported_schema", "")
	assertGETResponse(
		t,
		authenticatedClient(t),
		server.address,
		"/schedule",
		http.StatusNotFound,
		"404 page not found\n",
	)
	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"http://"+server.address+"/admin/events",
		strings.NewReader("{}"),
	)
	if err != nil {
		t.Fatalf("create recovery Crew request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	response, err := authenticatedClient(t).Do(request)
	if err != nil {
		t.Fatalf("send recovery Crew request: %v", err)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err = errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read recovery Crew response: %v", err)
	}
	if response.StatusCode != http.StatusNotFound ||
		response.Header.Get("X-Beamers-Build") != "" ||
		string(body) != "404 page not found\n" {
		t.Fatalf("recovery Crew response = %d %q, headers %v", response.StatusCode, body, response.Header)
	}
	server.stop(t)
}

func TestUnreadableInstallationMarkerStaysInLocalRecovery(t *testing.T) {
	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)
	markerPath := filepath.Join(dataDir, ".beamers.lock")
	if err := os.Chmod(markerPath, 0); err != nil {
		t.Fatalf("make installation marker unreadable: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(markerPath, 0o600)
	})

	server := startBeamersAt(t, bin, dataDir, "0.0.0.0:0")
	assertLoopbackAddress(t, server.address)
	assertRecoveryProbes(t, server.address)
	assertRecoveryDiagnostics(t, server.address, "unavailable", "open installation lock")
	server.stop(t)
}

func TestDamagedRestoreJournalStaysInLocalRecovery(t *testing.T) {
	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)
	backupPath := filepath.Join(t.TempDir(), "backup.zip")
	runBeamers(
		t,
		bin,
		"backup",
		"--data-dir", dataDir,
		"--output", backupPath,
	)
	journalPath := dataDir + ".beamers-restore.json"
	if err := os.WriteFile(journalPath, []byte("damaged"), 0o600); err != nil {
		t.Fatalf("damage Restore journal: %v", err)
	}

	server := startBeamersAt(t, bin, dataDir, "0.0.0.0:0")
	assertLoopbackAddress(t, server.address)
	assertRecoveryProbes(t, server.address)
	assertRecoveryDiagnostics(t, server.address, "unavailable", "recover interrupted Restore")
	server.stop(t)
	if contents, err := os.ReadFile(journalPath); err != nil || string(contents) != "damaged" {
		t.Fatalf("damaged Restore journal changed = %q, %v", contents, err)
	}

	runBeamersFails(
		t,
		bin,
		"restore", "quarantine-journal",
		"--data-dir", dataDir,
	)
	output := strings.TrimSpace(runBeamersOutput(
		t,
		bin,
		"restore", "quarantine-journal",
		"--data-dir", dataDir,
		"--acknowledge-damaged-journal",
	))
	const prefix = "preserved damaged Restore journal at "
	if !strings.HasPrefix(output, prefix) {
		t.Fatalf("quarantine journal output = %q", output)
	}
	preservedPath := strings.TrimPrefix(output, prefix)
	if contents, err := os.ReadFile(preservedPath); err != nil || string(contents) != "damaged" {
		t.Fatalf("preserved Restore journal = %q, %v", contents, err)
	}
	runBeamersOutput(
		t,
		bin,
		"restore", "preview",
		"--input", backupPath,
		"--data-dir", dataDir,
	)
}

func TestMissingDatabaseCannotBeReinitialized(t *testing.T) {
	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)
	if err := os.Remove(filepath.Join(dataDir, "beamers.db")); err != nil {
		t.Fatalf("remove initialized database: %v", err)
	}
	runBeamersFails(t, bin, "init", "--data-dir", dataDir)
}

func TestInitializationRequiresCompleteUnusedInstallationState(t *testing.T) {
	bin := buildBeamers(t)
	root := t.TempDir()

	failedCommandDataDir := filepath.Join(root, "failed-command-data")
	runBeamersFails(
		t,
		bin,
		"backup",
		"--data-dir", failedCommandDataDir,
		"--output", filepath.Join(root, "missing-backup.zip"),
	)
	runBeamersFails(t, bin, "bootstrap", "--data-dir", failedCommandDataDir)
	runBeamers(t, bin, "init", "--data-dir", failedCommandDataDir)

	journalDataDir := filepath.Join(root, "journal-data")
	if err := os.WriteFile(
		journalDataDir+".beamers-restore.json",
		[]byte("interrupted"),
		0o600,
	); err != nil {
		t.Fatalf("write Restore journal: %v", err)
	}
	runBeamersFails(t, bin, "init", "--data-dir", journalDataDir)
	if _, err := os.Stat(journalDataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("initialization changed journaled installation: %v", err)
	}

	usedAttachments := filepath.Join(root, "used-attachments")
	if err := os.Mkdir(usedAttachments, 0o700); err != nil {
		t.Fatalf("create used Attachment Store: %v", err)
	}
	if err := os.WriteFile(filepath.Join(usedAttachments, "owned"), []byte("state"), 0o600); err != nil {
		t.Fatalf("write Attachment state: %v", err)
	}
	attachmentDataDir := filepath.Join(root, "attachment-data")
	runBeamersFails(
		t,
		bin,
		"init",
		"--data-dir", attachmentDataDir,
		"--attachments-dir", usedAttachments,
	)
	if _, err := os.Stat(attachmentDataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("initialization changed installation with Attachment state: %v", err)
	}

	emptyAttachments := filepath.Join(root, "empty-attachments")
	if err := os.Mkdir(emptyAttachments, 0o700); err != nil {
		t.Fatalf("create empty Attachment Store: %v", err)
	}
	unusedDataDir := filepath.Join(root, "unused-data")
	runBeamers(
		t,
		bin,
		"init",
		"--data-dir", unusedDataDir,
		"--attachments-dir", emptyAttachments,
	)
}

func TestRestoreRequiresExclusiveDatabaseAndAttachmentRoots(t *testing.T) {
	bin := buildBeamers(t)
	root := t.TempDir()

	sourceDataDir := filepath.Join(root, "source")
	runBeamers(t, bin, "init", "--data-dir", sourceDataDir)
	backupPath := filepath.Join(root, "backup.zip")
	runBeamers(t, bin, "backup", "--data-dir", sourceDataDir, "--output", backupPath)

	targetDataDir := filepath.Join(root, "target")
	runBeamers(t, bin, "init", "--data-dir", targetDataDir)
	sharedAttachments := filepath.Join(root, "shared-attachments")
	if err := os.Mkdir(sharedAttachments, 0o700); err != nil {
		t.Fatalf("create shared Attachment Store: %v", err)
	}
	oldAttachment := filepath.Join(sharedAttachments, "owned")
	if err := os.WriteFile(oldAttachment, []byte("current"), 0o600); err != nil {
		t.Fatalf("write current Attachment state: %v", err)
	}
	planOutput := runBeamersOutput(
		t,
		bin,
		"restore", "preview",
		"--input", backupPath,
		"--data-dir", targetDataDir,
		"--attachments-dir", sharedAttachments,
	)
	var plan struct {
		JournalPath string `json:"journal_path"`
	}
	if err := json.Unmarshal([]byte(planOutput), &plan); err != nil || plan.JournalPath == "" {
		t.Fatalf("decode Restore plan: %+v, %v", plan, err)
	}

	holderDataDir := filepath.Join(root, "holder")
	runBeamers(t, bin, "init", "--data-dir", holderDataDir)
	holder := startBeamersWithAttachments(t, bin, holderDataDir, sharedAttachments)
	runBeamersFails(
		t,
		bin,
		"restore", "apply",
		"--journal", plan.JournalPath,
		"--acknowledge-replacement",
	)
	if contents, err := os.ReadFile(oldAttachment); err != nil || string(contents) != "current" {
		t.Fatalf("contended Restore changed Attachment state = %q, %v", contents, err)
	}
	holder.stop(t)

	runBeamers(
		t,
		bin,
		"restore", "apply",
		"--journal", plan.JournalPath,
		"--acknowledge-replacement",
	)
}
