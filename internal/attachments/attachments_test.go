package attachments

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/dotwaffle/beamers/ent/runtime"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/authz"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/store/storetest"
	"github.com/dotwaffle/beamers/internal/systemactor"
)

func TestReadVersionRequiresEventGrantForAdministrator(t *testing.T) {
	service := &Service{}
	_, _, err := service.ReadVersion(
		t.Context(),
		auth.Account{Administrator: true},
		1,
		1,
	)
	if !errors.Is(err, ErrUploadTargetNotFound) {
		t.Fatalf("Administrator without Event Grant error = %v, want not found", err)
	}
}

func TestStoreVersionReclaimsRepeatedMaximumSizeFailures(t *testing.T) {
	service := newAttachmentTestService(t)
	authorization := store.UploadAuthorization{
		EventID: 1, TargetType: TargetPresentation, TargetID: 1,
	}
	for attempt := range 3 {
		_, err := service.storeVersion(
			systemactor.NewContext(t.Context(), systemactor.HostMaintenance),
			authz.Event(1),
			authorization,
			"failed-upload-"+string(rune('a'+attempt)),
			"slides",
			"slides.bin",
			"application/octet-stream",
			io.LimitReader(zeroReader{}, maxAttachmentBytes),
			"Crew",
			1,
			false,
		)
		if !errors.Is(err, ErrUploadTargetNotFound) {
			t.Fatalf("failed upload %d error = %v, want target not found", attempt, err)
		}
	}
	if size := attachmentBlobBytes(t, service.root); size != 0 {
		t.Fatalf("failed uploads retain %d Attachment bytes", size)
	}
}

func TestNewReconcilesUnreferencedAttachmentFiles(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := store.Initialize(systemactor.NewContext(t.Context(), systemactor.HostMaintenance), dataDir); err != nil {
		t.Fatalf("initialize storage: %v", err)
	}
	root := filepath.Join(t.TempDir(), "attachments")
	referencedContent := []byte("referenced")
	referencedDigest := digestBytes(referencedContent)
	referencedKey := filepath.Join("sha256", referencedDigest[:2], referencedDigest)
	writeAttachmentTestFile(t, root, referencedKey, referencedContent)
	orphanContent := []byte("orphan")
	orphanDigest := digestBytes(orphanContent)
	orphanKey := filepath.Join("sha256", orphanDigest[:2], orphanDigest)
	writeAttachmentTestFile(t, root, orphanKey, orphanContent)
	writeAttachmentTestFile(t, root, filepath.Join(".tmp", "interrupted"), []byte("temporary"))
	if err := storetest.AddReferencedAttachment(
		t.Context(),
		filepath.Join(dataDir, "beamers.db"),
		referencedKey,
		referencedDigest,
		int64(len(referencedContent)),
	); err != nil {
		t.Fatalf("seed referenced Attachment: %v", err)
	}
	storage, err := store.Open(systemactor.NewContext(t.Context(), systemactor.HostMaintenance), dataDir)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := storage.Close(); closeErr != nil {
			t.Errorf("close storage: %v", closeErr)
		}
	})
	if _, err = New(t.Context(), storage, root, time.Now, nil); err != nil {
		t.Fatalf("create Attachment Service: %v", err)
	}
	if content, readErr := os.ReadFile(filepath.Join(root, referencedKey)); readErr != nil ||
		!bytes.Equal(content, referencedContent) {
		t.Fatalf("referenced Attachment = %q, %v", content, readErr)
	}
	for _, path := range []string{
		filepath.Join(root, orphanKey),
		filepath.Join(root, ".tmp", "interrupted"),
	} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("unreferenced Attachment path %q remains: %v", path, statErr)
		}
	}
}

func newAttachmentTestService(t *testing.T) *Service {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := store.Initialize(systemactor.NewContext(t.Context(), systemactor.HostMaintenance), dataDir); err != nil {
		t.Fatalf("initialize storage: %v", err)
	}
	storage, err := store.Open(systemactor.NewContext(t.Context(), systemactor.HostMaintenance), dataDir)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := storage.Close(); closeErr != nil {
			t.Errorf("close storage: %v", closeErr)
		}
	})
	service, err := New(
		t.Context(), storage, filepath.Join(t.TempDir(), "attachments"), time.Now, nil,
	)
	if err != nil {
		t.Fatalf("create Attachment Service: %v", err)
	}
	return service
}

func attachmentBlobBytes(t *testing.T, root string) int64 {
	t.Helper()
	var size int64
	err := filepath.WalkDir(filepath.Join(root, "sha256"), func(
		_ string,
		entry fs.DirEntry,
		walkErr error,
	) error {
		if errors.Is(walkErr, os.ErrNotExist) {
			return fs.SkipDir
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() {
			info, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			size += info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("inspect Attachment blobs: %v", err)
	}
	return size
}

func writeAttachmentTestFile(t *testing.T, root, key string, content []byte) {
	t.Helper()
	path := filepath.Join(root, key)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create Attachment fixture directory: %v", err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write Attachment fixture: %v", err)
	}
}

func digestBytes(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	clear(buffer)
	return len(buffer), nil
}
