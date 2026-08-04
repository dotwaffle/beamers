package systemactor_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// bypassProven names the one file allowed to mint an Ent allow decision: the
// tripwire test proves the decision no longer buys access.
const bypassProven = "internal/store/privacy_tripwire_test.go"

// forbidden lists the ways a caller used to reach storage without saying who
// is acting. Both are replaced by systemactor.NewContext at the boundary.
var forbidden = []string{
	"privacy.DecisionContext(",
	"systemContext(",
}

// TestNoUnnamedStorageAccess walks the module for callers that skip naming a
// System Actor. A single grep-clean assertion is cheaper to keep honest than a
// review habit, and it fails the moment the old bypass is reintroduced.
func TestNoUnnamedStorageAccess(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no source path for this test")
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), ".") && entry.Name() != "." {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if filepath.ToSlash(relative) == bypassProven || path == self {
			return nil
		}
		return assertNamed(t, path, relative)
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
}

func assertNamed(t *testing.T, path, relative string) error {
	t.Helper()
	source, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, call := range forbidden {
		if strings.Contains(string(source), call) {
			t.Errorf("%s calls %s: name a System Actor at the boundary instead", relative, call)
		}
	}
	return nil
}

// moduleRoot climbs to the directory holding go.mod, so the walk covers the
// whole module however the test is invoked.
func moduleRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("no go.mod above the working directory")
		}
		directory = parent
	}
}
