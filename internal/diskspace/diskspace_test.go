package diskspace_test

import (
	"errors"
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

func TestRequireFreeRefusesImpossibleRequirement(t *testing.T) {
	err := diskspace.RequireFree(t.TempDir(), 1<<62)
	if err == nil {
		t.Fatal("RequireFree(huge) unexpectedly succeeded")
	}
	if !errors.Is(err, diskspace.ErrInsufficientSpace) {
		t.Fatalf("error = %v, want ErrInsufficientSpace", err)
	}
}
