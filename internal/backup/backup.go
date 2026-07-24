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
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dotwaffle/beamers/internal/store"
)

const (
	formatVersion = 1
	manifestName  = "manifest.json"
	databaseName  = "database/beamers.db"
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

// Create writes and verifies one installation Backup.
func Create(ctx context.Context, input CreateInput) (Manifest, error) {
	if input.DataDir == "" || input.OutputPath == "" {
		return Manifest{}, errors.New("Backup data directory and output are required")
	}
	if input.Mode == "" {
		input.Mode = Sanitized
	}
	if input.Mode != Sanitized && input.Mode != FullFidelity {
		return Manifest{}, errors.New("Backup mode is invalid")
	}
	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	}
	if input.AttachmentsDir == "" {
		input.AttachmentsDir = filepath.Join(input.DataDir, "attachments")
	}
	if _, err := os.Stat(input.OutputPath); err == nil {
		return Manifest{}, errors.New("Backup output already exists")
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
	if err = installation.Close(); err != nil {
		return Manifest{}, err
	}
	if input.Mode == Sanitized {
		if err = store.SanitizeSnapshot(ctx, snapshotPath); err != nil {
			return Manifest{}, err
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
		if err = verifyAttachment(input.AttachmentsDir, attachment); err != nil {
			return Manifest{}, err
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
	if err = writeArchive(
		stagedArchive,
		snapshotPath,
		input.AttachmentsDir,
		manifest,
	); err != nil {
		return Manifest{}, err
	}
	if _, err = Verify(stagedArchive); err != nil {
		return Manifest{}, err
	}
	if err = os.Link(stagedArchive, input.OutputPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return Manifest{}, errors.New("Backup output already exists")
		}
		return Manifest{}, fmt.Errorf("install Backup archive: %w", err)
	}
	return manifest, nil
}

// Verify validates one complete Backup without extracting it.
func Verify(path string) (Manifest, error) {
	archive, err := zip.OpenReader(path)
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
				return Manifest{}, errors.New("Backup archive contains a duplicate manifest")
			}
			manifestFound = true
			if err := decodeZIPJSON(file, &manifest); err != nil {
				return Manifest{}, err
			}
		case databaseName:
			if database != nil {
				return Manifest{}, errors.New("Backup archive contains a duplicate database")
			}
			database = file
		default:
			if !strings.HasPrefix(file.Name, "attachments/") {
				return Manifest{}, errors.New("Backup archive contains an unknown entry")
			}
			if _, exists := archiveAttachments[file.Name]; exists {
				return Manifest{}, errors.New("Backup archive contains a duplicate Attachment")
			}
			archiveAttachments[file.Name] = file
		}
	}
	if manifest.FormatVersion != formatVersion ||
		manifest.Mode != Sanitized && manifest.Mode != FullFidelity ||
		manifest.SchemaVersion <= 0 ||
		database == nil ||
		!manifestFound {
		return Manifest{}, errors.New("Backup manifest is invalid")
	}
	foundHash, err := zipFileSHA256(database)
	if err != nil {
		return Manifest{}, err
	}
	if foundHash != manifest.DatabaseSHA256 {
		return Manifest{}, errors.New("Backup database integrity check failed")
	}
	seenKeys := make(map[string]struct{}, len(manifest.Attachments))
	for _, attachment := range manifest.Attachments {
		name, nameErr := attachmentArchiveName(attachment.StorageKey)
		if nameErr != nil || attachment.SizeBytes < 0 || len(attachment.SHA256) != sha256.Size*2 {
			return Manifest{}, errors.New("Backup Attachment inventory is invalid")
		}
		if _, exists := seenKeys[attachment.StorageKey]; exists {
			return Manifest{}, errors.New("Backup Attachment inventory contains a duplicate")
		}
		seenKeys[attachment.StorageKey] = struct{}{}
		file, exists := archiveAttachments[name]
		if !exists {
			return Manifest{}, errors.New("Backup archive is missing an Attachment")
		}
		foundHash, foundSize, hashErr := zipFileIntegrity(file)
		if hashErr != nil {
			return Manifest{}, hashErr
		}
		if foundHash != attachment.SHA256 || foundSize != attachment.SizeBytes {
			return Manifest{}, errors.New("Backup Attachment integrity check failed")
		}
		delete(archiveAttachments, name)
	}
	if len(archiveAttachments) != 0 {
		return Manifest{}, errors.New("Backup archive contains an unreferenced Attachment")
	}
	return manifest, nil
}

func writeArchive(
	archivePath, snapshotPath, attachmentsDir string,
	manifest Manifest,
) (returnErr error) {
	output, err := os.OpenFile(archivePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
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
	database, err := os.Open(snapshotPath)
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
	input, err := os.Open(attachmentPath)
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
	return path.Join("attachments", filepath.ToSlash(storageKey)), nil
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

func fileSHA256(path string) (string, error) {
	input, err := os.Open(path)
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
