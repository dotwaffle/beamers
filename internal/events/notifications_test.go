package events_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/dotwaffle/beamers/ent/runtime"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/displayviews"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestEventCommandsSelectStaleProjections(t *testing.T) {
	storage, administrator := openNotificationTest(t)
	displayNotifications := 0
	scheduleNotifications := 0
	service, err := events.New(
		storage,
		time.Now,
		func() { displayNotifications++ },
		func() { scheduleNotifications++ },
	)
	if err != nil {
		t.Fatalf("create Event service: %v", err)
	}
	created, err := service.Create(t.Context(), administrator, events.CreateInput{
		Name: "BeamConf 2099", PlannedStartDate: "2099-08-21", PlannedEndDate: "2099-08-23",
		Timezone: "Europe/Berlin", EventLocale: "de-DE", EventDayBoundary: "06:00",
		CommandID: "create-notification-event",
	})
	if err != nil {
		t.Fatalf("create Event: %v", err)
	}
	administrator.EventRoles = map[int]viewer.Role{created.ID: viewer.Producer}

	update := events.CreateInput{
		Name: "BeamConf 2099", PlannedStartDate: "2099-08-22", PlannedEndDate: "2099-08-24",
		Timezone: "Europe/Berlin", EventLocale: "de-DE", EventDayBoundary: "06:00",
		CommandID: "update-notification-event", ExpectedRevision: created.Revision,
	}
	updated, err := service.Update(t.Context(), administrator, created.ID, update)
	if err != nil {
		t.Fatalf("update Event: %v", err)
	}
	if _, err = service.Update(
		t.Context(),
		auth.Account{ID: administrator.ID},
		created.ID,
		update,
	); err != nil {
		t.Fatalf("replay Event update: %v", err)
	}
	if _, err = service.Update(t.Context(), administrator, created.ID, events.CreateInput{
		Name: "Stale BeamConf", PlannedStartDate: "2099-08-22", PlannedEndDate: "2099-08-24",
		Timezone: "Europe/Berlin", EventLocale: "de-DE", EventDayBoundary: "06:00",
		CommandID: "stale-notification-event", ExpectedRevision: created.Revision,
	}); !errors.Is(err, events.ErrRevisionConflict) {
		t.Fatalf("stale Event update error = %v, want %v", err, events.ErrRevisionConflict)
	}

	configuration := events.DisplayConfigurationInput{
		Configuration:         displayviews.DefaultConfiguration(),
		ExpectedEventRevision: updated.Revision,
		CommandID:             "configure-notification-displays",
	}
	if _, err = service.ConfigureDisplays(
		t.Context(),
		administrator,
		created.ID,
		configuration,
	); err != nil {
		t.Fatalf("configure Displays: %v", err)
	}
	if _, err = service.ConfigureDisplays(
		t.Context(),
		auth.Account{ID: administrator.ID},
		created.ID,
		configuration,
	); err != nil {
		t.Fatalf("replay Display configuration: %v", err)
	}
	if _, err = service.ConfigureDisplays(
		t.Context(),
		administrator,
		created.ID,
		events.DisplayConfigurationInput{
			Configuration:         displayviews.DefaultConfiguration(),
			ExpectedEventRevision: updated.Revision,
			CommandID:             "stale-notification-displays",
		},
	); !errors.Is(err, events.ErrRevisionConflict) {
		t.Fatalf("stale Display configuration error = %v, want %v", err, events.ErrRevisionConflict)
	}
	if displayNotifications != 4 || scheduleNotifications != 2 {
		t.Errorf(
			"notifications = Display %d, Schedule %d; want Display 4, Schedule 2",
			displayNotifications,
			scheduleNotifications,
		)
	}
}

func openNotificationTest(t *testing.T) (*store.SQLite, auth.Account) {
	t.Helper()
	dataDir := t.TempDir()
	if err := store.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize storage: %v", err)
	}
	storage, err := store.Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := storage.Close(); closeErr != nil {
			t.Errorf("close storage: %v", closeErr)
		}
	})
	now := time.Date(2099, 8, 20, 0, 0, 0, 0, time.UTC)
	bootstrapHash := strings.Repeat("b", 64)
	if err = storage.IssueBootstrap(t.Context(), bootstrapHash, now, now.Add(time.Hour)); err != nil {
		t.Fatalf("issue bootstrap: %v", err)
	}
	created, err := storage.BootstrapAdministrator(t.Context(), store.BootstrapAdministratorParams{
		BootstrapHash: bootstrapHash, Name: "Administrator", NormalizedName: "administrator",
		PasswordHash: "test-password-hash", SessionHash: strings.Repeat("s", 64),
		Now: now, SessionExpiry: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	return storage, auth.Account{ID: created.ID, Name: created.Name, Administrator: true}
}
