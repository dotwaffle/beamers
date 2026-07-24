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

	"github.com/dotwaffle/beamers/internal/store"
)

const (
	formatVersion        = 1
	manifestName         = "manifest.json"
	databaseName         = "database/beamers.db"
	maxRestoreEntryBytes = 64 << 30
)

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
	FormatVersion  int          `json:"format_version"`
	Mode           Mode         `json:"mode"`
	SchemaVersion  int          `json:"schema_version"`
	CreatedAt      time.Time    `json:"created_at"`
	DatabaseSHA256 string       `json:"database_sha256"`
	Attachments    []Attachment `json:"attachments"`
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
}

// RestoreInput selects one verified Backup and unused local destination.
type RestoreInput struct {
	InputPath      string
	DataDir        string
	AttachmentsDir string
}

// Create writes and verifies one installation Backup.
func Create(ctx context.Context, input CreateInput) (Manifest, error) {
	if input.DataDir == "" || input.OutputPath == "" {
		return Manifest{}, errors.New("backup data directory and output are required")
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
		input.AttachmentsDir = filepath.Join(input.DataDir, "attachments")
	}
	if _, err := os.Stat(input.OutputPath); err == nil {
		return Manifest{}, errors.New("backup output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, fmt.Errorf("inspect Backup output: %w", err)
	}

	workDir, err := os.MkdirTemp(filepath.Dir(input.OutputPath), ".beamers-backup-*")
	if err != nil {
		return Manifest{}, fmt.Errorf("create Backup staging directory: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(workDir)
	}()
	snapshotPath := filepath.Join(workDir, "beamers.db")
	installation, err := store.Open(ctx, input.DataDir)
	if err != nil {
		return Manifest{}, err
	}
	if startupErr := installation.StartupError(); startupErr != nil {
		return Manifest{}, errors.Join(startupErr, installation.Close())
	}
	schemaVersion := installation.SchemaVersion()
	storedAttachments, err := installation.BackupAttachments(ctx)
	if err != nil {
		return Manifest{}, errors.Join(err, installation.Close())
	}
	if err = installation.Snapshot(ctx, snapshotPath); err != nil {
		return Manifest{}, errors.Join(err, installation.Close())
	}
	if closeErr := installation.Close(); closeErr != nil {
		return Manifest{}, closeErr
	}
	if input.Mode == Sanitized {
		if sanitizeErr := store.SanitizeSnapshot(ctx, snapshotPath); sanitizeErr != nil {
			return Manifest{}, sanitizeErr
		}
	}
	databaseHash, err := fileSHA256(snapshotPath)
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
		if verifyErr := verifyAttachment(input.AttachmentsDir, attachment); verifyErr != nil {
			return Manifest{}, verifyErr
		}
		attachments = append(attachments, attachment)
	}
	manifest := Manifest{
		FormatVersion:  formatVersion,
		Mode:           input.Mode,
		SchemaVersion:  schemaVersion,
		CreatedAt:      input.Now.UTC(),
		DatabaseSHA256: databaseHash,
		Attachments:    attachments,
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
	if _, err = Verify(stagedArchive); err != nil {
		return Manifest{}, err
	}
	if err = os.Link(stagedArchive, input.OutputPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return Manifest{}, errors.New("backup output already exists")
		}
		return Manifest{}, fmt.Errorf("install Backup archive: %w", err)
	}
	return manifest, nil
}

// Verify validates one complete Backup without extracting it.
func Verify(archivePath string) (Manifest, error) {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return Manifest{}, errors.New("open Backup archive")
	}
	defer func() {
		_ = archive.Close()
	}()
	var manifest Manifest
	var database *zip.File
	archiveAttachments := make(map[string]*zip.File)
	manifestFound := false
	for _, file := range archive.File {
		switch file.Name {
		case manifestName:
			if manifestFound {
				return Manifest{}, errors.New("backup archive contains a duplicate manifest")
			}
			manifestFound = true
			if decodeErr := decodeZIPJSON(file, &manifest); decodeErr != nil {
				return Manifest{}, decodeErr
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
		database == nil ||
		!manifestFound {
		return Manifest{}, errors.New("backup manifest is invalid")
	}
	foundHash, err := zipFileSHA256(database)
	if err != nil {
		return Manifest{}, err
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
		foundHash, foundSize, hashErr := zipFileIntegrity(file)
		if hashErr != nil {
			return Manifest{}, hashErr
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

// Restore installs one verified Backup into unused local roots.
func Restore(ctx context.Context, input RestoreInput) (manifest Manifest, returnErr error) {
	if input.InputPath == "" || input.DataDir == "" {
		return Manifest{}, errors.New("Restore input and data directory are required")
	}
	defaultAttachments := filepath.Join(input.DataDir, "attachments")
	if input.AttachmentsDir == "" {
		input.AttachmentsDir = defaultAttachments
	}
	externalAttachments := filepath.Clean(input.AttachmentsDir) != filepath.Clean(defaultAttachments)
	if err := requireAbsent(input.DataDir, "Restore data directory"); err != nil {
		return Manifest{}, err
	}
	if externalAttachments {
		if err := requireAbsent(input.AttachmentsDir, "Restore Attachment Store"); err != nil {
			return Manifest{}, err
		}
	}

	workDir, err := os.MkdirTemp(filepath.Dir(input.DataDir), ".beamers-restore-*")
	if err != nil {
		return Manifest{}, fmt.Errorf("create Restore staging directory: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, os.RemoveAll(workDir))
	}()
	stagedArchive := filepath.Join(workDir, "archive")
	if copyErr := copyFileExclusive(input.InputPath, stagedArchive); copyErr != nil {
		return Manifest{}, copyErr
	}
	manifest, err = Verify(stagedArchive)
	if err != nil {
		return Manifest{}, err
	}

	stagedData := filepath.Join(workDir, "data")
	if err = os.Mkdir(stagedData, 0o700); err != nil {
		return Manifest{}, errors.New("create Restore data staging")
	}
	stagedAttachments := filepath.Join(stagedData, "attachments")
	var externalStaging string
	if externalAttachments {
		externalStaging, err = os.MkdirTemp(
			filepath.Dir(input.AttachmentsDir),
			".beamers-restore-attachments-*",
		)
		if err != nil {
			return Manifest{}, fmt.Errorf("create Restore Attachment staging: %w", err)
		}
		defer func() {
			returnErr = errors.Join(returnErr, os.RemoveAll(externalStaging))
		}()
		stagedAttachments = externalStaging
	}
	if extractErr := extractRestore(
		stagedArchive,
		stagedData,
		stagedAttachments,
		manifest,
	); extractErr != nil {
		return Manifest{}, extractErr
	}
	if validateErr := store.ValidateSnapshot(
		ctx,
		filepath.Join(stagedData, "beamers.db"),
	); validateErr != nil {
		return Manifest{}, validateErr
	}

	externalInstalled := false
	if externalAttachments {
		if installErr := installTree(
			stagedAttachments,
			input.AttachmentsDir,
			false,
		); installErr != nil {
			return Manifest{}, installErr
		}
		externalInstalled = true
	}
	if err = installTree(stagedData, input.DataDir, true); err != nil {
		if externalInstalled {
			return Manifest{}, errors.Join(err, os.RemoveAll(input.AttachmentsDir))
		}
		return Manifest{}, err
	}
	installed := true
	defer func() {
		if returnErr != nil && installed {
			returnErr = errors.Join(returnErr, os.RemoveAll(input.DataDir))
			if externalInstalled {
				returnErr = errors.Join(returnErr, os.RemoveAll(input.AttachmentsDir))
			}
		}
	}()
	installation, err := store.Open(ctx, input.DataDir)
	if err != nil {
		return Manifest{}, err
	}
	if startupErr := installation.StartupError(); startupErr != nil {
		return Manifest{}, errors.Join(startupErr, installation.Close())
	}
	if err = installation.Ready(ctx); err != nil {
		return Manifest{}, errors.Join(err, installation.Close())
	}
	if closeErr := installation.Close(); closeErr != nil {
		return Manifest{}, closeErr
	}
	installed = false
	return manifest, nil
}

func extractRestore(
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
	for _, file := range archive.File {
		destination, wanted := destinations[file.Name]
		if !wanted {
			continue
		}
		if extractErr := extractFile(file, destination); extractErr != nil {
			return extractErr
		}
		delete(destinations, file.Name)
	}
	if len(destinations) != 0 {
		return errors.New("restore archive is incomplete")
	}
	return nil
}

func extractFile(file *zip.File, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return errors.New("prepare Restore destination")
	}
	input, err := file.Open()
	if err != nil {
		return errors.New("open Restore entry")
	}
	defer func() {
		_ = input.Close()
	}()
	if file.UncompressedSize64 > maxRestoreEntryBytes ||
		file.UncompressedSize64 > math.MaxInt64 {
		return errors.New("restore entry exceeds size limit")
	}
	output, err := os.OpenFile( //nolint:gosec // Fixed names and validated storage keys.
		destination,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return errors.New("create Restore entry")
	}
	size, err := io.CopyN(output, input, int64(file.UncompressedSize64)+1)
	if err != nil && !errors.Is(err, io.EOF) {
		_ = output.Close()
		return errors.New("extract Restore entry")
	}
	if size != int64(file.UncompressedSize64) {
		_ = output.Close()
		return errors.New("restore entry size does not match ZIP metadata")
	}
	if err = output.Sync(); err != nil {
		_ = output.Close()
		return errors.New("sync Restore entry")
	}
	if err = output.Close(); err != nil {
		return errors.New("close Restore entry")
	}
	return nil
}

func installTree(source, destination string, marker bool) (returnErr error) {
	if err := requireAbsent(destination, "Restore destination"); err != nil {
		return err
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("Restore destination already exists")
		}
		return errors.New("create Restore destination")
	}
	installed := true
	defer func() {
		if returnErr != nil && installed {
			returnErr = errors.Join(returnErr, os.RemoveAll(destination))
		}
	}()
	err := filepath.WalkDir(source, func(foundPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, relativeErr := filepath.Rel(source, foundPath)
		if relativeErr != nil || relative == "." {
			return relativeErr
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.Mkdir(target, 0o700)
		}
		if !entry.Type().IsRegular() {
			return errors.New("Restore staging contains a non-regular file")
		}
		return os.Link(foundPath, target) //nolint:gosec // Private process-owned staging root.
	})
	if err != nil {
		return fmt.Errorf("install Restore files: %w", err)
	}
	if marker {
		lock, createErr := os.OpenFile( //nolint:gosec // Newly created unused destination root.
			filepath.Join(destination, ".beamers.lock"),
			os.O_CREATE|os.O_EXCL|os.O_WRONLY,
			0o600,
		)
		if createErr != nil {
			return errors.New("create restored installation marker")
		}
		if closeErr := lock.Close(); closeErr != nil {
			return errors.New("close restored installation marker")
		}
	}
	installed = false
	return nil
}

func requireAbsent(target, description string) error {
	if _, err := os.Stat(target); err == nil {
		return errors.New(description + " already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect %s: %w", description, err)
	}
	return nil
}

func copyFileExclusive(source, destination string) (returnErr error) {
	input, err := os.Open(source) //nolint:gosec // Explicit host-authorized Backup path.
	if err != nil {
		return errors.New("open Restore archive")
	}
	defer func() {
		returnErr = errors.Join(returnErr, input.Close())
	}()
	output, err := os.OpenFile( //nolint:gosec // Private process-owned Restore staging root.
		destination,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return errors.New("create staged Restore archive")
	}
	defer func() {
		returnErr = errors.Join(returnErr, output.Close())
	}()
	if _, err = io.Copy(output, input); err != nil {
		return errors.New("stage Restore archive")
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

func verifyAttachment(root string, attachment Attachment) error {
	input, err := openAttachment(root, attachment.StorageKey)
	if err != nil {
		return err
	}
	foundHash, foundSize, hashErr := readerIntegrity(input)
	closeErr := input.Close()
	if hashErr != nil || closeErr != nil {
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

func decodeZIPJSON(file *zip.File, destination any) error {
	input, err := file.Open()
	if err != nil {
		return errors.New("open Backup manifest")
	}
	defer func() {
		_ = input.Close()
	}()
	if err := json.NewDecoder(input).Decode(destination); err != nil {
		return errors.New("decode Backup manifest")
	}
	return nil
}

func fileSHA256(filePath string) (string, error) {
	input, err := os.Open(filePath) //nolint:gosec // Private process-owned Backup staging.
	if err != nil {
		return "", errors.New("open Backup database snapshot")
	}
	defer func() {
		_ = input.Close()
	}()
	return readerSHA256(input)
}

func zipFileSHA256(file *zip.File) (string, error) {
	input, err := file.Open()
	if err != nil {
		return "", errors.New("open Backup database entry")
	}
	defer func() {
		_ = input.Close()
	}()
	return readerSHA256(input)
}

func zipFileIntegrity(file *zip.File) (string, int64, error) {
	input, err := file.Open()
	if err != nil {
		return "", 0, errors.New("open Backup Attachment entry")
	}
	defer func() {
		_ = input.Close()
	}()
	return readerIntegrity(input)
}

func readerSHA256(input io.Reader) (string, error) {
	digest, _, err := readerIntegrity(input)
	return digest, err
}

func readerIntegrity(input io.Reader) (string, int64, error) {
	digest := sha256.New()
	size, err := io.Copy(digest, input)
	if err != nil {
		return "", 0, errors.New("hash Backup content")
	}
	return hex.EncodeToString(digest.Sum(nil)), size, nil
}
