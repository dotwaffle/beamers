package store

import (
	"errors"
	"testing"
	"time"

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
		lane := client.Lane.Create().SetEventID(event.ID).SaveX(ctx)
		client.LaneDraft.Create().
			SetLaneID(lane.ID).
			SetLocationID(location.ID).
			SetName("Lane").
			SaveX(ctx)
		client.LanePublishedVersion.Create().
			SetLaneID(lane.ID).
			SetLocationID(location.ID).
			SetPublishedRevision(1).
			SetName("Lane").
			SaveX(ctx)
		track := client.Track.Create().SetEventID(event.ID).SaveX(ctx)
		client.TrackDraft.Create().
			SetTrackID(track.ID).
			SetName("Track").
			SaveX(ctx)
		client.TrackPublishedVersion.Create().
			SetTrackID(track.ID).
			SetPublishedRevision(1).
			SetName("Track").
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
		"Lanes":                       client.Lane.Query().CountX(producerContext),
		"Lane Drafts":                 client.LaneDraft.Query().CountX(producerContext),
		"Published Lane versions":     client.LanePublishedVersion.Query().CountX(producerContext),
		"Tracks":                      client.Track.Query().CountX(producerContext),
		"Track Drafts":                client.TrackDraft.Query().CountX(producerContext),
		"Published Track versions":    client.TrackPublishedVersion.Query().CountX(producerContext),
	} {
		if count != 1 {
			t.Errorf("%s visible to Producer = %d, want 1", name, count)
		}
	}
	if _, err := otherDraft.Update().SetName("Cross-Event edit").Save(producerContext); !errors.Is(err, privacy.Deny) {
		t.Errorf("cross-Event Draft mutation error = %v, want privacy denial", err)
	}
}

func TestPublishedStructuralVersionsAreAppendOnly(t *testing.T) {
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
	lane := client.Lane.Create().SetEventID(event.ID).SaveX(ctx)
	laneVersion := client.LanePublishedVersion.Create().
		SetLaneID(lane.ID).
		SetLocationID(location.ID).
		SetPublishedRevision(1).
		SetName("Lane").
		SaveX(ctx)
	if err := client.LanePublishedVersion.DeleteOne(laneVersion).Exec(ctx); !errors.Is(err, privacy.Deny) {
		t.Fatalf("Published Lane deletion error = %v, want privacy denial", err)
	}
	track := client.Track.Create().SetEventID(event.ID).SaveX(ctx)
	trackVersion := client.TrackPublishedVersion.Create().
		SetTrackID(track.ID).
		SetPublishedRevision(1).
		SetName("Track").
		SaveX(ctx)
	if err := client.TrackPublishedVersion.DeleteOne(trackVersion).Exec(ctx); !errors.Is(err, privacy.Deny) {
		t.Fatalf("Published Track deletion error = %v, want privacy denial", err)
	}
}

func TestLaneDraftAndPublishedStatesAreIndependent(t *testing.T) {
	client := openEntTestClient(t)
	ctx := viewer.SystemContext(t.Context())
	event := createSchemaTestEvent(t, client)
	firstLocation := client.Location.Create().SetEventID(event.ID).SaveX(ctx)
	secondLocation := client.Location.Create().SetEventID(event.ID).SaveX(ctx)
	lane := client.Lane.Create().SetEventID(event.ID).SaveX(ctx)
	draft := client.LaneDraft.Create().
		SetLaneID(lane.ID).
		SetLocationID(firstLocation.ID).
		SetName("Main Lane").
		SaveX(ctx)
	client.LanePublishedVersion.Create().
		SetLaneID(lane.ID).
		SetLocationID(firstLocation.ID).
		SetPublishedRevision(1).
		SetName("Main Lane").
		SaveX(ctx)
	draft.Update().
		SetLocationID(secondLocation.ID).
		SetName("Second Lane").
		SaveX(ctx)

	published := lane.QueryPublishedVersions().OnlyX(ctx)
	if published.Name != "Main Lane" || published.LocationID != firstLocation.ID {
		t.Fatalf("Published Lane = %+v, want Main Lane at first Location", published)
	}
	updatedDraft := lane.QueryDraft().OnlyX(ctx)
	if updatedDraft.Name != "Second Lane" || updatedDraft.LocationID != secondLocation.ID {
		t.Fatalf("Draft Lane = %+v, want Second Lane at second Location", updatedDraft)
	}
}

