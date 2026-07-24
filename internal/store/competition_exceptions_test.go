package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/ent/competitionentry"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestCompetitionEntryReplayDeferAndResolution(t *testing.T) {
	client := openEntTestClient(t)
	installation := &SQLite{client: client}
	fixtureContext := systemContext(t.Context())
	event := createSchemaTestEvent(t, client)
	client.Installation.Create().SetActiveEventID(event.ID).SaveX(fixtureContext)
	competition := client.Session.Create().
		SetEventID(event.ID).
		SetLifecycle(session.LifecycleLive).
		SetEntryOrderSeed(29).
		SaveX(fixtureContext)
	lane := client.Lane.Create().SetEventID(event.ID).SaveX(fixtureContext)
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	client.SessionPublishedVersion.Create().
		SetSessionID(competition.ID).
		SetPublishedRevision(1).
		SetTitle("Demo Competition").
		SetType(sessionpublishedversion.TypeCompetition).
		SetAudienceVisibility(sessionpublishedversion.AudienceVisibilityPublic).
		SetPlannedStart(now).
		SetPlannedEnd(now.Add(time.Hour)).
		SetTimingPolicy(sessionpublishedversion.TimingPolicyFixedEnd).
		SetMinimumDurationSeconds(1800).
		SetStartBoundary(sessionpublishedversion.StartBoundaryHard).
		SetEndBoundary(sessionpublishedversion.EndBoundaryHard).
		SetSubmissionDeadline(now.Add(2 * time.Hour)).
		AddLaneIDs(lane.ID).
		SaveX(fixtureContext)
	for _, name := range []string{"First", "Second", "Third"} {
		client.CompetitionEntry.Create().
			SetEventID(event.ID).
			SetCompetitionSessionID(competition.ID).
			SetName(name).
			SetDisposition(competitionentry.DispositionIncluded).
			SaveX(fixtureContext)
	}
	client.SessionRun.Create().
		SetSessionID(competition.ID).
		SetActualStart(now).
		SetSnapshotJSON(`{"type":"Competition"}`).
		SaveX(fixtureContext)
	producerContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID: 1, EventRoles: map[int]viewer.Role{event.ID: viewer.Producer},
	})
	operatorContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID: 2, EventRoles: map[int]viewer.Role{event.ID: viewer.Operator},
		EventScopes: map[int]viewer.EventScope{
			event.ID: {LaneIDs: map[int]struct{}{lane.ID: {}}},
		},
	})

	takeProgramItem(t, installation, producerContext, TakeProgramItemParams{
		EventID: event.ID, SessionID: competition.ID,
		ExpectedRevision: 0, Item: ProgramItem{Kind: ProgramItemUpcoming}, Now: now,
	})
	takeProgramItem(t, installation, producerContext, TakeProgramItemParams{
		EventID: event.ID, SessionID: competition.ID,
		ExpectedRevision: 1, Item: ProgramItem{Kind: ProgramItemStarting}, Now: now,
	})
	order, fingerprint, err := installation.LoadCompetitionEntryOrder(
		producerContext, event.ID, competition.ID,
	)
	if err != nil {
		t.Fatalf("load Entry Order: %v", err)
	}
	entries := order.EntryIDs
	takeProgramItem(t, installation, producerContext, TakeProgramItemParams{
		EventID: event.ID, SessionID: competition.ID,
		ExpectedRevision: 2, Item: ProgramItem{Kind: ProgramItemEntry, EntryID: entries[0]},
		ExpectedEntryOrderRevision: order.Revision, EntryOrderFingerprint: fingerprint, Now: now,
	})
	order, fingerprint, err = installation.LoadCompetitionEntryOrder(
		producerContext, event.ID, competition.ID,
	)
	if err != nil {
		t.Fatalf("load Locked Entry Order: %v", err)
	}
	replayed := takeProgramItem(t, installation, producerContext, TakeProgramItemParams{
		EventID: event.ID, SessionID: competition.ID,
		ExpectedRevision: 3, Item: ProgramItem{Kind: ProgramItemEntry, EntryID: entries[0]},
		ExpectedEntryOrderRevision: order.Revision, EntryOrderFingerprint: fingerprint, Now: now,
	})
	if replayed.Current.EntryID != entries[0] || replayed.Next.EntryID != entries[1] {
		t.Fatalf("Replay moved canonical cursor: %+v", replayed)
	}

	second := client.CompetitionEntry.GetX(fixtureContext, entries[1])
	deferCompetitionEntry(t, installation, producerContext, DeferCompetitionEntryParams{
		EventID: event.ID, SessionID: competition.ID, EntryID: second.ID,
		ExpectedEntryRevision: second.Revision, ExpectedProgramRevision: 4, Now: now,
	})
	third := client.CompetitionEntry.GetX(fixtureContext, entries[2])
	deferCompetitionEntry(t, installation, producerContext, DeferCompetitionEntryParams{
		EventID: event.ID, SessionID: competition.ID, EntryID: third.ID,
		ExpectedEntryRevision: third.Revision, ExpectedProgramRevision: 5, Now: now,
	})
	channel, err := installation.LoadProgramChannel(producerContext, event.ID, competition.ID)
	if err != nil {
		t.Fatalf("load retry queue: %v", err)
	}
	if len(channel.Items) != 9 ||
		!channel.Items[5].Retry || channel.Items[5].EntryID != entries[1] ||
		!channel.Items[6].Retry || channel.Items[6].EntryID != entries[2] {
		t.Fatalf("retry queue = %+v", channel.Items)
	}

	third = client.CompetitionEntry.GetX(fixtureContext, third.ID)
	failure := beginCommand(t, installation, operatorContext)
	thirdFailure, err := failure.RecordCompetitionTechnicalFailure(
		operatorContext,
		TechnicalFailureParams{
			EventID: event.ID, SessionID: competition.ID, EntryID: third.ID,
			ExpectedRevision: third.Revision, Reason: "projector lost signal",
		},
	)
	if err != nil {
		t.Fatalf("record Technical Failure: %v", err)
	}
	if err = failure.Commit(); err != nil {
		t.Fatalf("commit Technical Failure: %v", err)
	}
	if thirdFailure.ResolutionRequired || thirdFailure.ReleaseHold {
		t.Fatalf("Technical Failure decided resolution: %+v", thirdFailure)
	}
	draftTransaction := beginCommand(t, installation, producerContext)
	if _, err = draftTransaction.SaveCompetitionResultsDraft(
		producerContext,
		SaveCompetitionResultsDraftParams{
			EventID: event.ID, SessionID: competition.ID, ExpectedRevision: 0,
			Disposition: "Publish", ScoreType: "None",
			CreatedByAccountID: 1, Now: now,
			Standings: []CompetitionResultStandingInput{
				{EntryID: entries[0], Standing: "Placed", Placement: 1, DisplayOrder: 1},
				{EntryID: entries[1], Standing: "Unplaced", DisplayOrder: 2},
				{EntryID: entries[2], Standing: "Unplaced", DisplayOrder: 3},
			},
		},
	); err != nil {
		t.Fatalf("save Ready Results fixture: %v", err)
	}
	if _, err = draftTransaction.MarkCompetitionResultsReady(
		producerContext,
		MarkCompetitionResultsReadyParams{
			EventID: event.ID, SessionID: competition.ID, ExpectedRevision: 1,
			ReviewedByAccountID: 1, Now: now,
		},
	); err != nil {
		t.Fatalf("mark Ready Results fixture: %v", err)
	}
	if err = draftTransaction.Commit(); err != nil {
		t.Fatalf("commit Ready Results fixture: %v", err)
	}

	preflight, err := installation.PreflightCompetitionEnd(
		producerContext, event.ID, competition.ID,
	)
	if err != nil {
		t.Fatalf("preflight Competition End: %v", err)
	}
	if !preflight.RequiresConfirmation || len(preflight.DeferredEntries) != 2 {
		t.Fatalf("End preflight = %+v", preflight)
	}
	unconfirmed := beginCommand(t, installation, producerContext)
	if _, err = unconfirmed.EndSession(
		producerContext, event.ID, competition.ID, 0, false, "", now,
	); !errors.Is(err, ErrDeferredEntriesConfirmation) {
		t.Fatalf("unconfirmed End error = %v", err)
	}
	if err = unconfirmed.Rollback(); err != nil {
		t.Fatalf("roll back unconfirmed End: %v", err)
	}
	ending := beginCommand(t, installation, producerContext)
	if _, err = ending.EndSession(
		producerContext, event.ID, competition.ID, 0, true, preflight.Fingerprint, now,
	); err != nil {
		t.Fatalf("confirmed End: %v", err)
	}
	if err = ending.Commit(); err != nil {
		t.Fatalf("commit confirmed End: %v", err)
	}
	superseded, err := installation.LoadCompetitionResultsDraft(
		producerContext, event.ID, competition.ID,
	)
	if err != nil || superseded.Revision != 2 || superseded.Ready {
		t.Fatalf("required resolution did not supersede Ready Results = %+v, %v", superseded, err)
	}
	blocked, err := installation.LoadCompetition(producerContext, event.ID, competition.ID)
	if err != nil {
		t.Fatalf("load blocked resolution state: %v", err)
	}
	if blocked.ResultsReady || blocked.ReleaseReady {
		t.Fatalf("unresolved Entries did not block results and release: %+v", blocked)
	}

	first := client.CompetitionEntry.GetX(fixtureContext, entries[0])
	resolveCompetitionEntry(t, installation, producerContext, ResolveCompetitionEntryParams{
		EventID: event.ID, SessionID: competition.ID, EntryID: first.ID,
		ExpectedRevision: first.Revision, ResultDisposition: "Disqualified",
		CrewReason: "rules violation", PublicDisqualificationMessage: "Disqualified",
		Now: now,
	})
	first = client.CompetitionEntry.GetX(fixtureContext, first.ID)
	if first.PresentationStatus != competitionentry.PresentationStatusPresented ||
		first.FirstPresentedAt.IsZero() || !first.ReleaseHold {
		t.Fatalf("Disqualification lost presentation history: %+v", first)
	}
	second = client.CompetitionEntry.GetX(fixtureContext, second.ID)
	resolveCompetitionEntry(t, installation, producerContext, ResolveCompetitionEntryParams{
		EventID: event.ID, SessionID: competition.ID, EntryID: second.ID,
		ExpectedRevision: second.Revision, ResultDisposition: "Withheld",
		CrewReason: "private organizer decision", Now: now,
	})
	third = client.CompetitionEntry.GetX(fixtureContext, third.ID)
	resolveCompetitionEntry(t, installation, producerContext, ResolveCompetitionEntryParams{
		EventID: event.ID, SessionID: competition.ID, EntryID: third.ID,
		ExpectedRevision: third.Revision, ResultDisposition: "Eligible",
		CrewReason: "technical failure accepted", Now: now,
	})
	resolved, err := installation.LoadCompetition(producerContext, event.ID, competition.ID)
	if err != nil {
		t.Fatalf("load resolved Competition: %v", err)
	}
	if !resolved.ResultsReady || !resolved.ReleaseReady {
		t.Fatalf("resolved readiness = results %v, release %v", resolved.ResultsReady, resolved.ReleaseReady)
	}
	public, err := installation.LoadPublicSchedule(t.Context())
	if err != nil {
		t.Fatalf("load public Competition resolution: %v", err)
	}
	var publicEntries []PublicCompetitionEntry
	for _, publicSession := range public.Sessions {
		if publicSession.ID == competition.ID {
			publicEntries = publicSession.CompetitionEntries
		}
	}
	if len(publicEntries) != 2 {
		t.Fatalf("public Competition Entries = %+v, want two visible resolutions", publicEntries)
	}
	foundDisqualified := false
	for _, publicEntry := range publicEntries {
		if publicEntry.Name == second.Name {
			t.Fatalf("Withheld Entry leaked publicly: %+v", publicEntry)
		}
		if publicEntry.Name == first.Name {
			foundDisqualified = publicEntry.ResultDisposition == "Disqualified" &&
				publicEntry.PublicDisqualificationMessage == "Disqualified"
		}
	}
	if !foundDisqualified {
		t.Fatalf("public Disqualification history = %+v", publicEntries)
	}
}

