package store

import (
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/ent/competitionentry"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
	"github.com/dotwaffle/beamers/internal/prizegivingvalue"
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

func TestTakePrizegivingResultCommitsUnrevealedProgramOutput(t *testing.T) {
	client := openEntTestClient(t)
	installation := &SQLite{client: client}
	fixtureContext := systemContext(t.Context())
	event := createSchemaTestEvent(t, client)
	client.Installation.Create().SetActiveEventID(event.ID).SaveX(fixtureContext)
	ceremony := createPublishedResultsSession(
		t,
		client,
		event.ID,
		sessionpublishedversion.TypeCeremony,
		"Prizegiving",
	)
	ceremony.Update().SetLifecycle(session.LifecycleLive).SaveX(fixtureContext)
	client.SessionRun.Create().
		SetSessionID(ceremony.ID).
		SetActualStart(time.Date(2026, 8, 21, 13, 55, 0, 0, time.UTC)).
		SetSnapshotJSON(`{"type":"Ceremony"}`).
		SaveX(fixtureContext)
	competition := createPublishedResultsSession(
		t,
		client,
		event.ID,
		sessionpublishedversion.TypeCompetition,
		"Final",
	)
	producerContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID: 1, EventRoles: map[int]viewer.Role{event.ID: viewer.Producer},
	})
	draftTransaction, err := installation.BeginCommand(producerContext)
	if err != nil {
		t.Fatalf("begin Results Draft: %v", err)
	}
	draft, err := draftTransaction.SaveCompetitionResultsDraft(
		producerContext,
		SaveCompetitionResultsDraftParams{
			EventID: event.ID, SessionID: competition.ID,
			Disposition: "Publish", ScoreType: "None",
			ScoreVisibility: "Public", ScoreRequirement: "Optional",
			ScoreInterpretation: "Informational",
			CreatedByAccountID:  1,
			Now:                 time.Date(2026, 8, 21, 13, 0, 0, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatalf("save Results Draft: %v", err)
	}
	if err = draftTransaction.Commit(); err != nil {
		t.Fatalf("commit Results Draft: %v", err)
	}
	item := prizegivingvalue.LockedItem{
		Item: prizegivingvalue.Item{
			ItemRef: prizegivingvalue.ItemRef{
				Kind: "CompetitionResults", CompetitionSessionID: competition.ID,
				DisplayOrder: 1,
			},
			RevealMethod: "SequentialPodium",
		},
		RevealSeed: 73,
	}
	client.Prizegiving.Create().
		SetEventID(event.ID).
		SetCeremonySessionID(ceremony.ID).
		SetRevision(1).
		SetLocked(true).
		SetPreflightLock(prizegivingvalue.Lock{
			PlanRevision: 1,
			CompetitionSources: []prizegivingvalue.CompetitionLock{{
				SessionID: competition.ID, DraftID: draft.ID,
				DraftRevision: draft.Revision, Disposition: draft.Disposition,
			}},
			Sequence: []prizegivingvalue.LockedItem{item},
		}).
		SetCreatedByAccountID(1).
		SaveX(fixtureContext)

	channel, err := installation.LoadProgramChannel(
		producerContext,
		event.ID,
		ceremony.ID,
	)
	if err != nil {
		t.Fatalf("load Prizegiving Program Channel: %v", err)
	}
	if len(channel.Items) != 1 ||
		channel.Next.Kind != ProgramItemResult ||
		channel.Next.Result == nil ||
		channel.Next.Result.Ref.CompetitionSessionID != competition.ID ||
		channel.Next.Result.RevealMethod != "SequentialPodium" ||
		channel.Next.Result.RevealSeed != 73 ||
		channel.Next.Result.CompetitionResults.Revision != draft.Revision {
		t.Fatalf("initial Prizegiving Program Channel = %+v", channel)
	}
	command, err := installation.BeginCommand(producerContext)
	if err != nil {
		t.Fatalf("begin Prizegiving Take: %v", err)
	}
	taken, err := command.TakeProgramItem(
		producerContext,
		TakeProgramItemParams{
			EventID: event.ID, SessionID: ceremony.ID,
			ExpectedRevision: 0, Item: channel.Next,
			Now: time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC),
			ResultState: &PrizegivingStageState{
				Ref: channel.Next.Result.Ref, Status: "Taken", Release: "Held",
				TakenAt: time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC),
			},
		},
	)
	if err != nil {
		t.Fatalf("Take Prizegiving Result Item: %v", err)
	}
	if err = command.Commit(); err != nil {
		t.Fatalf("commit Prizegiving Take: %v", err)
	}
	if taken.Revision != 1 ||
		taken.Output.Kind != ProgramItemResult ||
		taken.Output.Result == nil ||
		taken.Output.Result.Status != "Taken" ||
		taken.Output.Result.Release != "Held" ||
		taken.Next.Kind != "" {
		t.Fatalf("Taken Prizegiving Program Channel = %+v", taken)
	}
	blockedEnd := beginCommand(t, installation, producerContext)
	_, err = blockedEnd.EndSession(
		producerContext,
		event.ID,
		ceremony.ID,
		0,
		false,
		"",
		time.Date(2026, 8, 21, 14, 0, 30, 0, time.UTC),
	)
	var unresolved *PrizegivingResultsUnresolvedError
	if !errors.As(err, &unresolved) ||
		len(unresolved.Items) != 1 ||
		unresolved.Items[0] != taken.Output.Result.Ref {
		t.Fatalf("Prizegiving End error = %#v, want current unresolved Result", err)
	}
	if err = blockedEnd.Rollback(); err != nil {
		t.Fatalf("roll back blocked Prizegiving End: %v", err)
	}
	revealStartedAt := time.Date(2026, 8, 21, 14, 1, 0, 0, time.UTC)
	revealTransaction, err := installation.BeginCommand(producerContext)
	if err != nil {
		t.Fatalf("begin Prizegiving Reveal: %v", err)
	}
	revealing, err := revealTransaction.ApplyPrizegivingResultAction(
		producerContext,
		PrizegivingResultActionParams{
			EventID: event.ID, SessionID: ceremony.ID,
			ExpectedRevision: 1, Item: taken.Output,
			State: PrizegivingStageState{
				Ref: taken.Output.Result.Ref, Status: "Revealing", Release: "Held",
				TakenAt:         taken.Output.Result.TakenAt,
				RevealStartedAt: revealStartedAt,
				RevealDuration:  3 * time.Second,
			},
			Presentation: PrizegivingPresentationRun{
				StartedAt: revealStartedAt, Duration: 3 * time.Second,
			},
		},
	)
	if err != nil {
		t.Fatalf("apply Prizegiving Reveal: %v", err)
	}
	if err = revealTransaction.Commit(); err != nil {
		t.Fatalf("commit Prizegiving Reveal: %v", err)
	}
	if revealing.Revision != 2 ||
		revealing.Output.Result.Status != "Revealing" ||
		revealing.Output.Result.Release != "Held" ||
		revealing.Output.Result.PresentationStartedAt != revealStartedAt ||
		revealing.Output.Result.PresentationDuration != 3*time.Second ||
		revealing.Output.Result.Replay {
		t.Fatalf("Revealing Prizegiving Program Channel = %+v", revealing)
	}
	completedAt := revealStartedAt.Add(3 * time.Second)
	elapsed, err := installation.LoadProgramChannelAt(
		producerContext,
		event.ID,
		ceremony.ID,
		completedAt,
	)
	if err != nil {
		t.Fatalf("load elapsed Prizegiving Reveal: %v", err)
	}
	if elapsed.Output.Result.Status != "Revealed" ||
		elapsed.Output.Result.Release != "Ready" ||
		elapsed.Output.Result.RevealCompletedAt != completedAt {
		t.Fatalf("elapsed Prizegiving Reveal = %+v", elapsed)
	}
	completeTransaction, err := installation.BeginCommand(producerContext)
	if err != nil {
		t.Fatalf("begin Prizegiving Reveal completion: %v", err)
	}
	revealed, err := completeTransaction.ApplyPrizegivingResultAction(
		producerContext,
		PrizegivingResultActionParams{
			EventID: event.ID, SessionID: ceremony.ID,
			ExpectedRevision: 2, Item: revealing.Output,
			State: PrizegivingStageState{
				Ref: revealing.Output.Result.Ref, Status: "Revealed", Release: "Ready",
				TakenAt:           revealing.Output.Result.TakenAt,
				RevealStartedAt:   revealStartedAt,
				RevealDuration:    3 * time.Second,
				RevealCompletedAt: completedAt,
			},
			Presentation: PrizegivingPresentationRun{
				StartedAt: revealStartedAt, Duration: 3 * time.Second,
			},
		},
	)
	if err != nil {
		t.Fatalf("complete Prizegiving Reveal: %v", err)
	}
	if err = completeTransaction.Commit(); err != nil {
		t.Fatalf("commit Prizegiving Reveal completion: %v", err)
	}
	replayStartedAt := completedAt.Add(time.Minute)
	replayTransaction, err := installation.BeginCommand(producerContext)
	if err != nil {
		t.Fatalf("begin Prizegiving Replay: %v", err)
	}
	replayed, err := replayTransaction.ApplyPrizegivingResultAction(
		producerContext,
		PrizegivingResultActionParams{
			EventID: event.ID, SessionID: ceremony.ID,
			ExpectedRevision: 3, Item: revealed.Output,
			State: PrizegivingStageState{
				Ref: revealed.Output.Result.Ref, Status: "Revealed", Release: "Ready",
				TakenAt:           revealed.Output.Result.TakenAt,
				RevealStartedAt:   revealStartedAt,
				RevealDuration:    3 * time.Second,
				RevealCompletedAt: completedAt,
			},
			Presentation: PrizegivingPresentationRun{
				Replay: true, StartedAt: replayStartedAt,
				Duration: 3 * time.Second,
			},
		},
	)
	if err != nil {
		t.Fatalf("Replay Prizegiving Result: %v", err)
	}
	if err = replayTransaction.Commit(); err != nil {
		t.Fatalf("commit Prizegiving Replay: %v", err)
	}
	if replayed.Revision != 4 ||
		replayed.Output.Result.Status != "Revealed" ||
		!replayed.Output.Result.Replay ||
		replayed.Output.Result.PresentationStartedAt != replayStartedAt ||
		replayed.Output.Result.RevealCompletedAt != completedAt {
		t.Fatalf("Replayed Prizegiving Program Channel = %+v", replayed)
	}
	endTransaction := beginCommand(t, installation, producerContext)
	ended, err := endTransaction.EndSession(
		producerContext,
		event.ID,
		ceremony.ID,
		0,
		false,
		"",
		replayStartedAt.Add(3*time.Second),
	)
	if err != nil {
		t.Fatalf("end resolved Prizegiving: %v", err)
	}
	if err = endTransaction.Commit(); err != nil {
		t.Fatalf("commit resolved Prizegiving End: %v", err)
	}
	if ended.Lifecycle != "Ended" {
		t.Fatalf("resolved Prizegiving state = %+v", ended)
	}
}

