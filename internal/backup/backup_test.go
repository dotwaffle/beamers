package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	for _, table := range []string{"accounts", "displays", "audit_entries"} {
		var count int
		if err = database.QueryRowContext(
			ctx,
			"SELECT count(*) FROM "+table,
		).Scan(&count); err != nil {
			t.Fatalf("count preserved %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("preserved %s count = %d, want 1", table, count)
		}
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

func TestVerifyRejectsTamperedManifest(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "installation")
	if err := store.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	if _, err := Create(t.Context(), CreateInput{
		DataDir: dataDir, OutputPath: archivePath, Mode: Sanitized,
	}); err != nil {
		t.Fatalf("create Backup: %v", err)
	}
	tamperedPath := filepath.Join(t.TempDir(), "tampered.zip")
	rewriteZIPEntry(
		t,
		archivePath,
		tamperedPath,
		manifestName,
		[]byte(`{"format_version":1,"mode":"Sanitized","schema_version":1,`+
			`"minimum_reader_schema_version":1,"minimum_writer_schema_version":1,`+
			`"created_at":"2026-07-24T12:00:00Z","database_sha256":"tampered",`+
			`"attachments":[]}`),
	)
	if _, err := Verify(tamperedPath); err == nil {
		t.Fatal("tampered manifest unexpectedly verified")
	}
}

func TestRestoreRejectsFullFidelityBackupRelabeledSanitized(t *testing.T) {
	ctx := t.Context()
	dataDir := filepath.Join(t.TempDir(), "installation")
	if err := store.Initialize(ctx, dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	storage, err := store.Open(ctx, dataDir)
	if err != nil {
		t.Fatalf("open installation: %v", err)
	}
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	bootstrapHash := strings.Repeat("b", 64)
	if err = storage.IssueBootstrap(ctx, bootstrapHash, now, now.Add(time.Hour)); err != nil {
		_ = storage.Close()
		t.Fatalf("issue bootstrap: %v", err)
	}
	if _, err = storage.BootstrapAdministrator(ctx, store.BootstrapAdministratorParams{
		BootstrapHash: bootstrapHash, Name: "Administrator", NormalizedName: "administrator",
		PasswordHash: "secret-password-hash", SessionHash: strings.Repeat("s", 64),
		Now: now, SessionExpiry: now.Add(time.Hour),
	}); err != nil {
		_ = storage.Close()
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	if err = storage.Close(); err != nil {
		t.Fatalf("close installation: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "full-fidelity.zip")
	if _, err = Create(ctx, CreateInput{
		DataDir: dataDir, OutputPath: archivePath, Mode: FullFidelity,
	}); err != nil {
		t.Fatalf("create Full-Fidelity Backup: %v", err)
	}
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatalf("open Full-Fidelity Backup: %v", err)
	}
	var manifest Manifest
	for _, file := range archive.File {
		if file.Name == manifestName {
			if err = decodeZIPJSON(file, &manifest); err != nil {
				_ = archive.Close()
				t.Fatalf("decode manifest: %v", err)
			}
		}
	}
	if err = archive.Close(); err != nil {
		t.Fatalf("close Full-Fidelity Backup: %v", err)
	}
	manifest.Mode = Sanitized
	replacement, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode relabeled manifest: %v", err)
	}
	tamperedPath := filepath.Join(t.TempDir(), "relabeled.zip")
	rewriteZIPEntry(t, archivePath, tamperedPath, manifestName, replacement)
	if _, err = PrepareRestore(ctx, RestoreInput{
		InputPath: tamperedPath,
		DataDir:   filepath.Join(t.TempDir(), "restored"),
	}); err == nil {
		t.Fatal("Full-Fidelity Backup relabeled Sanitized unexpectedly prepared")
	}
}

func TestRestoreReplacesExistingInstallationThroughDurableJournal(t *testing.T) {
	ctx := t.Context()
	sourceDataDir := filepath.Join(t.TempDir(), "source")
	if err := store.Initialize(ctx, sourceDataDir); err != nil {
		t.Fatalf("initialize source installation: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	if _, err := Create(ctx, CreateInput{
		DataDir: sourceDataDir, OutputPath: archivePath, Mode: Sanitized,
	}); err != nil {
		t.Fatalf("create Backup: %v", err)
	}

	targetDataDir := filepath.Join(t.TempDir(), "target")
	if err := store.Initialize(ctx, targetDataDir); err != nil {
		t.Fatalf("initialize target installation: %v", err)
	}
	oldMarker := filepath.Join(targetDataDir, "old-generation")
	if err := os.WriteFile(oldMarker, []byte("old"), 0o600); err != nil {
		t.Fatalf("write old-generation marker: %v", err)
	}

	plan, err := PrepareRestore(ctx, RestoreInput{
		InputPath: archivePath,
		DataDir:   targetDataDir,
		Replace:   true,
	})
	if err != nil {
		t.Fatalf("prepare Restore: %v", err)
	}
	if plan.JournalPath == "" || plan.DataQuarantine == "" {
		t.Fatalf("Restore plan lacks exact durable paths: %+v", plan)
	}
	if _, err = os.Stat(oldMarker); err != nil {
		t.Fatalf("Restore preview changed current installation: %v", err)
	}

	if _, err = ApplyRestore(ctx, plan.JournalPath); err != nil {
		t.Fatalf("apply Restore: %v", err)
	}
	if _, err = os.Stat(oldMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old marker remains in restored installation: %v", err)
	}
	if content, readErr := os.ReadFile(
		filepath.Join(plan.DataQuarantine, "old-generation"),
	); readErr != nil || string(content) != "old" {
		t.Fatalf("quarantined old generation = %q, %v", content, readErr)
	}
	if _, err = os.Stat(plan.JournalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed Restore journal remains: %v", err)
	}
	restored, err := store.Open(ctx, targetDataDir)
	if err != nil {
		t.Fatalf("open restored installation: %v", err)
	}
	if err = errors.Join(restored.Ready(ctx), restored.Close()); err != nil {
		t.Fatalf("restored installation readiness: %v", err)
	}
}

func TestRestoreRejectsStagingChangedAfterPreview(t *testing.T) {
	ctx := t.Context()
	sourceDataDir := filepath.Join(t.TempDir(), "source")
	if err := store.Initialize(ctx, sourceDataDir); err != nil {
		t.Fatalf("initialize source installation: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	if _, err := Create(ctx, CreateInput{
		DataDir: sourceDataDir, OutputPath: archivePath, Mode: Sanitized,
	}); err != nil {
		t.Fatalf("create Backup: %v", err)
	}
	targetDataDir := filepath.Join(t.TempDir(), "target")
	if err := store.Initialize(ctx, targetDataDir); err != nil {
		t.Fatalf("initialize target installation: %v", err)
	}
	oldMarker := filepath.Join(targetDataDir, "old-generation")
	if err := os.WriteFile(oldMarker, []byte("old"), 0o600); err != nil {
		t.Fatalf("write old-generation marker: %v", err)
	}
	plan, err := PrepareRestore(ctx, RestoreInput{
		InputPath: archivePath, DataDir: targetDataDir, Replace: true,
	})
	if err != nil {
		t.Fatalf("prepare Restore: %v", err)
	}
	journal, err := readRestoreJournal(plan.JournalPath)
	if err != nil {
		t.Fatalf("read Restore journal: %v", err)
	}
	if err = os.WriteFile(
		filepath.Join(journal.StagedData, "beamers.db"),
		[]byte("changed after preview"),
		0o600,
	); err != nil {
		t.Fatalf("change staged database: %v", err)
	}

	if _, err = ApplyRestore(ctx, plan.JournalPath); err == nil {
		t.Fatal("changed Restore staging unexpectedly applied")
	}
	if content, readErr := os.ReadFile(oldMarker); readErr != nil || string(content) != "old" {
		t.Fatalf("current installation changed = %q, %v", content, readErr)
	}
}

func TestRestoreRecoversInterruptedCrossFilesystemCutover(t *testing.T) {
	ctx := t.Context()
	sourceDataDir := filepath.Join(t.TempDir(), "source")
	if err := store.Initialize(ctx, sourceDataDir); err != nil {
		t.Fatalf("initialize source installation: %v", err)
	}
	sourceAttachmentsDir := filepath.Join(t.TempDir(), "source-attachments")
	content := []byte("new attachment")
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	storageKey := filepath.Join("sha256", digest[:2], digest)
	sourceAttachment := filepath.Join(sourceAttachmentsDir, storageKey)
	if err := os.MkdirAll(filepath.Dir(sourceAttachment), 0o700); err != nil {
		t.Fatalf("prepare source Attachment directory: %v", err)
	}
	if err := os.WriteFile(sourceAttachment, content, 0o600); err != nil {
		t.Fatalf("write source Attachment: %v", err)
	}
	seedBackupState(t, ctx, sourceDataDir, storageKey, digest, int64(len(content)))
	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	if _, err := Create(ctx, CreateInput{
		DataDir:        sourceDataDir,
		AttachmentsDir: sourceAttachmentsDir,
		OutputPath:     archivePath,
		Mode:           Sanitized,
	}); err != nil {
		t.Fatalf("create Backup: %v", err)
	}

	targetDataDir := filepath.Join(t.TempDir(), "target")
	if err := store.Initialize(ctx, targetDataDir); err != nil {
		t.Fatalf("initialize target installation: %v", err)
	}
	targetAttachmentsDir := filepath.Join(t.TempDir(), "target-attachments")
	if err := os.Mkdir(targetAttachmentsDir, 0o700); err != nil {
		t.Fatalf("create target Attachment Store: %v", err)
	}
	oldAttachment := filepath.Join(targetAttachmentsDir, "old")
	if err := os.WriteFile(oldAttachment, []byte("old attachment"), 0o600); err != nil {
		t.Fatalf("write old Attachment: %v", err)
	}

	plan, err := PrepareRestore(ctx, RestoreInput{
		InputPath:      archivePath,
		DataDir:        targetDataDir,
		AttachmentsDir: targetAttachmentsDir,
		Replace:        true,
	})
	if err != nil {
		t.Fatalf("prepare Restore: %v", err)
	}
	interrupted := errors.New("simulated interruption")
	if _, err = applyRestore(ctx, plan.JournalPath, func(phase restorePhase) error {
		if phase == restoreAttachmentsInstalled {
			return interrupted
		}
		return nil
	}); !errors.Is(err, interrupted) {
		t.Fatalf("interrupted Restore error = %v", err)
	}

	if err = RecoverRestore(targetDataDir); err != nil {
		t.Fatalf("recover interrupted Restore: %v", err)
	}
	if content, err = os.ReadFile(oldAttachment); err != nil ||
		string(content) != "old attachment" {
		t.Fatalf("recovered Attachment = %q, %v", content, err)
	}
	if _, err = os.Stat(filepath.Join(targetAttachmentsDir, storageKey)); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("mixed-generation Attachment remains: %v", err)
	}
	recovered, err := store.Open(ctx, targetDataDir)
	if err != nil {
		t.Fatalf("open recovered installation: %v", err)
	}
	if err = errors.Join(recovered.Ready(ctx), recovered.Close()); err != nil {
		t.Fatalf("recovered installation readiness: %v", err)
	}
	if _, err = os.Stat(archivePath); err != nil {
		t.Fatalf("Restore recovery removed input Backup: %v", err)
	}
}

func TestForcedUnsupportedRestoreReportsUnknownSchemaAndMakesNoSafetyClaim(
	t *testing.T,
) {
	ctx := t.Context()
	sourceDataDir := filepath.Join(t.TempDir(), "source")
	if err := store.Initialize(ctx, sourceDataDir); err != nil {
		t.Fatalf("initialize source installation: %v", err)
	}
	supportedArchive := filepath.Join(t.TempDir(), "supported.zip")
	if _, err := Create(ctx, CreateInput{
		DataDir: sourceDataDir, OutputPath: supportedArchive,
	}); err != nil {
		t.Fatalf("create supported Backup: %v", err)
	}
	unsupportedArchive := filepath.Join(t.TempDir(), "unsupported.zip")
	makeUnsupportedBackup(t, supportedArchive, unsupportedArchive)
	targetDataDir := filepath.Join(t.TempDir(), "target")

	if _, err := PrepareRestore(ctx, RestoreInput{
		InputPath: unsupportedArchive,
		DataDir:   targetDataDir,
	}); err == nil {
		t.Fatal("unsupported Restore unexpectedly prepared normally")
	}
	if _, err := PrepareRestore(ctx, RestoreInput{
		InputPath:        unsupportedArchive,
		DataDir:          targetDataDir,
		ForceUnsupported: true,
	}); err == nil {
		t.Fatal("forced unsupported Restore unexpectedly accepted without safeguards")
	}
	plan, err := PrepareRestore(ctx, RestoreInput{
		InputPath:                   unsupportedArchive,
		DataDir:                     targetDataDir,
		ForceUnsupported:            true,
		ForceReason:                 "recover after newer binary failure",
		AcknowledgeUnsupportedRisks: true,
	})
	if err != nil {
		t.Fatalf("prepare forced unsupported Restore: %v", err)
	}
	if !plan.ForcedUnsupported ||
		plan.ForceReason != "recover after newer binary failure" ||
		!slices.Contains(plan.UnknownSchemaElements, "table future_state") {
		t.Fatalf("forced unsupported Restore plan = %+v", plan)
	}
	if _, err = ApplyRestore(ctx, plan.JournalPath); err == nil {
		t.Fatal("forced unsupported Restore applied without repeated acknowledgment")
	}
	if _, err = ApplyRestoreWithOptions(
		ctx,
		plan.JournalPath,
		ApplyOptions{AcknowledgeUnsupportedRisks: true},
	); err != nil {
		t.Fatalf("apply forced unsupported Restore: %v", err)
	}
	installed, err := store.Open(ctx, targetDataDir)
	if err != nil {
		t.Fatalf("open forced unsupported Restore: %v", err)
	}
	if !errors.Is(installed.StartupError(), store.ErrUnsupportedSchema) {
		_ = installed.Close()
		t.Fatalf("forced Restore startup error = %v", installed.StartupError())
	}
	if err = installed.Close(); err != nil {
		t.Fatalf("close forced unsupported Restore: %v", err)
	}
	if _, err = os.Stat(unsupportedArchive); err != nil {
		t.Fatalf("forced Restore removed input Backup: %v", err)
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
			"INSERT INTO events (id, name, planned_start_date, planned_end_date, timezone, event_locale, event_day_boundary, created_at) VALUES (1, 'Event', '2026-07-24', '2026-07-24', 'UTC', 'en-US', '00:00', ?)",
			[]any{now},
		},
		{
			"INSERT INTO audit_entries (actor_kind, created_at, action, target_type, target_id, result, actor_account_id) VALUES ('Account', ?, 'BackupTest', 'Installation', '1', 'Succeeded', 1)",
			[]any{now},
		},
		{
			"INSERT INTO displays (id, name, created_at, enrolled_at) VALUES (1, 'Stage', ?, ?)",
			[]any{now, now},
		},
		{
			"INSERT INTO display_credentials (display_id, token_hash, created_at) VALUES (1, ?, ?)",
			[]any{fmt.Sprintf("%064x", 2), now},
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

func makeUnsupportedBackup(t *testing.T, sourcePath, destinationPath string) {
	t.Helper()
	source, err := zip.OpenReader(sourcePath)
	if err != nil {
		t.Fatalf("open supported Backup: %v", err)
	}
	defer func() {
		if closeErr := source.Close(); closeErr != nil {
			t.Errorf("close supported Backup: %v", closeErr)
		}
	}()
	databasePath := filepath.Join(t.TempDir(), "future.db")
	extractZIPEntry(t, source.File, databaseName, databasePath)
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open future database: %v", err)
	}
	current, err := store.CurrentSchemaVersion()
	if err != nil {
		t.Fatalf("read current schema version: %v", err)
	}
	if _, err = database.ExecContext(
		t.Context(),
		"CREATE TABLE future_state (id integer PRIMARY KEY); PRAGMA user_version = "+
			fmt.Sprint(current+1),
	); err != nil {
		_ = database.Close()
		t.Fatalf("create future schema element: %v", err)
	}
	if err = database.Close(); err != nil {
		t.Fatalf("close future database: %v", err)
	}
	databaseContent, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatalf("read future database: %v", err)
	}
	databaseHash := fmt.Sprintf("%x", sha256.Sum256(databaseContent))

	output, err := os.OpenFile(
		destinationPath,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		t.Fatalf("create unsupported Backup: %v", err)
	}
	writer := zip.NewWriter(output)
	for _, file := range source.File {
		var content []byte
		switch file.Name {
		case manifestName:
			input, openErr := file.Open()
			if openErr != nil {
				t.Fatalf("open manifest: %v", openErr)
			}
			var manifest Manifest
			if decodeErr := json.NewDecoder(input).Decode(&manifest); decodeErr != nil {
				_ = input.Close()
				t.Fatalf("decode manifest: %v", decodeErr)
			}
			_ = input.Close()
			manifest.SchemaVersion = current + 1
			manifest.MinimumReaderSchemaVersion = current + 1
			manifest.MinimumWriterSchemaVersion = current + 1
			manifest.DatabaseSHA256 = databaseHash
			content, err = json.Marshal(manifest)
		case databaseName:
			content = databaseContent
		default:
			input, openErr := file.Open()
			if openErr != nil {
				t.Fatalf("open %s: %v", file.Name, openErr)
			}
			content, err = io.ReadAll(input)
			_ = input.Close()
		}
		if err != nil {
			t.Fatalf("read %s: %v", file.Name, err)
		}
		entry, createErr := writer.CreateHeader(&zip.FileHeader{
			Name: file.Name, Method: file.Method,
		})
		if createErr != nil {
			t.Fatalf("create %s: %v", file.Name, createErr)
		}
		if _, err = entry.Write(content); err != nil {
			t.Fatalf("write %s: %v", file.Name, err)
		}
	}
	if err = errors.Join(writer.Close(), output.Close()); err != nil {
		t.Fatalf("close unsupported Backup: %v", err)
	}
}
