package rundown_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "github.com/dotwaffle/beamers/ent/runtime"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestEditDraftCreatesMinimalRundownAtomically(t *testing.T) {
	storage, actor, eventID := openRundownTest(t)
	commands, err := rundown.NewCommands(storage, func() time.Time {
		return time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("create Rundown Commands: %v", err)
	}
	plannedStart := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	input := rundown.EditDraftInput{
		EventID:               eventID,
		CommandID:             "draft-minimal-1",
		ExpectedDraftRevision: 0,
		Locations:             []rundown.LocationDraftInput{{Ref: "main", Name: "Main Hall"}},
		Lanes: []rundown.LaneDraftInput{{
			Ref: "main-lane", Name: "Main Lane", Location: rundown.TargetRef{Ref: "main"},
		}},
		Tracks: []rundown.TrackDraftInput{{Ref: "systems", Name: "Systems"}},
		Sessions: []rundown.SessionDraftInput{{
			Ref: "opening", Title: "Opening Session", Type: rundown.SessionCeremony,
			AudienceVisibility: rundown.AudiencePublic,
			PlannedStart:       plannedStart, PlannedEnd: plannedStart.Add(time.Hour),
			TimingPolicy: rundown.TimingFixedEnd, MinimumDuration: 30 * time.Minute,
			StartBoundary: rundown.BoundaryHard, EndBoundary: rundown.BoundarySoft,
			Lanes:     []rundown.TargetRef{{Ref: "main-lane"}},
			Locations: []rundown.TargetRef{{Ref: "main"}},
			Tracks:    []rundown.TargetRef{{Ref: "systems"}},
		}},
	}

	created, err := commands.EditDraft(t.Context(), actor, input)
	if err != nil {
		t.Fatalf("Edit Draft: %v", err)
	}
	if created.DraftRevision != 1 || len(created.Changes) != 4 {
		t.Fatalf("Edit Draft result = %+v, want revision 1 and four changes", created)
	}
	for _, change := range created.Changes {
		if change.ID <= 0 || change.TargetID <= 0 {
			t.Errorf("Draft Change = %+v, want durable identities", change)
		}
	}

	revokedActor := actor
	revokedActor.EventRoles = nil
	replayed, err := commands.EditDraft(t.Context(), revokedActor, input)
	if err != nil {
		t.Fatalf("retry Edit Draft: %v", err)
	}
	if replayed.DraftRevision != created.DraftRevision || len(replayed.Changes) != len(created.Changes) {
		t.Fatalf("replayed result = %+v, want %+v", replayed, created)
	}
}

func TestEditDraftRejectionIsAtomicAndReplayable(t *testing.T) {
	storage, actor, eventID := openRundownTest(t)
	commands, err := rundown.NewCommands(storage, time.Now)
	if err != nil {
		t.Fatalf("create Rundown Commands: %v", err)
	}
	invalid := rundown.EditDraftInput{
		EventID: eventID, CommandID: "invalid-draft", ExpectedDraftRevision: 0,
		Sessions: []rundown.SessionDraftInput{{Ref: "orphan", Title: "Orphan"}},
	}
	if _, commandErr := commands.EditDraft(t.Context(), actor, invalid); commandErr == nil {
		t.Fatal("invalid Edit Draft succeeded")
	}
	revokedActor := actor
	revokedActor.EventRoles = nil
	if _, commandErr := commands.EditDraft(t.Context(), revokedActor, invalid); commandErr == nil {
		t.Fatal("exact rejected retry succeeded")
	} else {
		var validation *rundown.ValidationError
		if !errors.As(commandErr, &validation) {
			t.Fatalf("exact rejected retry error = %v, want ValidationError", commandErr)
		}
	}
	conflict := invalid
	conflict.Sessions = nil
	conflict.Locations = []rundown.LocationDraftInput{{Ref: "main", Name: "Main Hall"}}
	if _, commandErr := commands.EditDraft(t.Context(), actor, conflict); !errors.Is(commandErr, rundown.ErrCommandConflict) {
		t.Fatalf("conflicting Command ID error = %v, want %v", commandErr, rundown.ErrCommandConflict)
	}
	invalidReference := rundown.EditDraftInput{
		EventID: eventID, CommandID: "invalid-reference", ExpectedDraftRevision: 0,
		Lanes: []rundown.LaneDraftInput{{
			Ref: "missing-location-lane", Name: "Missing Location Lane",
			Location: rundown.TargetRef{ID: 999999},
		}},
	}
	if _, commandErr := commands.EditDraft(t.Context(), actor, invalidReference); commandErr == nil {
		t.Fatal("Edit Draft with unknown stable reference succeeded")
	} else {
		var validation *rundown.ValidationError
		if !errors.As(commandErr, &validation) || validation.Field != "references" {
			t.Fatalf("unknown stable reference error = %v, want references ValidationError", commandErr)
		}
	}
	conflict.CommandID = "valid-after-rejection"
	created, err := commands.EditDraft(t.Context(), actor, conflict)
	if err != nil {
		t.Fatalf("valid Edit Draft after rejection: %v", err)
	}
	if created.DraftRevision != 1 {
		t.Fatalf("Draft revision = %d, want 1 after atomic rejection", created.DraftRevision)
	}
	stale := rundown.EditDraftInput{
		EventID: eventID, CommandID: "stale-draft", ExpectedDraftRevision: 0,
		Locations: []rundown.LocationDraftInput{{Ref: "late", Name: "Late Hall"}},
	}
	if _, commandErr := commands.EditDraft(t.Context(), actor, stale); !errors.Is(commandErr, rundown.ErrDraftRevisionConflict) {
		t.Fatalf("stale Edit Draft error = %v, want %v", commandErr, rundown.ErrDraftRevisionConflict)
	}
	if _, replayErr := commands.EditDraft(t.Context(), revokedActor, stale); !errors.Is(replayErr, rundown.ErrDraftRevisionConflict) {
		t.Fatalf("stale Edit Draft retry error = %v, want %v", replayErr, rundown.ErrDraftRevisionConflict)
	}
}

func TestPublishClosesDependenciesAndExposesCrewRundown(t *testing.T) {
	storage, actor, eventID := openRundownTest(t)
	now := func() time.Time { return time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC) }
	commands, err := rundown.NewCommands(storage, now)
	if err != nil {
		t.Fatalf("create Rundown Commands: %v", err)
	}
	queries, err := rundown.NewQueries(storage)
	if err != nil {
		t.Fatalf("create Rundown Queries: %v", err)
	}
	plannedStart := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	edited, err := commands.EditDraft(t.Context(), actor, rundown.EditDraftInput{
		EventID: eventID, CommandID: "draft-for-publish", ExpectedDraftRevision: 0,
		Locations: []rundown.LocationDraftInput{{Ref: "main", Name: "Main Hall"}},
		Lanes: []rundown.LaneDraftInput{{
			Ref: "main-lane", Name: "Main Lane", Location: rundown.TargetRef{Ref: "main"},
		}},
		Tracks: []rundown.TrackDraftInput{{Ref: "systems", Name: "Systems"}},
		Sessions: []rundown.SessionDraftInput{{
			Ref: "opening", Title: "Opening Session", Type: rundown.SessionCeremony,
			AudienceVisibility: rundown.AudiencePublic,
			PlannedStart:       plannedStart, PlannedEnd: plannedStart.Add(time.Hour),
			TimingPolicy: rundown.TimingFixedEnd, MinimumDuration: 30 * time.Minute,
			StartBoundary: rundown.BoundaryHard, EndBoundary: rundown.BoundarySoft,
			Lanes:     []rundown.TargetRef{{Ref: "main-lane"}},
			Locations: []rundown.TargetRef{{Ref: "main"}},
			Tracks:    []rundown.TargetRef{{Ref: "systems"}},
		}},
	})
	if err != nil {
		t.Fatalf("Edit Draft: %v", err)
	}
	sessionChangeID := edited.Changes[len(edited.Changes)-1].ID
	preview, err := queries.PublishPreview(t.Context(), actor, rundown.PublishPreviewInput{
		EventID: eventID, ChangeIDs: []int{sessionChangeID},
	})
	if err != nil {
		t.Fatalf("Publish Preview: %v", err)
	}
	if len(preview.ChangeIDs) != 4 || len(preview.AutoIncludedChangeIDs) != 3 {
		t.Fatalf("Publish Preview = %+v, want four changes with three auto-included dependencies", preview)
	}
	published, err := commands.Publish(t.Context(), actor, rundown.PublishInput{
		EventID:   eventID,
		CommandID: "publish-minimal-1",
		Confirmation: rundown.PublishConfirmation{
			DraftRevision: preview.DraftRevision, PublishedRevision: preview.PublishedRevision,
			ChangeIDs: preview.ChangeIDs, Fingerprint: preview.Fingerprint,
		},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if published.PublishedRevision != 1 || len(published.ChangeIDs) != 4 {
		t.Fatalf("Publish result = %+v, want Published revision 1 and four changes", published)
	}
	crew, err := queries.CrewRundown(t.Context(), actor, eventID)
	if err != nil {
		t.Fatalf("Crew Rundown: %v", err)
	}
	if crew.PublishedRevision != 1 || len(crew.Locations) != 1 || len(crew.Lanes) != 1 ||
		len(crew.Tracks) != 1 || len(crew.Sessions) != 1 {
		t.Fatalf("Crew Rundown = %+v, want one complete Published Session structure", crew)
	}
	if crew.Sessions[0].Title != "Opening Session" || crew.Sessions[0].Type != rundown.SessionCeremony {
		t.Fatalf("Published Session = %+v, want Opening Session Ceremony", crew.Sessions[0])
	}
}

func TestPublishRejectsStalePreviewAndLeavesSelectionEffective(t *testing.T) {
	storage, actor, eventID := openRundownTest(t)
	commands, err := rundown.NewCommands(storage, time.Now)
	if err != nil {
		t.Fatalf("create Rundown Commands: %v", err)
	}
	queries, err := rundown.NewQueries(storage)
	if err != nil {
		t.Fatalf("create Rundown Queries: %v", err)
	}
	first, err := commands.EditDraft(t.Context(), actor, rundown.EditDraftInput{
		EventID: eventID, CommandID: "first-location", ExpectedDraftRevision: 0,
		Locations: []rundown.LocationDraftInput{{Ref: "first", Name: "First Hall"}},
	})
	if err != nil {
		t.Fatalf("first Edit Draft: %v", err)
	}
	preview, err := queries.PublishPreview(t.Context(), actor, rundown.PublishPreviewInput{
		EventID: eventID, ChangeIDs: []int{first.Changes[0].ID},
	})
	if err != nil {
		t.Fatalf("Publish Preview: %v", err)
	}
	if _, editErr := commands.EditDraft(t.Context(), actor, rundown.EditDraftInput{
		EventID: eventID, CommandID: "second-location", ExpectedDraftRevision: 1,
		Locations: []rundown.LocationDraftInput{{Ref: "second", Name: "Second Hall"}},
	}); editErr != nil {
		t.Fatalf("second Edit Draft: %v", editErr)
	}
	publishInput := rundown.PublishInput{
		EventID: eventID, CommandID: "stale-publish",
		Confirmation: rundown.PublishConfirmation{
			DraftRevision: preview.DraftRevision, PublishedRevision: preview.PublishedRevision,
			ChangeIDs: preview.ChangeIDs, Fingerprint: preview.Fingerprint,
		},
	}
	if _, publishErr := commands.Publish(t.Context(), actor, publishInput); !errors.Is(publishErr, rundown.ErrStalePreview) {
		t.Fatalf("stale Publish error = %v, want %v", publishErr, rundown.ErrStalePreview)
	}
	current, err := queries.PublishPreview(t.Context(), actor, rundown.PublishPreviewInput{EventID: eventID})
	if err != nil {
		t.Fatalf("current Publish Preview: %v", err)
	}
	if current.PublishedRevision != 0 || len(current.ChangeIDs) != 2 {
		t.Fatalf("current Publish Preview = %+v, want both changes still effective", current)
	}
}

func TestPublishLeavesUnselectedChangesInDraft(t *testing.T) {
	storage, actor, eventID := openRundownTest(t)
	commands, err := rundown.NewCommands(storage, time.Now)
	if err != nil {
		t.Fatalf("create Rundown Commands: %v", err)
	}
	queries, err := rundown.NewQueries(storage)
	if err != nil {
		t.Fatalf("create Rundown Queries: %v", err)
	}
	edited, err := commands.EditDraft(t.Context(), actor, rundown.EditDraftInput{
		EventID: eventID, CommandID: "two-locations", ExpectedDraftRevision: 0,
		Locations: []rundown.LocationDraftInput{
			{Ref: "first", Name: "First Hall"}, {Ref: "second", Name: "Second Hall"},
		},
	})
	if err != nil {
		t.Fatalf("Edit Draft: %v", err)
	}
	preview, err := queries.PublishPreview(t.Context(), actor, rundown.PublishPreviewInput{
		EventID: eventID, ChangeIDs: []int{edited.Changes[0].ID},
	})
	if err != nil {
		t.Fatalf("Publish Preview: %v", err)
	}
	published, err := commands.Publish(t.Context(), actor, rundown.PublishInput{
		EventID: eventID, CommandID: "publish-first-location",
		Confirmation: rundown.PublishConfirmation{
			DraftRevision: preview.DraftRevision, PublishedRevision: preview.PublishedRevision,
			ChangeIDs: preview.ChangeIDs, Fingerprint: preview.Fingerprint,
		},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	revokedActor := actor
	revokedActor.EventRoles = nil
	if replayed, replayErr := commands.Publish(t.Context(), revokedActor, rundown.PublishInput{
		EventID: eventID, CommandID: "publish-first-location",
		Confirmation: rundown.PublishConfirmation{
			DraftRevision: preview.DraftRevision, PublishedRevision: preview.PublishedRevision,
			ChangeIDs: preview.ChangeIDs, Fingerprint: preview.Fingerprint,
		},
	}); replayErr != nil || replayed.PublishedRevision != published.PublishedRevision {
		t.Fatalf("exact Publish retry after revoked authority = %+v, %v", replayed, replayErr)
	}
	crew, err := queries.CrewRundown(t.Context(), actor, eventID)
	if err != nil {
		t.Fatalf("Crew Rundown: %v", err)
	}
	if len(crew.Locations) != 1 || crew.Locations[0].Name != "First Hall" {
		t.Fatalf("Crew Locations = %+v, want only First Hall", crew.Locations)
	}
	remaining, err := queries.PublishPreview(t.Context(), actor, rundown.PublishPreviewInput{EventID: eventID})
	if err != nil {
		t.Fatalf("remaining Publish Preview: %v", err)
	}
	if len(remaining.ChangeIDs) != 1 || remaining.ChangeIDs[0] != edited.Changes[1].ID {
		t.Fatalf("remaining changes = %v, want [%d]", remaining.ChangeIDs, edited.Changes[1].ID)
	}
}

func TestEditDraftAcceptsEverySessionTypeAndDefaultsLocations(t *testing.T) {
	storage, actor, eventID := openRundownTest(t)
	commands, err := rundown.NewCommands(storage, time.Now)
	if err != nil {
		t.Fatalf("create Rundown Commands: %v", err)
	}
	types := []rundown.SessionType{
		rundown.SessionPresentation, rundown.SessionCompetition, rundown.SessionBreak,
		rundown.SessionActivity, rundown.SessionCeremony, rundown.SessionPerformance, rundown.SessionHold,
	}
	start := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	input := rundown.EditDraftInput{
		EventID: eventID, CommandID: "all-session-types", ExpectedDraftRevision: 0,
		Locations: []rundown.LocationDraftInput{{Ref: "main", Name: "Main Hall"}},
		Lanes: []rundown.LaneDraftInput{{
			Ref: "main-lane", Name: "Main Lane", Location: rundown.TargetRef{Ref: "main"},
		}},
	}
	for index, sessionType := range types {
		input.Sessions = append(input.Sessions, rundown.SessionDraftInput{
			Ref: fmt.Sprintf("session-%d", index), Title: string(sessionType), Type: sessionType,
			AudienceVisibility: rundown.AudienceCrewOnly,
			PlannedStart:       start.Add(time.Duration(index) * time.Hour),
			PlannedEnd:         start.Add(time.Duration(index+1) * time.Hour),
			TimingPolicy:       rundown.TimingFixedDuration, MinimumDuration: 30 * time.Minute,
			StartBoundary: rundown.BoundarySoft, EndBoundary: rundown.BoundarySoft,
			Lanes: []rundown.TargetRef{{Ref: "main-lane"}},
		})
	}
	created, err := commands.EditDraft(t.Context(), actor, input)
	if err != nil {
		t.Fatalf("Edit Draft all Session types: %v", err)
	}
	if len(created.Changes) != 2+len(types) {
		t.Fatalf("Draft Changes = %d, want %d", len(created.Changes), 2+len(types))
	}
}

func openRundownTest(t *testing.T) (*store.SQLite, auth.Account, int) {
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
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	bootstrapHash := strings.Repeat("b", 64)
	if issueErr := storage.IssueBootstrap(t.Context(), bootstrapHash, now, now.Add(time.Hour)); issueErr != nil {
		t.Fatalf("issue bootstrap: %v", issueErr)
	}
	created, err := storage.BootstrapAdministrator(t.Context(), store.BootstrapAdministratorParams{
		BootstrapHash: bootstrapHash,
		Name:          "Producer", NormalizedName: "producer", PasswordHash: "test-password-hash",
		SessionHash: strings.Repeat("s", 64), Now: now, SessionExpiry: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	administrator := auth.Account{ID: created.ID, Name: created.Name, Administrator: true}
	eventService, err := events.New(storage, func() time.Time { return now })
	if err != nil {
		t.Fatalf("create Event service: %v", err)
	}
	event, err := eventService.Create(t.Context(), administrator, events.CreateInput{
		Name: "Revision 2026", PlannedStartDate: "2026-08-21", PlannedEndDate: "2026-08-23",
		Timezone: "Europe/Berlin", EventLocale: "de-DE", EventDayBoundary: "06:00",
		CommandID: "create-event-for-rundown",
	})
	if err != nil {
		t.Fatalf("create Event: %v", err)
	}
	if _, err := eventService.GrantProducer(
		t.Context(), administrator, event.ID, administrator.ID, "Producer", "grant-rundown-producer",
	); err != nil {
		t.Fatalf("grant Producer: %v", err)
	}
	administrator.EventRoles = map[int]viewer.Role{event.ID: viewer.Producer}
	return storage, administrator, event.ID
}
