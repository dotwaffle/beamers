package eventthemes_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/dotwaffle/beamers/ent/runtime"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/eventthemes"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/themes"
	"github.com/dotwaffle/beamers/internal/themevalue"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestProducerActivatesAndRollsBackInheritedEventTheme(t *testing.T) {
	storage, producer, eventID := openEventThemeTest(t)
	now := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
	installationThemes, err := themes.New(storage, func() time.Time { return now }, nil)
	if err != nil {
		t.Fatalf("create Installation Theme service: %v", err)
	}
	notifications := 0
	service, err := eventthemes.New(
		storage,
		installationThemes,
		func() time.Time { return now },
		func() { notifications++ },
	)
	if err != nil {
		t.Fatalf("create Event Theme service: %v", err)
	}

	installation := themevalue.DefaultConfig()
	installation.AccentColor = "#79d7ff"
	installationRevision, err := installationThemes.CreateDraft(
		t.Context(),
		producer,
		themes.CreateDraftInput{Config: installation, CommandID: "event-theme-installation"},
	)
	if err != nil {
		t.Fatalf("create Installation Theme: %v", err)
	}
	if _, err = installationThemes.Activate(t.Context(), producer, themes.ActivateInput{
		RevisionID: installationRevision.ID, CommandID: "activate-event-theme-installation",
	}); err != nil {
		t.Fatalf("activate Installation Theme: %v", err)
	}

	first, err := service.CreateDraft(t.Context(), producer, eventID, eventthemes.CreateDraftInput{
		Config: themevalue.EventConfig{
			Overrides: themevalue.Config{BrandAsset: themevalue.BrandAssetWordmark},
			Variants: map[string]themevalue.Config{
				themevalue.VariantLocationSignage: {AccentColor: "#ffdf6e"},
				themevalue.VariantTimeline:        {AccentColor: "#f5b544"},
				themevalue.VariantCrewOverview:    {Motion: themevalue.MotionStill},
			},
		},
		CommandID: "create-first-event-theme",
	})
	if err != nil {
		t.Fatalf("create Event Theme: %v", err)
	}
	preview, err := service.Preview(t.Context(), producer, eventID, first.ID)
	if err != nil || len(preview.Findings) != 0 ||
		preview.Resolved.AccentColor != installation.AccentColor {
		t.Fatalf("preview Event Theme = %+v, %v", preview, err)
	}
	if got := preview.Variants[themevalue.VariantLocationSignage].AccentColor; got != "#ffdf6e" {
		t.Fatalf("Location Signage preview accent = %q, want #ffdf6e", got)
	}
	if got := preview.Variants[themevalue.VariantTimeline].AccentColor; got != "#f5b544" {
		t.Fatalf("Timeline preview accent = %q, want #f5b544", got)
	}
	if got := preview.Variants[themevalue.VariantCrewOverview].Motion; got != themevalue.MotionStill {
		t.Fatalf("Crew Overview preview motion = %q, want %q", got, themevalue.MotionStill)
	}
	if _, err = service.Activate(t.Context(), producer, eventID, eventthemes.ActivateInput{
		RevisionID: first.ID, CommandID: "activate-first-event-theme",
	}); err != nil {
		t.Fatalf("activate Event Theme: %v", err)
	}
	active, err := service.Active(t.Context(), eventID, themevalue.VariantLocationSignage)
	if err != nil || active.Revision.ID != first.ID ||
		active.Resolved.AccentColor != "#ffdf6e" ||
		active.Resolved.BackgroundColor != installation.BackgroundColor {
		t.Fatalf("active Event Theme = %+v, %v", active, err)
	}

	inaccessible, err := service.CreateDraft(
		t.Context(),
		producer,
		eventID,
		eventthemes.CreateDraftInput{
			Config: themevalue.EventConfig{
				Overrides: themevalue.Config{
					TextColor:       "#777777",
					BackgroundColor: "#ffffff",
					SurfaceColor:    "#ffffff",
				},
			},
			CommandID: "create-inaccessible-event-theme",
		},
	)
	if err != nil {
		t.Fatalf("create inaccessible Event Theme: %v", err)
	}
	if _, err = service.Activate(t.Context(), producer, eventID, eventthemes.ActivateInput{
		RevisionID: inaccessible.ID, ExpectedActiveRevisionID: first.ID,
		CommandID: "activate-inaccessible-event-theme",
	}); !errors.Is(err, eventthemes.ErrActivationBlocked) {
		t.Fatalf("inaccessible activation error = %v", err)
	}

	if _, err = service.Activate(t.Context(), producer, eventID, eventthemes.ActivateInput{
		RevisionID: 0, ExpectedActiveRevisionID: first.ID,
		CommandID: "roll-back-to-inherited-theme",
	}); err != nil {
		t.Fatalf("roll back Event Theme: %v", err)
	}
	active, err = service.Active(t.Context(), eventID, "")
	if err != nil || active.Revision.ID != 0 || active.Resolved != installation {
		t.Fatalf("inherited Event Theme after rollback = %+v, %v", active, err)
	}
	if _, err = service.Activate(
		t.Context(),
		auth.Account{ID: producer.ID},
		eventID,
		eventthemes.ActivateInput{
			RevisionID: 0, ExpectedActiveRevisionID: first.ID,
			CommandID: "roll-back-to-inherited-theme",
		},
	); err != nil {
		t.Fatalf("replay Event Theme rollback: %v", err)
	}
	if notifications != 3 {
		t.Errorf("Event Theme notifications = %d, want 3 successful commits and replays", notifications)
	}
}

func openEventThemeTest(t *testing.T) (*store.SQLite, auth.Account, int) {
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
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	bootstrapHash := strings.Repeat("b", 64)
	if err = storage.IssueBootstrap(t.Context(), bootstrapHash, now, now.Add(time.Hour)); err != nil {
		t.Fatalf("issue bootstrap: %v", err)
	}
	created, err := storage.BootstrapAdministrator(t.Context(), store.BootstrapAdministratorParams{
		BootstrapHash: bootstrapHash, Name: "Producer", NormalizedName: "producer",
		PasswordHash: "test-password-hash", SessionHash: strings.Repeat("s", 64),
		Now: now, SessionExpiry: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("bootstrap Producer: %v", err)
	}
	producer := auth.Account{ID: created.ID, Name: created.Name, Administrator: true}
	eventService, err := events.New(storage, func() time.Time { return now }, nil, nil)
	if err != nil {
		t.Fatalf("create Event service: %v", err)
	}
	event, err := eventService.Create(t.Context(), producer, events.CreateInput{
		Name: "Theme Event", PlannedStartDate: "2026-07-28", PlannedEndDate: "2026-07-29",
		Timezone: "UTC", EventLocale: "en-GB", EventDayBoundary: "06:00",
		CommandID: "create-theme-event",
	})
	if err != nil {
		t.Fatalf("create Event: %v", err)
	}
	producer.EventRoles = map[int]viewer.Role{event.ID: viewer.Producer}
	return storage, producer, event.ID
}
