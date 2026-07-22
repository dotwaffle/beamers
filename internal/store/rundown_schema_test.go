package store

import (
	"errors"
	"testing"

	"entgo.io/ent/privacy"
	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/locationpublishedversion"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestLocationDraftAndPublishedStatesAreIndependent(t *testing.T) {
	client := openEntTestClient(t)
	ctx := viewer.SystemContext(t.Context())
	event := createSchemaTestEvent(t, client)
	rundown := client.Rundown.Create().SetEventID(event.ID).SaveX(ctx)
	location := client.Location.Create().SetEventID(event.ID).SaveX(ctx)
	draft := client.LocationDraft.Create().
		SetLocationID(location.ID).
		SetName("Main Stage").
		SaveX(ctx)
	client.LocationPublishedVersion.Create().
		SetLocationID(location.ID).
		SetPublishedRevision(1).
		SetName("Main Stage").
		SaveX(ctx)
	rundown.Update().AddDraftRevision(1).AddPublishedRevision(1).SaveX(ctx)
	draft.Update().SetName("Main Hall").SaveX(ctx)

	current := location.QueryPublishedVersions().
		Order(ent.Desc(locationpublishedversion.FieldPublishedRevision)).
		FirstX(ctx)
	if current.Name != "Main Stage" || current.PublishedRevision != 1 {
		t.Fatalf("current Published Location = %+v, want Main Stage at revision 1", current)
	}
	updatedDraft := location.QueryDraft().OnlyX(ctx)
	if updatedDraft.Name != "Main Hall" {
		t.Fatalf("Draft Location name = %q, want Main Hall", updatedDraft.Name)
	}
	updatedRundown := client.Rundown.GetX(ctx, rundown.ID)
	if updatedRundown.DraftRevision != 1 || updatedRundown.PublishedRevision != 1 {
		t.Fatalf(
			"Rundown revisions = (%d, %d), want (1, 1)",
			updatedRundown.DraftRevision,
			updatedRundown.PublishedRevision,
		)
	}
}

func TestEventHasAtMostOneRundown(t *testing.T) {
	client := openEntTestClient(t)
	ctx := viewer.SystemContext(t.Context())
	event := createSchemaTestEvent(t, client)
	client.Rundown.Create().SetEventID(event.ID).SaveX(ctx)

	_, err := client.Rundown.Create().SetEventID(event.ID).Save(ctx)
	if !ent.IsConstraintError(err) {
		t.Fatalf("second Rundown error = %v, want constraint error", err)
	}
}

func TestRundownStateStaysWithinGrantedEvents(t *testing.T) {
	client := openEntTestClient(t)
	ctx := viewer.SystemContext(t.Context())
	granted := createSchemaTestEvent(t, client)
	other := createSchemaTestEvent(t, client)
	var otherDraft *ent.LocationDraft
	for _, event := range []*ent.Event{granted, other} {
		client.Rundown.Create().SetEventID(event.ID).SaveX(ctx)
		location := client.Location.Create().SetEventID(event.ID).SaveX(ctx)
		draft := client.LocationDraft.Create().
			SetLocationID(location.ID).
			SetName("Stage").
			SaveX(ctx)
		if event.ID == other.ID {
			otherDraft = draft
		}
		client.LocationPublishedVersion.Create().
			SetLocationID(location.ID).
			SetPublishedRevision(1).
			SetName("Stage").
			SaveX(ctx)
	}
	producerContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID:  11,
		EventRoles: map[int]viewer.Role{granted.ID: viewer.Producer},
	})

	for name, count := range map[string]int{
		"Rundowns":                    client.Rundown.Query().CountX(producerContext),
		"Locations":                   client.Location.Query().CountX(producerContext),
		"Location Drafts":             client.LocationDraft.Query().CountX(producerContext),
		"Published Location versions": client.LocationPublishedVersion.Query().CountX(producerContext),
	} {
		if count != 1 {
			t.Errorf("%s visible to Producer = %d, want 1", name, count)
		}
	}
	if _, err := otherDraft.Update().SetName("Cross-Event edit").Save(producerContext); !errors.Is(err, privacy.Deny) {
		t.Errorf("cross-Event Draft mutation error = %v, want privacy denial", err)
	}
}

func TestPublishedLocationVersionsAreAppendOnly(t *testing.T) {
	client := openEntTestClient(t)
	ctx := viewer.SystemContext(t.Context())
	event := createSchemaTestEvent(t, client)
	location := client.Location.Create().SetEventID(event.ID).SaveX(ctx)
	version := client.LocationPublishedVersion.Create().
		SetLocationID(location.ID).
		SetPublishedRevision(1).
		SetName("Stage").
		SaveX(ctx)

	if err := client.LocationPublishedVersion.DeleteOne(version).Exec(ctx); !errors.Is(err, privacy.Deny) {
		t.Fatalf("Published Location deletion error = %v, want privacy denial", err)
	}
}

func createSchemaTestEvent(t *testing.T, client *ent.Client) *ent.Event {
	t.Helper()
	return client.Event.Create().
		SetName("Test Event").
		SetPlannedStartDate("2026-08-21").
		SetPlannedEndDate("2026-08-23").
		SetTimezone("Europe/Berlin").
		SetEventLocale("de-DE").
		SetEventDayBoundary("06:00").
		SaveX(viewer.SystemContext(t.Context()))
}