func takeProgramItem(
	t *testing.T,
	installation *SQLite,
	ctx context.Context,
	params TakeProgramItemParams,
) ProgramChannelState {
	t.Helper()
	command := beginCommand(t, installation, ctx)
	state, err := command.TakeProgramItem(ctx, params)
	if err != nil {
		t.Fatalf("Take Program Item: %v", err)
	}
	if err = command.Commit(); err != nil {
		t.Fatalf("commit Take Program Item: %v", err)
	}
	return state
}

func deferCompetitionEntry(
	t *testing.T,
	installation *SQLite,
	ctx context.Context,
	params DeferCompetitionEntryParams,
) CompetitionEntry {
	t.Helper()
	command := beginCommand(t, installation, ctx)
	entry, err := command.DeferCompetitionEntry(ctx, params)
	if err != nil {
		t.Fatalf("Defer Competition Entry: %v", err)
	}
	if err = command.Commit(); err != nil {
		t.Fatalf("commit Defer Competition Entry: %v", err)
	}
	return entry
}

func resolveCompetitionEntry(
	t *testing.T,
	installation *SQLite,
	ctx context.Context,
	params ResolveCompetitionEntryParams,
) CompetitionEntry {
	t.Helper()
	command := beginCommand(t, installation, ctx)
	entry, err := command.ResolveCompetitionEntry(ctx, params)
	if err != nil {
		t.Fatalf("Resolve Competition Entry: %v", err)
	}
	if err = command.Commit(); err != nil {
		t.Fatalf("commit Resolve Competition Entry: %v", err)
	}
	return entry
}

func beginCommand(t *testing.T, installation *SQLite, ctx context.Context) *CommandTx {
	t.Helper()
	command, err := installation.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin command: %v", err)
	}
	return command
}
