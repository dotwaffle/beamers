package store

import (
	"context"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/ent/eventgrant"
	"github.com/dotwaffle/beamers/internal/viewer"
)

// readPoolProbe names one read-only load that attendees and crew consoles
// issue while the single writer connection is busy with a live command.
type readPoolProbe struct {
	name string
	load func(context.Context, *SQLite, int) error
}

func TestReadOnlyLoadsDoNotQueueBehindTheWriter(t *testing.T) {
	t.Parallel()
	probes := []readPoolProbe{
		{
			name: "public Schedule",
			load: func(ctx context.Context, installationStore *SQLite, _ int) error {
				_, err := installationStore.LoadPublicSchedule(ctx)
				return err
			},
		},
		{
			name: "crew Rundown",
			load: func(ctx context.Context, installationStore *SQLite, eventID int) error {
				_, err := installationStore.LoadCrewRundown(ctx, eventID)
				return err
			},
		},
		{
			name: "Display Locations",
			load: func(ctx context.Context, installationStore *SQLite, eventID int) error {
				_, err := installationStore.LoadDisplayLocations(ctx, eventID)
				return err
			},
		},
		{
			name: "Display statuses",
			load: func(ctx context.Context, installationStore *SQLite, _ int) error {
				_, _, err := installationStore.ListDisplayStatuses(ctx)
				return err
			},
		},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			t.Parallel()
			installationStore := openEventTestInstallation(t)
			now := time.Date(2026, time.July, 23, 18, 0, 0, 0, time.UTC)
			administrator := bootstrapEventTestAdministrator(t, installationStore, now)
			ctx := viewer.NewContext(t.Context(), viewer.Identity{
				AccountID: administrator.ID, Administrator: true,
			})
			event := createEventTestEvent(
				t, installationStore, ctx, administrator.ID, "FOSDEM 2026", now,
			)
			grantEventTestRole(
				t, installationStore, ctx, administrator.ID, event.ID,
				administrator.ID, eventgrant.RoleProducer, now,
			)
			ctx = viewer.NewContext(ctx, viewer.Identity{
				AccountID: administrator.ID, Administrator: true,
				EventRoles: map[int]viewer.Role{event.ID: viewer.Role(eventgrant.RoleProducer)},
			})
			busy, err := installationStore.client.Tx(systemContext(t.Context()))
			if err != nil {
				t.Fatalf("occupy writer connection: %v", err)
			}
			t.Cleanup(func() {
				_ = busy.Rollback()
			})
			deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
			t.Cleanup(cancel)
			if err := probe.load(deadline, installationStore, event.ID); err != nil {
				t.Fatalf("load %s while the writer is busy: %v", probe.name, err)
			}
		})
	}
}
