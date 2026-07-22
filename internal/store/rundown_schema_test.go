package store

import (
	"errors"
	"testing"
	"time"

	"entgo.io/ent/privacy"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/locationpublishedversion"
	"github.com/dotwaffle/beamers/ent/sessiondraft"
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
		plannedStart := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
		session := client.Session.Create().SetEventID(event.ID).SaveX(ctx)
		client.SessionDraft.Create().
			SetSessionID(session.ID).
			SetTitle("Session").
			SetType("Presentation").
			SetAudienceVisibility("Public").
			SetPlannedStart(plannedStart).
			SetPlannedEnd(plannedStart.Add(time.Hour)).
			SetTimingPolicy("FixedEnd").
			SetMinimumDurationSeconds(3600).
			SetStartBoundary("Soft").
			SetEndBoundary("Soft").
			AddLaneIDs(lane.ID).
			AddLocationIDs(location.ID).
			AddTrackIDs(track.ID).
			SaveX(ctx)
		client.SessionPublishedVersion.Create().
			SetSessionID(session.ID).
			SetPublishedRevision(1).
			SetTitle("Session").
			SetType("Presentation").
			SetAudienceVisibility("Public").
			SetPlannedStart(plannedStart).
			SetPlannedEnd(plannedStart.Add(time.Hour)).
			SetTimingPolicy("FixedEnd").
			SetMinimumDurationSeconds(3600).
			SetStartBoundary("Soft").
			SetEndBoundary("Soft").
			AddLaneIDs(lane.ID).
			AddLocationIDs(location.ID).
			AddTrackIDs(track.ID).
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
		"Sessions":                    client.Session.Query().CountX(producerContext),
		"Session Drafts":              client.SessionDraft.Query().CountX(producerContext),
		"Published Session versions":  client.SessionPublishedVersion.Query().CountX(producerContext),
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
	session := client.Session.Create().SetEventID(event.ID).SaveX(ctx)
	plannedStart := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	sessionVersion := client.SessionPublishedVersion.Create().
		SetSessionID(session.ID).
		SetPublishedRevision(1).
		SetTitle("Session").
		SetType("Presentation").
		SetAudienceVisibility("Public").
		SetPlannedStart(plannedStart).
		SetPlannedEnd(plannedStart.Add(time.Hour)).
		SetTimingPolicy("FixedEnd").
		SetMinimumDurationSeconds(3600).
		SetStartBoundary("Soft").
		SetEndBoundary("Soft").
		SaveX(ctx)
	if err := client.SessionPublishedVersion.DeleteOne(sessionVersion).Exec(ctx); !errors.Is(err, privacy.Deny) {
		t.Fatalf("Published Session deletion error = %v, want privacy denial", err)
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

func TestSessionDraftAndPublishedStatesAreIndependent(t *testing.T) {
	client := openEntTestClient(t)
	ctx := viewer.SystemContext(t.Context())
	event := createSchemaTestEvent(t, client)
	firstLocation := client.Location.Create().SetEventID(event.ID).SaveX(ctx)
	secondLocation := client.Location.Create().SetEventID(event.ID).SaveX(ctx)
	lane := client.Lane.Create().SetEventID(event.ID).SaveX(ctx)
	track := client.Track.Create().SetEventID(event.ID).SaveX(ctx)
	session := client.Session.Create().SetEventID(event.ID).SaveX(ctx)
	plannedStart := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	plannedEnd := plannedStart.Add(time.Hour)
	draft := client.SessionDraft.Create().
		SetSessionID(session.ID).
		SetTitle("Opening Session").
		SetType("Ceremony").
		SetAudienceVisibility("Public").
		SetPlannedStart(plannedStart).
		SetPlannedEnd(plannedEnd).
		SetTimingPolicy("FixedEnd").
		SetMinimumDurationSeconds(1800).
		SetStartBoundary("Hard").
		SetEndBoundary("Soft").
		AddLaneIDs(lane.ID).
		AddLocationIDs(firstLocation.ID).
		AddTrackIDs(track.ID).
		SaveX(ctx)
	client.SessionPublishedVersion.Create().
		SetSessionID(session.ID).
		SetPublishedRevision(1).
		SetTitle("Opening Session").
		SetType("Ceremony").
		SetAudienceVisibility("Public").
		SetPlannedStart(plannedStart).
		SetPlannedEnd(plannedEnd).
		SetTimingPolicy("FixedEnd").
		SetMinimumDurationSeconds(1800).
		SetStartBoundary("Hard").
		SetEndBoundary("Soft").
		AddLaneIDs(lane.ID).
		AddLocationIDs(firstLocation.ID).
		AddTrackIDs(track.ID).
		SaveX(ctx)
	draft.Update().
		SetTitle("Welcome Session").
		ClearLocations().
		AddLocationIDs(secondLocation.ID).
		SaveX(ctx)

	published := session.QueryPublishedVersions().OnlyX(ctx)
	if published.Title != "Opening Session" || published.PublishedRevision != 1 {
		t.Fatalf("Published Session = %+v, want Opening Session at revision 1", published)
	}
	publishedLocations := published.QueryLocations().IDsX(ctx)
	if len(publishedLocations) != 1 || publishedLocations[0] != firstLocation.ID {
		t.Fatalf("Published Session Locations = %v, want first Location", publishedLocations)
	}
	updatedDraft := session.QueryDraft().OnlyX(ctx)
	if updatedDraft.Title != "Welcome Session" {
		t.Fatalf("Draft Session title = %q, want Welcome Session", updatedDraft.Title)
	}
	draftLocations := updatedDraft.QueryLocations().IDsX(ctx)
	if len(draftLocations) != 1 || draftLocations[0] != secondLocation.ID {
		t.Fatalf("Draft Session Locations = %v, want second Location", draftLocations)
	}
}

func TestSessionDraftSupportsEveryVersionOneType(t *testing.T) {
	client := openEntTestClient(t)
	ctx := viewer.SystemContext(t.Context())
	event := createSchemaTestEvent(t, client)
	location := client.Location.Create().SetEventID(event.ID).SaveX(ctx)
	lane := client.Lane.Create().SetEventID(event.ID).SaveX(ctx)
	plannedStart := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	types := []string{"Presentation", "Competition", "Break", "Activity", "Ceremony", "Performance", "Hold"}
	for _, sessionType := range types {
		session := client.Session.Create().SetEventID(event.ID).SaveX(ctx)
		created, err := client.SessionDraft.Create().
			SetSessionID(session.ID).
			SetTitle(sessionType).
			SetType(sessiondraft.Type(sessionType)).
			SetAudienceVisibility("Public").
			SetPlannedStart(plannedStart).
			SetPlannedEnd(plannedStart.Add(time.Hour)).
			SetTimingPolicy("FixedEnd").
			SetMinimumDurationSeconds(3600).
			SetStartBoundary("Soft").
			SetEndBoundary("Soft").
			AddLaneIDs(lane.ID).
			AddLocationIDs(location.ID).
			Save(ctx)
		if err != nil {
			t.Errorf("create %s Session Draft: %v", sessionType, err)
			continue
		}
		if created.Type.String() != sessionType {
			t.Errorf("Session type = %q, want %q", created.Type, sessionType)
		}
	}
}

func TestSessionStateRejectsCrossEventMemberships(t *testing.T) {
	client := openEntTestClient(t)
	ctx := viewer.SystemContext(t.Context())
	sessionEvent := createSchemaTestEvent(t, client)
	memberEvent := createSchemaTestEvent(t, client)
	session := client.Session.Create().SetEventID(sessionEvent.ID).SaveX(ctx)
	lane := client.Lane.Create().SetEventID(memberEvent.ID).SaveX(ctx)
	location := client.Location.Create().SetEventID(memberEvent.ID).SaveX(ctx)
	track := client.Track.Create().SetEventID(memberEvent.ID).SaveX(ctx)
	plannedStart := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)

	createDraft := client.SessionDraft.Create().
		SetSessionID(session.ID).
		SetTitle("Invalid Session").
		SetType("Presentation").
		SetAudienceVisibility("Public").
		SetPlannedStart(plannedStart).
		SetPlannedEnd(plannedStart.Add(time.Hour)).
		SetTimingPolicy("FixedEnd").
		SetMinimumDurationSeconds(3600).
		SetStartBoundary("Soft").
		SetEndBoundary("Soft").
		AddLaneIDs(lane.ID).
		AddLocationIDs(location.ID).
		AddTrackIDs(track.ID)
	if _, err := createDraft.Save(ctx); err == nil {
		t.Fatal("cross-Event Session Draft succeeded, want rejection")
	}
}

func TestCommittedMigrationRejectsCrossEventSessionMembership(t *testing.T) {
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
	sessionEvent := createSchemaTestEvent(t, installation.client)
	memberEvent := createSchemaTestEvent(t, installation.client)
	session := installation.client.Session.Create().SetEventID(sessionEvent.ID).SaveX(ctx)
	lane := installation.client.Lane.Create().SetEventID(memberEvent.ID).SaveX(ctx)
	plannedStart := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	draft := installation.client.SessionDraft.Create().
		SetSessionID(session.ID).
		SetTitle("Session").
		SetType("Presentation").
		SetAudienceVisibility("Public").
		SetPlannedStart(plannedStart).
		SetPlannedEnd(plannedStart.Add(time.Hour)).
		SetTimingPolicy("FixedEnd").
		SetMinimumDurationSeconds(3600).
		SetStartBoundary("Soft").
		SetEndBoundary("Soft").
		SaveX(ctx)
	published := installation.client.SessionPublishedVersion.Create().
		SetSessionID(session.ID).
		SetPublishedRevision(1).
		SetTitle("Session").
		SetType("Presentation").
		SetAudienceVisibility("Public").
		SetPlannedStart(plannedStart).
		SetPlannedEnd(plannedStart.Add(time.Hour)).
		SetTimingPolicy("FixedEnd").
		SetMinimumDurationSeconds(3600).
		SetStartBoundary("Soft").
		SetEndBoundary("Soft").
		SaveX(ctx)

	if _, err := installation.database.ExecContext(
		ctx,
		"INSERT INTO session_draft_lanes (session_draft_id, lane_id) VALUES (?, ?)",
		draft.ID, lane.ID,
	); err == nil {
		t.Fatal("direct cross-Event Session Draft Lane insert succeeded, want rejection")
	}
	if _, err := installation.database.ExecContext(
		ctx,
		"INSERT INTO session_published_version_lanes (session_published_version_id, lane_id) VALUES (?, ?)",
		published.ID, lane.ID,
	); err == nil {
		t.Fatal("direct cross-Event Published Session Lane insert succeeded, want rejection")
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
