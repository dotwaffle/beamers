package acceptance_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/benbjohnson/litestream"
	"github.com/benbjohnson/litestream/file"
	_ "modernc.org/sqlite"
)

func TestExecutableReplicatesFullFidelityDatabaseBeforeStorageShutdown(t *testing.T) {
	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)
	_ = runBeamersOutput(t, bin, "bootstrap", "--data-dir", dataDir)

	replicaPath := filepath.Join(t.TempDir(), "replica")
	replicaURL := (&url.URL{Scheme: "file", Path: replicaPath}).String()
	process := startShutdownProcess(
		t,
		bin,
		"--data-dir", dataDir,
		"--replica-url", replicaURL,
		"--shutdown-timeout", "10s",
	)
	waitForReplication(t, process.address)
	if err := process.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("stop beamers: %v", err)
	}
	if err := waitForShutdownProcess(process, 5*time.Second); err != nil {
		t.Fatalf("beamers shutdown: %v\n%s", err, process.logs.String())
	}
	logs := process.logs.String()
	replicationFinalizer := strings.Index(logs, `"finalizer":"replication"`)
	storageFinalizer := strings.Index(logs, `"finalizer":"storage"`)
	if replicationFinalizer == -1 ||
		storageFinalizer == -1 ||
		replicationFinalizer > storageFinalizer {
		t.Fatalf("shutdown finalizer order is not replication then storage:\n%s", logs)
	}

	restoreClient := file.NewReplicaClient(replicaPath)
	restoreReplica := litestream.NewReplicaWithClient(nil, restoreClient)
	restoredPath := filepath.Join(t.TempDir(), "restored.db")
	options := litestream.NewRestoreOptions()
	options.OutputPath = restoredPath
	if err := restoreReplica.Restore(t.Context(), options); err != nil {
		t.Fatalf("restore full-fidelity replica: %v", err)
	}
	restored, err := sql.Open("sqlite", restoredPath)
	if err != nil {
		t.Fatalf("open restored database: %v", err)
	}
	defer func() {
		if closeErr := restored.Close(); closeErr != nil {
			t.Errorf("close restored database: %v", closeErr)
		}
	}()
	var tokenHash string
	if err = restored.QueryRowContext(
		t.Context(),
		`SELECT token_hash FROM bootstrap_credentials`,
	).Scan(&tokenHash); err != nil {
		t.Fatalf("read replicated authentication secret: %v", err)
	}
	if tokenHash == "" {
		t.Fatal("replicated authentication secret is empty")
	}
}

func TestReplicationConfigurationFailureNeverBlocksLiveControl(t *testing.T) {
	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)
	const secretURL = "https://operator:secret@example.test/replica"
	process := startShutdownProcess(
		t,
		bin,
		"--data-dir", dataDir,
		"--replica-url", secretURL,
	)

	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"http://"+process.address+"/readyz",
		http.NoBody,
	)
	if err != nil {
		t.Fatalf("create readiness request: %v", err)
	}
	response, err := (&http.Client{Timeout: time.Second}).Do(request)
	if err != nil {
		t.Fatalf("send readiness request: %v", err)
	}
	if closeErr := response.Body.Close(); closeErr != nil {
		t.Fatalf("close readiness: %v", closeErr)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("readiness = %d, want 200", response.StatusCode)
	}
	request, err = http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"http://"+process.address+"/diagnostics",
		http.NoBody,
	)
	if err != nil {
		t.Fatalf("create diagnostics request: %v", err)
	}
	diagnostics, err := (&http.Client{Timeout: time.Second}).Do(request)
	if err != nil {
		t.Fatalf("send diagnostics request: %v", err)
	}
	var found struct {
		Replication struct {
			Status     string `json:"status"`
			ErrorClass string `json:"error_class"`
		} `json:"replication"`
	}
	decodeErr := json.NewDecoder(diagnostics.Body).Decode(&found)
	closeErr := diagnostics.Body.Close()
	if decodeErr != nil || closeErr != nil {
		t.Fatalf("read diagnostics body: %v", errors.Join(decodeErr, closeErr))
	}
	if found.Replication.Status != "degraded" ||
		found.Replication.ErrorClass != "configuration" {
		t.Fatalf("replication diagnostics = %+v", found.Replication)
	}
	if strings.Contains(process.logs.String(), "operator") ||
		strings.Contains(process.logs.String(), "secret") {
		t.Fatalf("replica credentials leaked in logs:\n%s", process.logs.String())
	}
	if err = process.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("stop beamers: %v", err)
	}
	if err = waitForShutdownProcess(process, 5*time.Second); err != nil {
		t.Fatalf("beamers shutdown: %v", err)
	}
}

func waitForReplication(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	client := &http.Client{Timeout: time.Second}
	for time.Now().Before(deadline) {
		request, err := http.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			"http://"+address+"/diagnostics",
			http.NoBody,
		)
		if err != nil {
			t.Fatalf("create diagnostics request: %v", err)
		}
		response, err := client.Do(request)
		if err == nil {
			var found struct {
				Replication struct {
					Status             string     `json:"status"`
					Position           string     `json:"position"`
					LagTransactions    uint64     `json:"lag_transactions"`
					LastSuccessfulSync *time.Time `json:"last_successful_sync"`
					ErrorClass         string     `json:"error_class"`
				} `json:"replication"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&found)
			closeErr := response.Body.Close()
			if decodeErr == nil &&
				closeErr == nil &&
				found.Replication.Status == "replicating" &&
				found.Replication.Position != "" &&
				found.Replication.LagTransactions == 0 &&
				found.Replication.LastSuccessfulSync != nil &&
				found.Replication.ErrorClass == "" {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("replication did not become current")
}
