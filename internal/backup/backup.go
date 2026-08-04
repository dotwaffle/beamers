// Package backup creates and verifies versioned installation archives.
package backup

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	pathpkg "path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dotwaffle/beamers/internal/diskspace"
	"github.com/dotwaffle/beamers/internal/store"
)

const (
	formatVersion         = 2
	manifestName          = "manifest.json"
	databaseName          = "database/beamers.db"
	maxArchiveEntries     = 32_770
	maxManifestBytes      = 64 << 20
	maxDatabaseBytes      = 64 << 30
	maxAttachmentBytes    = 64 << 20
	maxArchiveOutputBytes = 128 << 30
)

// MaxArchiveBytes is the largest supported compressed Backup input.
const MaxArchiveBytes int64 = 65 << 30

// Mode identifies whether an archive contains authentication secrets.
type Mode string

const (
	// Sanitized excludes authentication secrets and is the default.
	Sanitized Mode = "Sanitized"
	// FullFidelity preserves authentication secrets.
	FullFidelity Mode = "FullFidelity"
)

// Manifest is the independently verifiable archive contract.
type Manifest struct {
	FormatVersion              int           `json:"format_version"`
	Mode                       Mode          `json:"mode"`
	SchemaVersion              int           `json:"schema_version"`
	MinimumReaderSchemaVersion int           `json:"minimum_reader_schema_version"`
	MinimumWriterSchemaVersion int           `json:"minimum_writer_schema_version"`
	CreatedAt                  time.Time     `json:"created_at"`
	DatabaseSHA256             string        `json:"database_sha256"`
	Configuration              Configuration `json:"configuration"`
	ConfigurationSHA256        string        `json:"configuration_sha256"`
	Attachments                []Attachment  `json:"attachments"`
}

// Attachment identifies one immutable Attachment Store object.
type Attachment struct {
	StorageKey string `json:"storage_key"`
	SHA256     string `json:"sha256"`
	SizeBytes  int64  `json:"size_bytes"`
}

// CreateInput selects one installation and non-overwriting archive output.
type CreateInput struct {
	DataDir        string
	AttachmentsDir string
	OutputPath     string
	Mode           Mode
	Now            time.Time
	Configuration  Configuration
}

// RestoreInput selects one verified Backup and local destination.
type RestoreInput struct {
	InputPath                           string
	DataDir                             string
	AttachmentsDir                      string
	Configuration                       Configuration
	Replace                             bool
	ForceUnsupported                    bool
	ForceReason                         string
	AcknowledgeUnsupportedRisks         bool
	AcknowledgeConfigurationDifferences bool
}

// Create writes and verifies one installation Backup.
func Create(ctx context.Context, input CreateInput) (manifest Manifest, returnErr error) {
	if input.DataDir == "" {
		return Manifest{}, errors.New("backup data directory is required")
	}
	if input.AttachmentsDir == "" {
		input.AttachmentsDir = filepath.Join(input.DataDir, "attachments")
	}
	accessRoots := []string{input.DataDir}
	if store.UsesExternalAttachments(input.DataDir, input.AttachmentsDir) {
		accessRoots = append(accessRoots, input.AttachmentsDir)
	}
	releaseAccess, err := store.HoldExclusiveAccess(accessRoots...)
	if err != nil {
		return Manifest{}, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, releaseAccess())
	}()
	installation, err := store.Open(ctx, input.DataDir)
	if err != nil {
		return Manifest{}, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, installation.Close())
	}()
	if startupErr := installation.StartupError(); startupErr != nil {
		return Manifest{}, startupErr
	}
	return CreateWithStorage(ctx, installation, input)
}