func TestRetiredDraftLaneReleasesItsLocation(t *testing.T) {
	client := openEntTestClient(t)
	ctx := viewer.SystemContext(t.Context())
	event := createSchemaTestEvent(t, client)
	location := client.Location.Create().SetEventID(event.ID).SaveX(ctx)
	first := client.Lane.Create().SetEventID(event.ID).SaveX(ctx)
	second := client.Lane.Create().SetEventID(event.ID).SaveX(ctx)
	firstDraft := client.LaneDraft.Create().
		SetLaneID(first.ID).
		SetLocationID(location.ID).
		SetName("First Lane").
		SaveX(ctx)

	_, err := client.LaneDraft.Create().
		SetLaneID(second.ID).
		SetLocationID(location.ID).
		SetName("Second Lane").
		Save(ctx)
	if !ent.IsConstraintError(err) {
		t.Fatalf("second active Lane error = %v, want constraint error", err)
	}
	firstDraft.Update().SetRetired(true).SaveX(ctx)
	if _, err := client.LaneDraft.Create().
		SetLaneID(second.ID).
		SetLocationID(location.ID).
		SetName("Second Lane").
		Save(ctx); err != nil {
		t.Fatalf("reuse Location after Draft retirement: %v", err)
	}
}

func TestLaneDraftRejectsCrossEventLocation(t *testing.T) {
	client := openEntTestClient(t)
	ctx := viewer.SystemContext(t.Context())
	laneEvent := createSchemaTestEvent(t, client)
	locationEvent := createSchemaTestEvent(t, client)
	lane := client.Lane.Create().SetEventID(laneEvent.ID).SaveX(ctx)
	location := client.Location.Create().SetEventID(locationEvent.ID).SaveX(ctx)

	_, err := client.LaneDraft.Create().
		SetLaneID(lane.ID).
		SetLocationID(location.ID).
		SetName("Invalid Lane").
		Save(ctx)
	if err == nil {
		t.Fatal("cross-Event Lane Draft succeeded, want rejection")
	}
	_, err = client.LanePublishedVersion.Create().
		SetLaneID(lane.ID).
		SetLocationID(location.ID).
		SetPublishedRevision(1).
		SetName("Invalid Lane").
		Save(ctx)
	if err == nil {
		t.Fatal("cross-Event Published Lane succeeded, want rejection")
	}
}

func TestCommittedMigrationRejectsCrossEventLanePlacement(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	installation, err := Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open installation: %v", err)
	}
	t.Cleanup(func() {
		if err := installation.Close(); err != nil {
			t.Errorf("close installation: %v", err)
		}
	})
	ctx := viewer.SystemContext(t.Context())
	laneEvent := createSchemaTestEvent(t, installation.client)
	locationEvent := createSchemaTestEvent(t, installation.client)
	lane := installation.client.Lane.Create().SetEventID(laneEvent.ID).SaveX(ctx)
	location := installation.client.Location.Create().SetEventID(locationEvent.ID).SaveX(ctx)

	if _, err := installation.database.ExecContext(
		ctx,
		"INSERT INTO lane_drafts (name, retired, lane_id, location_id) VALUES (?, false, ?, ?)",
		"Invalid Lane", lane.ID, location.ID,
	); err == nil {
		t.Fatal("direct cross-Event Lane Draft insert succeeded, want rejection")
	}
	if _, err := installation.database.ExecContext(
		ctx,
		"INSERT INTO lane_published_versions (published_revision, name, retired, created_at, lane_id, location_id) VALUES (1, ?, false, ?, ?, ?)",
		"Invalid Lane", time.Now(), lane.ID, location.ID,
	); err == nil {
		t.Fatal("direct cross-Event Published Lane insert succeeded, want rejection")
	}
}

func TestTrackDraftAndPublishedStatesAreIndependent(t *testing.T) {
	client := openEntTestClient(t)
	ctx := viewer.SystemContext(t.Context())
	event := createSchemaTestEvent(t, client)
	track := client.Track.Create().SetEventID(event.ID).SaveX(ctx)
	draft := client.TrackDraft.Create().
		SetTrackID(track.ID).
		SetName("Systems").
		SaveX(ctx)
	client.TrackPublishedVersion.Create().
		SetTrackID(track.ID).
		SetPublishedRevision(1).
		SetName("Systems").
		SaveX(ctx)
	draft.Update().SetName("Systems Engineering").SaveX(ctx)

	published := track.QueryPublishedVersions().OnlyX(ctx)
	if published.Name != "Systems" || published.PublishedRevision != 1 {
		t.Fatalf("Published Track = %+v, want Systems at revision 1", published)
	}
	updatedDraft := track.QueryDraft().OnlyX(ctx)
	if updatedDraft.Name != "Systems Engineering" {
		t.Fatalf("Draft Track name = %q, want Systems Engineering", updatedDraft.Name)
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
