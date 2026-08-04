package presentation

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/dotwaffle/beamers/ent/runtime"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/systemactor"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestPresentationCommandsAuditAssignmentReplacementAndOwnerUpdates(t *testing.T) {
	storage, producer, eventID := openPresentationTest(t)
	first := createPresentationTestAccount(t, storage, producer, "alice", "Alice")
	second := createPresentationTestAccount(t, storage, producer, "blair", "Blair")
	sessionID := publishPresentationTestSession(t, storage, producer, eventID)
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	var displayNotifications, scheduleNotifications int
	service, err := New(
		storage,
		func() time.Time { return now },
		func() { displayNotifications++ },
		func() { scheduleNotifications++ },
	)
	if err != nil {
		t.Fatalf("create Presentation service: %v", err)
	}

	denied := AssignSubmitterInput{
		EventID: eventID, SessionID: sessionID, AccountID: first.ID,
		ExpectedRevision: 0, CommandID: "assign-without-producer",
	}
	if _, err = service.AssignSubmitter(
		t.Context(),
		auth.Account{ID: producer.ID},
		denied,
	); !errors.Is(err, ErrProducerRequired) {
		t.Fatalf("unauthorized assignment error = %v", err)
	}
	if _, err = service.AssignSubmitter(
		t.Context(),
		producer,
		denied,
	); !errors.Is(err, ErrProducerRequired) {
		t.Fatalf("unauthorized assignment replay error = %v", err)
	}

	assigned, err := service.AssignSubmitter(t.Context(), producer, AssignSubmitterInput{
		EventID: eventID, SessionID: sessionID, AccountID: first.ID,
		ExpectedRevision: 0, CommandID: "assign-first-speaker",
	})
	if err != nil || assigned.SubmitterAccountID != first.ID || assigned.Revision != 1 {
		t.Fatalf("assign first Submitter = %+v, %v", assigned, err)
	}
	replaced, err := service.AssignSubmitter(t.Context(), producer, AssignSubmitterInput{
		EventID: eventID, SessionID: sessionID, AccountID: second.ID,
		ExpectedRevision: assigned.Revision, CommandID: "replace-speaker",
	})
	if err != nil || replaced.SubmitterAccountID != second.ID || replaced.Revision != 2 {
		t.Fatalf("replace Submitter = %+v, %v", replaced, err)
	}
	if _, err = service.UpdateSubmission(t.Context(), first, UpdateSubmissionInput{
		EventID: eventID, SessionID: sessionID, ExpectedRevision: replaced.Revision,
		Speaker: "Wrong owner", CommandID: "wrong-owner-update",
	}); !errors.Is(err, ErrSubmitterRequired) {
		t.Fatalf("replaced Submitter update error = %v", err)
	}
	update := UpdateSubmissionInput{
		EventID: eventID, SessionID: sessionID, ExpectedRevision: replaced.Revision,
		Speaker: "Blair Credit", PublicDetails: "Approved details",
		CommandID: "update-presentation-details",
	}
	updated, err := service.UpdateSubmission(t.Context(), second, update)
	if err != nil || updated.Revision != 3 || updated.Speaker != "Blair Credit" {
		t.Fatalf("update Presentation submission = %+v, %v", updated, err)
	}
	if displayNotifications != 1 || scheduleNotifications != 1 {
		t.Fatalf(
			"successful public update notifications = display %d, schedule %d",
			displayNotifications,
			scheduleNotifications,
		)
	}
	if _, err = service.UpdateSubmission(t.Context(), second, update); err != nil {
		t.Fatalf("replay Presentation submission update: %v", err)
	}
	if displayNotifications != 2 || scheduleNotifications != 2 {
		t.Fatalf(
			"replayed public update notifications = display %d, schedule %d",
			displayNotifications,
			scheduleNotifications,
		)
	}
	update.PublicDetails = "Conflicting details"
	if _, err = service.UpdateSubmission(
		t.Context(),
		second,
		update,
	); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("conflicting Presentation submission update error = %v", err)
	}
	if displayNotifications != 2 || scheduleNotifications != 2 {
		t.Fatalf(
			"rejected update notifications = display %d, schedule %d",
			displayNotifications,
			scheduleNotifications,
		)
	}
	submissions, err := service.Submissions(t.Context(), second)
	if err != nil || len(submissions) != 1 ||
		submissions[0].SessionID != sessionID ||
		submissions[0].PublicDetails != "Approved details" {
		t.Fatalf("Account Presentation submissions = %+v, %v", submissions, err)
	}

	audit, err := storage.ListAuditEntries(producer.Context(t.Context()))
	if err != nil {
		t.Fatalf("list Presentation Audit Entries: %v", err)
	}
	var rejected, assignedAudit, replacedAudit, updatedAudit bool
	for _, entry := range audit {
		switch {
		case entry.Action == "AssignPresentationSubmitter" &&
			entry.Outcome == "Rejected" &&
			entry.Reason == "producer_required":
			rejected = true
		case entry.Action == "AssignPresentationSubmitter" &&
			entry.Outcome == "Succeeded" &&
			entry.TargetID == strconv.Itoa(sessionID):
			if assignedAudit {
				replacedAudit = true
			} else {
				assignedAudit = true
			}
		case entry.Action == "UpdatePresentationSubmission" &&
			entry.Outcome == "Succeeded":
			updatedAudit = true
		}
	}
	if !rejected || !assignedAudit || !replacedAudit || !updatedAudit {
		t.Fatalf("Presentation Audit coverage = %+v", audit)
	}
}

