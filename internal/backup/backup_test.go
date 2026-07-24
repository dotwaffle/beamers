package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/dotwaffle/beamers/ent/runtime"
	"github.com/dotwaffle/beamers/internal/store"

	_ "modernc.org/sqlite"
)

func TestSanitizedBackupIncludesConfiguredAttachmentsAndRemovesCredentials(t *testing.T) {
	ctx := t.Context()
	dataDir := filepath.Join(t.TempDir(), "installation")
	if err := store.Initialize(ctx, dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	attachmentRoot := filepath.Join(t.TempDir(), "attachment-store")
	content := []byte("immutable attachment")
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	storageKey := filepath.Join("sha256", digest[:2], digest)
	attachmentPath := filepath.Join(attachmentRoot, storageKey)
	if err := os.MkdirAll(filepath.Dir(attachmentPath), 0o700); err != nil {
		t.Fatalf("prepare Attachment directory: %v", err)
	}
	if err := os.WriteFile(attachmentPath, content, 0o600); err != nil {
		t.Fatalf("write Attachment: %v", err)
	}
	seedBackupState(t, ctx, dataDir, storageKey, digest, int64(len(content)))

	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	manifest, err := Create(ctx, CreateInput{
		DataDir:        dataDir,
		AttachmentsDir: attachmentRoot,
		OutputPath:     archivePath,
		Mode:           Sanitized,
		Now:            time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create Backup: %v", err)
	}
	if len(manifest.Attachments) != 1 ||
		manifest.Attachments[0].StorageKey != storageKey ||
		manifest.Attachments[0].SHA256 != digest {
		t.Fatalf("Attachment inventory = %+v", manifest.Attachments)
	}

	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatalf("open Backup: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := archive.Close(); closeErr != nil {
			t.Errorf("close Backup: %v", closeErr)
		}
	})
	attachmentName, err := attachmentArchiveName(storageKey)
	if err != nil {
		t.Fatalf("name Attachment entry: %v", err)
	}
	assertZIPContent(t, archive.File, attachmentName, content)
	databasePath := filepath.Join(t.TempDir(), "restored.db")
	extractZIPEntry(t, archive.File, databaseName, databasePath)
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open sanitized database: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := database.Close(); closeErr != nil {
			t.Errorf("close sanitized database: %v", closeErr)
		}
	})
	for _, table := range []string{
		"password_credentials",
		"account_sessions",
		"bootstrap_credentials",
		"display_credentials",
		"display_enrollments",
		"upload_links",
	} {
		var count int
		if err = database.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s count = %d, want 0", table, count)
		}
	}
	var accounts int
	if err = database.QueryRowContext(ctx, "SELECT count(*) FROM accounts").Scan(&accounts); err != nil {
		t.Fatalf("count Accounts: %v", err)
	}
	if accounts != 1 {
		t.Fatalf("Account identities = %d, want 1", accounts)
	}

	restoredDataDir := filepath.Join(t.TempDir(), "restored")
	restoredAttachmentsDir := filepath.Join(t.TempDir(), "restored-attachments")
	restoredManifest, err := Restore(ctx, RestoreInput{
		InputPath:      archivePath,
		DataDir:        restoredDataDir,
		AttachmentsDir: restoredAttachmentsDir,
	})
	if err != nil {
		t.Fatalf("Restore Backup: %v", err)
	}
	if restoredManifest.Mode != Sanitized {
		t.Fatalf("restored mode = %q, want %q", restoredManifest.Mode, Sanitized)
	}
	restoredContent, err := os.ReadFile(filepath.Join(restoredAttachmentsDir, storageKey))
	if err != nil {
		t.Fatalf("read restored Attachment: %v", err)
	}
	if !bytes.Equal(restoredContent, content) {
		t.Fatalf("restored Attachment = %q, want %q", restoredContent, content)
	}
	if err = store.ValidateSnapshot(
		ctx,
		filepath.Join(restoredDataDir, "beamers.db"),
	); err != nil {
		t.Fatalf("validate restored database: %v", err)
	}
}

