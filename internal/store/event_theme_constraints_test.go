package store

import (
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/themevalue"
)

func TestEventThemeSelectionRejectsAnotherEventsRevision(t *testing.T) {
	installation := openEventTestInstallation(t)
	ctx := hostMaintenanceContext(t.Context())
	createEvent := func(name string) int {
		t.Helper()
		created, err := installation.client.Event.Create().
			SetName(name).
			SetPlannedStartDate("2026-07-28").
			SetPlannedEndDate("2026-07-29").
			SetTimezone("UTC").
			SetEventLocale("en-US").
			SetEventDayBoundary("06:00").
			Save(ctx)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return created.ID
	}
	firstID := createEvent("First Event")
	secondID := createEvent("Second Event")
	revision, err := installation.client.EventThemeRevision.Create().
		SetEventID(firstID).
		SetConfig(themevalue.EventConfig{}).
		SetCreatedByAccountID(1).
		SetCreatedAt(time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)).
		Save(ctx)
	if err != nil {
		t.Fatalf("create Event Theme Revision: %v", err)
	}
	if _, err = installation.client.Event.UpdateOneID(secondID).
		SetActiveThemeRevisionID(revision.ID).
		Save(ctx); err == nil {
		t.Fatal("selected another Event's Theme Revision")
	}
}
