package store

import (
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/ent/competitionentry"
	"github.com/dotwaffle/beamers/ent/event"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
)

func TestCompetitionBallotPresentationPrivacyEditsAndFreeze(t *testing.T) {
	client := openEntTestClient(t)
	storage := &SQLite{client: client}
	ctx := systemContext(t.Context())
	now := time.Date(2026, time.August, 21, 12, 0, 0, 0, time.UTC)
	foundEvent := createSchemaTestEvent(t, client)
	client.Installation.Create().SetActiveEventID(foundEvent.ID).SaveX(ctx)
	competition := client.Session.Create().
		SetEventID(foundEvent.ID).
		SetLifecycle(session.LifecycleLive).
		SaveX(ctx)
	client.SessionPublishedVersion.Create().
		SetSessionID(competition.ID).
		SetPublishedRevision(1).
		SetTitle("Live Ballot").
		SetType(sessionpublishedversion.TypeCompetition).
		SetAudienceVisibility(sessionpublishedversion.AudienceVisibilityPublic).
		SetPlannedStart(now).
		SetPlannedEnd(now.Add(time.Hour)).
		SetTimingPolicy(sessionpublishedversion.TimingPolicyFixedEnd).
		SetMinimumDurationSeconds(1800).
		SetStartBoundary(sessionpublishedversion.StartBoundaryHard).
		SetEndBoundary(sessionpublishedversion.EndBoundaryHard).
		SaveX(ctx)
	alice := createSubmissionTestAccount(t, client, ctx, "alice-ballot", "Alice")
	bob := createSubmissionTestAccount(t, client, ctx, "bob-ballot", "Bob")
	client.VotingEligibility.Create().
		SetEventID(foundEvent.ID).
		SetAccountID(alice.ID).
		SaveX(ctx)
	client.VotingEligibility.Create().
		SetEventID(foundEvent.ID).
		SetAccountID(bob.ID).
		SaveX(ctx)
	first := client.CompetitionEntry.Create().
		SetEventID(foundEvent.ID).
		SetCompetitionSessionID(competition.ID).
		SetSubmitterAccountID(alice.ID).
		SetName("Alice Entry").
		SetDisposition(competitionentry.DispositionIncluded).
		SaveX(ctx)
	second := client.CompetitionEntry.Create().
		SetEventID(foundEvent.ID).
		SetCompetitionSessionID(competition.ID).
		SetName("Crew Entry").
		SetDisposition(competitionentry.DispositionIncluded).
		SaveX(ctx)

	configure, err := storage.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin Voting configuration: %v", err)
	}
	configured, err := configure.ConfigureVoting(ctx, ConfigureVotingParams{
		EventID: foundEvent.ID, SessionID: competition.ID,
		ExpectedRevision: 0, SelfVoteOverride: "Allowed",
	})
	if err != nil {
		t.Fatalf("configure Voting: %v", err)
	}
	if err = configure.Commit(); err != nil {
		t.Fatalf("commit Voting configuration: %v", err)
	}
	open, err := storage.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin Voting open: %v", err)
	}
	window, err := open.OpenVotingWindow(ctx, VotingWindowParams{
		EventID: foundEvent.ID, SessionID: competition.ID,
		ExpectedRevision: configured.Revision, Now: now,
	})
	if err != nil {
		t.Fatalf("open Voting Window: %v", err)
	}
	if err = open.Commit(); err != nil {
		t.Fatalf("commit Voting open: %v", err)
	}
	if window.Method != "Range1To5" || window.SelfVotePolicy != "Allowed" {
		t.Fatalf("frozen Voting configuration = %+v", window)
	}
	if ballot, loadErr := storage.LoadBallot(
		ctx, foundEvent.ID, competition.ID, alice.ID,
	); loadErr != nil || len(ballot.Entries) != 0 {
		t.Fatalf("pre-presentation Ballot = %+v, %v", ballot, loadErr)
	}

	first.Update().SetFirstPresentedAt(now).SaveX(ctx)
	competition.Update().AddVotingRevision(1).SaveX(ctx)
	aliceBallot, err := storage.LoadBallot(ctx, foundEvent.ID, competition.ID, alice.ID)
	if err != nil || len(aliceBallot.Entries) != 1 || aliceBallot.Entries[0].ID != first.ID {
		t.Fatalf("first presentation Ballot = %+v, %v", aliceBallot, err)
	}
	bobBallot, err := storage.LoadBallot(ctx, foundEvent.ID, competition.ID, bob.ID)
	if err != nil {
		t.Fatalf("load Bob Ballot: %v", err)
	}
	bobRevision := bobBallot.Window.Revision
	cast, err := storage.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin Vote: %v", err)
	}
	aliceBallot, err = cast.CastVote(ctx, CastVoteParams{
		EventID: foundEvent.ID, SessionID: competition.ID,
		AccountID: alice.ID, EntryID: first.ID, Value: 5,
		ExpectedRevision: aliceBallot.Window.Revision, Now: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("cast Vote: %v", err)
	}
	if err = cast.Commit(); err != nil {
		t.Fatalf("commit Vote: %v", err)
	}
	if aliceBallot.Entries[0].Value != 5 {
		t.Fatalf("Alice Vote = %+v", aliceBallot.Entries)
	}
	bobBallot, err = storage.LoadBallot(ctx, foundEvent.ID, competition.ID, bob.ID)
	if err != nil || bobBallot.Entries[0].Value != 0 {
		t.Fatalf("Bob saw another Account Vote = %+v, %v", bobBallot, err)
	}
	bobVote, err := storage.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin Bob Vote: %v", err)
	}
	if _, err = bobVote.CastVote(ctx, CastVoteParams{
		EventID: foundEvent.ID, SessionID: competition.ID,
		AccountID: bob.ID, EntryID: first.ID, Value: 4,
		ExpectedRevision: bobRevision, Now: now.Add(90 * time.Second),
	}); err != nil {
		t.Fatalf("cast Bob Vote from shared Ballot revision: %v", err)
	}
	if err = bobVote.Commit(); err != nil {
		t.Fatalf("commit Bob Vote: %v", err)
	}

	second.Update().SetFirstPresentedAt(now.Add(2 * time.Minute)).SaveX(ctx)
	competition.Update().AddVotingRevision(1).SaveX(ctx)
	aliceBallot, err = storage.LoadBallot(ctx, foundEvent.ID, competition.ID, alice.ID)
	if err != nil || len(aliceBallot.Entries) != 2 {
		t.Fatalf("live-added Entry Ballot = %+v, %v", aliceBallot, err)
	}
	edit, err := storage.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin Vote edit: %v", err)
	}
	aliceBallot, err = edit.CastVote(ctx, CastVoteParams{
		EventID: foundEvent.ID, SessionID: competition.ID,
		AccountID: alice.ID, EntryID: first.ID, Value: 2,
		ExpectedRevision: aliceBallot.Window.Revision, Now: now.Add(3 * time.Minute),
	})
	if err != nil {
		t.Fatalf("edit Vote: %v", err)
	}
	if err = edit.Commit(); err != nil {
		t.Fatalf("commit Vote edit: %v", err)
	}
	participation, err := storage.LoadVotingParticipation(
		ctx, foundEvent.ID, competition.ID,
	)
	if err != nil || participation.Eligible != 2 ||
		participation.Participating != 2 || participation.Complete != 0 {
		t.Fatalf("Crew participation = %+v, %v", participation, err)
	}

	foundEvent.Update().SetSelfVotePolicy(event.SelfVotePolicyNeutral).SaveX(ctx)
	frozen, err := storage.LoadVotingWindow(ctx, foundEvent.ID, competition.ID)
	if err != nil || frozen.SelfVotePolicy != "Allowed" {
		t.Fatalf("policy changed after open = %+v, %v", frozen, err)
	}
	closeWindow, err := storage.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin Voting close: %v", err)
	}
	closed, err := closeWindow.CloseVotingWindow(ctx, VotingWindowParams{
		EventID: foundEvent.ID, SessionID: competition.ID,
		ExpectedRevision: aliceBallot.Window.Revision, CreatedByAccountID: alice.ID,
		Now: now.Add(4 * time.Minute),
	})
	if err != nil {
		t.Fatalf("close Voting Window: %v", err)
	}
	if err = closeWindow.Commit(); err != nil {
		t.Fatalf("commit Voting close: %v", err)
	}
	afterClose, err := storage.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin post-close Vote: %v", err)
	}
	if _, err = afterClose.CastVote(ctx, CastVoteParams{
		EventID: foundEvent.ID, SessionID: competition.ID,
		AccountID: alice.ID, EntryID: second.ID, Value: 3,
		ExpectedRevision: closed.Revision, Now: now.Add(5 * time.Minute),
	}); !errors.Is(err, ErrVotingWindowState) {
		t.Fatalf("post-close Vote error = %v", err)
	}
	_ = afterClose.Rollback()

	neutralCompetition := client.Session.Create().
		SetEventID(foundEvent.ID).
		SetLifecycle(session.LifecycleLive).
		SaveX(ctx)
	client.SessionPublishedVersion.Create().
		SetSessionID(neutralCompetition.ID).
		SetPublishedRevision(1).
		SetTitle("Neutral Ballot").
		SetType(sessionpublishedversion.TypeCompetition).
		SetAudienceVisibility(sessionpublishedversion.AudienceVisibilityPublic).
		SetPlannedStart(now).
		SetPlannedEnd(now.Add(time.Hour)).
		SetTimingPolicy(sessionpublishedversion.TimingPolicyFixedEnd).
		SetMinimumDurationSeconds(1800).
		SetStartBoundary(sessionpublishedversion.StartBoundaryHard).
		SetEndBoundary(sessionpublishedversion.EndBoundaryHard).
		SaveX(ctx)
	selfEntry := client.CompetitionEntry.Create().
		SetEventID(foundEvent.ID).
		SetCompetitionSessionID(neutralCompetition.ID).
		SetSubmitterAccountID(alice.ID).
		SetName("Neutral Self Entry").
		SetDisposition(competitionentry.DispositionIncluded).
		SetFirstPresentedAt(now).
		SaveX(ctx)
	openNeutral, err := storage.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin neutral Voting open: %v", err)
	}
	neutralWindow, err := openNeutral.OpenVotingWindow(ctx, VotingWindowParams{
		EventID: foundEvent.ID, SessionID: neutralCompetition.ID, Now: now,
	})
	if err != nil {
		t.Fatalf("open neutral Voting Window: %v", err)
	}
	if err = openNeutral.Commit(); err != nil {
		t.Fatalf("commit neutral Voting open: %v", err)
	}
	if neutralWindow.SelfVotePolicy != "Neutral" {
		t.Fatalf("inherited Self-Vote Policy = %q, want Neutral", neutralWindow.SelfVotePolicy)
	}
	selfVote, err := storage.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin neutral self-Vote: %v", err)
	}
	if _, err = selfVote.CastVote(ctx, CastVoteParams{
		EventID: foundEvent.ID, SessionID: neutralCompetition.ID,
		AccountID: alice.ID, EntryID: selfEntry.ID, Value: 5,
		ExpectedRevision: neutralWindow.Revision, Now: now,
	}); !errors.Is(err, ErrVoteUnavailable) {
		t.Fatalf("neutral self-Vote error = %v", err)
	}
	_ = selfVote.Rollback()
}

