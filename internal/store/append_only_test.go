package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/dotwaffle/beamers/ent/schema"
)

// TestAppendOnlyMixin proves every append-only entity — the published-version
// entity types, Results Publications, and Results Corrections — refuses bulk
// Update and Delete unconditionally, even under the store's explicit allow
// decision. It walks the generated client by naming convention rather than a
// fixed list, so a future append-only type that forgets the mixin fails this
// test instead of silently becoming mutable.
func TestAppendOnlyMixin(t *testing.T) {
	t.Parallel()
	installation := openTripwireTestInstallation(t)
	entities := entityClients(t, installation.client)
	ctx := systemContext(t.Context())
	found := 0
	for name, entity := range entities {
		if !isAppendOnlyEntity(name) {
			continue
		}
		found++
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assertAppendOnlyDenial(t, name+" update", updateEntities(ctx, entity))
			assertAppendOnlyDenial(t, name+" delete", deleteEntities(ctx, entity))
		})
	}
	for _, required := range []string{
		"SessionPublishedVersion", "LanePublishedVersion", "LocationPublishedVersion",
		"TrackPublishedVersion", "ResultsPublication", "ResultsCorrection",
	} {
		if !isAppendOnlyEntity(required) {
			t.Fatalf("append-only naming convention missed %s", required)
		}
	}
	if found == 0 {
		t.Fatal("append-only entity walk found no append-only entities")
	}
}

// isAppendOnlyEntity identifies entities that must carry the AppendOnly
// mixin by the naming convention already established for published-version
// types, Results Publications, and Results Corrections, independent of
// whether the mixin is actually present. This is what lets the walking test
// catch a future type that forgets it, rather than only re-checking a fixed
// list.
func isAppendOnlyEntity(name string) bool {
	switch {
	case strings.HasSuffix(name, "PublishedVersion"):
		return true
	case name == "ResultsPublication", name == "ResultsCorrection":
		return true
	default:
		return false
	}
}

// updateEntities attempts a bulk update against every row of an entity and
// reports the trailing error, mirroring deleteEntities in
// privacy_tripwire_test.go.
func updateEntities(ctx context.Context, entity reflect.Value) error {
	update := entity.MethodByName("Update").Call(nil)[0]
	return callError(ctx, update.MethodByName("Save"))
}

// assertAppendOnlyDenial requires an error wrapping schema.ErrAppendOnly,
// distinguishing this schema-level invariant refusal from an authorization
// denial.
func assertAppendOnlyDenial(t *testing.T, subject string, err error) {
	t.Helper()
	if !errors.Is(err, schema.ErrAppendOnly) {
		t.Errorf("%s error = %v, want %v", subject, err, schema.ErrAppendOnly)
	}
}

// TestAppendOnlyMixinRefusesSingleRowMutations proves the guard also denies
// UpdateOne and DeleteOne — the single-row builders bulk Update and Delete
// do not exercise — against a concrete, already-created row, using
// LocationPublishedVersion as the representative append-only entity.
func TestAppendOnlyMixinRefusesSingleRowMutations(t *testing.T) {
	t.Parallel()
	client := openEntTestClient(t)
	ctx := systemContext(t.Context())
	event := createSchemaTestEvent(t, client)
	location := client.Location.Create().SetEventID(event.ID).SaveX(ctx)
	published := client.LocationPublishedVersion.Create().
		SetLocationID(location.ID).
		SetPublishedRevision(1).
		SetName("Main Stage").
		SaveX(ctx)

	// All fields are Immutable(), so no setter exists on the update builder;
	// the hook must deny the update before any field is even considered.
	_, updateErr := client.LocationPublishedVersion.UpdateOne(published).Save(ctx)
	assertAppendOnlyDenial(t, "LocationPublishedVersion UpdateOne", updateErr)

	deleteErr := client.LocationPublishedVersion.DeleteOne(published).Exec(ctx)
	assertAppendOnlyDenial(t, "LocationPublishedVersion DeleteOne", deleteErr)

	// The row must survive both refused attempts.
	if _, err := client.LocationPublishedVersion.Get(ctx, published.ID); err != nil {
		t.Fatalf("published version must survive refused mutations: %v", err)
	}
}
