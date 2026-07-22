package store

import (
	"testing"
)

func TestCommittedMigrationsReplayIntoCleanSQLite(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize clean database: %v", err)
	}

	installation, err := Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open migrated database: %v", err)
	}
	defer func() {
		if closeErr := installation.Close(); closeErr != nil {
			t.Errorf("close migrated database: %v", closeErr)
		}
	}()
	if err := installation.StartupError(); err != nil {
		t.Fatalf("validate migrated database: %v", err)
	}
	if err := installation.Ready(t.Context()); err != nil {
		t.Fatalf("query migrated database through Ent: %v", err)
	}
}
