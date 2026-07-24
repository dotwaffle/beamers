package schedulebaseline_test

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
	"github.com/dotwaffle/beamers/internal/schedule"
	"github.com/dotwaffle/beamers/internal/schedulebaseline"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestProducerCapturesExactNonActiveEventPreview(t *testing.T) {
	storage, producer, eventID := openBaselineTest(t)
	_, _ = publishBaselineTestRundown(t, storage, producer, eventID)
	queries := newBaselineQueries(t, storage)
	commands := newBaselineCommands(t, storage)

	preview, err := queries.Preview(t.Context(), producer, eventID)
	if err != nil {
		t.Fatalf("baseline Preview: %v", err)
	}
	if preview.EventName != "Baseline Event" || preview.Active ||
		!preview.RequiresNonActiveAcknowledgment || len(preview.Sessions) != 1 ||
		preview.Confirmation.PublishedRevision != 1 || preview.Confirmation.Fingerprint == "" {
		t.Fatalf("baseline Preview = %+v, want one non-Active Public Session at revision 1", preview)
	}

	withoutAcknowledgment := schedulebaseline.CaptureInput{
		EventID: eventID, CommandID: "capture-baseline-without-ack",
		Confirmation: preview.Confirmation,
	}
	if _, captureErr := commands.Capture(
		t.Context(),
		producer,
		withoutAcknowledgment,
	); !errors.Is(captureErr, schedulebaseline.ErrNonActiveAcknowledgment) {
		t.Fatalf(
			"capture without non-Active acknowledgment error = %v, want %v",
			captureErr,
			schedulebaseline.ErrNonActiveAcknowledgment,
		)
	}

	input := schedulebaseline.CaptureInput{
		EventID: eventID, CommandID: "capture-baseline",
		Confirmation:          preview.Confirmation,
		AcknowledgedEventName: preview.EventName,
	}
	captured, err := commands.Capture(t.Context(), producer, input)
	if err != nil {
		t.Fatalf("capture baseline: %v", err)
	}
	if captured.EventID != eventID || captured.PublishedRevision != 1 ||
		captured.SessionCount != 1 {
		t.Fatalf("captured baseline = %+v, want one Session at revision 1", captured)
	}
	if _, previewErr := queries.Preview(
		t.Context(),
		producer,
		eventID,
	); !errors.Is(previewErr, schedulebaseline.ErrAlreadyCaptured) {
		t.Fatalf("repeated Preview error = %v, want %v", previewErr, schedulebaseline.ErrAlreadyCaptured)
	}

	revoked := producer
	revoked.EventRoles = nil
	replayed, err := commands.Capture(t.Context(), revoked, input)
	if err != nil || replayed != captured {
		t.Fatalf("capture replay = %+v, %v; want %+v", replayed, err, captured)
	}
}

func TestCaptureRejectsPublishedRevisionChangedAfterPreview(t *testing.T) {
	storage, producer, eventID := openBaselineTest(t)
	queries := newBaselineQueries(t, storage)
	commands := newBaselineCommands(t, storage)
	preview, err := queries.Preview(t.Context(), producer, eventID)
	if err != nil {
		t.Fatalf("empty baseline Preview: %v", err)
	}
	if preview.Confirmation.PublishedRevision != 0 || len(preview.Sessions) != 0 {
		t.Fatalf("empty baseline Preview = %+v, want revision 0 and no Sessions", preview)
	}

	_, _ = publishBaselineTestRundown(t, storage, producer, eventID)
	_, err = commands.Capture(t.Context(), producer, schedulebaseline.CaptureInput{
		EventID: eventID, CommandID: "capture-stale-baseline",
		Confirmation:          preview.Confirmation,
		AcknowledgedEventName: preview.EventName,
	})
	if !errors.Is(err, schedulebaseline.ErrStalePreview) {
		t.Fatalf("stale baseline capture error = %v, want %v", err, schedulebaseline.ErrStalePreview)
	}
}

