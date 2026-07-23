package activation_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/dotwaffle/beamers/ent/runtime"
	"github.com/dotwaffle/beamers/internal/activation"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestActivationPreflightBlocksMissingPublishedRundown(t *testing.T) {
	storage, administrator, eventID := openActivationTest(t)
	service := newActivationService(t, storage)

	preflight, err := service.Preflight(t.Context(), administrator, eventID)
	if err != nil {
		t.Fatalf("Activation Preflight: %v", err)
	}
	if len(preflight.Blockers) != 1 || preflight.Blockers[0].Code != "published_rundown_missing" {
		t.Fatalf("Activation Preflight blockers = %+v, want missing Published Rundown", preflight.Blockers)
	}
	if _, err := service.Activate(t.Context(), administrator, activation.ActivateInput{
		EventID: eventID, CommandID: "activate-invalid", Confirmation: preflight.Confirmation,
	}); !errors.Is(err, activation.ErrPreflightBlocked) {
		t.Fatalf("blocked activation error = %v, want %v", err, activation.ErrPreflightBlocked)
	}
}

func TestAdministratorActivatesExactPreflightAndSwitchesEvents(t *testing.T) {
	storage, administrator, firstEventID := openActivationTest(t)
	secondEventID := createActivationEvent(t, storage, administrator, "Second Event", "create-second-event")
	publishMinimalRundown(t, storage, administrator, firstEventID, "first")
	publishMinimalRundown(t, storage, administrator, secondEventID, "second")
	service := newActivationService(t, storage)
	staleSecondPreflight, err := service.Preflight(t.Context(), administrator, secondEventID)
	if err != nil {
		t.Fatalf("early second Activation Preflight: %v", err)
	}

	firstPreflight, err := service.Preflight(t.Context(), administrator, firstEventID)
	if err != nil {
		t.Fatalf("first Activation Preflight: %v", err)
	}
	first, err := service.Activate(t.Context(), administrator, activation.ActivateInput{
		EventID: firstEventID, CommandID: "activate-first", Confirmation: firstPreflight.Confirmation,
	})
	if err != nil {
		t.Fatalf("activate first Event: %v", err)
	}
	if first.EventID != firstEventID || first.Generation != 1 {
		t.Fatalf("first activation = %+v, want Event %d generation 1", first, firstEventID)
	}
	if _, staleErr := service.Activate(t.Context(), administrator, activation.ActivateInput{
		EventID: secondEventID, CommandID: "activate-stale-second",
		Confirmation: staleSecondPreflight.Confirmation,
	}); !errors.Is(staleErr, activation.ErrStalePreflight) {
		t.Fatalf("concurrent stale activation error = %v, want %v", staleErr, activation.ErrStalePreflight)
	}

	secondPreflight, err := service.Preflight(t.Context(), administrator, secondEventID)
	if err != nil {
		t.Fatalf("second Activation Preflight: %v", err)
	}
	second, err := service.Activate(t.Context(), administrator, activation.ActivateInput{
		EventID: secondEventID, CommandID: "activate-second", Confirmation: secondPreflight.Confirmation,
	})
	if err != nil {
		t.Fatalf("activate second Event: %v", err)
	}
	if second.EventID != secondEventID || second.Generation != 2 {
		t.Fatalf("second activation = %+v, want Event %d generation 2", second, secondEventID)
	}
	active, err := service.ActiveEvent(t.Context(), administrator)
	if err != nil {
		t.Fatalf("read Active Event: %v", err)
	}
	if active != second {
		t.Errorf("Active Event = %+v, want %+v", active, second)
	}

	replayed, err := service.Activate(t.Context(), auth.Account{ID: administrator.ID}, activation.ActivateInput{
		EventID: secondEventID, CommandID: "activate-second", Confirmation: secondPreflight.Confirmation,
	})
	if err != nil || replayed != second {
		t.Fatalf("activation replay = %+v, %v; want %+v", replayed, err, second)
	}
}