// CreateWithStorage writes a Backup through an already open installation.
func CreateWithStorage(
	ctx context.Context,
	installation *store.SQLite,
	input CreateInput,
) (Manifest, error) {
	if installation == nil || input.OutputPath == "" {
		return Manifest{}, errors.New("backup storage and output are required")
	}
	if input.Mode == "" {
		input.Mode = Sanitized
	}
	if input.Mode != Sanitized && input.Mode != FullFidelity {
		return Manifest{}, errors.New("backup mode is invalid")
	}
	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	}
	if input.AttachmentsDir == "" {
		if input.DataDir == "" {
			return Manifest{}, errors.New("backup Attachment Store root is required")
		}
		input.AttachmentsDir = filepath.Join(input.DataDir, "attachments")
	}
	if _, err := os.Stat(input.OutputPath); err == nil {
		return Manifest{}, errors.New("backup output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, fmt.Errorf("inspect Backup output: %w", err)
	}
	if err := preflightDiskSpace(preflightDiskSpaceInput{
		DataDir:        input.DataDir,
		AttachmentsDir: input.AttachmentsDir,
		OutputPath:     input.OutputPath,
		RequireFree:    diskspace.RequireFree,
	}); err != nil {
		return Manifest{}, err
	}

	workDir, err := os.MkdirTemp(filepath.Dir(input.OutputPath), ".beamers-backup-*")
	if err != nil {
		return Manifest{}, fmt.Errorf("create Backup staging directory: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(workDir)
	}()
	snapshotPath := filepath.Join(workDir, "beamers.db")
	schemaVersion := installation.SchemaVersion()
	if snapshotErr := installation.Snapshot(ctx, snapshotPath); snapshotErr != nil {
		return Manifest{}, snapshotErr
	}
	storedAttachments, err := store.BackupAttachmentsFromSnapshot(ctx, snapshotPath)
	if err != nil {
		return Manifest{}, err
	}
	if input.Mode == Sanitized {
		if sanitizeErr := store.SanitizeSnapshot(ctx, snapshotPath); sanitizeErr != nil {
			return Manifest{}, sanitizeErr
		}
	}
	databaseHash, err := fileSHA256(ctx, snapshotPath)
	if err != nil {
		return Manifest{}, err
	}
	attachments := make([]Attachment, 0, len(storedAttachments))
	for _, stored := range storedAttachments {
		attachment := Attachment{
			StorageKey: stored.StorageKey,
			SHA256:     stored.SHA256,
			SizeBytes:  stored.SizeBytes,
		}
		if verifyErr := verifyAttachment(ctx, input.AttachmentsDir, attachment); verifyErr != nil {
			return Manifest{}, verifyErr
		}
		attachments = append(attachments, attachment)
	}
	configuration, err := normalizeConfiguration(input.Configuration)
	if err != nil {
		return Manifest{}, err
	}
	configurationHash, err := configurationSHA256(configuration)
	if err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{
		FormatVersion:              formatVersion,
		Mode:                       input.Mode,
		SchemaVersion:              schemaVersion,
		MinimumReaderSchemaVersion: schemaVersion,
		MinimumWriterSchemaVersion: schemaVersion,
		CreatedAt:                  input.Now.UTC(),
		DatabaseSHA256:             databaseHash,
		Configuration:              configuration,
		ConfigurationSHA256:        configurationHash,
		Attachments:                attachments,
	}
	stagedArchive := filepath.Join(workDir, "archive")
	if writeErr := writeArchive(
		stagedArchive,
		snapshotPath,
		input.AttachmentsDir,
		manifest,
	); writeErr != nil {
		return Manifest{}, writeErr
	}
	if _, err = Verify(ctx, stagedArchive); err != nil {
		return Manifest{}, err
	}
	if err := installArchive(stagedArchive, input.OutputPath, syncDirectory); err != nil {
		return Manifest{}, err
	}
	if err := recordCompletion(input.DataDir, manifest.CreatedAt); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// completionMarkerName holds the timestamp of the most recently completed
// Backup, so diagnostics can report Backup age without scanning for
// archives (which may live outside DataDir, or be rotated away).
const completionMarkerName = ".beamers-last-backup"

// recordCompletion best-effort records that a Backup completed at
// completedAt. A caller without a DataDir (CreateWithStorage may run
// against an installation opened without one) has nowhere durable to
// record completion, so it is skipped rather than treated as an error.
func recordCompletion(dataDir string, completedAt time.Time) error {
	if dataDir == "" {
		return nil
	}
	marker := filepath.Join(dataDir, completionMarkerName)
	content := completedAt.UTC().Format(time.RFC3339Nano) + "\n"
	if err := os.WriteFile(marker, []byte(content), 0o600); err != nil {
		return fmt.Errorf("record Backup completion marker: %w", err)
	}
	return nil
}

// LastCompletedAt reports when the most recent Backup completed for the
// installation at dataDir. The second return value is false when no
// Backup has completed since the marker was introduced.
func LastCompletedAt(dataDir string) (time.Time, bool, error) {
	if dataDir == "" {
		return time.Time{}, false, nil
	}
	content, err := os.ReadFile(filepath.Join(dataDir, completionMarkerName)) //nolint:gosec // Fixed marker name under the configured data directory.
	if errors.Is(err, os.ErrNotExist) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("read Backup completion marker: %w", err)
	}
	completedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(content)))
	if err != nil {
		return time.Time{}, false, fmt.Errorf("parse Backup completion marker: %w", err)
	}
	return completedAt, true, nil
}

