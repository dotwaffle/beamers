package store

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"modernc.org/sqlite"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/competitionentry"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
)

// countingDriver counts every statement one read path issues, including the
// statements issued inside a transaction, so a test can assert that a load
// does not scale its query count with the size of the Event.
type countingDriver struct {
	dialect.Driver
	statements atomic.Int64
}

func (driver *countingDriver) Exec(ctx context.Context, query string, args, result any) error {
	driver.statements.Add(1)
	return driver.Driver.Exec(ctx, query, args, result)
}

func (driver *countingDriver) Query(ctx context.Context, query string, args, result any) error {
	driver.statements.Add(1)
	return driver.Driver.Query(ctx, query, args, result)
}

func (driver *countingDriver) Tx(ctx context.Context) (dialect.Tx, error) {
	transaction, err := driver.Driver.Tx(ctx)
	if err != nil {
		return nil, err
	}
	return &countingTx{Tx: transaction, driver: driver}, nil
}

type countingTx struct {
	dialect.Tx
	driver *countingDriver
}

func (transaction *countingTx) Exec(ctx context.Context, query string, args, result any) error {
	transaction.driver.statements.Add(1)
	return transaction.Tx.Exec(ctx, query, args, result)
}

func (transaction *countingTx) Query(ctx context.Context, query string, args, result any) error {
	transaction.driver.statements.Add(1)
	return transaction.Tx.Query(ctx, query, args, result)
}

func openCountingEntTestClient(t *testing.T) (*ent.Client, *countingDriver) {
	t.Helper()
	registerEntSQLite.Do(func() {
		sql.Register("sqlite3", &sqlite.Driver{})
	})
	dsn := fmt.Sprintf(
		"file:beamers_counting_%d?mode=memory&cache=shared&_pragma=foreign_keys(1)",
		entDatabaseID.Add(1),
	)
	base, err := entsql.Open(dialect.SQLite, dsn)
	if err != nil {
		t.Fatalf("open counting Ent test database: %v", err)
	}
	driver := &countingDriver{Driver: base}
	client := ent.NewClient(ent.Driver(driver))
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close counting Ent test database: %v", err)
		}
	})
	if err := client.Schema.Create(systemContext(t.Context())); err != nil {
		t.Fatalf("create counting Ent test schema: %v", err)
	}
	return client, driver
}

// TestPublicScheduleQueryCountDoesNotScaleWithTheEvent pins the shape of the
// public Schedule read path: attendee load is unbounded, so the number of
// round trips it costs must depend on the number of kinds of data it needs,
// never on how large the Event is.
func TestPublicScheduleQueryCountDoesNotScaleWithTheEvent(t *testing.T) {
	t.Parallel()
	small := countPublicScheduleStatements(t, 2)
	large := countPublicScheduleStatements(t, 10)
	if large != small {
		t.Fatalf(
			"public Schedule statements = %d at scale 10 and %d at scale 2; want the same count",
			large,
			small,
		)
	}
}

func countPublicScheduleStatements(t *testing.T, scale int) int64 {
	t.Helper()
	client, driver := openCountingEntTestClient(t)
	installationStore := &SQLite{client: client}
	buildPublicScheduleFixture(t, client, scale)
	before := driver.statements.Load()
	state, err := installationStore.LoadPublicSchedule(t.Context())
	if err != nil {
		t.Fatalf("load public Schedule at scale %d: %v", scale, err)
	}
	if len(state.Sessions) != scale {
		t.Fatalf("public Schedule Sessions at scale %d = %d", scale, len(state.Sessions))
	}
	if len(state.Locations) != scale || len(state.Lanes) != scale || len(state.Tracks) != scale {
		t.Fatalf(
			"public Schedule names at scale %d = %d Locations, %d Lanes, %d Tracks",
			scale, len(state.Locations), len(state.Lanes), len(state.Tracks),
		)
	}
	return driver.statements.Load() - before
}

// buildPublicScheduleFixture creates one Active Event whose every attendee
// visible dimension grows with scale, so a per-Session or per-name query
// shows up as a query count that grows with it.
type publicScheduleFixture struct {
	eventID     int
	locationIDs []int
	laneIDs     []int
}

