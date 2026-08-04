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
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	_ "github.com/dotwaffle/beamers/ent/runtime"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/diskspace"
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
		Configuration:  testConfiguration(t, dataDir, attachmentRoot),
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
		"web_authn_credentials",
		"federated_identities",
		"account_sessions",
		"bootstrap_credentials",
		"recovery_codes",
		"recovery_tokens",
		"display_credentials",
		"display_enrollments",
		"voting_keys",
	} {
		var count int
		if err = database.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s count = %d, want 0", table, count)
		}
	}
	var uploadLinkTables int
	if err = database.QueryRowContext(
		ctx,
		"SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'upload_links'",
	).Scan(&uploadLinkTables); err != nil {
		t.Fatalf("inspect sanitized Upload Link persistence: %v", err)
	}
	if uploadLinkTables != 0 {
		t.Fatalf("sanitized Upload Link table count = %d, want 0", uploadLinkTables)
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
		Configuration:  testConfiguration(t, restoredDataDir, restoredAttachmentsDir),
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

func TestPreflightDiskSpaceRefusesWhenInsufficient(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "installation")
	if err := store.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	outputPath := filepath.Join(t.TempDir(), "backup.zip")

	err := preflightDiskSpace(preflightDiskSpaceInput{
		DataDir:    dataDir,
		OutputPath: outputPath,
		RequireFree: func(string, uint64) error {
			return fmt.Errorf("stub: %w", diskspace.ErrInsufficientSpace)
		},
	})
	if !errors.Is(err, diskspace.ErrInsufficientSpace) {
		t.Fatalf("preflightDiskSpace error = %v, want ErrInsufficientSpace", err)
	}
}

func TestPreflightDiskSpaceAllowsSufficientCapacity(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "installation")
	if err := store.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	outputPath := filepath.Join(t.TempDir(), "backup.zip")

	var observedPath string
	var observedNeeded uint64
	err := preflightDiskSpace(preflightDiskSpaceInput{
		DataDir:    dataDir,
		OutputPath: outputPath,
		RequireFree: func(path string, needed uint64) error {
			observedPath, observedNeeded = path, needed
			return nil
		},
	})
	if err != nil {
		t.Fatalf("preflightDiskSpace: %v", err)
	}
	if observedPath != filepath.Dir(outputPath) {
		t.Fatalf("preflight checked %q, want %q", observedPath, filepath.Dir(outputPath))
	}
	if observedNeeded < backupDiskSpaceMarginBytes {
		t.Fatalf("preflight needed %d bytes, want at least the %d byte margin",
			observedNeeded, backupDiskSpaceMarginBytes)
	}
}

func TestCreateRecordsCompletionMarkerForDiagnosticsAge(t *testing.T) {
	ctx := t.Context()
	dataDir := filepath.Join(t.TempDir(), "installation")
	if err := store.Initialize(ctx, dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	if _, _, err := LastCompletedAt(dataDir); err != nil {
		t.Fatalf("LastCompletedAt before any Backup: %v", err)
	}
	if _, found, err := LastCompletedAt(dataDir); err != nil || found {
		t.Fatalf("LastCompletedAt before any Backup = (found %v, err %v), want (false, nil)", found, err)
	}

	completedAt := time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC)
	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	if _, err := Create(ctx, CreateInput{
		DataDir:       dataDir,
		OutputPath:    archivePath,
		Mode:          Sanitized,
		Now:           completedAt,
		Configuration: testConfiguration(t, dataDir, ""),
	}); err != nil {
		t.Fatalf("create Backup: %v", err)
	}

	found, ok, err := LastCompletedAt(dataDir)
	if err != nil {
		t.Fatalf("LastCompletedAt after Backup: %v", err)
	}
	if !ok {
		t.Fatal("LastCompletedAt after Backup unexpectedly reported no completion")
	}
	if !found.Equal(completedAt) {
		t.Fatalf("Backup completion time = %s, want %s", found, completedAt)
	}
}

func TestInstallArchiveSyncsPublishedFilenameBeforeSuccess(t *testing.T) {
	directory := t.TempDir()
	staged := filepath.Join(directory, "staged")
	output := filepath.Join(directory, "backup.zip")
	if err := os.WriteFile(staged, []byte("backup"), 0o600); err != nil {
		t.Fatalf("write staged archive: %v", err)
	}
	syncFailure := errors.New("sync failed")
	err := installArchive(staged, output, func(path string) error {
		if path != directory {
			t.Fatalf("sync path = %q, want %q", path, directory)
		}
		if _, statErr := os.Stat(output); statErr != nil {
			t.Fatalf("published archive is unavailable before sync: %v", statErr)
		}
		return syncFailure
	})
	if !errors.Is(err, syncFailure) {
		t.Fatalf("install archive error = %v, want %v", err, syncFailure)
	}
}