func TestVerifyRejectsTamperedAttachment(t *testing.T) {
	ctx := t.Context()
	dataDir := filepath.Join(t.TempDir(), "installation")
	if err := store.Initialize(ctx, dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	content := []byte("original")
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	storageKey := filepath.Join("sha256", digest[:2], digest)
	attachmentPath := filepath.Join(dataDir, "attachments", storageKey)
	if err := os.MkdirAll(filepath.Dir(attachmentPath), 0o700); err != nil {
		t.Fatalf("prepare Attachment directory: %v", err)
	}
	if err := os.WriteFile(attachmentPath, content, 0o600); err != nil {
		t.Fatalf("write Attachment: %v", err)
	}
	seedBackupState(t, ctx, dataDir, storageKey, digest, int64(len(content)))
	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	if _, err := Create(ctx, CreateInput{
		DataDir: dataDir, OutputPath: archivePath, Mode: Sanitized,
	}); err != nil {
		t.Fatalf("create Backup: %v", err)
	}

	tamperedPath := filepath.Join(t.TempDir(), "tampered.zip")
	rewriteZIPEntry(t, archivePath, tamperedPath, attachmentArchivePath(t, storageKey), []byte("tampered"))
	if _, err := Verify(tamperedPath); err == nil {
		t.Fatal("tampered Attachment unexpectedly verified")
	}
}

func seedBackupState(
	t *testing.T,
	ctx context.Context,
	dataDir, storageKey, digest string,
	size int64,
) {
	t.Helper()
	database, err := sql.Open("sqlite", filepath.Join(dataDir, "beamers.db"))
	if err != nil {
		t.Fatalf("open installation database: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := database.Close(); closeErr != nil {
			t.Errorf("close installation database: %v", closeErr)
		}
	})
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	statements := []struct {
		query string
		args  []any
	}{
		{
			"INSERT INTO accounts (id, name, normalized_name, administrator, created_at) VALUES (1, 'Admin', 'admin', 1, ?)",
			[]any{now},
		},
		{
			"INSERT INTO password_credentials (account_id, password_hash, created_at) VALUES (1, 'secret-hash', ?)",
			[]any{now},
		},
		{
			"INSERT INTO account_sessions (account_id, token_hash, created_at, expires_at) VALUES (1, ?, ?, ?)",
			[]any{digest, now, now.Add(time.Hour)},
		},
		{
			"INSERT INTO attachments (id, event_id, owner_type, owner_id, name, created_at) VALUES (1, 1, 'Presentation', 1, 'slides', ?)",
			[]any{now},
		},
		{
			"INSERT INTO attachment_versions (id, version, original_filename, media_type, size_bytes, sha256, storage_key, uploader_type, uploader_id, created_at, attachment_id) VALUES (1, 1, 'slides.pdf', 'application/pdf', ?, ?, ?, 'Crew', 1, ?, 1)",
			[]any{size, digest, storageKey, now},
		},
	}
	for _, statement := range statements {
		if _, err = database.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed Backup state: %v", err)
		}
	}
}

func assertZIPContent(t *testing.T, files []*zip.File, name string, want []byte) {
	t.Helper()
	for _, file := range files {
		if file.Name != name {
			continue
		}
		input, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		found, err := io.ReadAll(input)
		_ = input.Close()
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !bytes.Equal(found, want) {
			t.Fatalf("%s content = %q, want %q", name, found, want)
		}
		return
	}
	t.Fatalf("Backup entry %q not found", name)
}

func extractZIPEntry(t *testing.T, files []*zip.File, name, destination string) {
	t.Helper()
	for _, file := range files {
		if file.Name != name {
			continue
		}
		input, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			_ = input.Close()
			t.Fatalf("create extracted database: %v", err)
		}
		_, copyErr := io.Copy(output, input)
		closeErr := output.Close()
		_ = input.Close()
		if copyErr != nil || closeErr != nil {
			t.Fatalf("extract database: %v / %v", copyErr, closeErr)
		}
		return
	}
	t.Fatalf("Backup entry %q not found", name)
}

func attachmentArchivePath(t *testing.T, storageKey string) string {
	t.Helper()
	name, err := attachmentArchiveName(storageKey)
	if err != nil {
		t.Fatalf("name Attachment entry: %v", err)
	}
	return name
}

func rewriteZIPEntry(t *testing.T, source, destination, name string, replacement []byte) {
	t.Helper()
	input, err := zip.OpenReader(source)
	if err != nil {
		t.Fatalf("open source Backup: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := input.Close(); closeErr != nil {
			t.Errorf("close source Backup: %v", closeErr)
		}
	})
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create rewritten Backup: %v", err)
	}
	writer := zip.NewWriter(output)
	for _, file := range input.File {
		entry, createErr := writer.CreateHeader(&zip.FileHeader{Name: file.Name, Method: file.Method})
		if createErr != nil {
			t.Fatalf("create rewritten entry: %v", createErr)
		}
		if file.Name == name {
			if _, createErr = entry.Write(replacement); createErr != nil {
				t.Fatalf("replace rewritten entry: %v", createErr)
			}
			continue
		}
		sourceEntry, openErr := file.Open()
		if openErr != nil {
			t.Fatalf("open source entry: %v", openErr)
		}
		_, copyErr := io.Copy(entry, sourceEntry)
		_ = sourceEntry.Close()
		if copyErr != nil {
			t.Fatalf("copy source entry: %v", copyErr)
		}
	}
	if err = writer.Close(); err != nil {
		t.Fatalf("close rewritten Backup: %v", err)
	}
	if err = output.Close(); err != nil {
		t.Fatalf("close rewritten file: %v", err)
	}
}