// backupDiskSpaceMarginBytes covers the ZIP central directory, manifest,
// and filesystem metadata overhead the size estimate below does not
// itself account for.
const backupDiskSpaceMarginBytes = 64 << 20

// preflightDiskSpaceInput names the inputs preflightDiskSpace estimates a
// Backup's disk requirement from. RequireFree is an explicit dependency
// (rather than calling diskspace.RequireFree directly) so tests can
// exercise the refusal path without needing a filesystem that is
// actually full.
type preflightDiskSpaceInput struct {
	DataDir        string
	AttachmentsDir string
	OutputPath     string
	RequireFree    func(path string, neededBytes uint64) error
}

// preflightDiskSpace refuses to start a Backup when the filesystem holding
// its output cannot plausibly hold one. The estimate sums the current
// database and Attachment Store sizes: Sanitized mode never grows the
// database, and Store compression is not counted, so this is
// conservative rather than exact.
func preflightDiskSpace(input preflightDiskSpaceInput) error {
	var databasePath string
	if input.DataDir != "" {
		databasePath = filepath.Join(input.DataDir, "beamers.db")
	}
	databaseBytes, err := diskspace.FileSize(databasePath)
	if err != nil {
		return err
	}
	attachmentsBytes, err := diskspace.DirSize(input.AttachmentsDir)
	if err != nil {
		return err
	}
	needed := databaseBytes + attachmentsBytes + backupDiskSpaceMarginBytes
	if err := input.RequireFree(filepath.Dir(input.OutputPath), needed); err != nil {
		return fmt.Errorf("preflight Backup disk space: %w", err)
	}
	return nil
}

func installArchive(staged, output string, syncParent func(string) error) error {
	if err := os.Link(staged, output); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("backup output already exists")
		}
		return fmt.Errorf("install Backup archive: %w", err)
	}
	if err := syncParent(filepath.Dir(output)); err != nil {
		return fmt.Errorf("sync Backup output directory: %w", err)
	}
	return nil
}

