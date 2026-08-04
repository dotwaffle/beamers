package store

import (
	"strings"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/ent"
)

// TestDisplaySnapshotQueryCountDoesNotScaleWithTheEvent pins the fan-out path:
// after a Publish every connected Display rebuilds its Snapshot at once, so a
// per-Session query there multiplies by the number of Displays before it
// multiplies by the number of Sessions.
func TestDisplaySnapshotQueryCountDoesNotScaleWithTheEvent(t *testing.T) {
	t.Parallel()
	small := countDisplaySnapshotStatements(t, 2)
	large := countDisplaySnapshotStatements(t, 10)
	if large != small {
		t.Fatalf(
			"Display Snapshot statements = %d at scale 10 and %d at scale 2; want the same count",
			large,
			small,
		)
	}
}

func countDisplaySnapshotStatements(t *testing.T, scale int) int64 {
	t.Helper()
	client, driver := openCountingEntTestClient(t)
	installationStore := &SQLite{client: client, reader: client}
	fixture := buildPublicScheduleFixture(t, client, scale)
	credentialHash := buildDisplaySnapshotFixture(t, client, fixture)
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	before := driver.statements.Load()
	snapshot, err := installationStore.LoadDisplaySnapshot(t.Context(), credentialHash, now)
	if err != nil {
		t.Fatalf("load Display Snapshot at scale %d: %v", scale, err)
	}
	if len(snapshot.Sessions) != scale {
		t.Fatalf(
			"Display Snapshot Sessions at scale %d = %d",
			scale,
			len(snapshot.Sessions),
		)
	}
	return driver.statements.Load() - before
}

func buildDisplaySnapshotFixture(
	t *testing.T,
	client *ent.Client,
	fixture publicScheduleFixture,
) string {
	t.Helper()
	ctx := systemContext(t.Context())
	client.Rundown.Create().
		SetEventID(fixture.eventID).SetDraftRevision(1).SetPublishedRevision(1).
		SaveX(ctx)
	display := client.Display.Create().
		SetName("Hall display").
		SetEnrolledAt(time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)).
		SaveX(ctx)
	credentialHash := strings.Repeat("d", 64)
	client.DisplayCredential.Create().
		SetDisplayID(display.ID).SetTokenHash(credentialHash).
		SaveX(ctx)
	client.DisplayAssignment.Create().
		SetDisplayID(display.ID).
		SetEventID(fixture.eventID).
		SetLocationID(fixture.locationIDs[0]).
		SetViewKey("schedule").
		SaveX(ctx)
	return credentialHash
}
