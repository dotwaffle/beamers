package store

import (
	"testing"
	"time"

	"github.com/dotwaffle/beamers/ent/competitionentry"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestCompetitionResultsDraftRevisionsClearReady(t *testing.T) {
	client := openEntTestClient(t)
	installation := &SQLite{client: client}
	fixtureContext := systemContext(t.Context())
	event := createSchemaTestEvent(t, client)
	competition := client.Session.Create().
		SetEventID(event.ID).
		SetLifecycle(session.LifecycleEnded).
		SaveX(fixtureContext)
	client.SessionPublishedVersion.Create().
		SetSessionID(competition.ID).
		SetPublishedRevision(1).
		SetTitle("Results Competition").
		SetType(sessionpublishedversion.TypeCompetition).
		SetAudienceVisibility(sessionpublishedversion.AudienceVisibilityPublic).
		SetPlannedStart(time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)).
		SetPlannedEnd(time.Date(2026, 8, 21, 13, 0, 0, 0, time.UTC)).
		SetTimingPolicy(sessionpublishedversion.TimingPolicyFixedEnd).
		SetMinimumDurationSeconds(1800).
		SetStartBoundary(sessionpublishedversion.StartBoundaryHard).
		SetEndBoundary(sessionpublishedversion.EndBoundaryHard).
		SetSubmissionDeadline(time.Date(2026, 8, 21, 11, 30, 0, 0, time.UTC)).
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
	competition.Update().SetLockedEntryOrderIds([]int{first.ID, second.ID}).SaveX(fixtureContext)
	producerContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID:  7,
		EventRoles: map[int]viewer.Role{event.ID: viewer.Producer},
	})
	now := time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)

	firstTransaction, err := installation.BeginCommand(producerContext)
	if err != nil {
		t.Fatalf("begin first Results Draft: %v", err)
	}
	firstDraft, err := firstTransaction.SaveCompetitionResultsDraft(
		producerContext,
		SaveCompetitionResultsDraftParams{
			EventID: event.ID, SessionID: competition.ID, ExpectedRevision: 0,
			Disposition: "Publish", ScoreType: "None",
			CreatedByAccountID: 7, Now: now,
			Standings: []CompetitionResultStandingInput{
				{EntryID: first.ID, Standing: "Placed", Placement: 1, DisplayOrder: 1},
				{EntryID: second.ID, Standing: "Unplaced", DisplayOrder: 2},
			},
		},
	)
	if err != nil {
		t.Fatalf("save first Results Draft: %v", err)
	}
	if err = firstTransaction.Commit(); err != nil {
		t.Fatalf("commit first Results Draft: %v", err)
	}
	if firstDraft.Revision != 1 || firstDraft.Ready {
		t.Fatalf("first Results Draft = %+v", firstDraft)
	}

	readyTransaction, err := installation.BeginCommand(producerContext)
	if err != nil {
		t.Fatalf("begin Results Ready: %v", err)
	}
	ready, err := readyTransaction.MarkCompetitionResultsReady(
		producerContext,
		MarkCompetitionResultsReadyParams{
			EventID: event.ID, SessionID: competition.ID, ExpectedRevision: 1,
			ReviewedByAccountID: 7, Now: now.Add(time.Minute),
		},
	)
	if err != nil {
		t.Fatalf("mark Results Ready: %v", err)
	}
	if err = readyTransaction.Commit(); err != nil {
		t.Fatalf("commit Results Ready: %v", err)
	}
	if !ready.Ready || ready.Revision != 1 {
		t.Fatalf("Ready Results Draft = %+v", ready)
	}

	secondTransaction, err := installation.BeginCommand(producerContext)
	if err != nil {
		t.Fatalf("begin second Results Draft: %v", err)
	}
	secondDraft, err := secondTransaction.SaveCompetitionResultsDraft(
		producerContext,
		SaveCompetitionResultsDraftParams{
			EventID: event.ID, SessionID: competition.ID, ExpectedRevision: 1,
			Disposition: "Publish", ScoreType: "None",
			CreatedByAccountID: 7, Now: now.Add(2 * time.Minute),
			Standings: []CompetitionResultStandingInput{
				{EntryID: first.ID, Standing: "Unplaced", DisplayOrder: 1},
				{EntryID: second.ID, Standing: "Placed", Placement: 1, DisplayOrder: 2},
			},
		},
	)
	if err != nil {
		t.Fatalf("save second Results Draft: %v", err)
	}
	if err = secondTransaction.Commit(); err != nil {
		t.Fatalf("commit second Results Draft: %v", err)
	}
	if secondDraft.Revision != 2 || secondDraft.Ready {
		t.Fatalf("second Results Draft = %+v", secondDraft)
	}
	current, err := installation.LoadCompetitionResultsDraft(
		producerContext,
		event.ID,
		competition.ID,
	)
	if err != nil || current.Revision != 2 || current.Ready {
		t.Fatalf("current Results Draft = %+v, %v", current, err)
	}

	observerContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID:  8,
		EventRoles: map[int]viewer.Role{event.ID: viewer.Observer},
	})
	if _, err = installation.LoadCompetitionResultsDraft(
		observerContext,
		event.ID,
		competition.ID,
	); err == nil {
		t.Fatal("Observer without Results Access read an unreleased Draft")
	}
	viewContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID:  9,
		EventRoles: map[int]viewer.Role{event.ID: viewer.Observer},
		EventScopes: map[int]viewer.EventScope{
			event.ID: {
				Capabilities: map[viewer.Capability]struct{}{viewer.ViewResults: {}},
			},
		},
	})
	visible, err := installation.LoadCompetitionResultsDraft(
		viewContext,
		event.ID,
		competition.ID,
	)
	if err != nil || visible.Revision != 2 {
		t.Fatalf("View Results Draft = %+v, %v", visible, err)
	}
	manageContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID:  10,
		EventRoles: map[int]viewer.Role{event.ID: viewer.Operator},
		EventScopes: map[int]viewer.EventScope{
			event.ID: {
				Capabilities: map[viewer.Capability]struct{}{viewer.ManageResults: {}},
			},
		},
	})
	managedTransaction, err := installation.BeginCommand(manageContext)
	if err != nil {
		t.Fatalf("begin Manage Results Draft: %v", err)
	}
	if _, err = managedTransaction.SaveCompetitionResultsDraft(
		manageContext,
		SaveCompetitionResultsDraftParams{
			EventID: event.ID, SessionID: competition.ID, ExpectedRevision: 2,
			Disposition: "Pending", ScoreType: "None",
			CreatedByAccountID: 10, Now: now.Add(3 * time.Minute),
		},
	); err != nil {
		t.Fatalf("Manage Results without View Results: %v", err)
	}
	if err = managedTransaction.Commit(); err != nil {
		t.Fatalf("commit managed Results Draft: %v", err)
	}
}