func TestBackupConfigurationIsVerifiedAndRestoreRequiresAcknowledgment(t *testing.T) {
	ctx := t.Context()
	dataDir := filepath.Join(t.TempDir(), "installation")
	if err := store.Initialize(ctx, dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	keyPath := filepath.Join(t.TempDir(), "tls.key")
	const privateMaterial = "private key material must not enter the Backup"
	if err := os.WriteFile(keyPath, []byte(privateMaterial), 0o600); err != nil {
		t.Fatalf("write TLS private key: %v", err)
	}
	configuration := Configuration{
		DataDir:         dataDir,
		AttachmentsDir:  filepath.Join(dataDir, "attachments"),
		ListenAddress:   "0.0.0.0:8443",
		PublicListen:    "127.0.0.1:8081",
		TLSCertificate:  "/etc/beamers/tls.crt",
		TLSPrivateKey:   keyPath,
		TrustedProxies:  []string{"192.0.2.0/24"},
		ReplicaURL:      "file:///var/lib/beamers-replica",
		ShutdownTimeout: 10 * time.Second,
		InsecureCrew:    false,
		InsecureDisplay: false,
	}
	if err := RecordConfiguration(configuration); err != nil {
		t.Fatalf("record effective configuration: %v", err)
	}

	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	manifest, err := Create(ctx, CreateInput{
		DataDir: dataDir, OutputPath: archivePath, Mode: Sanitized,
		Configuration: configuration,
	})
	if err != nil {
		t.Fatalf("create Backup: %v", err)
	}
	if manifest.Configuration.Version != 1 ||
		manifest.ConfigurationSHA256 == "" ||
		manifest.Configuration.ListenAddress != configuration.ListenAddress {
		t.Fatalf("configuration evidence = %+v", manifest)
	}
	archiveBytes, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read Backup: %v", err)
	}
	if bytes.Contains(archiveBytes, []byte(privateMaterial)) {
		t.Fatal("Sanitized Backup contains TLS private material")
	}

	tampered := manifest
	tampered.Configuration.ListenAddress = "127.0.0.1:1"
	replacement, err := json.Marshal(tampered)
	if err != nil {
		t.Fatalf("encode tampered manifest: %v", err)
	}
	tamperedPath := filepath.Join(t.TempDir(), "tampered.zip")
	rewriteZIPEntry(t, archivePath, tamperedPath, manifestName, replacement)
	if _, err = Verify(ctx, tamperedPath); err == nil {
		t.Fatal("tampered configuration unexpectedly verified")
	}

	targetDataDir := filepath.Join(t.TempDir(), "restored")
	target := configuration
	target.DataDir = targetDataDir
	target.AttachmentsDir = filepath.Join(targetDataDir, "attachments")
	target.ListenAddress = "127.0.0.1:8080"
	target.TLSCertificate = "/srv/beamers/tls.crt"
	target.TLSPrivateKey = "/srv/beamers/tls.key"
	plan, err := PrepareRestore(ctx, RestoreInput{
		InputPath:     archivePath,
		DataDir:       targetDataDir,
		Configuration: target,
	})
	if err != nil {
		t.Fatalf("preview Restore: %v", err)
	}
	if !plan.RequiresConfigurationAcknowledgment ||
		!hasConfigurationDifference(plan, "data_dir", "mapped") ||
		!hasConfigurationDifference(plan, "tls_private_key", "acknowledgment_required") ||
		!hasConfigurationDifference(plan, "listen_address", "acknowledgment_required") {
		t.Fatalf("configuration differences = %+v", plan.ConfigurationDifferences)
	}
	if _, err = ApplyRestore(ctx, plan.JournalPath); err == nil {
		t.Fatal("unacknowledged configuration differences unexpectedly applied")
	}
	if _, err = ApplyRestoreWithOptions(ctx, plan.JournalPath, ApplyOptions{
		AcknowledgeConfigurationDifferences: true,
	}); err != nil {
		t.Fatalf("apply acknowledged Restore: %v", err)
	}
}

func hasConfigurationDifference(
	plan RestorePlan,
	field string,
	resolution ConfigurationResolution,
) bool {
	for _, difference := range plan.ConfigurationDifferences {
		if difference.Field == field && difference.Resolution == resolution {
			return true
		}
	}
	return false
}

func testConfiguration(t *testing.T, dataDir, attachmentsDir string) Configuration {
	t.Helper()
	configuration, err := LoadConfiguration(dataDir, attachmentsDir)
	if err != nil {
		t.Fatalf("load test service configuration: %v", err)
	}
	return configuration
}

