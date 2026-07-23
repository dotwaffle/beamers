package store

import (
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestCompetitionDeadlineClosesEntryHistory(t *testing.T) {
	client := openEntTestClient(t)
	installation := &SQLite{client: client}
	fixtureContext := systemContext(t.Context())
	event := createSchemaTestEvent(t, client)
	event.Update().
		SetEntryDefaultDisposition("Included").
		SaveX(fixtureContext)
	competition := client.Session.Create().SetEventID(event.ID).SaveX(fixtureContext)
	deadline := time.Date(2026, 8, 21, 11, 30, 0, 0, time.UTC)
	client.SessionPublishedVersion.Create().
		SetSessionID(competition.ID).
		SetPublishedRevision(1).
		SetTitle("Demo Competition").
		SetType(sessionpublishedversion.TypeCompetition).
		SetAudienceVisibility(sessionpublishedversion.AudienceVisibilityPublic).
		SetPlannedStart(deadline.Add(30 * time.Minute)).
		SetPlannedEnd(deadline.Add(90 * time.Minute)).
		SetTimingPolicy(sessionpublishedversion.TimingPolicyFixedEnd).
		SetMinimumDurationSeconds(1800).
		SetStartBoundary(sessionpublishedversion.StartBoundaryHard).
		SetEndBoundary(sessionpublishedversion.EndBoundaryHard).
		SetSubmissionDeadline(deadline).
		SaveX(fixtureContext)
	producerContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID: 1, EventRoles: map[int]viewer.Role{event.ID: viewer.Producer},
	})
	beforeDeadline, err := installation.BeginCommand(producerContext)
	if err != nil {
		t.Fatalf("begin Entry creation: %v", err)
	}
	entry, err := beforeDeadline.CreateCompetitionEntry(producerContext, CreateCompetitionEntryParams{
		EventID: event.ID, SessionID: competition.ID, Name: "Aurora",
		PublicDetails: "Public", CrewNotes: "Crew", Now: deadline.Add(-time.Nanosecond),
	})
	if err != nil {
		t.Fatalf("create Entry before Deadline: %v", err)
	}
	if entry.Disposition != "Included" {
		t.Fatalf("Event-default Entry disposition = %q, want Included", entry.Disposition)
	}
	if commitErr := beforeDeadline.Commit(); commitErr != nil {
		t.Fatalf("commit Entry before Deadline: %v", commitErr)
	}

	atDeadline, err := installation.BeginCommand(producerContext)
	if err != nil {
		t.Fatalf("begin closed Entry commands: %v", err)
	}
	if _, createErr := atDeadline.CreateCompetitionEntry(producerContext, CreateCompetitionEntryParams{
		EventID: event.ID, SessionID: competition.ID, Name: "Late", Now: deadline,
	}); !errors.Is(createErr, ErrCompetitionSubmissionClosed) {
		t.Errorf("create at Deadline error = %v", createErr)
	}
	if _, updateErr := atDeadline.UpdateCompetitionEntry(producerContext, UpdateCompetitionEntryParams{
		EventID: event.ID, SessionID: competition.ID, EntryID: entry.ID,
		ExpectedRevision: entry.Revision, Name: "Changed", Now: deadline,
	}); !errors.Is(updateErr, ErrCompetitionSubmissionClosed) {
		t.Errorf("update at Deadline error = %v", updateErr)
	}
	if _, dispositionErr := atDeadline.ChangeCompetitionEntryDisposition(producerContext, ChangeCompetitionEntryDispositionParams{
		EventID: event.ID, SessionID: competition.ID, EntryID: entry.ID,
		ExpectedRevision: entry.Revision, Disposition: "Rejected", Now: deadline,
	}); !errors.Is(dispositionErr, ErrCompetitionSubmissionClosed) {
		t.Errorf("disposition at Deadline error = %v", dispositionErr)
	}
	if rollbackErr := atDeadline.Rollback(); rollbackErr != nil {
		t.Fatalf("rollback closed Entry commands: %v", rollbackErr)
	}
	retained, err := installation.LoadCompetition(producerContext, event.ID, competition.ID)
	if err != nil {
		t.Fatalf("load retained Competition history: %v", err)
	}
	if len(retained.Entries) != 1 || retained.Entries[0] != entry {
		t.Fatalf("retained Entries = %+v, want %+v", retained.Entries, entry)
	}
}