func TestCloseVotingWindowCreatesAggregateTallyAndResultsDraft(t *testing.T) {
	client := openEntTestClient(t)
	storage := &SQLite{client: client}
	ctx := systemContext(t.Context())
	now := time.Date(2026, time.August, 21, 12, 0, 0, 0, time.UTC)
	foundEvent := createSchemaTestEvent(t, client)
	foundEvent.Update().SetSelfVotePolicy(event.SelfVotePolicyNeutral).SaveX(ctx)
	client.Installation.Create().SetActiveEventID(foundEvent.ID).SaveX(ctx)
	competition := client.Session.Create().
		SetEventID(foundEvent.ID).
		SetLifecycle(session.LifecycleLive).
		SaveX(ctx)
	client.SessionPublishedVersion.Create().
		SetSessionID(competition.ID).
		SetPublishedRevision(1).
		SetTitle("Tally Competition").
		SetType(sessionpublishedversion.TypeCompetition).
		SetAudienceVisibility(sessionpublishedversion.AudienceVisibilityPublic).
		SetPlannedStart(now).
		SetPlannedEnd(now.Add(time.Hour)).
		SetTimingPolicy(sessionpublishedversion.TimingPolicyFixedEnd).
		SetMinimumDurationSeconds(1800).
		SetStartBoundary(sessionpublishedversion.StartBoundaryHard).
		SetEndBoundary(sessionpublishedversion.EndBoundaryHard).
		SaveX(ctx)
	alice := createSubmissionTestAccount(t, client, ctx, "alice-tally", "Alice")
	bob := createSubmissionTestAccount(t, client, ctx, "bob-tally", "Bob")
	for _, accountID := range []int{alice.ID, bob.ID} {
		client.VotingEligibility.Create().
			SetEventID(foundEvent.ID).
			SetAccountID(accountID).
			SaveX(ctx)
	}
	first := client.CompetitionEntry.Create().
		SetEventID(foundEvent.ID).
		SetCompetitionSessionID(competition.ID).
		SetSubmitterAccountID(alice.ID).
		SetName("First").
		SetDisposition(competitionentry.DispositionIncluded).
		SetFirstPresentedAt(now).
		SaveX(ctx)
	second := client.CompetitionEntry.Create().
		SetEventID(foundEvent.ID).
		SetCompetitionSessionID(competition.ID).
		SetSubmitterAccountID(bob.ID).
		SetName("Second").
		SetDisposition(competitionentry.DispositionIncluded).
		SetFirstPresentedAt(now.Add(time.Second)).
		SaveX(ctx)
	third := client.CompetitionEntry.Create().
		SetEventID(foundEvent.ID).
		SetCompetitionSessionID(competition.ID).
		SetName("Third").
		SetDisposition(competitionentry.DispositionIncluded).
		SetFirstPresentedAt(now.Add(2 * time.Second)).
		SaveX(ctx)

	open, err := storage.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin Voting open: %v", err)
	}
	window, err := open.OpenVotingWindow(ctx, VotingWindowParams{
		EventID: foundEvent.ID, SessionID: competition.ID, Now: now,
	})
	if err != nil {
		t.Fatalf("open Voting Window: %v", err)
	}
	if err = open.Commit(); err != nil {
		t.Fatalf("commit Voting open: %v", err)
	}
	for _, cast := range []CastVoteParams{
		{
			EventID: foundEvent.ID, SessionID: competition.ID,
			AccountID: alice.ID, EntryID: second.ID, Value: 5,
			ExpectedRevision: window.Revision, Now: now.Add(time.Minute),
		},
		{
			EventID: foundEvent.ID, SessionID: competition.ID,
			AccountID: alice.ID, EntryID: third.ID, Value: 1,
			ExpectedRevision: window.Revision, Now: now.Add(time.Minute),
		},
		{
			EventID: foundEvent.ID, SessionID: competition.ID,
			AccountID: bob.ID, EntryID: first.ID, Value: 5,
			ExpectedRevision: window.Revision, Now: now.Add(time.Minute),
		},
	} {
		transaction, beginErr := storage.BeginCommand(ctx)
		if beginErr != nil {
			t.Fatalf("begin Vote: %v", beginErr)
		}
		if _, castErr := transaction.CastVote(ctx, cast); castErr != nil {
			t.Fatalf("cast Vote: %v", castErr)
		}
		if commitErr := transaction.Commit(); commitErr != nil {
			t.Fatalf("commit Vote: %v", commitErr)
		}
	}

	closeWindow, err := storage.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin Voting close: %v", err)
	}
	closed, err := closeWindow.CloseVotingWindow(ctx, VotingWindowParams{
		EventID: foundEvent.ID, SessionID: competition.ID,
		ExpectedRevision: window.Revision, CreatedByAccountID: alice.ID,
		Now: now.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("close Voting Window: %v", err)
	}
	if err = closeWindow.Commit(); err != nil {
		t.Fatalf("commit Voting close: %v", err)
	}
	if closed.State != "Closed" {
		t.Fatalf("closed Voting Window = %+v", closed)
	}
	tally, err := storage.LoadVotingTally(ctx, foundEvent.ID, competition.ID)
	if err != nil {
		t.Fatalf("load Voting Tally: %v", err)
	}
	if tally.Method != "Range1To5" ||
		tally.SelfVotePolicy != "Neutral" ||
		tally.Participating != 2 {
		t.Fatalf("Voting Tally = %+v", tally)
	}
	if len(tally.Entries) != 3 ||
		tally.Entries[0].EntryID != first.ID ||
		tally.Entries[0].Total != 8 ||
		tally.Entries[0].Count != 2 ||
		tally.Entries[1].EntryID != second.ID ||
		tally.Entries[1].Total != 8 ||
		tally.Entries[1].Count != 2 ||
		tally.Entries[2].EntryID != third.ID ||
		tally.Entries[2].Total != 4 ||
		tally.Entries[2].Count != 2 {
		t.Fatalf("Voting Tally Entries = %+v", tally.Entries)
	}
	draft, err := storage.LoadCompetitionResultsDraft(ctx, foundEvent.ID, competition.ID)
	if err != nil {
		t.Fatalf("load seeded Results Draft: %v", err)
	}
	if draft.Revision != 1 || draft.Ready || draft.Disposition != "Pending" ||
		draft.VotingTallyID != tally.ID ||
		len(draft.Standings) != 3 ||
		draft.Standings[0].EntryID != first.ID ||
		draft.Standings[0].Placement != 1 ||
		draft.Standings[1].EntryID != second.ID ||
		draft.Standings[1].Placement != 1 ||
		draft.Standings[2].EntryID != third.ID ||
		draft.Standings[2].Placement != 3 {
		t.Fatalf("seeded Results Draft = %+v", draft)
	}
}
