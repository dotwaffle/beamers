package store

import (
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/ent"
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

	if _, err = installation.LoadCompetitionResultsDraft(
		t.Context(), event.ID, competition.ID,
	); err == nil {
		t.Fatal("missing viewer read an unreleased Results Draft")
	}
	deniedTransaction, err := installation.BeginCommand(observerContext)
	if err != nil {
		t.Fatalf("begin denied Results mutation: %v", err)
	}
	if _, err = deniedTransaction.SaveCompetitionResultsDraft(
		observerContext,
		SaveCompetitionResultsDraftParams{
			EventID: event.ID, SessionID: competition.ID, ExpectedRevision: 3,
			Disposition: "Pending", ScoreType: "None",
			CreatedByAccountID: 8, Now: now.Add(4 * time.Minute),
		},
	); err == nil {
		t.Fatal("Observer without Manage Results mutated an unreleased Draft")
	}
	if err = deniedTransaction.Rollback(); err != nil {
		t.Fatalf("roll back denied Results mutation: %v", err)
	}

	otherEvent := createSchemaTestEvent(t, client)
	otherCompetition := client.Session.Create().
		SetEventID(otherEvent.ID).
		SetLifecycle(session.LifecycleEnded).
		SaveX(fixtureContext)
	client.SessionPublishedVersion.Create().
		SetSessionID(otherCompetition.ID).
		SetPublishedRevision(1).
		SetTitle("Other Results Competition").
		SetType(sessionpublishedversion.TypeCompetition).
		SetAudienceVisibility(sessionpublishedversion.AudienceVisibilityPublic).
		SetPlannedStart(now).
		SetPlannedEnd(now.Add(time.Hour)).
		SetTimingPolicy(sessionpublishedversion.TimingPolicyFixedEnd).
		SetMinimumDurationSeconds(1800).
		SetStartBoundary(sessionpublishedversion.StartBoundaryHard).
		SetEndBoundary(sessionpublishedversion.EndBoundaryHard).
		SetSubmissionDeadline(now.Add(-time.Minute)).
		SaveX(fixtureContext)
	otherEntry := client.CompetitionEntry.Create().
		SetEventID(otherEvent.ID).
		SetCompetitionSessionID(otherCompetition.ID).
		SetName("Other Event Entry").
		SetDisposition(competitionentry.DispositionIncluded).
		SaveX(fixtureContext)
	otherProducerContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID:  11,
		EventRoles: map[int]viewer.Role{otherEvent.ID: viewer.Producer},
	})
	otherTransaction, err := installation.BeginCommand(otherProducerContext)
	if err != nil {
		t.Fatalf("begin other Event Results Draft: %v", err)
	}
	if _, err = otherTransaction.SaveCompetitionResultsDraft(
		otherProducerContext,
		SaveCompetitionResultsDraftParams{
			EventID: otherEvent.ID, SessionID: otherCompetition.ID,
			ExpectedRevision: 0, Disposition: "Pending", ScoreType: "None",
			CreatedByAccountID: 11, Now: now,
		},
	); err != nil {
		t.Fatalf("save other Event Results Draft: %v", err)
	}
	if err = otherTransaction.Commit(); err != nil {
		t.Fatalf("commit other Event Results Draft: %v", err)
	}
	crossEvent, crossEventErr := installation.LoadCompetitionResultsDraft(
		viewContext, otherEvent.ID, otherCompetition.ID,
	)
	if crossEventErr == nil && crossEvent.Revision != 0 {
		t.Fatalf("View Results leaked another Event Draft = %+v", crossEvent)
	}
	crossMutation, err := installation.BeginCommand(manageContext)
	if err != nil {
		t.Fatalf("begin cross-Event Results mutation: %v", err)
	}
	if _, err = crossMutation.SaveCompetitionResultsDraft(
		manageContext,
		SaveCompetitionResultsDraftParams{
			EventID: otherEvent.ID, SessionID: otherCompetition.ID,
			ExpectedRevision: 1, Disposition: "Pending", ScoreType: "None",
			CreatedByAccountID: 10, Now: now.Add(5 * time.Minute),
		},
	); err == nil {
		t.Fatal("Manage Results mutated another Event Draft")
	}
	if err = crossMutation.Rollback(); err != nil {
		t.Fatalf("roll back cross-Event Results mutation: %v", err)
	}
	crossStanding, err := installation.BeginCommand(manageContext)
	if err != nil {
		t.Fatalf("begin cross-Event Standing mutation: %v", err)
	}
	if _, err = crossStanding.SaveCompetitionResultsDraft(
		manageContext,
		SaveCompetitionResultsDraftParams{
			EventID: event.ID, SessionID: competition.ID, ExpectedRevision: 3,
			Disposition: "Publish", ScoreType: "None",
			CreatedByAccountID: 10, Now: now.Add(6 * time.Minute),
			Standings: []CompetitionResultStandingInput{{
				EntryID: otherEntry.ID, Standing: "Placed", Placement: 1, DisplayOrder: 1,
			}},
		},
	); !errors.Is(err, ErrCompetitionResultsEntry) {
		t.Fatalf("cross-Event Standing error = %v", err)
	}
	if err = crossStanding.Rollback(); err != nil {
		t.Fatalf("roll back cross-Event Standing mutation: %v", err)
	}
}