// Verify validates one complete Backup without extracting it.
func Verify(ctx context.Context, archivePath string) (Manifest, error) {
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		return Manifest{}, errors.New("open Backup archive")
	}
	if info.Size() > MaxArchiveBytes {
		return Manifest{}, errors.New("backup archive exceeds compressed size limit")
	}
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return Manifest{}, errors.New("open Backup archive")
	}
	defer func() {
		_ = archive.Close()
	}()
	if resourceErr := validateArchiveResources(archive.File); resourceErr != nil {
		return Manifest{}, resourceErr
	}
	var manifest Manifest
	var database *zip.File
	archiveAttachments := make(map[string]*zip.File)
	manifestFound := false
	remainingOutputBytes := int64(maxArchiveOutputBytes)
	for _, file := range archive.File {
		switch file.Name {
		case manifestName:
			if manifestFound {
				return Manifest{}, errors.New("backup archive contains a duplicate manifest")
			}
			manifestFound = true
			manifestBytes, decodeErr := decodeZIPJSON(ctx, file, &manifest)
			if decodeErr != nil {
				return Manifest{}, decodeErr
			}
			if budgetErr := consumeArchiveOutput(
				&remainingOutputBytes,
				manifestBytes,
			); budgetErr != nil {
				return Manifest{}, budgetErr
			}
		case databaseName:
			if database != nil {
				return Manifest{}, errors.New("backup archive contains a duplicate database")
			}
			database = file
		default:
			if !strings.HasPrefix(file.Name, "attachments/") {
				return Manifest{}, errors.New("backup archive contains an unknown entry")
			}
			if _, exists := archiveAttachments[file.Name]; exists {
				return Manifest{}, errors.New("backup archive contains a duplicate attachment")
			}
			archiveAttachments[file.Name] = file
		}
	}
	if manifest.FormatVersion != formatVersion ||
		manifest.Mode != Sanitized && manifest.Mode != FullFidelity ||
		manifest.SchemaVersion <= 0 ||
		manifest.MinimumReaderSchemaVersion <= 0 ||
		manifest.MinimumReaderSchemaVersion > manifest.SchemaVersion ||
		manifest.MinimumWriterSchemaVersion <= 0 ||
		manifest.MinimumWriterSchemaVersion > manifest.SchemaVersion ||
		manifest.Configuration.Version != configurationVersion ||
		len(manifest.ConfigurationSHA256) != sha256.Size*2 ||
		database == nil ||
		!manifestFound {
		return Manifest{}, errors.New("backup manifest is invalid")
	}
	configurationHash, err := configurationSHA256(manifest.Configuration)
	if err != nil || configurationHash != manifest.ConfigurationSHA256 {
		return Manifest{}, errors.New("backup service configuration integrity check failed")
	}
	manifest.Configuration, err = normalizeConfiguration(manifest.Configuration)
	if err != nil {
		return Manifest{}, errors.New("backup service configuration is invalid")
	}
	foundHash, foundSize, err := zipFileIntegrity(
		ctx,
		database,
		maxDatabaseBytes,
	)
	if err != nil {
		return Manifest{}, err
	}
	if uint64(foundSize) != database.UncompressedSize64 { //nolint:gosec // io.Copy sizes are nonnegative.
		return Manifest{}, errors.New("backup database size does not match ZIP metadata")
	}
	if budgetErr := consumeArchiveOutput(
		&remainingOutputBytes,
		foundSize,
	); budgetErr != nil {
		return Manifest{}, budgetErr
	}
	if foundHash != manifest.DatabaseSHA256 {
		return Manifest{}, errors.New("backup database integrity check failed")
	}
	seenKeys := make(map[string]struct{}, len(manifest.Attachments))
	for _, attachment := range manifest.Attachments {
		name, nameErr := attachmentArchiveName(attachment.StorageKey)
		if nameErr != nil || attachment.SizeBytes < 0 || len(attachment.SHA256) != sha256.Size*2 {
			return Manifest{}, errors.New("backup attachment inventory is invalid")
		}
		if _, exists := seenKeys[attachment.StorageKey]; exists {
			return Manifest{}, errors.New("backup attachment inventory contains a duplicate")
		}
		seenKeys[attachment.StorageKey] = struct{}{}
		file, exists := archiveAttachments[name]
		if !exists {
			return Manifest{}, errors.New("backup archive is missing an attachment")
		}
		foundHash, foundSize, hashErr := zipFileIntegrity(
			ctx,
			file,
			maxAttachmentBytes,
		)
		if hashErr != nil {
			return Manifest{}, hashErr
		}
		if budgetErr := consumeArchiveOutput(
			&remainingOutputBytes,
			foundSize,
		); budgetErr != nil {
			return Manifest{}, budgetErr
		}
		if foundHash != attachment.SHA256 || foundSize != attachment.SizeBytes {
			return Manifest{}, errors.New("backup attachment integrity check failed")
		}
		delete(archiveAttachments, name)
	}
	if len(archiveAttachments) != 0 {
		return Manifest{}, errors.New("backup archive contains an unreferenced attachment")
	}
	return manifest, nil
}

func consumeArchiveOutput(remaining *int64, size int64) error {
	if size > *remaining {
		return errors.New("backup archive exceeds expanded size limit")
	}
	*remaining -= size
	return nil
}

