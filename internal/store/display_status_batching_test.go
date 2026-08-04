package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/ent"
)

// TestDisplayStatusListQueryCountDoesNotScaleWithTheEvent pins the crew
// Displays page: it lists every enrolled Display at once, so per-Display and
// per-Session work there compounds exactly when an Administrator is trying to
// see what is wrong mid-Event.
func TestDisplayStatusListQueryCountDoesNotScaleWithTheEvent(t *testing.T) {
	t.Parallel()
	small := countDisplayStatusStatements(t, 2)
	large := countDisplayStatusStatements(t, 10)
	if large != small {
		t.Fatalf(
			"Display status statements = %d at scale 10 and %d at scale 2; want the same count",
			large,
			small,
		)
	}
}

func countDisplayStatusStatements(t *testing.T, scale int) int64 {
	t.Helper()
	client, driver := openCountingEntTestClient(t)
	installationStore := &SQLite{client: client, reader: client}
	fixture := buildPublicScheduleFixture(t, client, scale)
	buildDisplayStatusFixture(t, client, fixture, scale)
	before := driver.statements.Load()
	activeEventID, statuses, err := installationStore.ListDisplayStatuses(hostMaintenanceContext(t.Context()))
	if err != nil {
		t.Fatalf("list Display statuses at scale %d: %v", scale, err)
	}
	if activeEventID != fixture.eventID || len(statuses) != scale {
		t.Fatalf(
			"Display statuses at scale %d = Event %d with %d entries",
			scale, activeEventID, len(statuses),
		)
	}
	for _, status := range statuses {
		if status.Standby || status.ProgramChannelID == 0 {
			t.Fatalf(
				"competition-output Display status at scale %d = %+v, want a routed Program Channel",
				scale,
				status,
			)
		}
	}
	return driver.statements.Load() - before
}

// buildDisplayStatusFixture assigns every Display to the same Location with the
// competition-output view, so the Program Channel it routes has to be resolved
// once per Location rather than once per Display.
func buildDisplayStatusFixture(
	t *testing.T,
	client *ent.Client,
	fixture publicScheduleFixture,
	scale int,
) {
	t.Helper()
	ctx := hostMaintenanceContext(t.Context())
	client.Rundown.Create().
		SetEventID(fixture.eventID).SetDraftRevision(1).SetPublishedRevision(1).
		SaveX(ctx)
	for index := range scale {
		display := client.Display.Create().
			SetName(fmt.Sprintf("Display %d", index)).
			SetEnrolledAt(time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)).
			SaveX(ctx)
		client.DisplayAssignment.Create().
			SetDisplayID(display.ID).
			SetEventID(fixture.eventID).
			SetLocationID(fixture.locationIDs[0]).
			SetViewKey("competition-output").
			SaveX(ctx)
	}
}
