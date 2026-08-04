package attachments

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
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/systemactor"
)

const finalFilesFormat = 1

var (
	// ErrFinalFilesPreviewChanged means canonical export state changed after preview.
	ErrFinalFilesPreviewChanged = errors.New("preview for Final Files Export changed")
	// ErrFinalFilesDestinationExists prevents replacement of any existing output.
	ErrFinalFilesDestinationExists = errors.New("destination for Final Files Export already exists")
)

// FinalFilesFile maps one exported path to its source identities and immutable bytes.
type FinalFilesFile struct {
	Path             string `json:"path"`
	EventID          int    `json:"event_id"`
	TrackID          int    `json:"track_id,omitempty"`
	TrackName        string `json:"track_name,omitempty"`
	SessionID        int    `json:"session_id"`
	SessionTitle     string `json:"session_title"`
	SessionType      string `json:"session_type"`
	EntryID          int    `json:"entry_id,omitempty"`
	EntryName        string `json:"entry_name,omitempty"`
	AttachmentID     int    `json:"attachment_id"`
	AttachmentName   string `json:"attachment_name"`
	VersionID        int    `json:"version_id"`
	Version          int    `json:"version"`
	SHA256           string `json:"sha256"`
	SizeBytes        int64  `json:"size_bytes"`
	OriginalFilename string `json:"original_filename"`
}

// FinalFilesManifest makes a disposable export traceable to canonical state.
type FinalFilesManifest struct {
	Format    int              `json:"format"`
	EventID   int              `json:"event_id"`
	EventName string           `json:"event_name"`
	Files     []FinalFilesFile `json:"files"`
}

// FinalFilesPlan is the exact output presented before an export is confirmed.
type FinalFilesPlan struct {
	FinalFilesManifest
	OutputPath    string   `json:"output_path,omitempty"`
	PreviewDigest string   `json:"preview_digest"`
	Collisions    []string `json:"collisions"`
}

// PlanFinalFiles builds the deterministic logical tree for one Event.
func (service *Service) PlanFinalFiles(
	ctx context.Context,
	eventID int,
	outputPath string,
) (FinalFilesPlan, error) {
	if eventID <= 0 {
		return FinalFilesPlan{}, ErrInvalidInput
	}
	ctx = systemactor.NewContext(ctx, systemactor.HostMaintenance)
	if outputPath != "" {
		var err error
		outputPath, err = canonicalFinalFilesOutput(outputPath)
		if err != nil {
			return FinalFilesPlan{}, err
		}
	}
	versions, err := service.storage.LoadFinalFileVersions(ctx, eventID)
	if err != nil {
		return FinalFilesPlan{}, err
	}
	manifest := FinalFilesManifest{Format: finalFilesFormat, EventID: eventID, Files: []FinalFilesFile{}}
	if len(versions) != 0 {
		manifest.EventName = versions[0].EventName
	}
	for _, version := range versions {
		manifest.Files = append(manifest.Files, finalFilesFiles(version)...)
	}
	sort.Slice(manifest.Files, func(left, right int) bool {
		return manifest.Files[left].Path < manifest.Files[right].Path
	})
	plan := FinalFilesPlan{
		FinalFilesManifest: manifest,
		OutputPath:         outputPath,
		Collisions:         []string{},
	}
	if outputPath != "" {
		if _, err = os.Lstat(outputPath); err == nil {
			plan.Collisions = append(plan.Collisions, outputPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return FinalFilesPlan{}, fmt.Errorf("inspect Final Files Export destination: %w", err)
		}
	}
	plan.PreviewDigest, err = finalFilesPlanDigest(plan)
	if err != nil {
		return FinalFilesPlan{}, err
	}
	return plan, nil
}

// WriteFinalFilesDirectory creates a new export directory without replacing content.
func (service *Service) WriteFinalFilesDirectory(
	ctx context.Context,
	eventID int,
	outputPath string,
	previewDigest string,
) (FinalFilesManifest, error) {
	if outputPath == "" || previewDigest == "" {
		return FinalFilesManifest{}, ErrInvalidInput
	}
	ctx = systemactor.NewContext(ctx, systemactor.HostMaintenance)
	plan, err := service.PlanFinalFiles(ctx, eventID, outputPath)
	if err != nil {
		return FinalFilesManifest{}, err
	}
	if previewDigest != plan.PreviewDigest {
		return FinalFilesManifest{}, ErrFinalFilesPreviewChanged
	}
	if len(plan.Collisions) != 0 {
		return FinalFilesManifest{}, ErrFinalFilesDestinationExists
	}
	outputPath = plan.OutputPath
	if err = os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return FinalFilesManifest{}, fmt.Errorf("prepare Final Files Export parent: %w", err)
	}
	if err = os.Mkdir(outputPath, 0o700); err != nil {
		return FinalFilesManifest{}, fmt.Errorf("reserve Final Files Export destination: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(outputPath)
		}
	}()
	for _, file := range plan.Files {
		content, readErr := service.finalFilesContent(ctx, eventID, file.VersionID)
		if readErr != nil {
			return FinalFilesManifest{}, readErr
		}
		destination := filepath.Join(outputPath, filepath.FromSlash(file.Path))
		if err = os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return FinalFilesManifest{}, fmt.Errorf("prepare Final Files Export path: %w", err)
		}
		if err = os.WriteFile(destination, content, 0o600); err != nil {
			return FinalFilesManifest{}, fmt.Errorf("write Final Files Export file: %w", err)
		}
	}
	manifestBytes, err := encodeFinalFilesManifest(plan.FinalFilesManifest)
	if err != nil {
		return FinalFilesManifest{}, err
	}
	if err = os.WriteFile(filepath.Join(outputPath, "manifest.json"), manifestBytes, 0o600); err != nil {
		return FinalFilesManifest{}, fmt.Errorf("write Final Files Export manifest: %w", err)
	}
	keep = true
	return plan.FinalFilesManifest, nil
}