func TestProgramCompetitionResultsOmitsUnreleasedAndCrewOnlyData(t *testing.T) {
	score := "17.5"
	found := CompetitionResultsDraft{
		NoPublicCrewReason: "private reason",
		ScoreVisibility:    "CrewOnly",
		ReadyByAccountID:   7,
		CreatedByAccountID: 8,
		Standings: []CompetitionResultStanding{{
			EntryID: 1, Standing: "Placed", Placement: 1,
			DecimalScore: &score,
		}},
		Awards: []CompetitionAward{
			{Key: "embedded", Name: "Embedded"},
			{Key: "promoted", Name: "Promoted", Promoted: true},
		},
	}
	projected := programCompetitionResults(
		found,
		PrizegivingResultItemRef{Kind: "CompetitionResults"},
	)
	if projected.NoPublicCrewReason != "" ||
		projected.ReadyByAccountID != 0 ||
		projected.CreatedByAccountID != 0 ||
		projected.Standings[0].DecimalScore != nil ||
		len(projected.Awards) != 1 ||
		projected.Awards[0].Key != "embedded" {
		t.Fatalf("public Competition Results = %+v", projected)
	}
	award := programCompetitionResults(
		found,
		PrizegivingResultItemRef{
			Kind: "CompetitionAward", AwardKey: "promoted",
		},
	)
	if len(award.Standings) != 0 ||
		len(award.Awards) != 1 ||
		award.Awards[0].Key != "promoted" {
		t.Fatalf("promoted Competition Award = %+v", award)
	}
	nonPublic := programCompetitionResults(
		found,
		PrizegivingResultItemRef{Kind: "NoPublicResults"},
	)
	if len(nonPublic.Standings) != 0 || len(nonPublic.Awards) != 0 {
		t.Fatalf("No Public Results output = %+v", nonPublic)
	}
	secondScore := "12.5"
	found.Standings = append(found.Standings, CompetitionResultStanding{
		EntryID: 2, Standing: "Placed", Placement: 2,
		DecimalScore: &secondScore,
	})
	found.ScoreInterpretation = "HigherWins"
	bars := programScoreBars(found)
	if len(bars) != 2 ||
		bars[0] != (ProgramScoreBar{EntryID: 1, BasisPoints: 10000}) ||
		bars[1] != (ProgramScoreBar{EntryID: 2, BasisPoints: 2500}) {
		t.Fatalf("crew-only relative score bars = %+v", bars)
	}
}