func validateArchiveResources(files []*zip.File) error {
	if len(files) > maxArchiveEntries {
		return errors.New("backup archive exceeds entry count limit")
	}
	var compressedBytes, outputBytes uint64
	for _, file := range files {
		switch {
		case file.Name == manifestName && file.UncompressedSize64 > maxManifestBytes:
			return errors.New("backup manifest exceeds size limit")
		case file.Name == databaseName && file.UncompressedSize64 > maxDatabaseBytes:
			return errors.New("backup database exceeds size limit")
		case strings.HasPrefix(file.Name, "attachments/") &&
			file.UncompressedSize64 > maxAttachmentBytes:
			return errors.New("backup Attachment exceeds size limit")
		}
		if file.CompressedSize64 > uint64(MaxArchiveBytes)-compressedBytes {
			return errors.New("backup archive exceeds compressed size limit")
		}
		compressedBytes += file.CompressedSize64
		if file.UncompressedSize64 > maxArchiveOutputBytes-outputBytes {
			return errors.New("backup archive exceeds expanded size limit")
		}
		outputBytes += file.UncompressedSize64
	}
	return nil
}

// Restore prepares and applies one verified Backup without replacing existing state.
func Restore(ctx context.Context, input RestoreInput) (Manifest, error) {
	plan, err := PrepareRestore(ctx, input)
	if err != nil {
		return Manifest{}, err
	}
	return ApplyRestoreWithOptions(ctx, plan.JournalPath, ApplyOptions{
		AcknowledgeUnsupportedRisks:         input.AcknowledgeUnsupportedRisks,
		AcknowledgeConfigurationDifferences: input.AcknowledgeConfigurationDifferences,
	})
}

func extractRestore(
	ctx context.Context,
	archivePath, dataDir, attachmentsDir string,
	manifest Manifest,
) error {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return errors.New("open staged Restore archive")
	}
	defer func() {
		_ = archive.Close()
	}()
	destinations := map[string]string{
		databaseName: filepath.Join(dataDir, "beamers.db"),
	}
	for _, attachment := range manifest.Attachments {
		name, nameErr := attachmentArchiveName(attachment.StorageKey)
		if nameErr != nil {
			return nameErr
		}
		destinations[name] = filepath.Join(attachmentsDir, attachment.StorageKey)
	}
	remainingOutputBytes := int64(maxArchiveOutputBytes)
	for _, file := range archive.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		destination, wanted := destinations[file.Name]
		if !wanted {
			continue
		}
		limit := int64(maxAttachmentBytes)
		if file.Name == databaseName {
			limit = maxDatabaseBytes
		}
		size, extractErr := extractFile(ctx, file, destination, limit)
		if extractErr != nil {
			return extractErr
		}
		if budgetErr := consumeArchiveOutput(
			&remainingOutputBytes,
			size,
		); budgetErr != nil {
			return budgetErr
		}
		delete(destinations, file.Name)
	}
	if len(destinations) != 0 {
		return errors.New("restore archive is incomplete")
	}
	return nil
}

func extractFile(
	ctx context.Context,
	file *zip.File,
	destination string,
	limit int64,
) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return 0, errors.New("prepare Restore destination")
	}
	input, err := file.Open()
	if err != nil {
		return 0, errors.New("open Restore entry")
	}
	defer func() {
		_ = input.Close()
	}()
	if file.UncompressedSize64 > uint64(limit) || //nolint:gosec // Fixed positive archive limits.
		file.UncompressedSize64 > math.MaxInt64 {
		return 0, errors.New("restore entry exceeds size limit")
	}
	output, err := os.OpenFile( //nolint:gosec // Fixed names and validated storage keys.
		destination,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return 0, errors.New("create Restore entry")
	}
	size, err := io.CopyN(
		output,
		cancelableReader(ctx, input),
		limit+1,
	)
	if err != nil && !errors.Is(err, io.EOF) {
		_ = output.Close()
		if contextErr := ctx.Err(); contextErr != nil {
			return 0, contextErr
		}
		return 0, errors.New("extract Restore entry")
	}
	if size > limit {
		_ = output.Close()
		return 0, errors.New("restore entry exceeds size limit")
	}
	if size != int64(file.UncompressedSize64) {
		_ = output.Close()
		return 0, errors.New("restore entry size does not match ZIP metadata")
	}
	if err = ctx.Err(); err != nil {
		_ = output.Close()
		return 0, err
	}
	if err = output.Sync(); err != nil {
		_ = output.Close()
		return 0, errors.New("sync Restore entry")
	}
	if err = output.Close(); err != nil {
		return 0, errors.New("close Restore entry")
	}
	return size, nil
}