// WriteFinalFilesZIP writes a deterministic verified download after matching a preview.
func (service *Service) WriteFinalFilesZIP(
	ctx context.Context,
	eventID int,
	previewDigest string,
	output io.Writer,
) error {
	if output == nil || previewDigest == "" {
		return ErrInvalidInput
	}
	ctx = systemactor.NewContext(ctx, systemactor.HostMaintenance)
	plan, err := service.PlanFinalFiles(ctx, eventID, "")
	if err != nil {
		return err
	}
	if previewDigest != plan.PreviewDigest {
		return ErrFinalFilesPreviewChanged
	}
	archive := zip.NewWriter(output)
	for _, file := range plan.Files {
		content, readErr := service.finalFilesContent(ctx, eventID, file.VersionID)
		if readErr != nil {
			_ = archive.Close()
			return readErr
		}
		if err = writeFinalFilesZIPEntry(archive, file.Path, content); err != nil {
			_ = archive.Close()
			return err
		}
	}
	manifestBytes, err := encodeFinalFilesManifest(plan.FinalFilesManifest)
	if err != nil {
		_ = archive.Close()
		return err
	}
	if err = writeFinalFilesZIPEntry(archive, "manifest.json", manifestBytes); err != nil {
		_ = archive.Close()
		return err
	}
	if err = archive.Close(); err != nil {
		return fmt.Errorf("close Final Files Export archive: %w", err)
	}
	return nil
}

func (service *Service) finalFilesContent(
	ctx context.Context,
	eventID int,
	versionID int,
) ([]byte, error) {
	stored, err := service.storage.LoadAttachmentVersion(ctx, eventID, versionID)
	if err != nil {
		return nil, err
	}
	return service.readStoredVersion(stored)
}

func finalFilesFile(version store.FinalFileVersion, track store.FinalFileTrack) FinalFilesFile {
	root := "untracked"
	if track.ID != 0 {
		root = path.Join("tracks", labeledPath(track.Name, "track", track.ID))
	}
	kind := "presentations"
	owner := ""
	if version.OwnerType == store.UploadTargetEntry {
		kind = "competitions"
		owner = labeledPath(version.EntryName, "entry", version.EntryID)
	}
	parts := []string{
		root,
		kind,
		labeledPath(version.SessionTitle, "session", version.SessionID),
	}
	if owner != "" {
		parts = append(parts, owner)
	}
	parts = append(parts, exportedFilename(
		version.OriginalFilename, version.AttachmentID, version.Version,
	))
	return FinalFilesFile{
		Path:    path.Join(parts...),
		EventID: version.EventID,
		TrackID: track.ID, TrackName: track.Name,
		SessionID: version.SessionID, SessionTitle: version.SessionTitle,
		SessionType: version.SessionType,
		EntryID:     version.EntryID, EntryName: version.EntryName,
		AttachmentID: version.AttachmentID, AttachmentName: version.Name,
		VersionID: version.ID, Version: version.Version,
		SHA256: version.SHA256, SizeBytes: version.SizeBytes,
		OriginalFilename: version.OriginalFilename,
	}
}

