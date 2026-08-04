package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"entgo.io/ent/privacy"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/internal/viewer"
)

// TestPrivacyTripwire proves the fail-closed tripwire covers every generated
// entity: work carrying neither a viewer identity nor an explicit store
// decision is denied before any SQL runs, and either one carries it through.
func TestPrivacyTripwire(t *testing.T) {
	t.Parallel()
	installation := openTripwireTestInstallation(t)
	entities := entityClients(t, installation.client)
	tests := []struct {
		name     string
		decided  func(context.Context) context.Context
		wantDeny bool
	}{
		{
			name:     "undecided authorization",
			decided:  func(ctx context.Context) context.Context { return ctx },
			wantDeny: true,
		},
		{
			name: "viewer identity naming no Account",
			decided: func(ctx context.Context) context.Context {
				return viewer.NewContext(ctx, viewer.Identity{})
			},
			wantDeny: true,
		},
		{
			name:    "explicit store decision",
			decided: systemContext,
		},
		{
			name: "viewer identity",
			decided: func(ctx context.Context) context.Context {
				return viewer.NewContext(ctx, viewer.Identity{AccountID: 1})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := test.decided(t.Context())
			for name, entity := range entities {
				assertTripwire(t, name+" query", countEntities(ctx, entity), test.wantDeny)
				if !test.wantDeny {
					// Mutations are only exercised for the denial case, so a
					// passing tripwire never empties a table.
					continue
				}
				assertTripwire(t, name+" mutation", deleteEntities(ctx, entity), true)
			}
		})
	}
}

func assertTripwire(t *testing.T, subject string, err error, wantDeny bool) {
	t.Helper()
	switch {
	case wantDeny && !errors.Is(err, privacy.Deny):
		t.Errorf("%s error = %v, want privacy denial", subject, err)
	case !wantDeny && err != nil:
		t.Errorf("%s: %v", subject, err)
	}
}

// entityClients returns every generated entity client keyed by entity name, so
// a schema added without the tripwire fails the coverage assertions.
func entityClients(t *testing.T, client *ent.Client) map[string]reflect.Value {
	t.Helper()
	clients := make(map[string]reflect.Value)
	value := reflect.ValueOf(client).Elem()
	for index := range value.NumField() {
		field := value.Type().Field(index)
		if !field.IsExported() {
			continue
		}
		entity := value.Field(index)
		if !entity.MethodByName("Query").IsValid() || !entity.MethodByName("Delete").IsValid() {
			continue
		}
		clients[field.Name] = entity
	}
	for _, required := range []string{"Account", "Event", "Migration", "Session"} {
		if _, found := clients[required]; !found {
			t.Fatalf("entity client walk missed %s", required)
		}
	}
	return clients
}

func countEntities(ctx context.Context, entity reflect.Value) error {
	query := entity.MethodByName("Query").Call(nil)[0]
	return callError(ctx, query.MethodByName("Count"))
}

func deleteEntities(ctx context.Context, entity reflect.Value) error {
	deletion := entity.MethodByName("Delete").Call(nil)[0]
	return callError(ctx, deletion.MethodByName("Exec"))
}

// callError reports the trailing error of a reflected Ent call, and refuses to
// read success into a call whose shape it does not recognize.
func callError(ctx context.Context, method reflect.Value) error {
	results := method.Call([]reflect.Value{reflect.ValueOf(ctx)})
	last := results[len(results)-1].Interface()
	switch value := last.(type) {
	case nil:
		return nil
	case error:
		return value
	default:
		return fmt.Errorf("unexpected Ent result %T", last)
	}
}

func openTripwireTestInstallation(t *testing.T) *SQLite {
	t.Helper()
	dataDir := t.TempDir()
	if err := Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize tripwire database: %v", err)
	}
	installation, err := Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open tripwire database: %v", err)
	}
	t.Cleanup(func() {
		if err := installation.Close(); err != nil {
			t.Errorf("close tripwire database: %v", err)
		}
	})
	return installation
}
