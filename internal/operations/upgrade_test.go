package operations

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/dotwaffle/beamers/internal/diskspace"
)

func TestPreflightUpgradeDiskSpaceRefusesWhenInsufficient(t *testing.T) {
	ctx := t.Context()
	dataDir := filepath.Join(t.TempDir(), "installation")
	if err := Initialize(ctx, dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	config := OpenConfig{DataDir: dataDir, AttachmentsDir: filepath.Join(dataDir, "attachments")}

	err := preflightUpgradeDiskSpace(config, func(string, uint64) error {
		return fmt.Errorf("stub: %w", diskspace.ErrInsufficientSpace)
	})
	if !errors.Is(err, diskspace.ErrInsufficientSpace) {
		t.Fatalf("preflightUpgradeDiskSpace error = %v, want ErrInsufficientSpace", err)
	}
}

func TestPreflightUpgradeDiskSpaceAllowsSufficientCapacity(t *testing.T) {
	ctx := t.Context()
	dataDir := filepath.Join(t.TempDir(), "installation")
	if err := Initialize(ctx, dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	config := OpenConfig{DataDir: dataDir, AttachmentsDir: filepath.Join(dataDir, "attachments")}

	var observedPath string
	var observedNeeded uint64
	err := preflightUpgradeDiskSpace(config, func(path string, needed uint64) error {
		observedPath, observedNeeded = path, needed
		return nil
	})
	if err != nil {
		t.Fatalf("preflightUpgradeDiskSpace: %v", err)
	}
	if observedPath != filepath.Dir(dataDir) {
		t.Fatalf("preflight checked %q, want %q", observedPath, filepath.Dir(dataDir))
	}
	if observedNeeded < upgradeDiskSpaceMarginBytes {
		t.Fatalf("preflight needed %d bytes, want at least the %d byte margin",
			observedNeeded, upgradeDiskSpaceMarginBytes)
	}
}
