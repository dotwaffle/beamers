package store

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/ent/competitionentry"
	"github.com/dotwaffle/beamers/ent/session"
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

func TestTakeCompetitionEntrySlideLocksRunOrderAndPresentation(t *testing.T) {
	client := openEntTestClient(t)
	installation := &SQLite{client: client}
	fixtureContext := systemContext(t.Context())
	event := createSchemaTestEvent(t, client)
	competition := client.Session.Create().
		SetEventID(event.ID).
		SetLifecycle(session.LifecycleLive).
		SetEntryOrderPolicy(session.EntryOrderPolicyManualOrder).
		SetEntryOrderSeed(41).
		SetEntryOrderRevision(3).
		SaveX(fixtureContext)
	client.SessionPublishedVersion.Create().
		SetSessionID(competition.ID).
		SetPublishedRevision(1).
		SetTitle("Demo Competition").
		SetType(sessionpublishedversion.TypeCompetition).
		SetAudienceVisibility(sessionpublishedversion.AudienceVisibilityPublic).
		SetPlannedStart(time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)).
		SetPlannedEnd(time.Date(2026, 8, 21, 13, 0, 0, 0, time.UTC)).
		SetTimingPolicy(sessionpublishedversion.TimingPolicyFixedEnd).
		SetMinimumDurationSeconds(1800).
		SetStartBoundary(sessionpublishedversion.StartBoundaryHard).
		SetEndBoundary(sessionpublishedversion.EndBoundaryHard).
		SetSubmissionDeadline(time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)).
		SaveX(fixtureContext)
	first := client.CompetitionEntry.Create().
		SetEventID(event.ID).
		SetCompetitionSessionID(competition.ID).
		SetName("First").
		SetDisposition(competitionentry.DispositionIncluded).
		SaveX(fixtureContext)
	second := client.CompetitionEntry.Create().
		SetEventID(event.ID).
		SetCompetitionSessionID(competition.ID).
		SetName("Second").
		SetDisposition(competitionentry.DispositionIncluded).
		SaveX(fixtureContext)
	manualOrder := []int{second.ID, first.ID}
	competition.Update().SetEntryOrderManualIds(manualOrder).SaveX(fixtureContext)
	run := client.SessionRun.Create().
		SetSessionID(competition.ID).
		SetActualStart(time.Date(2026, 8, 21, 12, 1, 0, 0, time.UTC)).
		SetSnapshotJSON(`{"type":"Competition"}`).
		SaveX(fixtureContext)
	producerContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID: 1, EventRoles: map[int]viewer.Role{event.ID: viewer.Producer},
	})
	preview, fingerprint, err := installation.LoadCompetitionEntryOrder(
		producerContext, event.ID, competition.ID,
	)
	if err != nil {
		t.Fatalf("load Entry Order preview: %v", err)
	}
	now := time.Date(2026, 8, 21, 12, 2, 0, 0, time.UTC)
	take, err := installation.BeginCommand(producerContext)
	if err != nil {
		t.Fatalf("begin first Entry Slide Take: %v", err)
	}
	locked, err := take.TakeCompetitionEntrySlide(producerContext, TakeEntrySlideParams{
		EventID: event.ID, SessionID: competition.ID, EntryID: second.ID,
		ExpectedRevision: preview.Revision, PreviewFingerprint: fingerprint, Now: now,
	})
	if err != nil {
		t.Fatalf("take first Entry Slide: %v", err)
	}
	if err = take.Commit(); err != nil {
		t.Fatalf("commit first Entry Slide Take: %v", err)
	}
	if !locked.Locked || locked.Revision != 4 || !slices.Equal(locked.EntryIDs, manualOrder) {
		t.Fatalf("Locked Entry Order = %+v, want revision 4 and %v", locked, manualOrder)
	}
	storedRun := client.SessionRun.GetX(fixtureContext, run.ID)
	if !slices.Equal(storedRun.LockedEntryOrderIds, manualOrder) {
		t.Fatalf("Run locked Entry order = %v, want %v", storedRun.LockedEntryOrderIds, manualOrder)
	}
	presented := client.CompetitionEntry.GetX(fixtureContext, second.ID)
	if !presented.FirstPresentedAt.Equal(now) {
		t.Fatalf("first presented at = %v, want %v", presented.FirstPresentedAt, now)
	}
	lockedPreview, lockedFingerprint, err := installation.LoadCompetitionEntryOrder(
		producerContext, event.ID, competition.ID,
	)
	if err != nil {
		t.Fatalf("load Locked Entry Order preview: %v", err)
	}
	secondTake, err := installation.BeginCommand(producerContext)
	if err != nil {
		t.Fatalf("begin second Entry Slide Take: %v", err)
	}
	if _, err = secondTake.TakeCompetitionEntrySlide(producerContext, TakeEntrySlideParams{
		EventID: event.ID, SessionID: competition.ID, EntryID: first.ID,
		ExpectedRevision: lockedPreview.Revision, PreviewFingerprint: lockedFingerprint,
		Now: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("take second Entry Slide: %v", err)
	}
	if err = secondTake.Commit(); err != nil {
		t.Fatalf("commit second Entry Slide Take: %v", err)
	}
	first = client.CompetitionEntry.GetX(fixtureContext, first.ID)
	disposition, err := installation.BeginCommand(producerContext)
	if err != nil {
		t.Fatalf("begin presented Entry disposition: %v", err)
	}
	_, err = disposition.ChangeCompetitionEntryDisposition(
		producerContext,
		ChangeCompetitionEntryDispositionParams{
			EventID: event.ID, SessionID: competition.ID, EntryID: first.ID,
			ExpectedRevision: first.Revision, Disposition: "Rejected",
			ConfirmedLive: true, Now: now.Add(2 * time.Minute),
		},
	)
	if !errors.Is(err, ErrPresentedEntryDisposition) {
		t.Fatalf("presented Entry disposition error = %v, want %v", err, ErrPresentedEntryDisposition)
	}
	if err = disposition.Rollback(); err != nil {
		t.Fatalf("roll back presented Entry disposition: %v", err)
	}
	history, err := installation.LoadSessionHistory(producerContext, event.ID, competition.ID)
	if err != nil || len(history.Runs) != 1 ||
		!slices.Equal(history.Runs[0].Snapshot.LockedEntryOrderIDs, manualOrder) {
		t.Fatalf("Run Snapshot locked Entry order = %+v, %v", history, err)
	}
}