func TestSanitizedBackupRetainsSecureCreateAccountReceipt(t *testing.T) {
	testBackupRetainsSecureCreateAccountReceipt(t, Sanitized)
}

func TestFullFidelityBackupRetainsSecureCreateAccountReceipt(t *testing.T) {
	testBackupRetainsSecureCreateAccountReceipt(t, FullFidelity)
}

func testBackupRetainsSecureCreateAccountReceipt(t *testing.T, mode Mode) {
	t.Helper()
	ctx := t.Context()
	dataDir := filepath.Join(t.TempDir(), "installation")
	if err := store.Initialize(ctx, dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	storage, err := store.Open(ctx, dataDir)
	if err != nil {
		t.Fatalf("open installation: %v", err)
	}
	storageOpen := true
	t.Cleanup(func() {
		if storageOpen {
			if closeErr := storage.Close(); closeErr != nil {
				t.Errorf("close installation: %v", closeErr)
			}
		}
	})
	authentication, err := auth.New(storage, auth.DefaultConfig())
	if err != nil {
		t.Fatalf("create authentication service: %v", err)
	}
	bootstrap, err := authentication.IssueBootstrap(ctx)
	if err != nil {
		t.Fatalf("issue bootstrap: %v", err)
	}
	session, err := authentication.BootstrapAdministrator(
		ctx,
		bootstrap,
		"Ada Admin",
		"administrator password",
	)
	if err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	created, err := authentication.CreateAccount(
		ctx,
		session.Account,
		"Pat Producer",
		"correct horse battery staple",
		"create-pat",
	)
	if err != nil {
		t.Fatalf("create Account: %v", err)
	}
	err = storage.Close()
	storageOpen = false
	if err != nil {
		t.Fatalf("close installation: %v", err)
	}

	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	if _, err = Create(ctx, CreateInput{
		DataDir: dataDir, OutputPath: archivePath, Mode: mode,
		Configuration: testConfiguration(t, dataDir, ""),
	}); err != nil {
		t.Fatalf("create %s Backup: %v", mode, err)
	}
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatalf("open %s Backup: %v", mode, err)
	}
	archiveOpen := true
	t.Cleanup(func() {
		if archiveOpen {
			if closeErr := archive.Close(); closeErr != nil {
				t.Errorf("close %s Backup: %v", mode, closeErr)
			}
		}
	})
	databasePath := filepath.Join(t.TempDir(), "backup.db")
	extractZIPEntry(t, archive.File, databaseName, databasePath)
	err = archive.Close()
	archiveOpen = false
	if err != nil {
		t.Fatalf("close %s Backup: %v", mode, err)
	}
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open Backup database: %v", err)
	}
	databaseOpen := true
	t.Cleanup(func() {
		if databaseOpen {
			if closeErr := database.Close(); closeErr != nil {
				t.Errorf("close Backup database: %v", closeErr)
			}
		}
	})
	var payloadHash, outcome string
	if err = database.QueryRowContext(
		ctx,
		"SELECT payload_hash, outcome_json FROM command_receipts WHERE command_id = 'create-pat'",
	).Scan(&payloadHash, &outcome); err != nil {
		_ = database.Close()
		t.Fatalf("read CreateAccount receipt: %v", err)
	}
	var credentialCount int
	if err = database.QueryRowContext(
		ctx,
		"SELECT count(*) FROM password_credentials",
	).Scan(&credentialCount); err != nil {
		_ = database.Close()
		t.Fatalf("count sanitized credentials: %v", err)
	}
	err = database.Close()
	databaseOpen = false
	if err != nil {
		t.Fatalf("close Backup database: %v", err)
	}
	wantCredentials := 0
	if mode == FullFidelity {
		wantCredentials = 2
	}
	if payloadHash != command.PayloadHash("pat producer") ||
		strings.Contains(outcome, "correct horse battery staple") ||
		strings.Contains(outcome, "argon2") ||
		credentialCount != wantCredentials {
		t.Fatalf("receipt hash/outcome/credentials = %q/%q/%d", payloadHash, outcome, credentialCount)
	}

	restoredDataDir := filepath.Join(t.TempDir(), "restored")
	if _, err = Restore(ctx, RestoreInput{
		InputPath: archivePath, DataDir: restoredDataDir,
		Configuration: testConfiguration(t, restoredDataDir, ""),
	}); err != nil {
		t.Fatalf("Restore %s Backup: %v", mode, err)
	}
	restoredStorage, err := store.Open(ctx, restoredDataDir)
	if err != nil {
		t.Fatalf("open restored installation: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := restoredStorage.Close(); closeErr != nil {
			t.Errorf("close restored installation: %v", closeErr)
		}
	})
	restoredAuth, err := auth.New(restoredStorage, auth.DefaultConfig())
	if err != nil {
		t.Fatalf("create restored authentication service: %v", err)
	}
	retried, err := restoredAuth.CreateAccount(
		ctx,
		session.Account,
		"Pat Producer",
		"correct horse battery staple",
		"create-pat",
	)
	if err != nil || !reflect.DeepEqual(retried, created) {
		t.Fatalf("retry restored Account = %+v, %v; want %+v", retried, err, created)
	}
	audits, err := restoredAuth.ListAuditEntries(ctx, session.Account)
	if err != nil {
		t.Fatalf("list restored Audit Entries: %v", err)
	}
	if len(audits) != 1 {
		t.Fatalf("restored Audit Entry count = %d, want 1", len(audits))
	}
	if _, err = restoredAuth.CreateAccount(
		ctx,
		session.Account,
		"Different Account",
		"correct horse battery staple",
		"create-pat",
	); !errors.Is(err, auth.ErrCommandConflict) {
		t.Fatalf("reused consumed Command ID error = %v", err)
	}
	accounts, err := restoredAuth.ListAccounts(ctx, session.Account)
	if err != nil {
		t.Fatalf("list restored Accounts: %v", err)
	}
	if len(accounts) != 2 {
		t.Fatalf("restored Account count = %d, want 2", len(accounts))
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
		Configuration: testConfiguration(t, dataDir, ""),
	}); err != nil {
		t.Fatalf("create Backup: %v", err)
	}

	tamperedPath := filepath.Join(t.TempDir(), "tampered.zip")
	rewriteZIPEntry(t, archivePath, tamperedPath, attachmentArchivePath(t, storageKey), []byte("tampered"))
	if _, err := Verify(t.Context(), tamperedPath); err == nil {
		t.Fatal("tampered Attachment unexpectedly verified")
	}
}

func TestVerifyStopsWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := Verify(ctx, filepath.Join(t.TempDir(), "backup.zip")); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("Verify error = %v, want context cancellation", err)
	}
}

func TestVerifyRejectsOversizedCompressedArchive(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "oversized.zip")
	if err := os.WriteFile(archivePath, nil, 0o600); err != nil {
		t.Fatalf("create sparse Backup: %v", err)
	}
	if err := os.Truncate(archivePath, (65<<30)+1); err != nil {
		t.Fatalf("size sparse Backup: %v", err)
	}

	if _, err := Verify(t.Context(), archivePath); err == nil ||
		err.Error() != "backup archive exceeds compressed size limit" {
		t.Fatalf("Verify error = %v, want compressed size limit", err)
	}
	assertRejectedRestoreCleansStaging(t, t.Context(), archivePath)
}

func TestVerifyRejectsExcessiveEntries(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "excessive-entries.zip")
	output, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create Backup: %v", err)
	}
	writer := zip.NewWriter(output)
	for index := range 32_771 {
		if _, err = writer.Create(fmt.Sprintf("entry-%d", index)); err != nil {
			t.Fatalf("create Backup entry %d: %v", index, err)
		}
	}
	if err = errors.Join(writer.Close(), output.Close()); err != nil {
		t.Fatalf("close Backup: %v", err)
	}

	if _, err = Verify(t.Context(), archivePath); err == nil ||
		err.Error() != "backup archive exceeds entry count limit" {
		t.Fatalf("Verify error = %v, want entry count limit", err)
	}
	assertRejectedRestoreCleansStaging(t, t.Context(), archivePath)
}

func TestVerifyRejectsHighRatioManifest(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "high-ratio.zip")
	output, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create Backup: %v", err)
	}
	writer := zip.NewWriter(output)
	entry, err := writer.CreateHeader(&zip.FileHeader{Name: manifestName, Method: zip.Deflate})
	if err != nil {
		t.Fatalf("create manifest: %v", err)
	}
	if _, err = io.CopyN(entry, zeroReader{}, (64<<20)+1); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err = errors.Join(writer.Close(), output.Close()); err != nil {
		t.Fatalf("close Backup: %v", err)
	}

	if _, err = Verify(t.Context(), archivePath); err == nil ||
		err.Error() != "backup manifest exceeds size limit" {
		t.Fatalf("Verify error = %v, want manifest size limit", err)
	}
	assertRejectedRestoreCleansStaging(t, t.Context(), archivePath)
}

func TestPrepareRestoreCleansTruncatedAndCanceledStaging(t *testing.T) {
	truncated := filepath.Join(t.TempDir(), "truncated.zip")
	if err := os.WriteFile(truncated, []byte("PK\x03\x04"), 0o600); err != nil {
		t.Fatalf("write truncated Backup: %v", err)
	}
	assertRejectedRestoreCleansStaging(t, t.Context(), truncated)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assertRejectedRestoreCleansStaging(t, ctx, truncated)
}

