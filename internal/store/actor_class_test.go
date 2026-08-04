package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/dotwaffle/beamers/internal/systemactor"
	"github.com/dotwaffle/beamers/internal/viewer"
)

// TestRequireActorJudgesByDeclaredClass exercises requireActor directly
// against real registry keys, so the four ways a caller can be judged —
// an accepted viewer, a refused viewer, an accepted System Actor, and a
// refused System Actor — are each covered without standing up storage.
func TestRequireActorJudgesByDeclaredClass(t *testing.T) {
	t.Parallel()

	viewerOnlyKey := "CommandTx.CancelSession"     // {actorViewer, actorHostMaintenance}
	displayOnlyKey := "SQLite.LoadDisplaySnapshot" // {actorDisplay, actorHostMaintenance}
	if _, ok := entrypointDeclarations[viewerOnlyKey]; !ok {
		t.Fatalf("fixture key %s has no declaration; update the fixture", viewerOnlyKey)
	}
	if _, ok := entrypointDeclarations[displayOnlyKey]; !ok {
		t.Fatalf("fixture key %s has no declaration; update the fixture", displayOnlyKey)
	}

	tests := []struct {
		name     string
		key      string
		buildCtx func() context.Context
		wantErr  bool
	}{
		{
			name:     "viewer identity admitted by a row naming actorViewer",
			key:      viewerOnlyKey,
			buildCtx: func() context.Context { return viewer.NewContext(context.Background(), viewer.Identity{AccountID: 1}) },
			wantErr:  false,
		},
		{
			name:     "viewer identity refused by a row that does not name actorViewer",
			key:      displayOnlyKey,
			buildCtx: func() context.Context { return viewer.NewContext(context.Background(), viewer.Identity{AccountID: 1}) },
			wantErr:  true,
		},
		{
			name:     "named System Actor admitted by a row naming its class",
			key:      displayOnlyKey,
			buildCtx: func() context.Context { return systemactor.NewContext(context.Background(), systemactor.Display) },
			wantErr:  false,
		},
		{
			name:     "named System Actor refused by a row that does not name its class",
			key:      displayOnlyKey,
			buildCtx: func() context.Context { return systemactor.NewContext(context.Background(), systemactor.PublicVisitor) },
			wantErr:  true,
		},
		{
			name:     "no actor named at all is refused",
			key:      viewerOnlyKey,
			buildCtx: context.Background,
			wantErr:  true,
		},
		{
			name:     "an entrypoint absent from the registry is refused closed",
			key:      "SQLite.NoSuchMethod",
			buildCtx: func() context.Context { return viewer.NewContext(context.Background(), viewer.Identity{AccountID: 1}) },
			wantErr:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := requireActor(test.buildCtx(), test.key)
			if test.wantErr && !errors.Is(err, ErrActorNotPermitted) {
				t.Errorf("requireActor(%s) = %v, want ErrActorNotPermitted", test.key, err)
			}
			if !test.wantErr && err != nil {
				t.Errorf("requireActor(%s) = %v, want nil", test.key, err)
			}
		})
	}
}

// TestEveryExportedEntrypointDeclaresItsActors walks the exported method sets
// of *SQLite and *CommandTx and asserts each carries an entry in
// entrypointDeclarations naming at least one actor class. A method reachable
// with no declaration is exactly the hole ticket 238 closes: "who may even
// reach this" must be stated at the boundary, never left implicit.
func TestEveryExportedEntrypointDeclaresItsActors(t *testing.T) {
	t.Parallel()

	for _, key := range exportedEntrypoints(t) {
		allowed, declared := entrypointDeclarations[key]
		if !declared {
			t.Errorf("%s has no entry in entrypointDeclarations", key)
			continue
		}
		if len(allowed) == 0 {
			t.Errorf("%s declares no actor class at all", key)
		}
	}
}

// TestEntrypointDeclarationsNameOnlyRealMethods is the converse check: every
// key in the registry names a method that still exists, so a rename or
// removal cannot leave a stale, silently-ignored declaration behind.
func TestEntrypointDeclarationsNameOnlyRealMethods(t *testing.T) {
	t.Parallel()

	existing := make(map[string]bool)
	for _, key := range exportedEntrypoints(t) {
		existing[key] = true
	}
	for key := range entrypointDeclarations {
		if !existing[key] {
			t.Errorf("entrypointDeclarations names %s, which is not an exported *SQLite or *CommandTx method", key)
		}
	}
}

// exportedEntrypoints returns "ReceiverType.MethodName" for every exported
// method on *SQLite and *CommandTx.
func exportedEntrypoints(t *testing.T) []string {
	t.Helper()

	var keys []string
	for _, named := range []struct {
		receiver string
		typ      reflect.Type
	}{
		{"SQLite", reflect.TypeFor[*SQLite]()},
		{"CommandTx", reflect.TypeFor[*CommandTx]()},
	} {
		for method := range named.typ.Methods() {
			if !method.IsExported() {
				continue
			}
			keys = append(keys, named.receiver+"."+method.Name)
		}
	}
	return keys
}