func TestMissingEventActivationRejectionReplaysAfterAuthorityChanges(t *testing.T) {
	storage, administrator, _ := openActivationTest(t)
	service := newActivationService(t, storage)
	input := activation.ActivateInput{
		EventID: 999999, CommandID: "activate-missing-event",
		Confirmation: activation.Confirmation{EventRevision: 1, PublishedRevision: 1},
	}
	if _, err := service.Activate(t.Context(), administrator, input); !errors.Is(err, activation.ErrEventNotFound) {
		t.Fatalf("missing Event activation error = %v, want %v", err, activation.ErrEventNotFound)
	}
	administrator.Administrator = false
	if _, err := service.Activate(t.Context(), administrator, input); !errors.Is(err, activation.ErrEventNotFound) {
		t.Fatalf("missing Event activation replay error = %v, want %v", err, activation.ErrEventNotFound)
	}
}

func TestActivationPreflightWarnsForEmptyLanesAndSuspiciousDates(t *testing.T) {
	storage, administrator, eventID := openActivationTest(t)
	publishEmptyLane(t, storage, administrator, eventID)
	service := newActivationService(t, storage)

	preflight, err := service.Preflight(t.Context(), administrator, eventID)
	if err != nil {
		t.Fatalf("Activation Preflight: %v", err)
	}
	if len(preflight.Blockers) != 0 {
		t.Fatalf("Activation Preflight blockers = %+v, want none", preflight.Blockers)
	}
	wantCodes := map[string]bool{"empty_lane": false, "suspicious_dates": false}
	for _, warning := range preflight.Warnings {
		if _, ok := wantCodes[warning.Code]; ok {
			wantCodes[warning.Code] = true
		}
	}
	for code, found := range wantCodes {
		if !found {
			t.Errorf("Activation Preflight warnings = %+v, want %s", preflight.Warnings, code)
		}
	}
}