func finalFilesFiles(version store.FinalFileVersion) []FinalFilesFile {
	tracks := version.Tracks
	if len(tracks) == 0 {
		tracks = []store.FinalFileTrack{{}}
	}
	files := make([]FinalFilesFile, 0, len(tracks))
	for _, track := range tracks {
		files = append(files, finalFilesFile(version, track))
	}
	return files
}

func labeledPath(label, kind string, id int) string {
	return fmt.Sprintf("%s--%s-%d", safePathComponent(label), kind, id)
}

func exportedFilename(original string, attachmentID, version int) string {
	extension := path.Ext(original)
	stem := strings.TrimSuffix(original, extension)
	return fmt.Sprintf(
		"%s--attachment-%d-version-%d%s",
		safePathComponent(stem),
		attachmentID,
		version,
		safeExtension(extension),
	)
}

func safePathComponent(value string) string {
	var result strings.Builder
	dash := false
	for _, character := range strings.TrimSpace(value) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			result.WriteRune(character)
			dash = false
			continue
		}
		if !dash && result.Len() != 0 {
			result.WriteByte('-')
			dash = true
		}
	}
	safe := strings.Trim(result.String(), "-")
	if safe == "" {
		return "unnamed"
	}
	return safe
}

func safeExtension(extension string) string {
	extension = strings.TrimPrefix(extension, ".")
	if extension == "" {
		return ""
	}
	return "." + safePathComponent(extension)
}

func encodeFinalFilesManifest(manifest FinalFilesManifest) ([]byte, error) {
	content, err := json.Marshal(manifest)
	if err != nil {
		return nil, errors.New("encode Final Files Export manifest")
	}
	return append(content, '\n'), nil
}

func finalFilesPlanDigest(plan FinalFilesPlan) (string, error) {
	commitment, err := json.Marshal(struct {
		FinalFilesManifest
		OutputPath string   `json:"output_path,omitempty"`
		Collisions []string `json:"collisions"`
	}{
		FinalFilesManifest: plan.FinalFilesManifest,
		OutputPath:         plan.OutputPath,
		Collisions:         plan.Collisions,
	})
	if err != nil {
		return "", errors.New("encode Final Files Export preview")
	}
	digest := sha256.Sum256(commitment)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalFinalFilesOutput(outputPath string) (string, error) {
	absolute, err := filepath.Abs(outputPath)
	if err != nil {
		return "", fmt.Errorf("resolve Final Files Export destination: %w", err)
	}
	parts := []string{filepath.Base(absolute)}
	parent := filepath.Dir(absolute)
	for {
		resolved, resolveErr := filepath.EvalSymlinks(parent)
		if resolveErr == nil {
			for _, part := range slices.Backward(parts) {
				resolved = filepath.Join(resolved, part)
			}
			return resolved, nil
		}
		if !errors.Is(resolveErr, os.ErrNotExist) {
			return "", fmt.Errorf("resolve Final Files Export destination: %w", resolveErr)
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", fmt.Errorf("resolve Final Files Export destination: %w", resolveErr)
		}
		parts = append(parts, filepath.Base(parent))
		parent = next
	}
}

func writeFinalFilesZIPEntry(archive *zip.Writer, name string, content []byte) error {
	header := &zip.FileHeader{Name: name, Method: zip.Store}
	header.SetMode(0o600)
	header.Modified = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)
	entry, err := archive.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("create Final Files Export archive entry: %w", err)
	}
	if _, err = entry.Write(content); err != nil {
		return fmt.Errorf("write Final Files Export archive entry: %w", err)
	}
	return nil
}
