package store

import (
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/ent/competitionentry"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestTakeProgramItemCommitsOutputAndEntryLockTogether(t *testing.T) {
	client := openEntTestClient(t)
	installation := &SQLite{client: client}
	fixtureContext := systemContext(t.Context())
	event := createSchemaTestEvent(t, client)
	client.Installation.Create().SetActiveEventID(event.ID).SaveX(fixtureContext)
	competition := client.Session.Create().
		SetEventID(event.ID).
		SetLifecycle(session.LifecycleLive).
		SetEntryOrderSeed(73).
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
	entry := client.CompetitionEntry.Create().
		SetEventID(event.ID).
		SetCompetitionSessionID(competition.ID).
		SetName("Aurora").
		SetDisposition(competitionentry.DispositionIncluded).
		SaveX(fixtureContext)
	run := client.SessionRun.Create().
		SetSessionID(competition.ID).
		SetActualStart(time.Date(2026, 8, 21, 12, 1, 0, 0, time.UTC)).
		SetSnapshotJSON(`{"type":"Competition"}`).
		SaveX(fixtureContext)
	producerContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID: 1, EventRoles: map[int]viewer.Role{event.ID: viewer.Producer},
	})
	channel, err := installation.LoadProgramChannel(producerContext, event.ID, competition.ID)
	if err != nil {
		t.Fatalf("load Program Channel: %v", err)
	}
	if len(channel.Items) != 5 || channel.Next.Kind != ProgramItemUpcoming {
		t.Fatalf("initial Program Channel = %+v", channel)
	}
	_, orderFingerprint, err := installation.LoadCompetitionEntryOrder(
		producerContext, event.ID, competition.ID,
	)
	if err != nil {
		t.Fatalf("load Entry Order preview: %v", err)
	}
	command, err := installation.BeginCommand(producerContext)
	if err != nil {
		t.Fatalf("begin Take: %v", err)
	}
	taken, err := command.TakeProgramItem(producerContext, TakeProgramItemParams{
		EventID: event.ID, SessionID: competition.ID,
		ExpectedRevision: 0, Item: ProgramItem{Kind: ProgramItemEntry, EntryID: entry.ID},
		ExpectedEntryOrderRevision: 0, EntryOrderFingerprint: orderFingerprint,
		Now: time.Date(2026, 8, 21, 12, 2, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Take Entry Program Item: %v", err)
	}
	if err = command.Commit(); err != nil {
		t.Fatalf("commit Take: %v", err)
	}
	if taken.Revision != 1 || taken.Output.Kind != ProgramItemEntry ||
		taken.Output.EntryID != entry.ID || taken.Next.Kind != ProgramItemUpcoming {
		t.Fatalf("Taken Program Channel = %+v", taken)
	}
	storedRun := client.SessionRun.GetX(fixtureContext, run.ID)
	if len(storedRun.LockedEntryOrderIds) != 1 || storedRun.LockedEntryOrderIds[0] != entry.ID {
		t.Fatalf("Run locked Entry order = %v, want [%d]", storedRun.LockedEntryOrderIds, entry.ID)
	}
	stale, err := installation.BeginCommand(producerContext)
	if err != nil {
		t.Fatalf("begin stale Take: %v", err)
	}
	_, err = stale.TakeProgramItem(producerContext, TakeProgramItemParams{
		EventID: event.ID, SessionID: competition.ID,
		ExpectedRevision: 0, Item: ProgramItem{Kind: ProgramItemEnding},
		Now: time.Date(2026, 8, 21, 12, 3, 0, 0, time.UTC),
	})
	if !errors.Is(err, ErrProgramRevision) {
		t.Fatalf("stale Take error = %v, want %v", err, ErrProgramRevision)
	}
	if err = stale.Rollback(); err != nil {
		t.Fatalf("roll back stale Take: %v", err)
	}
}