func TestArchiveResourceLimits(t *testing.T) {
	file := func(name string, compressed, expanded uint64) *zip.File {
		return &zip.File{FileHeader: zip.FileHeader{
			Name:               name,
			CompressedSize64:   compressed,
			UncompressedSize64: expanded,
		}}
	}
	atBoundary := []*zip.File{
		file(manifestName, 1, 64<<20),
		file(databaseName, 1, 64<<30),
	}
	for index := range 1_023 {
		atBoundary = append(
			atBoundary,
			file(fmt.Sprintf("attachments/%d", index), 1, 64<<20),
		)
	}
	if err := validateArchiveResources(atBoundary); err != nil {
		t.Fatalf("boundary resources rejected: %v", err)
	}

	tests := []struct {
		name  string
		files []*zip.File
	}{
		{"manifest", []*zip.File{file(manifestName, 1, (64<<20)+1)}},
		{"database", []*zip.File{file(databaseName, 1, (64<<30)+1)}},
		{"Attachment", []*zip.File{file("attachments/a", 1, (64<<20)+1)}},
		{"compressed", []*zip.File{file(databaseName, (65<<30)+1, 1)}},
		{"aggregate expansion", append(
			atBoundary,
			file("attachments/too-much", 1, 1),
		)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateArchiveResources(test.files); err == nil {
				t.Fatal("resources unexpectedly accepted")
			}
		})
	}
}

func TestRestoreAcceptsAttachmentAtSizeBoundary(t *testing.T) {
	ctx := t.Context()
	dataDir := filepath.Join(t.TempDir(), "installation")
	if err := store.Initialize(ctx, dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	source := filepath.Join(t.TempDir(), "attachment")
	if err := os.WriteFile(source, nil, 0o600); err != nil {
		t.Fatalf("create Attachment: %v", err)
	}
	if err := os.Truncate(source, 64<<20); err != nil {
		t.Fatalf("size Attachment: %v", err)
	}
	digest, err := fileSHA256(ctx, source)
	if err != nil {
		t.Fatalf("hash Attachment: %v", err)
	}
	storageKey := filepath.Join("sha256", digest[:2], digest)
	attachmentPath := filepath.Join(dataDir, "attachments", storageKey)
	if err = os.MkdirAll(filepath.Dir(attachmentPath), 0o700); err != nil {
		t.Fatalf("prepare Attachment Store: %v", err)
	}
	if err = os.Rename(source, attachmentPath); err != nil {
		t.Fatalf("install Attachment: %v", err)
	}
	seedBackupState(t, ctx, dataDir, storageKey, digest, 64<<20)
	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	if _, err = Create(ctx, CreateInput{
		DataDir: dataDir, OutputPath: archivePath, Mode: Sanitized,
		Configuration: testConfiguration(t, dataDir, ""),
	}); err != nil {
		t.Fatalf("create boundary Backup: %v", err)
	}
	targetDataDir := filepath.Join(t.TempDir(), "restored")
	if _, err = Restore(ctx, RestoreInput{
		InputPath: archivePath, DataDir: targetDataDir,
		Configuration: testConfiguration(t, targetDataDir, ""),
	}); err != nil {
		t.Fatalf("Restore boundary Backup: %v", err)
	}
	info, err := os.Stat(filepath.Join(targetDataDir, "attachments", storageKey))
	if err != nil || info.Size() != 64<<20 {
		t.Fatalf("restored Attachment size = %v, %v", info, err)
	}
}

func TestReaderIntegrityStopsWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	input := readerFunc(func(buffer []byte) (int, error) {
		cancel()
		return copy(buffer, "content"), nil
	})

	if _, _, err := readerIntegrity(ctx, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("readerIntegrity error = %v, want context cancellation", err)
	}
}

func TestExtractFileStopsWhenCanceled(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "entry.zip")
	output, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	writer := zip.NewWriter(output)
	entry, err := writer.Create(databaseName)
	if err != nil {
		t.Fatalf("create archive entry: %v", err)
	}
	if _, err = entry.Write([]byte("content")); err != nil {
		t.Fatalf("write archive entry: %v", err)
	}
	if err = errors.Join(writer.Close(), output.Close()); err != nil {
		t.Fatalf("close archive: %v", err)
	}
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := archive.Close(); closeErr != nil {
			t.Errorf("close archive: %v", closeErr)
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	destination := filepath.Join(t.TempDir(), "restored.db")

	if _, err = extractFile(
		ctx,
		archive.File[0],
		destination,
		maxDatabaseBytes,
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("extractFile error = %v, want context cancellation", err)
	}
	if _, err = os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Restore entry residue: %v", err)
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
		Configuration: testConfiguration(t, dataDir, ""),
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
	if _, err := Verify(t.Context(), tamperedPath); err == nil {
		t.Fatal("tampered manifest unexpectedly verified")
	}
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	clear(buffer)
	return len(buffer), nil
}