func openPresentationTest(t *testing.T) (*store.SQLite, auth.Account, int) {
	t.Helper()
	dataDir := t.TempDir()
	if err := store.Initialize(systemactor.NewContext(t.Context(), systemactor.HostMaintenance), dataDir); err != nil {
		t.Fatalf("initialize Presentation storage: %v", err)
	}
	storage, err := store.Open(systemactor.NewContext(t.Context(), systemactor.HostMaintenance), dataDir)
	if err != nil {
		t.Fatalf("open Presentation storage: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := storage.Close(); closeErr != nil {
			t.Errorf("close Presentation storage: %v", closeErr)
		}
	})
	now := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	bootstrapHash := strings.Repeat("b", 64)
	if err = storage.IssueBootstrap(systemactor.NewContext(t.Context(), systemactor.HostMaintenance), bootstrapHash, now, now.Add(time.Hour)); err != nil {
		t.Fatalf("issue Presentation bootstrap: %v", err)
	}
	created, err := storage.BootstrapAdministrator(
		systemactor.NewContext(t.Context(), systemactor.HostMaintenance),
		store.BootstrapAdministratorParams{
			BootstrapHash: bootstrapHash, Name: "Administrator",
			NormalizedName: "administrator", PasswordHash: "password-hash",
			SessionHash: strings.Repeat("s", 64), Now: now, SessionExpiry: now.Add(time.Hour),
		},
	)
	if err != nil {
		t.Fatalf("bootstrap Presentation Administrator: %v", err)
	}
	producer := auth.Account{ID: created.ID, Name: created.Name, Administrator: true}
	eventService, err := events.New(storage, time.Now, nil, nil)
	if err != nil {
		t.Fatalf("create Event service: %v", err)
	}
	foundEvent, err := eventService.Create(t.Context(), producer, events.CreateInput{
		Name:             "Presentation Event",
		PlannedStartDate: "2026-08-21", PlannedEndDate: "2026-08-21",
		Timezone: "UTC", EventLocale: "en-GB", CommandID: "create-presentation-event",
	})
	if err != nil {
		t.Fatalf("create Presentation Event: %v", err)
	}
	if _, err = eventService.GrantEventAccess(
		t.Context(),
		producer,
		foundEvent.ID,
		producer.ID,
		"Producer",
		"grant-presentation-producer",
	); err != nil {
		t.Fatalf("grant Presentation Producer: %v", err)
	}
	producer.EventRoles = map[int]viewer.Role{foundEvent.ID: viewer.Producer}
	return storage, producer, foundEvent.ID
}

