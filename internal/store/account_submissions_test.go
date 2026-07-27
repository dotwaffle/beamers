package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/competitionentry"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
)

func TestAccountCompetitionSubmissionsHonorPolicyOwnershipAndReopenWindows(t *testing.T) {
	client := openEntTestClient(t)
	installation := &SQLite{client: client}
	ctx := systemContext(t.Context())
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	deadline := now.Add(time.Hour)
	event := createSchemaTestEvent(t, client)
	event = event.Update().
		SetPublic(true).
		SetSubmissionEligibility("VotingEligibleAccounts").
		SaveX(ctx)
	competition := client.Session.Create().SetEventID(event.ID).SaveX(ctx)
	client.SessionPublishedVersion.Create().
		SetSessionID(competition.ID).
		SetPublishedRevision(1).
		SetTitle("Account Competition").
		SetType(sessionpublishedversion.TypeCompetition).
		SetAudienceVisibility(sessionpublishedversion.AudienceVisibilityPublic).
		SetPlannedStart(deadline.Add(time.Hour)).
		SetPlannedEnd(deadline.Add(2 * time.Hour)).
		SetTimingPolicy(sessionpublishedversion.TimingPolicyFixedEnd).
		SetMinimumDurationSeconds(1800).
		SetStartBoundary(sessionpublishedversion.StartBoundaryHard).
		SetEndBoundary(sessionpublishedversion.EndBoundaryHard).
		SetSubmissionDeadline(deadline).
		SaveX(ctx)
	firstAccount := createSubmissionTestAccount(t, client, ctx, "alice", "Alice")
	secondAccount := createSubmissionTestAccount(t, client, ctx, "bob", "Bob")

	create := beginSubmissionTestCommand(t, installation, ctx)
	_, err := create.CreateSubmittedCompetitionEntry(
		ctx,
		CreateSubmittedCompetitionEntryParams{
			EventID: event.ID, SessionID: competition.ID, AccountID: firstAccount.ID,
			Name: "Ineligible", Now: now,
		},
	)
	if !errors.Is(err, ErrCompetitionSubmissionIneligible) {
		t.Fatalf("ineligible creation error = %v", err)
	}
	rollbackSubmissionTestCommand(t, create)

	configureAll := beginSubmissionTestCommand(t, installation, ctx)
	policy, err := configureAll.ConfigureCompetitionSubmissionEligibility(
		ctx,
		ConfigureSubmissionEligibilityParams{
			EventID: event.ID, SessionID: competition.ID,
			ExpectedRevision: 0, Eligibility: "AllAccounts", Override: true,
		},
	)
	if err != nil || policy.Effective != "AllAccounts" || policy.Revision != 1 {
		t.Fatalf("All Accounts override = %+v, %v", policy, err)
	}
	commitSubmissionTestCommand(t, configureAll)
	createAll := beginSubmissionTestCommand(t, installation, ctx)
	allAccountsEntry, err := createAll.CreateSubmittedCompetitionEntry(
		ctx,
		CreateSubmittedCompetitionEntryParams{
			EventID: event.ID, SessionID: competition.ID, AccountID: secondAccount.ID,
			Name: "All Accounts Entry", Now: now,
		},
	)
	if err != nil {
		t.Fatalf("create All Accounts submission: %v", err)
	}
	commitSubmissionTestCommand(t, createAll)
	tighten := beginSubmissionTestCommand(t, installation, ctx)
	policy, err = tighten.ConfigureCompetitionSubmissionEligibility(
		ctx,
		ConfigureSubmissionEligibilityParams{
			EventID: event.ID, SessionID: competition.ID,
			ExpectedRevision: 1, Eligibility: "VotingEligibleAccounts", Override: false,
		},
	)
	if err != nil || policy.Effective != "VotingEligibleAccounts" || policy.Revision != 2 {
		t.Fatalf("tightened Event-default policy = %+v, %v", policy, err)
	}
	commitSubmissionTestCommand(t, tighten)

	client.VotingEligibility.Create().
		SetEventID(event.ID).
		SetAccountID(firstAccount.ID).
		SetCreatedAt(now).
		SaveX(ctx)
	create = beginSubmissionTestCommand(t, installation, ctx)
	entry, err := create.CreateSubmittedCompetitionEntry(
		ctx,
		CreateSubmittedCompetitionEntryParams{
			EventID: event.ID, SessionID: competition.ID, AccountID: firstAccount.ID,
			Name: "Public credit", PublicDetails: "Independent details", Now: now,
		},
	)
	if err != nil {
		t.Fatalf("create eligible submission: %v", err)
	}
	commitSubmissionTestCommand(t, create)
	client.CompetitionEntry.UpdateOneID(entry.ID).
		SetReviewedContentRevision(entry.ContentRevision).
		SetReviewedByAccountID(secondAccount.ID).
		SetReviewedAt(now).
		SaveX(ctx)
	client.VotingEligibility.Delete().ExecX(ctx)

	update := beginSubmissionTestCommand(t, installation, ctx)
	updated, err := update.UpdateSubmittedCompetitionEntry(
		ctx,
		UpdateSubmittedCompetitionEntryParams{
			EventID: event.ID, SessionID: competition.ID, EntryID: entry.ID,
			AccountID: firstAccount.ID, ExpectedRevision: entry.Revision,
			Name: "Changed public credit", PublicDetails: "Changed details", Now: now.Add(time.Minute),
		},
	)
	if err != nil {
		t.Fatalf("maintain existing Entry after eligibility tightening: %v", err)
	}
	if updated.ReviewCurrent {
		t.Fatal("Account content update retained stale Entry Review")
	}
	commitSubmissionTestCommand(t, update)

	wrongOwner := beginSubmissionTestCommand(t, installation, ctx)
	_, err = wrongOwner.UpdateSubmittedCompetitionEntry(
		ctx,
		UpdateSubmittedCompetitionEntryParams{
			EventID: event.ID, SessionID: competition.ID, EntryID: entry.ID,
			AccountID: secondAccount.ID, ExpectedRevision: updated.Revision,
			Name: "Stolen", Now: now.Add(2 * time.Minute),
		},
	)
	if !errors.Is(err, ErrCompetitionSubmitterRequired) {
		t.Fatalf("wrong owner update error = %v", err)
	}
	rollbackSubmissionTestCommand(t, wrongOwner)

	closed := beginSubmissionTestCommand(t, installation, ctx)
	_, err = closed.UpdateSubmittedCompetitionEntry(
		ctx,
		UpdateSubmittedCompetitionEntryParams{
			EventID: event.ID, SessionID: competition.ID, EntryID: entry.ID,
			AccountID: firstAccount.ID, ExpectedRevision: updated.Revision,
			Name: "Late", Now: deadline,
		},
	)
	if !errors.Is(err, ErrCompetitionSubmissionClosed) {
		t.Fatalf("deadline update error = %v", err)
	}
	rollbackSubmissionTestCommand(t, closed)

	client.ReopenWindow.Create().
		SetEventID(event.ID).
		SetTargetType("Entry").
		SetTargetID(entry.ID).
		SetReason("Requested correction").
		SetExpiresAt(deadline.Add(time.Hour)).
		SetCreatedByAccountID(secondAccount.ID).
		SetCreatedAt(deadline).
		SaveX(ctx)
	reopened := beginSubmissionTestCommand(t, installation, ctx)
	_, err = reopened.UpdateSubmittedCompetitionEntry(
		ctx,
		UpdateSubmittedCompetitionEntryParams{
			EventID: event.ID, SessionID: competition.ID, EntryID: entry.ID,
			AccountID: firstAccount.ID, ExpectedRevision: updated.Revision,
			Name: "Reopened", PublicDetails: "Corrected", Now: deadline.Add(time.Minute),
		},
	)
	if err != nil {
		t.Fatalf("update in Reopen Window: %v", err)
	}
	commitSubmissionTestCommand(t, reopened)

	upload := beginSubmissionTestCommand(t, installation, ctx)
	version, err := upload.SaveAttachmentVersion(
		ctx,
		SaveAttachmentVersionParams{
			Authorization: UploadAuthorization{
				EventID: event.ID, TargetType: UploadTargetEntry, TargetID: entry.ID,
			},
			Name: "archive", OriginalFilename: "entry.zip",
			MediaType: "application/zip", SizeBytes: 4,
			SHA256: "abcd", StorageKey: "sha256/ab/abcd",
			UploaderType: "Account", UploaderID: firstAccount.ID,
			ReleaseEligibility: AttachmentReleasePublic,
			Now:                deadline.Add(2 * time.Minute),
		},
	)
	if err != nil || version.UploaderType != "Account" ||
		version.UploaderID != firstAccount.ID || !version.Primary {
		t.Fatalf("Account Attachment Version = %+v, %v", version, err)
	}
	commitSubmissionTestCommand(t, upload)

	wrongUpload := beginSubmissionTestCommand(t, installation, ctx)
	_, err = wrongUpload.SaveAttachmentVersion(
		ctx,
		SaveAttachmentVersionParams{
			Authorization: UploadAuthorization{
				EventID: event.ID, TargetType: UploadTargetEntry, TargetID: entry.ID,
			},
			Name: "stolen", OriginalFilename: "stolen.zip",
			SizeBytes: 1, SHA256: "ef", StorageKey: "sha256/ef/ef",
			UploaderType: "Account", UploaderID: secondAccount.ID,
			ReleaseEligibility: AttachmentReleasePublic,
			Now:                deadline.Add(3 * time.Minute),
		},
	)
	if !errors.Is(err, ErrUploadTargetNotFound) {
		t.Fatalf("wrong owner Attachment error = %v", err)
	}
	rollbackSubmissionTestCommand(t, wrongUpload)

	crewManaged := client.CompetitionEntry.Create().
		SetEventID(event.ID).
		SetCompetitionSessionID(competition.ID).
		SetName("Public credit stays").
		SetPublicDetails("Not the Account profile").
		SetDisposition(competitionentry.DispositionPending).
		SaveX(ctx)
	assign := beginSubmissionTestCommand(t, installation, ctx)
	assigned, err := assign.AssignCompetitionEntrySubmitter(
		ctx,
		AssignCompetitionEntrySubmitterParams{
			EventID: event.ID, SessionID: competition.ID, EntryID: crewManaged.ID,
			AccountID: secondAccount.ID, ExpectedRevision: crewManaged.Revision,
		},
	)
	if err != nil {
		t.Fatalf("assign Crew Managed Entry: %v", err)
	}
	if assigned.SubmitterAccountID != secondAccount.ID ||
		assigned.Name != crewManaged.Name ||
		assigned.PublicDetails != crewManaged.PublicDetails {
		t.Fatalf("assigned Entry changed public credit: %+v", assigned)
	}
	commitSubmissionTestCommand(t, assign)

	privateCompetition := client.Session.Create().SetEventID(event.ID).SaveX(ctx)
	client.SessionPublishedVersion.Create().
		SetSessionID(privateCompetition.ID).
		SetPublishedRevision(1).
		SetTitle("Crew Only Competition").
		SetType(sessionpublishedversion.TypeCompetition).
		SetAudienceVisibility(sessionpublishedversion.AudienceVisibilityCrewOnly).
		SetPlannedStart(deadline.Add(time.Hour)).
		SetPlannedEnd(deadline.Add(2 * time.Hour)).
		SetTimingPolicy(sessionpublishedversion.TimingPolicyFixedEnd).
		SetMinimumDurationSeconds(1800).
		SetStartBoundary(sessionpublishedversion.StartBoundaryHard).
		SetEndBoundary(sessionpublishedversion.EndBoundaryHard).
		SetSubmissionDeadline(deadline.Add(time.Hour)).
		SaveX(ctx)
	privateCreate := beginSubmissionTestCommand(t, installation, ctx)
	_, err = privateCreate.CreateSubmittedCompetitionEntry(
		ctx,
		CreateSubmittedCompetitionEntryParams{
			EventID: event.ID, SessionID: privateCompetition.ID,
			AccountID: secondAccount.ID, Name: "Hidden", Now: now,
		},
	)
	if !errors.Is(err, ErrCompetitionSubmitterRequired) {
		t.Fatalf("Crew Only Competition creation error = %v", err)
	}
	rollbackSubmissionTestCommand(t, privateCreate)

	firstView, err := installation.LoadAccountCompetitionSubmissions(
		t.Context(),
		firstAccount.ID,
		deadline.Add(time.Minute),
	)
	if err != nil || len(firstView) != 1 || len(firstView[0].Entries) != 1 ||
		firstView[0].Entries[0].ID != entry.ID || firstView[0].CanCreate {
		t.Fatalf("first Account submissions = %+v, %v", firstView, err)
	}
	secondView, err := installation.LoadAccountCompetitionSubmissions(
		t.Context(),
		secondAccount.ID,
		deadline.Add(time.Minute),
	)
	secondEntryIDs := make(map[int]struct{})
	if len(secondView) == 1 {
		for _, found := range secondView[0].Entries {
			secondEntryIDs[found.ID] = struct{}{}
		}
	}
	if _, found := secondEntryIDs[allAccountsEntry.ID]; !found {
		t.Errorf("All Accounts Entry missing from second Account view")
	}
	if _, found := secondEntryIDs[crewManaged.ID]; !found {
		t.Errorf("assigned Crew Managed Entry missing from second Account view")
	}
	if err != nil || len(secondView) != 1 || len(secondView[0].Entries) != 2 {
		t.Fatalf("second Account submissions = %+v, %v", secondView, err)
	}
}

func createSubmissionTestAccount(
	t *testing.T,
	client *ent.Client,
	ctx context.Context,
	handle, name string,
) *ent.Account {
	t.Helper()
	account := client.Account.Create().
		SetName(name).
		SetNormalizedName(handle).
		SetAdministrator(false).
		SaveX(ctx)
	client.AccountProfile.Create().
		SetAccountID(account.ID).
		SetNormalizedHandle(handle).
		SetDisplayName(name).
		SaveX(ctx)
	return account
}

func beginSubmissionTestCommand(
	t *testing.T,
	installation *SQLite,
	ctx context.Context,
) *CommandTx {
	t.Helper()
	transaction, err := installation.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin submission command: %v", err)
	}
	return transaction
}

func commitSubmissionTestCommand(t *testing.T, transaction *CommandTx) {
	t.Helper()
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit submission command: %v", err)
	}
}

func rollbackSubmissionTestCommand(t *testing.T, transaction *CommandTx) {
	t.Helper()
	if err := transaction.Rollback(); err != nil {
		t.Fatalf("roll back submission command: %v", err)
	}
}