func TestFirstPublicPublicationEnrollsImmutableBaseline(t *testing.T) {
	storage, producer, eventID := openBaselineTest(t)
	queries := newBaselineQueries(t, storage)
	commands := newBaselineCommands(t, storage)
	empty, err := queries.Preview(t.Context(), producer, eventID)
	if err != nil {
		t.Fatalf("empty baseline Preview: %v", err)
	}
	if _, captureErr := commands.Capture(t.Context(), producer, schedulebaseline.CaptureInput{
		EventID: eventID, CommandID: "capture-empty-baseline",
		Confirmation:          empty.Confirmation,
		AcknowledgedEventName: empty.EventName,
	}); captureErr != nil {
		t.Fatalf("capture empty baseline: %v", captureErr)
	}

	sessionID, published := publishBaselineTestRundown(t, storage, producer, eventID)
	rundownCommands, err := rundown.NewCommands(storage, time.Now)
	if err != nil {
		t.Fatalf("create Rundown Commands: %v", err)
	}
	rundownQueries, err := rundown.NewQueries(storage)
	if err != nil {
		t.Fatalf("create Rundown Queries: %v", err)
	}
	movedStart := time.Date(2026, 8, 21, 8, 30, 0, 0, time.UTC)
	edited, err := rundownCommands.EditDraft(t.Context(), producer, rundown.EditDraftInput{
		EventID: eventID, CommandID: "move-baseline-session",
		ExpectedDraftRevision: published.DraftRevision,
		Sessions: []rundown.SessionDraftInput{{
			ID: sessionID, PlannedStart: movedStart, UpdateFields: []string{"planned_start"},
		}},
	})
	if err != nil {
		t.Fatalf("move baseline Session: %v", err)
	}
	preview, err := rundownQueries.PublishPreview(
		t.Context(),
		producer,
		rundown.PublishPreviewInput{EventID: eventID, ChangeIDs: []int{edited.Changes[0].ID}},
	)
	if err != nil {
		t.Fatalf("preview moved Session: %v", err)
	}
	if _, publishErr := rundownCommands.Publish(t.Context(), producer, rundown.PublishInput{
		EventID: eventID, CommandID: "publish-moved-baseline-session",
		Confirmation: rundown.PublishConfirmation{
			DraftRevision: preview.DraftRevision, PublishedRevision: preview.PublishedRevision,
			ChangeIDs: preview.ChangeIDs, Fingerprint: preview.Fingerprint,
		},
	}); publishErr != nil {
		t.Fatalf("publish moved Session: %v", publishErr)
	}

	activationService, err := activation.New(storage, time.Now)
	if err != nil {
		t.Fatalf("create Activation service: %v", err)
	}
	preflight, err := activationService.Preflight(t.Context(), producer, eventID)
	if err != nil {
		t.Fatalf("Activation Preflight: %v", err)
	}
	if _, activateErr := activationService.Activate(t.Context(), producer, activation.ActivateInput{
		EventID: eventID, CommandID: "activate-baseline-event",
		Confirmation: preflight.Confirmation,
	}); activateErr != nil {
		t.Fatalf("activate baseline Event: %v", activateErr)
	}
	scheduleService, err := schedule.New(storage, func() time.Time {
		return time.Date(2026, 8, 21, 7, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("create Schedule service: %v", err)
	}
	_, session, found, err := scheduleService.Find(t.Context(), sessionID, "")
	if err != nil {
		t.Fatalf("find baseline Session: %v", err)
	}
	if !found || session.Was == nil {
		t.Fatalf("public Session = %+v, want immutable Was baseline", session)
	}
	if session.Was.Label != "Was" || session.Was.Display == session.Time.Start.Display {
		t.Fatalf("public Session Was = %+v, current = %+v", session.Was, session.Time.Start)
	}
}

func TestBaselineRequiresProducerAuthority(t *testing.T) {
	storage, producer, eventID := openBaselineTest(t)
	queries := newBaselineQueries(t, storage)
	commands := newBaselineCommands(t, storage)
	observer := producer
	observer.EventRoles[eventID] = viewer.Observer

	if _, err := queries.Preview(
		t.Context(),
		observer,
		eventID,
	); !errors.Is(err, schedulebaseline.ErrProducerRequired) {
		t.Fatalf("Observer Preview error = %v, want %v", err, schedulebaseline.ErrProducerRequired)
	}
	if _, err := commands.Capture(t.Context(), observer, schedulebaseline.CaptureInput{
		EventID: eventID, CommandID: "observer-capture",
	}); !errors.Is(err, schedulebaseline.ErrProducerRequired) {
		t.Fatalf("Observer capture error = %v, want %v", err, schedulebaseline.ErrProducerRequired)
	}
}

func openBaselineTest(t *testing.T) (*store.SQLite, auth.Account, int) {
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
	now := time.Date(2026, 7, 23, 18, 0, 0, 0, time.UTC)
	bootstrapHash := strings.Repeat("b", 64)
	if issueErr := storage.IssueBootstrap(
		t.Context(),
		bootstrapHash,
		now,
		now.Add(time.Hour),
	); issueErr != nil {
		t.Fatalf("issue bootstrap: %v", issueErr)
	}
	created, err := storage.BootstrapAdministrator(
		t.Context(),
		store.BootstrapAdministratorParams{
			BootstrapHash: bootstrapHash, Name: "Producer", NormalizedName: "producer",
			PasswordHash: "test-password-hash", SessionHash: strings.Repeat("s", 64),
			Now: now, SessionExpiry: now.Add(time.Hour),
		},
	)
	if err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	administrator := auth.Account{ID: created.ID, Name: created.Name, Administrator: true}
	eventService, err := events.New(storage, func() time.Time { return now })
	if err != nil {
		t.Fatalf("create Event service: %v", err)
	}
	event, err := eventService.Create(t.Context(), administrator, events.CreateInput{
		Name: "Baseline Event", PlannedStartDate: "2026-08-21", PlannedEndDate: "2026-08-23",
		Timezone: "Europe/Berlin", EventLocale: "de-DE", EventDayBoundary: "06:00",
		CommandID: "create-baseline-event",
	})
	if err != nil {
		t.Fatalf("create Event: %v", err)
	}
	if _, err := eventService.GrantEventAccess(
		t.Context(),
		administrator,
		event.ID,
		administrator.ID,
		"Producer",
		"grant-baseline-producer",
	); err != nil {
		t.Fatalf("grant Producer: %v", err)
	}
	administrator.EventRoles = map[int]viewer.Role{event.ID: viewer.Producer}
	return storage, administrator, event.ID
}

func publishBaselineTestRundown(
	t *testing.T,
	storage *store.SQLite,
	producer auth.Account,
	eventID int,
) (int, rundown.PublishResult) {
	t.Helper()
	now := func() time.Time {
		return time.Date(2026, 7, 23, 18, 0, 0, 0, time.UTC)
	}
	commands, err := rundown.NewCommands(storage, now)
	if err != nil {
		t.Fatalf("create Rundown Commands: %v", err)
	}
	queries, err := rundown.NewQueries(storage)
	if err != nil {
		t.Fatalf("create Rundown Queries: %v", err)
	}
	plannedStart := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	edited, err := commands.EditDraft(t.Context(), producer, rundown.EditDraftInput{
		EventID: eventID, CommandID: "draft-baseline-rundown", ExpectedDraftRevision: 0,
		Locations: []rundown.LocationDraftInput{{Ref: "main", Name: "Main Hall"}},
		Lanes: []rundown.LaneDraftInput{{
			Ref: "main-lane", Name: "Main Lane", Location: rundown.TargetRef{Ref: "main"},
		}},
		Sessions: []rundown.SessionDraftInput{{
			Ref: "opening", Title: "Opening Session", Type: rundown.SessionCeremony,
			AudienceVisibility: rundown.AudiencePublic,
			PlannedStart:       plannedStart, PlannedEnd: plannedStart.Add(time.Hour),
			TimingPolicy: rundown.TimingFixedEnd, MinimumDuration: 30 * time.Minute,
			StartBoundary: rundown.BoundaryHard, EndBoundary: rundown.BoundarySoft,
			Lanes:     []rundown.TargetRef{{Ref: "main-lane"}},
			Locations: []rundown.TargetRef{{Ref: "main"}},
		}},
	})
	if err != nil {
		t.Fatalf("Edit Draft: %v", err)
	}
	sessionChangeID := edited.Changes[len(edited.Changes)-1].ID
	preview, err := queries.PublishPreview(t.Context(), producer, rundown.PublishPreviewInput{
		EventID: eventID, ChangeIDs: []int{sessionChangeID},
	})
	if err != nil {
		t.Fatalf("Publish Preview: %v", err)
	}
	published, err := commands.Publish(t.Context(), producer, rundown.PublishInput{
		EventID: eventID, CommandID: "publish-baseline-rundown",
		Confirmation: rundown.PublishConfirmation{
			DraftRevision: preview.DraftRevision, PublishedRevision: preview.PublishedRevision,
			ChangeIDs: preview.ChangeIDs, Fingerprint: preview.Fingerprint,
		},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	return edited.Changes[len(edited.Changes)-1].TargetID, published
}

func newBaselineQueries(t *testing.T, storage *store.SQLite) *schedulebaseline.Queries {
	t.Helper()
	queries, err := schedulebaseline.NewQueries(storage)
	if err != nil {
		t.Fatalf("create baseline Queries: %v", err)
	}
	return queries
}

func newBaselineCommands(t *testing.T, storage *store.SQLite) *schedulebaseline.Commands {
	t.Helper()
	commands, err := schedulebaseline.NewCommands(storage, func() time.Time {
		return time.Date(2026, 7, 23, 19, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("create baseline Commands: %v", err)
	}
	return commands
}
