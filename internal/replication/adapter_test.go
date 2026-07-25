package replication

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/benbjohnson/litestream"
	"github.com/benbjohnson/litestream/file"
)

func TestAdapterReplicatesFullFidelity(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "beamers.db")
	database := openTestDatabase(t, databasePath)
	if _, err := database.ExecContext(
		t.Context(),
		`CREATE TABLE account (password_hash TEXT NOT NULL)`,
	); err != nil {
		t.Fatalf("create account table: %v", err)
	}

	replicaPath := filepath.Join(t.TempDir(), "replica")
	adapter := New(Config{
		DatabasePath: databasePath,
		Destination:  fileURL(replicaPath),
		Logger:       slog.New(slog.DiscardHandler),
	})
	startAdapter(t, adapter)
	if _, err := database.ExecContext(
		t.Context(),
		`INSERT INTO account (password_hash) VALUES ('secret-hash')`,
	); err != nil {
		t.Fatalf("insert secret: %v", err)
	}

	waitForCaughtUp(t, adapter)
	if err := adapter.Finalize(t.Context()); err != nil {
		t.Fatalf("finalize replication: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	restoreClient := file.NewReplicaClient(replicaPath)
	restoreReplica := litestream.NewReplicaWithClient(nil, restoreClient)
	restoredPath := filepath.Join(t.TempDir(), "restored.db")
	options := litestream.NewRestoreOptions()
	options.OutputPath = restoredPath
	if err := restoreReplica.Restore(t.Context(), options); err != nil {
		t.Fatalf("restore replica: %v", err)
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
	var passwordHash string
	if err = restored.QueryRowContext(
		t.Context(),
		`SELECT password_hash FROM account`,
	).Scan(&passwordHash); err != nil {
		t.Fatalf("read restored secret: %v", err)
	}
	if passwordHash != "secret-hash" {
		t.Fatalf("restored password hash = %q", passwordHash)
	}
}

func TestAdapterUsesCallerFinalizationBudget(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "beamers.db")
	database := openTestDatabase(t, databasePath)
	defer func() {
		if closeErr := database.Close(); closeErr != nil {
			t.Errorf("close database: %v", closeErr)
		}
	}()
	if _, err := database.ExecContext(t.Context(), `CREATE TABLE event (id INTEGER)`); err != nil {
		t.Fatalf("create event table: %v", err)
	}

	adapter := New(Config{
		DatabasePath: databasePath,
		Destination:  fileURL(filepath.Join(t.TempDir(), "replica")),
		Logger:       slog.New(slog.DiscardHandler),
	})
	startAdapter(t, adapter)
	if _, err := database.ExecContext(t.Context(), `INSERT INTO event (id) VALUES (1)`); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	started := time.Now()
	err := adapter.Finalize(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("finalize error = %v, want context canceled", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("finalize elapsed = %s, want caller-bounded", elapsed)
	}
}

func TestAdapterLifecycleWaitUsesCallerContext(t *testing.T) {
	adapter := New(Config{Logger: slog.New(slog.DiscardHandler)})
	if err := adapter.acquire(t.Context()); err != nil {
		t.Fatalf("hold lifecycle: %v", err)
	}
	defer adapter.release()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := adapter.Finalize(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("finalize error = %v, want deadline exceeded", err)
	}
}

func TestAdapterAsyncStartBudgetsLifecycleWait(t *testing.T) {
	adapter := New(Config{Logger: slog.New(slog.DiscardHandler)})
	if err := adapter.acquire(t.Context()); err != nil {
		t.Fatalf("hold lifecycle: %v", err)
	}
	defer adapter.release()

	started := time.Now()
	err := adapter.StartAsync(t.Context(), 10*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("start error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("start elapsed = %s, want coordinator-bounded", elapsed)
	}
}

func TestAdapterNeverStartsAfterLifecycleCancellation(t *testing.T) {
	adapter := New(Config{Logger: slog.New(slog.DiscardHandler)})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := adapter.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("start error = %v, want context canceled", err)
	}
	if status := adapter.Status(); status.ErrorClass != "canceled" {
		t.Fatalf("replication status = %+v", status)
	}
}

func TestAdapterNeverRestoresMissingAuthoritativeDatabase(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "missing.db")
	replicaPath := filepath.Join(t.TempDir(), "existing-replica")
	if err := os.MkdirAll(replicaPath, 0o700); err != nil {
		t.Fatalf("create replica directory: %v", err)
	}
	adapter := New(Config{
		DatabasePath: databasePath,
		Destination:  fileURL(replicaPath),
		Logger:       slog.New(slog.DiscardHandler),
	})
	if err := adapter.Start(t.Context()); err == nil {
		t.Fatal("replication unexpectedly started without an authoritative database")
	}
	if _, err := os.Stat(databasePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("authoritative database stat error = %v, want not exist", err)
	}
	if status := adapter.Status(); status.ErrorClass != "source_unavailable" {
		t.Fatalf("replication status = %+v", status)
	}
}

func openTestDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err = database.PingContext(t.Context()); err != nil {
		_ = database.Close()
		t.Fatalf("ping database: %v", err)
	}
	return database
}

func fileURL(path string) string {
	return (&url.URL{Scheme: "file", Path: path}).String()
}

func waitForCaughtUp(t *testing.T, adapter *Adapter) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status := adapter.Status()
		if status.Position != "" &&
			status.LagTransactions == 0 &&
			status.LastSuccessfulSync != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("replication did not catch up: %+v", adapter.Status())
}

func startAdapter(t *testing.T, adapter *Adapter) {
	t.Helper()
	if err := adapter.Start(t.Context()); err != nil {
		t.Fatalf("start replication: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := adapter.Finalize(ctx); err != nil {
			t.Errorf("clean up replication: %v", err)
		}
	})
}