func requireAbsent(target, description string) error {
	if _, err := os.Stat(target); err == nil {
		return errors.New(description + " already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect %s: %w", description, err)
	}
	return nil
}

func copyFileExclusive(
	ctx context.Context,
	source, destination string,
) (returnErr error) {
	created := false
	defer func() {
		if created && returnErr != nil {
			returnErr = errors.Join(returnErr, os.Remove(destination))
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	input, err := os.Open(source) //nolint:gosec // Explicit host-authorized Backup path.
	if err != nil {
		return errors.New("open Restore archive")
	}
	defer func() {
		returnErr = errors.Join(returnErr, input.Close())
	}()
	info, err := input.Stat()
	if err != nil {
		return errors.New("inspect Restore archive")
	}
	if info.Size() > MaxArchiveBytes {
		return errors.New("restore archive exceeds compressed size limit")
	}
	output, err := os.OpenFile( //nolint:gosec // Private process-owned Restore staging root.
		destination,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return errors.New("create staged Restore archive")
	}
	created = true
	defer func() {
		returnErr = errors.Join(returnErr, output.Close())
	}()
	size, err := io.Copy(
		output,
		io.LimitReader(
			cancelableReader(ctx, input),
			MaxArchiveBytes+1,
		),
	)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return errors.New("stage Restore archive")
	}
	if size > MaxArchiveBytes {
		return errors.New("restore archive exceeds compressed size limit")
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	return output.Sync()
}

func writeArchive(
	archivePath, snapshotPath, attachmentsDir string,
	manifest Manifest,
) (returnErr error) {
	output, err := os.OpenFile( //nolint:gosec // Host-authorized non-overwriting output.
		archivePath,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		return fmt.Errorf("create Backup archive: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, output.Close())
	}()
	writer := zip.NewWriter(output)
	manifestEntry, err := writer.CreateHeader(&zip.FileHeader{
		Name: manifestName, Method: zip.Deflate,
	})
	if err != nil {
		return errors.New("create Backup manifest entry")
	}
	if err = json.NewEncoder(manifestEntry).Encode(manifest); err != nil {
		return errors.New("encode Backup manifest")
	}
	databaseEntry, err := writer.CreateHeader(&zip.FileHeader{
		Name: databaseName, Method: zip.Deflate,
	})
	if err != nil {
		return errors.New("create Backup database entry")
	}
	database, err := os.Open(snapshotPath) //nolint:gosec // Private process-owned Backup staging.
	if err != nil {
		return errors.New("open Backup database snapshot")
	}
	if _, err = io.Copy(databaseEntry, database); err != nil {
		_ = database.Close()
		return errors.New("write Backup database snapshot")
	}
	if err = database.Close(); err != nil {
		return errors.New("close Backup database snapshot")
	}
	for _, attachment := range manifest.Attachments {
		name, nameErr := attachmentArchiveName(attachment.StorageKey)
		if nameErr != nil {
			return nameErr
		}
		entry, createErr := writer.CreateHeader(&zip.FileHeader{
			Name: name, Method: zip.Store,
		})
		if createErr != nil {
			return errors.New("create Backup Attachment entry")
		}
		input, openErr := openAttachment(attachmentsDir, attachment.StorageKey)
		if openErr != nil {
			return openErr
		}
		_, copyErr := io.Copy(entry, input)
		closeErr := input.Close()
		if copyErr != nil || closeErr != nil {
			return errors.New("write Backup Attachment")
		}
	}
	if err = writer.Close(); err != nil {
		return errors.New("close Backup archive")
	}
	return output.Sync()
}

func verifyAttachment(ctx context.Context, root string, attachment Attachment) error {
	input, err := openAttachment(root, attachment.StorageKey)
	if err != nil {
		return err
	}
	foundHash, foundSize, hashErr := readerIntegrity(ctx, input)
	closeErr := input.Close()
	if hashErr != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return errors.New("read Backup Attachment")
	}
	if closeErr != nil {
		return errors.New("read Backup Attachment")
	}
	if foundHash != attachment.SHA256 || foundSize != attachment.SizeBytes {
		return errors.New("Attachment Store integrity check failed")
	}
	return nil
}

func openAttachment(root, storageKey string) (*os.File, error) {
	if _, err := attachmentArchiveName(storageKey); err != nil {
		return nil, err
	}
	attachmentPath := filepath.Join(root, storageKey)
	info, err := os.Lstat(attachmentPath)
	if err != nil {
		return nil, errors.New("open referenced Attachment")
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("referenced Attachment is not a regular file")
	}
	input, err := os.Open(attachmentPath) //nolint:gosec // Configured root plus validated logical key.
	if err != nil {
		return nil, errors.New("open referenced Attachment")
	}
	return input, nil
}

func attachmentArchiveName(storageKey string) (string, error) {
	if storageKey == "" ||
		filepath.IsAbs(storageKey) ||
		filepath.Clean(storageKey) != storageKey ||
		slices.Contains(strings.FieldsFunc(storageKey, func(character rune) bool {
			return character == '/' || character == '\\'
		}), "..") {
		return "", errors.New("Attachment storage key is unsafe")
	}
	return pathpkg.Join("attachments", filepath.ToSlash(storageKey)), nil
}

func decodeZIPJSON(
	ctx context.Context,
	file *zip.File,
	destination any,
) (int64, error) {
	input, err := file.Open()
	if err != nil {
		return 0, errors.New("open Backup manifest")
	}
	defer func() {
		_ = input.Close()
	}()
	limited := &io.LimitedReader{
		R: cancelableReader(ctx, input),
		N: maxManifestBytes + 1,
	}
	decoder := json.NewDecoder(limited)
	if err = decoder.Decode(destination); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return 0, contextErr
		}
		return 0, errors.New("decode Backup manifest")
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if contextErr := ctx.Err(); contextErr != nil {
			return 0, contextErr
		}
		return 0, errors.New("backup manifest contains trailing content")
	}
	size := maxManifestBytes + 1 - limited.N
	if size > maxManifestBytes {
		return 0, errors.New("backup manifest exceeds size limit")
	}
	if uint64(size) != file.UncompressedSize64 { //nolint:gosec // LimitedReader sizes are nonnegative.
		return 0, errors.New("backup manifest size does not match ZIP metadata")
	}
	return size, ctx.Err()
}