func buildPublicScheduleFixture(
	t *testing.T,
	client *ent.Client,
	scale int,
) publicScheduleFixture {
	t.Helper()
	ctx := systemContext(t.Context())
	event := createSchemaTestEvent(t, client)
	client.Installation.Create().SetActiveEventID(event.ID).SaveX(ctx)
	now := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	baseline := client.PublicScheduleBaseline.Create().
		SetEventID(event.ID).SetSourcePublishedRevision(1).SetCapturedAt(now).SaveX(ctx)
	lanes := make([]int, 0, scale)
	locations := make([]int, 0, scale)
	tracks := make([]int, 0, scale)
	for index := range scale {
		location := client.Location.Create().SetEventID(event.ID).SaveX(ctx)
		client.LocationPublishedVersion.Create().
			SetLocationID(location.ID).SetPublishedRevision(1).
			SetName(fmt.Sprintf("Hall %d", index)).SaveX(ctx)
		locations = append(locations, location.ID)
		lane := client.Lane.Create().SetEventID(event.ID).SaveX(ctx)
		client.LanePublishedVersion.Create().
			SetLaneID(lane.ID).SetPublishedRevision(1).SetLocationID(location.ID).
			SetName(fmt.Sprintf("Lane %d", index)).SaveX(ctx)
		lanes = append(lanes, lane.ID)
		track := client.Track.Create().SetEventID(event.ID).SaveX(ctx)
		client.TrackPublishedVersion.Create().
			SetTrackID(track.ID).SetPublishedRevision(1).
			SetName(fmt.Sprintf("Track %d", index)).SaveX(ctx)
		tracks = append(tracks, track.ID)
	}
	for index := range scale {
		lifecycle := session.LifecycleScheduled
		if index%2 == 0 {
			lifecycle = session.LifecycleLive
		}
		sessionType := sessionpublishedversion.TypePresentation
		if index%3 == 0 {
			sessionType = sessionpublishedversion.TypeCompetition
		}
		identity := client.Session.Create().
			SetEventID(event.ID).SetLifecycle(lifecycle).SetEntryOrderSeed(int64(index + 1)).
			SaveX(ctx)
		plannedStart := now.Add(time.Duration(index) * time.Hour)
		client.SessionPublishedVersion.Create().
			SetSessionID(identity.ID).SetPublishedRevision(1).
			SetTitle(fmt.Sprintf("Session %d", index)).
			SetType(sessionType).
			SetAudienceVisibility(sessionpublishedversion.AudienceVisibilityPublic).
			SetPlannedStart(plannedStart).SetPlannedEnd(plannedStart.Add(time.Hour)).
			SetTimingPolicy(sessionpublishedversion.TimingPolicyFixedEnd).
			SetMinimumDurationSeconds(1800).
			SetStartBoundary(sessionpublishedversion.StartBoundaryHard).
			SetEndBoundary(sessionpublishedversion.EndBoundaryHard).
			AddLaneIDs(lanes[index]).
			AddLocationIDs(locations[index]).
			AddTrackIDs(tracks[index]).
			SaveX(ctx)
		client.PublicScheduleBaselineEntry.Create().
			SetBaselineID(baseline.ID).SetSourcePublishedRevision(1).
			SetSessionID(identity.ID).SetForecastStart(plannedStart.Add(-time.Minute)).
			SaveX(ctx)
		if lifecycle == session.LifecycleLive {
			client.SessionRun.Create().
				SetSessionID(identity.ID).SetActualStart(plannedStart).
				SetSnapshotJSON(publicScheduleTestSnapshotJSON(plannedStart)).
				SaveX(ctx)
		}
		if sessionType == sessionpublishedversion.TypeCompetition {
			client.CompetitionEntry.Create().
				SetCompetitionSessionID(identity.ID).SetEventID(event.ID).
				SetName(fmt.Sprintf("Entry %d", index)).
				SetDisposition(competitionentry.DispositionIncluded).
				SaveX(ctx)
		}
	}
	return publicScheduleFixture{eventID: event.ID, locationIDs: locations, laneIDs: lanes}
}

func publicScheduleTestSnapshotJSON(plannedStart time.Time) string {
	return fmt.Sprintf(
		`{"type":"Presentation","timing_policy":"FixedEnd",`+
			`"planned_start":%q,"planned_end":%q,"lane_ids":[],"location_ids":[]}`,
		plannedStart.Format(time.RFC3339Nano),
		plannedStart.Add(time.Hour).Format(time.RFC3339Nano),
	)
}