func TestUnlockedPrizegivingCannotEnd(t *testing.T) {
	client := openEntTestClient(t)
	installation := &SQLite{client: client}
	fixtureContext := systemContext(t.Context())
	event := createSchemaTestEvent(t, client)
	client.Installation.Create().SetActiveEventID(event.ID).SaveX(fixtureContext)
	ceremony := createPublishedResultsSession(
		t,
		client,
		event.ID,
		sessionpublishedversion.TypeCeremony,
		"Prizegiving",
	)
	ceremony.Update().SetLifecycle(session.LifecycleLive).SaveX(fixtureContext)
	client.SessionRun.Create().
		SetSessionID(ceremony.ID).
		SetActualStart(time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)).
		SetSnapshotJSON(`{"type":"Ceremony"}`).
		SaveX(fixtureContext)
	client.Prizegiving.Create().
		SetEventID(event.ID).
		SetCeremonySessionID(ceremony.ID).
		SetSequence([]prizegivingvalue.Item{{
			ItemRef: prizegivingvalue.ItemRef{
				Kind: "EventAward", AwardKey: "community", DisplayOrder: 1,
			},
			RevealMethod: "StaticResult",
		}}).
		SetCreatedByAccountID(1).
		SaveX(fixtureContext)
	producerContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID: 1, EventRoles: map[int]viewer.Role{event.ID: viewer.Producer},
	})
	command := beginCommand(t, installation, producerContext)
	_, err := command.EndSession(
		producerContext,
		event.ID,
		ceremony.ID,
		0,
		false,
		"",
		time.Date(2026, 8, 21, 14, 5, 0, 0, time.UTC),
	)
	var unresolved *PrizegivingResultsUnresolvedError
	if !errors.As(err, &unresolved) ||
		len(unresolved.Items) != 1 ||
		unresolved.Items[0].AwardKey != "community" {
		t.Fatalf("unlocked Prizegiving End error = %#v", err)
	}
	if rollbackErr := command.Rollback(); rollbackErr != nil {
		t.Fatalf("roll back unlocked Prizegiving End: %v", rollbackErr)
	}
}