func TestEventAwardsDraftKeepsReadinessPerReleasePath(t *testing.T) {
	client := openEntTestClient(t)
	installation := &SQLite{client: client}
	event := createSchemaTestEvent(t, client)
	ceremony := createPublishedResultsSession(
		t, client, event.ID, sessionpublishedversion.TypeCeremony, "Prizegiving",
	)
	producerContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID:  7,
		EventRoles: map[int]viewer.Role{event.ID: viewer.Producer},
	})
	now := time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)
	standalone := AwardReleasePath{Kind: "Standalone"}
	prizegiving := AwardReleasePath{
		Kind: "Prizegiving", PrizegivingSessionID: ceremony.ID,
	}
	firstAwards := []EventAwardInput{
		{
			Key: "community", Name: "Community", DisplayOrder: 1,
			Recipients:  []AwardRecipientInput{{DisplayName: "Volunteers"}},
			ReleasePath: standalone,
		},
		{
			Key: "best", Name: "Best in Show", DisplayOrder: 1,
			Recipients:  []AwardRecipientInput{{DisplayName: "Finalists"}},
			ReleasePath: prizegiving,
		},
	}
	saveTransaction, err := installation.BeginCommand(producerContext)
	if err != nil {
		t.Fatalf("begin Event Awards Draft: %v", err)
	}
	first, err := saveTransaction.SaveEventAwardsDraft(
		producerContext,
		SaveEventAwardsDraftParams{
			EventID: event.ID, ExpectedRevision: 0, CreatedByAccountID: 7, Now: now,
			Awards: firstAwards,
		},
	)
	if err != nil {
		t.Fatalf("save Event Awards Draft: %v", err)
	}
	if err = saveTransaction.Commit(); err != nil {
		t.Fatalf("commit Event Awards Draft: %v", err)
	}
	if first.Revision != 1 || len(first.PathStates) != 2 {
		t.Fatalf("first Event Awards Draft = %+v", first)
	}

	for index, path := range []AwardReleasePath{standalone, prizegiving} {
		transaction, beginErr := installation.BeginCommand(producerContext)
		if beginErr != nil {
			t.Fatalf("begin Event Awards review: %v", beginErr)
		}
		ready, markErr := transaction.MarkEventAwardsReady(
			producerContext,
			MarkEventAwardsReadyParams{
				EventID: event.ID, ExpectedRevision: 1, ReleasePath: path,
				ExpectedPathRevision: 1, ReviewedByAccountID: 7,
				Now: now.Add(time.Duration(index+1) * time.Minute),
			},
		)
		if markErr != nil {
			t.Fatalf("mark Event Awards path Ready: %v", markErr)
		}
		if commitErr := transaction.Commit(); commitErr != nil {
			t.Fatalf("commit Event Awards review: %v", commitErr)
		}
		if ready.Revision != 1 {
			t.Fatalf("Ready Event Awards revision = %d", ready.Revision)
		}
	}

	secondAwards := []EventAwardInput{firstAwards[0], firstAwards[1]}
	secondAwards[0].Name = "Community Award"
	secondTransaction, err := installation.BeginCommand(producerContext)
	if err != nil {
		t.Fatalf("begin revised Event Awards Draft: %v", err)
	}
	second, err := secondTransaction.SaveEventAwardsDraft(
		producerContext,
		SaveEventAwardsDraftParams{
			EventID: event.ID, ExpectedRevision: 1, CreatedByAccountID: 7,
			Now: now.Add(3 * time.Minute), Awards: secondAwards,
		},
	)
	if err != nil {
		t.Fatalf("save revised Event Awards Draft: %v", err)
	}
	if err = secondTransaction.Commit(); err != nil {
		t.Fatalf("commit revised Event Awards Draft: %v", err)
	}
	if second.PathStates[0].Ready || second.PathStates[0].Revision != 2 {
		t.Fatalf("changed Standalone path state = %+v", second.PathStates[0])
	}
	if !second.PathStates[1].Ready || second.PathStates[1].Revision != 1 {
		t.Fatalf("unchanged Prizegiving path state = %+v", second.PathStates[1])
	}

	observerContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID:  8,
		EventRoles: map[int]viewer.Role{event.ID: viewer.Observer},
	})
	if _, err = installation.LoadEventAwardsDraft(
		observerContext,
		event.ID,
	); err == nil {
		t.Fatal("Observer without Results Access read Event Awards")
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
	visible, err := installation.LoadEventAwardsDraft(viewContext, event.ID)
	if err != nil || visible.Revision != 2 {
		t.Fatalf("View Event Awards = %+v, %v", visible, err)
	}
	if _, err = installation.LoadEventAwardsDraft(t.Context(), event.ID); err == nil {
		t.Fatal("missing viewer read Event Awards")
	}
}

func createPublishedResultsSession(
	t *testing.T,
	client *ent.Client,
	eventID int,
	sessionType sessionpublishedversion.Type,
	title string,
) *ent.Session {
	t.Helper()
	ctx := systemContext(t.Context())
	found := client.Session.Create().
		SetEventID(eventID).
		SetLifecycle(session.LifecycleEnded).
		SaveX(ctx)
	client.SessionPublishedVersion.Create().
		SetSessionID(found.ID).
		SetPublishedRevision(1).
		SetTitle(title).
		SetType(sessionType).
		SetAudienceVisibility(sessionpublishedversion.AudienceVisibilityPublic).
		SetPlannedStart(time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)).
		SetPlannedEnd(time.Date(2026, 8, 21, 13, 0, 0, 0, time.UTC)).
		SetTimingPolicy(sessionpublishedversion.TimingPolicyFixedEnd).
		SetMinimumDurationSeconds(1800).
		SetStartBoundary(sessionpublishedversion.StartBoundaryHard).
		SetEndBoundary(sessionpublishedversion.EndBoundaryHard).
		SaveX(ctx)
	return found
}