func TestActivationPreflightUsesEventLocalDateForDateWarnings(t *testing.T) {
	storage, administrator, eventID := openActivationTest(t)
	eventService, err := events.New(storage, time.Now)
	if err != nil {
		t.Fatalf("create Event service: %v", err)
	}
	_, err = eventService.Update(t.Context(), administrator, eventID, events.CreateInput{
		Name: "Revision 2026", PlannedStartDate: "2026-07-23", PlannedEndDate: "2026-07-23",
		Timezone: "Pacific/Kiritimati", EventLocale: "en-KI", EventDayBoundary: "00:00",
		CommandID: "move-event-across-date-boundary", ExpectedRevision: 1,
	})
	if err != nil {
		t.Fatalf("move Event date and timezone: %v", err)
	}
	publishEmptyLane(t, storage, administrator, eventID)
	service, err := activation.New(storage, func() time.Time {
		return time.Date(2026, 7, 22, 12, 30, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("create Activation service: %v", err)
	}
	preflight, err := service.Preflight(t.Context(), administrator, eventID)
	if err != nil {
		t.Fatalf("Activation Preflight: %v", err)
	}
	for _, warning := range preflight.Warnings {
		if warning.Code == "suspicious_dates" {
			t.Fatalf("Activation Preflight warnings = %+v, want Event-local date in range", preflight.Warnings)
		}
	}
}

func TestActivationPreflightShowsResolvedEventDayBoundary(t *testing.T) {
	storage, administrator, eventID := openActivationTest(t)
	publishMinimalRundown(t, storage, administrator, eventID, "boundary")
	service := newActivationService(t, storage)

	preflight, err := service.Preflight(t.Context(), administrator, eventID)
	if err != nil {
		t.Fatalf("Activation Preflight: %v", err)
	}
	for _, warning := range preflight.Warnings {
		if warning.Code == "event_day_boundary_resolved" {
			if warning.Message != "Event Day Boundary for 2026-08-21 resolves to 2026-08-21T06:00:00+02:00" {
				t.Errorf("Event Day Boundary warning = %q", warning.Message)
			}
			return
		}
	}
	t.Errorf("Activation Preflight warnings = %+v, want Event Day Boundary resolution", preflight.Warnings)
}

func TestActivationRequiresAdministratorAndRejectsStalePreflight(t *testing.T) {
	storage, administrator, eventID := openActivationTest(t)
	publishMinimalRundown(t, storage, administrator, eventID, "main")
	service := newActivationService(t, storage)
	nonAdministrator := administrator
	nonAdministrator.Administrator = false

	if _, err := service.Preflight(t.Context(), nonAdministrator, eventID); !errors.Is(err, activation.ErrAdministratorRequired) {
		t.Fatalf("non-Administrator Preflight error = %v, want %v", err, activation.ErrAdministratorRequired)
	}
	preflight, err := service.Preflight(t.Context(), administrator, eventID)
	if err != nil {
		t.Fatalf("Activation Preflight: %v", err)
	}
	eventService, err := events.New(storage, time.Now)
	if err != nil {
		t.Fatalf("create Event service: %v", err)
	}
	_, err = eventService.Update(t.Context(), administrator, eventID, events.CreateInput{
		Name: "Changed Event", PlannedStartDate: "2026-08-21", PlannedEndDate: "2026-08-23",
		Timezone: "Europe/Berlin", EventLocale: "de-DE", EventDayBoundary: "06:00",
		CommandID: "change-after-preflight", ExpectedRevision: preflight.Confirmation.EventRevision,
	})
	if err != nil {
		t.Fatalf("change Event after Preflight: %v", err)
	}
	if _, err := service.Activate(t.Context(), administrator, activation.ActivateInput{
		EventID: eventID, CommandID: "activate-stale", Confirmation: preflight.Confirmation,
	}); !errors.Is(err, activation.ErrStalePreflight) {
		t.Fatalf("stale activation error = %v, want %v", err, activation.ErrStalePreflight)
	}
}

func newActivationService(t *testing.T, storage *store.SQLite) *activation.Service {
	t.Helper()
	service, err := activation.New(storage, func() time.Time {
		return time.Date(2026, 7, 22, 17, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("create Activation service: %v", err)
	}
	return service
}

func openActivationTest(t *testing.T) (*store.SQLite, auth.Account, int) {
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
	now := time.Date(2026, 7, 22, 16, 0, 0, 0, time.UTC)
	bootstrapHash := strings.Repeat("b", 64)
	if issueErr := storage.IssueBootstrap(t.Context(), bootstrapHash, now, now.Add(time.Hour)); issueErr != nil {
		t.Fatalf("issue bootstrap: %v", issueErr)
	}
	created, err := storage.BootstrapAdministrator(t.Context(), store.BootstrapAdministratorParams{
		BootstrapHash: bootstrapHash, Name: "Administrator", NormalizedName: "administrator",
		PasswordHash: "test-password-hash", SessionHash: strings.Repeat("s", 64),
		Now: now, SessionExpiry: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	administrator := auth.Account{ID: created.ID, Name: created.Name, Administrator: true}
	eventID := createActivationEvent(t, storage, administrator, "Revision 2026", "create-activation-event")
	administrator.EventRoles = map[int]viewer.Role{eventID: viewer.Producer}
	return storage, administrator, eventID
}

func createActivationEvent(
	t *testing.T,
	storage *store.SQLite,
	administrator auth.Account,
	name string,
	commandID string,
) int {
	t.Helper()
	service, err := events.New(storage, time.Now)
	if err != nil {
		t.Fatalf("create Event service: %v", err)
	}
	created, err := service.Create(t.Context(), administrator, events.CreateInput{
		Name: name, PlannedStartDate: "2026-08-21", PlannedEndDate: "2026-08-23",
		Timezone: "Europe/Berlin", EventLocale: "de-DE", EventDayBoundary: "06:00",
		CommandID: commandID,
	})
	if err != nil {
		t.Fatalf("create Event: %v", err)
	}
	if _, err := service.GrantEventAccess(
		t.Context(), administrator, created.ID, administrator.ID, "Producer", commandID+"-grant",
	); err != nil {
		t.Fatalf("grant Event Producer: %v", err)
	}
	if administrator.EventRoles == nil {
		administrator.EventRoles = make(map[int]viewer.Role)
	}
	administrator.EventRoles[created.ID] = viewer.Producer
	return created.ID
}

func publishMinimalRundown(
	t *testing.T,
	storage *store.SQLite,
	administrator auth.Account,
	eventID int,
	prefix string,
) {
	t.Helper()
	publishRundown(t, storage, administrator, eventID, prefix, true)
}

func publishEmptyLane(t *testing.T, storage *store.SQLite, administrator auth.Account, eventID int) {
	t.Helper()
	publishRundown(t, storage, administrator, eventID, "empty", false)
}

func publishRundown(
	t *testing.T,
	storage *store.SQLite,
	administrator auth.Account,
	eventID int,
	prefix string,
	withSession bool,
) {
	t.Helper()
	actor := administrator
	if actor.EventRoles == nil {
		actor.EventRoles = make(map[int]viewer.Role)
	}
	actor.EventRoles[eventID] = viewer.Producer
	commands, err := rundown.NewCommands(storage, time.Now)
	if err != nil {
		t.Fatalf("create Rundown commands: %v", err)
	}
	queries, err := rundown.NewQueries(storage)
	if err != nil {
		t.Fatalf("create Rundown queries: %v", err)
	}
	input := rundown.EditDraftInput{
		EventID: eventID, CommandID: prefix + "-draft", ExpectedDraftRevision: 0,
		Locations: []rundown.LocationDraftInput{{Ref: prefix + "-location", Name: "Main Hall"}},
		Lanes: []rundown.LaneDraftInput{{
			Ref: prefix + "-lane", Name: "Main Lane",
			Location: rundown.TargetRef{Ref: prefix + "-location"},
		}},
	}
	if withSession {
		plannedStart := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
		input.Sessions = []rundown.SessionDraftInput{{
			Ref: prefix + "-session", Title: "Opening", Type: rundown.SessionCeremony,
			AudienceVisibility: rundown.AudiencePublic,
			PlannedStart:       plannedStart, PlannedEnd: plannedStart.Add(time.Hour),
			TimingPolicy: rundown.TimingFixedEnd, MinimumDuration: 30 * time.Minute,
			StartBoundary: rundown.BoundaryHard, EndBoundary: rundown.BoundarySoft,
			Lanes:     []rundown.TargetRef{{Ref: prefix + "-lane"}},
			Locations: []rundown.TargetRef{{Ref: prefix + "-location"}},
		}}
	}
	edited, err := commands.EditDraft(t.Context(), actor, input)
	if err != nil {
		t.Fatalf("Edit Draft: %v", err)
	}
	changeIDs := make([]int, 0, len(edited.Changes))
	for _, change := range edited.Changes {
		changeIDs = append(changeIDs, change.ID)
	}
	preview, err := queries.PublishPreview(t.Context(), actor, rundown.PublishPreviewInput{
		EventID: eventID, ChangeIDs: changeIDs,
	})
	if err != nil {
		t.Fatalf("Publish Preview: %v", err)
	}
	if _, err := commands.Publish(t.Context(), actor, rundown.PublishInput{
		EventID: eventID, CommandID: prefix + "-publish",
		Confirmation: rundown.PublishConfirmation{
			DraftRevision: preview.DraftRevision, PublishedRevision: preview.PublishedRevision,
			ChangeIDs: preview.ChangeIDs, Fingerprint: preview.Fingerprint,
		},
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
}