func createPresentationTestAccount(
	t *testing.T,
	storage *store.SQLite,
	producer auth.Account,
	handle, name string,
) auth.Account {
	t.Helper()
	transaction, err := storage.BeginCommand(producer.Context(t.Context()))
	if err != nil {
		t.Fatalf("begin Presentation Account creation: %v", err)
	}
	created, err := transaction.CreateAccount(
		producer.Context(t.Context()),
		store.CreateAccountParams{
			ActorAccountID: producer.ID, Name: name, NormalizedName: handle,
			PasswordHash: "password-hash", Now: time.Now(),
		},
	)
	if err != nil {
		_ = transaction.Rollback()
		t.Fatalf("create Presentation Account: %v", err)
	}
	if err = transaction.Commit(); err != nil {
		t.Fatalf("commit Presentation Account: %v", err)
	}
	return auth.Account{ID: created.ID, Handle: handle, Name: name}
}

func publishPresentationTestSession(
	t *testing.T,
	storage *store.SQLite,
	producer auth.Account,
	eventID int,
) int {
	t.Helper()
	commands, err := rundown.NewCommands(storage, time.Now, nil, nil)
	if err != nil {
		t.Fatalf("create Rundown commands: %v", err)
	}
	queries, err := rundown.NewQueries(storage, nil)
	if err != nil {
		t.Fatalf("create Rundown queries: %v", err)
	}
	start := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	edited, err := commands.EditDraft(t.Context(), producer, rundown.EditDraftInput{
		EventID: eventID, CommandID: "create-presentation-draft",
		Locations: []rundown.LocationDraftInput{{Ref: "hall", Name: "Hall"}},
		Lanes: []rundown.LaneDraftInput{{
			Ref: "lane", Name: "Lane", Location: rundown.TargetRef{Ref: "hall"},
		}},
		Sessions: []rundown.SessionDraftInput{{
			Ref: "presentation", Title: "Assigned Presentation", Speaker: "Crew Credit",
			Type: rundown.SessionPresentation, AudienceVisibility: rundown.AudiencePublic,
			PublicDetails: "Crew details", PlannedStart: start, PlannedEnd: start.Add(time.Hour),
			TimingPolicy: rundown.TimingFixedEnd, MinimumDuration: 30 * time.Minute,
			StartBoundary: rundown.BoundaryHard, EndBoundary: rundown.BoundarySoft,
			UploadDeadline: start.Add(-time.Hour),
			Lanes:          []rundown.TargetRef{{Ref: "lane"}},
			Locations:      []rundown.TargetRef{{Ref: "hall"}},
		}},
	})
	if err != nil {
		t.Fatalf("create Presentation Draft: %v", err)
	}
	changeIDs := make([]int, 0, len(edited.Changes))
	for _, change := range edited.Changes {
		changeIDs = append(changeIDs, change.ID)
	}
	preview, err := queries.PublishPreview(t.Context(), producer, rundown.PublishPreviewInput{
		EventID: eventID, ChangeIDs: changeIDs,
	})
	if err != nil {
		t.Fatalf("preview Presentation Publish: %v", err)
	}
	if _, err = commands.Publish(t.Context(), producer, rundown.PublishInput{
		EventID: eventID, CommandID: "publish-presentation",
		Confirmation: rundown.PublishConfirmation{
			DraftRevision: preview.DraftRevision, PublishedRevision: preview.PublishedRevision,
			ChangeIDs: preview.ChangeIDs, Fingerprint: preview.Fingerprint,
		},
	}); err != nil {
		t.Fatalf("publish Presentation: %v", err)
	}
	crew, err := queries.CrewRundown(t.Context(), producer, eventID)
	if err != nil || len(crew.Sessions) != 1 {
		t.Fatalf("load Presentation Session = %+v, %v", crew, err)
	}
	return crew.Sessions[0].ID
}
