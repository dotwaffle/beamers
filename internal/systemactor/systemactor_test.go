package systemactor_test

import (
	"context"
	"testing"

	"github.com/dotwaffle/beamers/internal/systemactor"
)

// TestNewContextNamesEveryActor proves each named System Actor survives a
// round trip through the context, and that the names stay distinct.
func TestNewContextNamesEveryActor(t *testing.T) {
	t.Parallel()
	seen := make(map[string]systemactor.Actor)
	for _, actor := range systemactor.All() {
		t.Run(actor.String(), func(t *testing.T) {
			t.Parallel()
			named, ok := systemactor.FromContext(systemactor.NewContext(t.Context(), actor))
			if !ok {
				t.Fatalf("actor %s was not established", actor)
			}
			if named != actor {
				t.Errorf("actor = %s, want %s", named, actor)
			}
			if !named.Named() {
				t.Errorf("actor %s reports itself unnamed", named)
			}
		})
		if previous, duplicate := seen[actor.String()]; duplicate {
			t.Errorf("actors %d and %d share the name %s", previous, actor, actor)
		}
		seen[actor.String()] = actor
	}
	if len(seen) != 7 {
		t.Errorf("named actors = %d, want the 7 the closed set declares", len(seen))
	}
}

// TestFromContextRefusesUnnamedWork proves the closed set: a bare context, an
// actor outside the set, and a foreign value under a like-typed key all leave
// the work unnamed.
func TestFromContextRefusesUnnamedWork(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		context func(context.Context) context.Context
	}{
		{
			name:    "bare context",
			context: func(ctx context.Context) context.Context { return ctx },
		},
		{
			name: "zero actor",
			context: func(ctx context.Context) context.Context {
				return systemactor.NewContext(ctx, systemactor.Actor(0))
			},
		},
		{
			name: "actor outside the closed set",
			context: func(ctx context.Context) context.Context {
				return systemactor.NewContext(ctx, systemactor.Actor(200))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			actor, ok := systemactor.FromContext(test.context(t.Context()))
			if ok {
				t.Errorf("unnamed work was accepted as %s", actor)
			}
		})
	}
}

// TestNewContextReplacesTheNamedActor proves a later boundary renames the
// caller rather than layering, so the innermost boundary is what the store
// sees.
func TestNewContextReplacesTheNamedActor(t *testing.T) {
	t.Parallel()
	ctx := systemactor.NewContext(t.Context(), systemactor.PublicVisitor)
	actor, ok := systemactor.FromContext(systemactor.NewContext(ctx, systemactor.Display))
	if !ok || actor != systemactor.Display {
		t.Errorf("actor = %s (established %t), want display", actor, ok)
	}
}

// TestStringNamesUnknownActors keeps the unnamed value legible in errors.
func TestStringNamesUnknownActors(t *testing.T) {
	t.Parallel()
	for _, actor := range []systemactor.Actor{systemactor.Actor(0), systemactor.Actor(9)} {
		if name := actor.String(); name != "unknown" {
			t.Errorf("Actor(%d).String() = %q, want %q", actor, name, "unknown")
		}
	}
}