func fileSHA256(ctx context.Context, filePath string) (string, error) {
	input, err := os.Open(filePath) //nolint:gosec // Private process-owned Backup staging.
	if err != nil {
		return "", errors.New("open Backup database snapshot")
	}
	defer func() {
		_ = input.Close()
	}()
	return readerSHA256(ctx, input)
}

func zipFileIntegrity(
	ctx context.Context,
	file *zip.File,
	limit int64,
) (string, int64, error) {
	input, err := file.Open()
	if err != nil {
		return "", 0, errors.New("open Backup entry")
	}
	defer func() {
		_ = input.Close()
	}()
	limited := &io.LimitedReader{
		R: cancelableReader(ctx, input),
		N: limit + 1,
	}
	digest, size, err := readerIntegrity(ctx, limited)
	if err != nil {
		return "", 0, err
	}
	if size > limit {
		return "", 0, errors.New("backup entry exceeds size limit")
	}
	return digest, size, nil
}

func readerSHA256(ctx context.Context, input io.Reader) (string, error) {
	digest, _, err := readerIntegrity(ctx, input)
	return digest, err
}

func readerIntegrity(ctx context.Context, input io.Reader) (string, int64, error) {
	digest := sha256.New()
	size, err := io.Copy(digest, cancelableReader(ctx, input))
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return "", 0, contextErr
		}
		return "", 0, errors.New("hash Backup content")
	}
	return hex.EncodeToString(digest.Sum(nil)), size, nil
}

func cancelableReader(ctx context.Context, input io.Reader) io.Reader {
	return readerFunc(func(buffer []byte) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		count, err := input.Read(buffer)
		if contextErr := ctx.Err(); contextErr != nil {
			return count, contextErr
		}
		return count, err
	})
}

type readerFunc func([]byte) (int, error)

func (read readerFunc) Read(buffer []byte) (int, error) {
	return read(buffer)
}
