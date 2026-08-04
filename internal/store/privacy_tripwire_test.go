package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"entgo.io/ent/privacy"

	"github.com/dotwaffle/beamers/ent"
)

// TestPrivacyTripwireDeniesUnauthorizedContext proves the fail-closed tripwire
// covers every generated entity: a query or mutation carrying neither a viewer
// identity nor an explicit store decision is denied before any SQL runs.
func TestPrivacyTripwireDeniesUnauthorizedContext(t *testing.T) {
	t.Parallel()
	installation := openTripwireTestInstallation(t)
	entities := entityClients(t, installation.client)
	for name, entity := range entities {
		if err := countEntities(t.Context(), entity); !errors.Is(err, privacy.Deny) {
			t.Errorf("%s query error = %v, want privacy denial", name, err)
		}
		if err := deleteEntities(t.Context(), entity); !errors.Is(err, privacy.Deny) {
			t.Errorf("%s mutation error = %v, want privacy denial", name, err)
		}
	}
}

// TestPrivacyTripwireAllowsDecisionContext proves the store's explicit allow
// decision still carries every entity past the tripwire.
func TestPrivacyTripwireAllowsDecisionContext(t *testing.T) {
	t.Parallel()
	installation := openTripwireTestInstallation(t)
	decisionContext := systemContext(t.Context())
	for name, entity := range entityClients(t, installation.client) {
		if err := countEntities(decisionContext, entity); err != nil {
			t.Errorf("%s query under an allow decision: %v", name, err)
		}
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

func callError(ctx context.Context, method reflect.Value) error {
	results := method.Call([]reflect.Value{reflect.ValueOf(ctx)})
	last := results[len(results)-1].Interface()
	if last == nil {
		return nil
	}
	failure, ok := last.(error)
	if !ok {
		return nil
	}
	return failure
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
