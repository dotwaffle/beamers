package diskspace_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dotwaffle/beamers/internal/diskspace"
)

func TestStatReportsPositiveCapacity(t *testing.T) {
	usage, err := diskspace.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if usage.TotalBytes == 0 {
		t.Fatal("total bytes = 0, want positive")
	}
	if usage.FreeBytes > usage.TotalBytes {
		t.Fatalf("free bytes %d exceeds total bytes %d", usage.FreeBytes, usage.TotalBytes)
	}
}

func TestStatRejectsEmptyPath(t *testing.T) {
	if _, err := diskspace.Stat(""); err == nil {
		t.Fatal("Stat(\"\") unexpectedly succeeded")
	}
}

func TestStatRejectsMissingPath(t *testing.T) {
	if _, err := diskspace.Stat("/nonexistent/path/for/beamers/diskspace/test"); err == nil {
		t.Fatal("Stat on missing path unexpectedly succeeded")
	}
}

func TestRequireFreeAllowsSmallRequirement(t *testing.T) {
	if err := diskspace.RequireFree(t.TempDir(), 1); err != nil {
		t.Fatalf("RequireFree(1) = %v, want nil", err)
	}
}

func TestDirSizeSumsRegularFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a"), make([]byte, 100), 0o600); err != nil {
		t.Fatalf("write a: %v", err)
	}
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b"), make([]byte, 50), 0o600); err != nil {
		t.Fatalf("write b: %v", err)
	}
	size, err := diskspace.DirSize(root)
	if err != nil {
		t.Fatalf("DirSize: %v", err)
	}
	if size != 150 {
		t.Fatalf("DirSize = %d, want 150", size)
	}
}

func TestDirSizeReportsZeroForMissingRoot(t *testing.T) {
	size, err := diskspace.DirSize(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatalf("DirSize(missing): %v", err)
	}
	if size != 0 {
		t.Fatalf("DirSize(missing) = %d, want 0", size)
	}
}

func TestFileSizeReportsZeroForMissingFile(t *testing.T) {
	size, err := diskspace.FileSize(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatalf("FileSize(missing): %v", err)
	}
	if size != 0 {
		t.Fatalf("FileSize(missing) = %d, want 0", size)
	}
}

func TestFileSizeReportsExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, make([]byte, 42), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	size, err := diskspace.FileSize(path)
	if err != nil {
		t.Fatalf("FileSize: %v", err)
	}
	if size != 42 {
		t.Fatalf("FileSize = %d, want 42", size)
	}
}

func TestRequireFreeRefusesImpossibleRequirement(t *testing.T) {
	err := diskspace.RequireFree(t.TempDir(), 1<<62)
	if err == nil {
		t.Fatal("RequireFree(huge) unexpectedly succeeded")
	}
	if !errors.Is(err, diskspace.ErrInsufficientSpace) {
		t.Fatalf("error = %v, want ErrInsufficientSpace", err)
	}
}