func assertRejectedRestoreCleansStaging(
	t *testing.T,
	ctx context.Context,
	archivePath string,
) {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, "restored")
	if _, err := PrepareRestore(ctx, RestoreInput{
		InputPath: archivePath,
		DataDir:   dataDir,
	}); err == nil {
		t.Fatal("Restore preparation unexpectedly succeeded")
	}
	staging, err := filepath.Glob(
		filepath.Join(root, ".restored.beamers-restore-*"),
	)
	if err != nil {
		t.Fatalf("inspect Restore staging: %v", err)
	}
	if len(staging) != 0 {
		t.Fatalf("Restore staging residue = %v", staging)
	}
	if _, err = os.Stat(dataDir + ".beamers-restore.json"); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("Restore journal residue: %v", err)
	}
}

func TestVerifyRejectsTrailingManifestContent(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "installation")
	if err := store.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	if _, err := Create(t.Context(), CreateInput{
		DataDir: dataDir, OutputPath: archivePath, Mode: Sanitized,
		Configuration: testConfiguration(t, dataDir, ""),
	}); err != nil {
		t.Fatalf("create Backup: %v", err)
	}
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatalf("open Backup: %v", err)
	}
	var manifest []byte
	for _, file := range archive.File {
		if file.Name != manifestName {
			continue
		}
		input, openErr := file.Open()
		if openErr != nil {
			t.Fatalf("open manifest: %v", openErr)
		}
		manifest, err = io.ReadAll(input)
		_ = input.Close()
		if err != nil {
			t.Fatalf("read manifest: %v", err)
		}
	}
	if err = archive.Close(); err != nil {
		t.Fatalf("close Backup: %v", err)
	}
	tamperedPath := filepath.Join(t.TempDir(), "trailing-manifest.zip")
	rewriteZIPEntry(
		t,
		archivePath,
		tamperedPath,
		manifestName,
		append(manifest, []byte("{}")...),
	)

	if _, err = Verify(t.Context(), tamperedPath); err == nil {
		t.Fatal("manifest with trailing JSON unexpectedly verified")
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
		Configuration: testConfiguration(t, dataDir, ""),
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
			if _, err = decodeZIPJSON(t.Context(), file, &manifest); err != nil {
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
	targetDataDir := filepath.Join(t.TempDir(), "restored")
	if _, err = PrepareRestore(ctx, RestoreInput{
		InputPath:     tamperedPath,
		DataDir:       targetDataDir,
		Configuration: testConfiguration(t, targetDataDir, ""),
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
		Configuration: testConfiguration(t, sourceDataDir, ""),
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
		InputPath:     archivePath,
		DataDir:       targetDataDir,
		Replace:       true,
		Configuration: testConfiguration(t, targetDataDir, ""),
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

	installedGenerationLocked := false
	if _, err = applyRestore(ctx, plan.JournalPath, func(phase restorePhase) error {
		if phase != restoreDataInstalled {
			return nil
		}
		release, openErr := store.HoldExclusiveAccess(targetDataDir)
		if openErr == nil {
			return release()
		}
		installedGenerationLocked = errors.Is(openErr, store.ErrInstallationInUse)
		return nil
	}); err != nil {
		t.Fatalf("apply Restore: %v", err)
	}
	if !installedGenerationLocked {
		t.Fatal("installed generation was not held exclusively through Restore commit")
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
		Configuration: testConfiguration(t, sourceDataDir, ""),
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
		Configuration: testConfiguration(t, targetDataDir, ""),
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

func TestCancelPreparedRestoreRemovesReservedStateAndAllowsAnotherPreview(t *testing.T) {
	ctx := t.Context()
	sourceDataDir := filepath.Join(t.TempDir(), "source")
	if err := store.Initialize(ctx, sourceDataDir); err != nil {
		t.Fatalf("initialize source installation: %v", err)
	}
	sourceAttachmentsDir := filepath.Join(t.TempDir(), "source-attachments")
	if err := os.Mkdir(sourceAttachmentsDir, 0o700); err != nil {
		t.Fatalf("create source Attachment Store: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	if _, err := Create(ctx, CreateInput{
		DataDir:        sourceDataDir,
		AttachmentsDir: sourceAttachmentsDir,
		OutputPath:     archivePath,
		Mode:           Sanitized,
		Configuration:  testConfiguration(t, sourceDataDir, sourceAttachmentsDir),
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
	input := RestoreInput{
		InputPath:      archivePath,
		DataDir:        targetDataDir,
		AttachmentsDir: targetAttachmentsDir,
		Replace:        true,
		Configuration:  testConfiguration(t, targetDataDir, targetAttachmentsDir),
	}
	plan, err := PrepareRestore(ctx, input)
	if err != nil {
		t.Fatalf("prepare Restore: %v", err)
	}
	inspected, present, err := InspectPreparedRestore(ctx, plan.JournalPath)
	if err != nil || !present || !reflect.DeepEqual(inspected, plan) {
		t.Fatalf("inspect prepared Restore = %+v, %t, %v", inspected, present, err)
	}
	journal, err := readRestoreJournal(plan.JournalPath)
	if err != nil {
		t.Fatalf("read Restore journal: %v", err)
	}
	releaseAccess, err := store.HoldExclusiveAccess(targetDataDir, targetAttachmentsDir)
	if err != nil {
		t.Fatalf("hold installation access: %v", err)
	}
	if err = CancelPreparedRestore(ctx, plan.JournalPath); err == nil {
		t.Fatal("prepared Restore canceled while installation was in use")
	}
	if err = releaseAccess(); err != nil {
		t.Fatalf("release installation access before cancellation: %v", err)
	}
	if err = CancelPreparedRestore(ctx, plan.JournalPath); err != nil {
		t.Fatalf("cancel prepared Restore: %v", err)
	}
	if _, present, err = InspectPreparedRestore(ctx, plan.JournalPath); err != nil || present {
		t.Fatalf("inspect canceled Restore = %t, %v", present, err)
	}
	for _, path := range []string{
		plan.JournalPath,
		journal.StagingRoot,
		journal.StagedAttachments,
		journal.DataQuarantineRoot,
		journal.AttachmentsQuarantineRoot,
	} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("canceled Restore path %q remains: %v", path, statErr)
		}
	}
	current, err := store.Open(ctx, targetDataDir)
	if err != nil {
		t.Fatalf("current installation changed by cancellation: %v", err)
	}
	if err = current.Close(); err != nil {
		t.Fatalf("close current installation: %v", err)
	}
	second, err := PrepareRestore(ctx, input)
	if err != nil {
		t.Fatalf("prepare Restore after cancellation: %v", err)
	}
	if err = CancelPreparedRestore(ctx, second.JournalPath); err != nil {
		t.Fatalf("cancel second prepared Restore: %v", err)
	}
}

func TestCancelPreparedRestoreRefusesDamageAndStartedCutover(t *testing.T) {
	ctx := t.Context()
	sourceDataDir := filepath.Join(t.TempDir(), "source")
	if err := store.Initialize(ctx, sourceDataDir); err != nil {
		t.Fatalf("initialize source installation: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	if _, err := Create(ctx, CreateInput{
		DataDir: sourceDataDir, OutputPath: archivePath, Mode: Sanitized,
		Configuration: testConfiguration(t, sourceDataDir, ""),
	}); err != nil {
		t.Fatalf("create Backup: %v", err)
	}
	prepare := func(targetDataDir string) (RestorePlan, restoreJournal) {
		t.Helper()
		if err := store.Initialize(ctx, targetDataDir); err != nil {
			t.Fatalf("initialize target installation: %v", err)
		}
		plan, err := PrepareRestore(ctx, RestoreInput{
			InputPath: archivePath, DataDir: targetDataDir, Replace: true,
			Configuration: testConfiguration(t, targetDataDir, ""),
		})
		if err != nil {
			t.Fatalf("prepare Restore: %v", err)
		}
		journal, err := readRestoreJournal(plan.JournalPath)
		if err != nil {
			t.Fatalf("read Restore journal: %v", err)
		}
		return plan, journal
	}

	malformedPlan, malformed := prepare(filepath.Join(t.TempDir(), "malformed"))
	encodedJournal, err := json.Marshal(malformed)
	if err != nil {
		t.Fatalf("encode Restore journal: %v", err)
	}
	if err = os.WriteFile(
		malformedPlan.JournalPath,
		append(encodedJournal, []byte("\n{")...),
		0o600,
	); err != nil {
		t.Fatalf("append Restore journal corruption: %v", err)
	}
	if err := CancelPreparedRestore(ctx, malformedPlan.JournalPath); err == nil {
		t.Fatal("damaged Restore journal unexpectedly canceled")
	}
	if _, err := os.Stat(malformed.StagingRoot); err != nil {
		t.Fatalf("damaged Restore staging removed: %v", err)
	}

	occupiedPlan, occupied := prepare(filepath.Join(t.TempDir(), "occupied"))
	if err := os.WriteFile(
		filepath.Join(occupied.DataQuarantineRoot, "unexpected"),
		[]byte("preserve"),
		0o600,
	); err != nil {
		t.Fatalf("occupy reserved Restore quarantine: %v", err)
	}
	if err := CancelPreparedRestore(ctx, occupiedPlan.JournalPath); err == nil {
		t.Fatal("occupied Restore quarantine unexpectedly canceled")
	}
	if _, err := os.Stat(occupiedPlan.JournalPath); err != nil {
		t.Fatalf("occupied Restore journal removed: %v", err)
	}

	damagedPlan, damaged := prepare(filepath.Join(t.TempDir(), "damaged"))
	if err := os.WriteFile(
		filepath.Join(damaged.StagedData, "beamers.db"),
		[]byte("damaged"),
		0o600,
	); err != nil {
		t.Fatalf("damage staged Restore: %v", err)
	}
	if err := CancelPreparedRestore(ctx, damagedPlan.JournalPath); err == nil {
		t.Fatal("damaged prepared Restore unexpectedly canceled")
	}
	if _, err := os.Stat(damagedPlan.JournalPath); err != nil {
		t.Fatalf("damaged Restore journal removed: %v", err)
	}

	startedPlan, started := prepare(filepath.Join(t.TempDir(), "started"))
	if err := renameAndSync(started.Plan.DataDir, started.Plan.DataQuarantine); err != nil {
		t.Fatalf("start Restore cutover: %v", err)
	}
	if err := CancelPreparedRestore(ctx, startedPlan.JournalPath); err == nil {
		t.Fatal("started Restore cutover unexpectedly canceled")
	}
	if err := RecoverRestore(started.Plan.DataDir); err != nil {
		t.Fatalf("recover refused Restore cutover: %v", err)
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
		Configuration:  testConfiguration(t, sourceDataDir, sourceAttachmentsDir),
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
		Configuration:  testConfiguration(t, targetDataDir, targetAttachmentsDir),
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

	releaseAccess, err := store.HoldExclusiveAccess(targetAttachmentsDir)
	if err != nil {
		t.Fatalf("hold interrupted Attachment Store: %v", err)
	}
	if err = RecoverRestore(targetDataDir); !errors.Is(err, store.ErrInstallationInUse) {
		t.Fatalf("contended Restore recovery error = %v", err)
	}
	if err = releaseAccess(); err != nil {
		t.Fatalf("release interrupted Attachment Store: %v", err)
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
		Configuration: testConfiguration(t, sourceDataDir, ""),
	}); err != nil {
		t.Fatalf("create supported Backup: %v", err)
	}
	unsupportedArchive := filepath.Join(t.TempDir(), "unsupported.zip")
	makeUnsupportedBackup(t, supportedArchive, unsupportedArchive)
	targetDataDir := filepath.Join(t.TempDir(), "target")

	if _, err := PrepareRestore(ctx, RestoreInput{
		InputPath:     unsupportedArchive,
		DataDir:       targetDataDir,
		Configuration: testConfiguration(t, targetDataDir, ""),
	}); err == nil {
		t.Fatal("unsupported Restore unexpectedly prepared normally")
	}
	if _, err := PrepareRestore(ctx, RestoreInput{
		InputPath:        unsupportedArchive,
		DataDir:          targetDataDir,
		Configuration:    testConfiguration(t, targetDataDir, ""),
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
		Configuration:               testConfiguration(t, targetDataDir, ""),
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
			"INSERT INTO web_authn_credentials (account_id, credential_id, name, credential, created_at) VALUES (1, ?, 'Security key', ?, ?)",
			[]any{[]byte("credential-id"), []byte(`{"public_key":"secret"}`), now},
		},
		{
			"INSERT INTO federated_identities (account_id, provider, subject, created_at) VALUES (1, 'sceneid', 'secret-subject', ?)",
			[]any{now},
		},
		{
			"INSERT INTO account_sessions (account_id, token_hash, created_at, expires_at) VALUES (1, ?, ?, ?)",
			[]any{digest, now, now.Add(time.Hour)},
		},
		{
			"INSERT INTO recovery_codes (account_id, code_hash, created_at) VALUES (1, ?, ?)",
			[]any{fmt.Sprintf("%064x", 3), now},
		},
		{
			"INSERT INTO recovery_tokens (account_id, token_hash, created_at, expires_at) VALUES (1, ?, ?, ?)",
			[]any{fmt.Sprintf("%064x", 4), now, now.Add(time.Hour)},
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
