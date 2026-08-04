package store

import (
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
)

func TestPresentationSubmitterAssignmentReplacementAndAccess(t *testing.T) {
	client := openEntTestClient(t)
	installation := &SQLite{client: client}
	ctx := hostMaintenanceContext(t.Context())
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	deadline := now.Add(time.Hour)
	event := createSchemaTestEvent(t, client)
	event = event.Update().SetPublic(true).SaveX(ctx)
	presentation := client.Session.Create().SetEventID(event.ID).SaveX(ctx)
	client.SessionPublishedVersion.Create().
		SetSessionID(presentation.ID).
		SetPublishedRevision(1).
		SetTitle("Assigned Presentation").
		SetSpeaker("Crew credit").
		SetType(sessionpublishedversion.TypePresentation).
		SetAudienceVisibility(sessionpublishedversion.AudienceVisibilityPublic).
		SetPublicDetails("Crew-approved details").
		SetPlannedStart(deadline.Add(time.Hour)).
		SetPlannedEnd(deadline.Add(2 * time.Hour)).
		SetTimingPolicy(sessionpublishedversion.TimingPolicyFixedEnd).
		SetMinimumDurationSeconds(1800).
		SetStartBoundary(sessionpublishedversion.StartBoundaryHard).
		SetEndBoundary(sessionpublishedversion.EndBoundaryHard).
		SetUploadDeadline(deadline).
		SaveX(ctx)
	first := createSubmissionTestAccount(t, client, ctx, "alice-speaker", "Alice Speaker")
	second := createSubmissionTestAccount(t, client, ctx, "blair-speaker", "Blair Speaker")

	assign := beginSubmissionTestCommand(t, installation, ctx)
	assigned, err := assign.AssignPresentationSubmitter(
		ctx,
		AssignPresentationSubmitterParams{
			EventID: event.ID, SessionID: presentation.ID,
			AccountID: first.ID, ExpectedRevision: 0,
		},
	)
	if err != nil || assigned.SubmitterAccountID != first.ID ||
		assigned.Revision != 1 || assigned.Speaker != "Crew credit" {
		t.Fatalf("assign Presentation Submitter = %+v, %v", assigned, err)
	}
	commitSubmissionTestCommand(t, assign)

	replace := beginSubmissionTestCommand(t, installation, ctx)
	replaced, err := replace.AssignPresentationSubmitter(
		ctx,
		AssignPresentationSubmitterParams{
			EventID: event.ID, SessionID: presentation.ID,
			AccountID: second.ID, ExpectedRevision: assigned.Revision,
		},
	)
	if err != nil || replaced.SubmitterAccountID != second.ID || replaced.Revision != 2 {
		t.Fatalf("replace Presentation Submitter = %+v, %v", replaced, err)
	}
	commitSubmissionTestCommand(t, replace)

	firstView, err := installation.LoadAccountPresentationSubmissions(hostMaintenanceContext(t.Context()), first.ID)
	if err != nil || len(firstView) != 0 {
		t.Fatalf("replaced Account Presentation view = %+v, %v", firstView, err)
	}
	secondView, err := installation.LoadAccountPresentationSubmissions(hostMaintenanceContext(t.Context()), second.ID)
	if err != nil || len(secondView) != 1 ||
		secondView[0].SessionID != presentation.ID ||
		secondView[0].PublicDetails != "Crew-approved details" {
		t.Fatalf("assigned Account Presentation view = %+v, %v", secondView, err)
	}

	wrongOwner := beginSubmissionTestCommand(t, installation, ctx)
	_, err = wrongOwner.UpdatePresentationSubmission(
		ctx,
		UpdatePresentationSubmissionParams{
			EventID: event.ID, SessionID: presentation.ID, AccountID: first.ID,
			ExpectedRevision: replaced.Revision,
			Speaker:          "Stolen credit", PublicDetails: "Stolen details", Now: now,
		},
	)
	if !errors.Is(err, ErrPresentationSubmitterRequired) {
		t.Fatalf("replaced Account update error = %v", err)
	}
	rollbackSubmissionTestCommand(t, wrongOwner)

	update := beginSubmissionTestCommand(t, installation, ctx)
	updated, err := update.UpdatePresentationSubmission(
		ctx,
		UpdatePresentationSubmissionParams{
			EventID: event.ID, SessionID: presentation.ID, AccountID: second.ID,
			ExpectedRevision: replaced.Revision,
			Speaker:          "Blair's public credit", PublicDetails: "Approved by Blair", Now: now,
		},
	)
	if err != nil || updated.Revision != 3 ||
		updated.Speaker != "Blair's public credit" ||
		updated.PublicDetails != "Approved by Blair" {
		t.Fatalf("update assigned Presentation = %+v, %v", updated, err)
	}
	commitSubmissionTestCommand(t, update)

	closed := beginSubmissionTestCommand(t, installation, ctx)
	_, err = closed.UpdatePresentationSubmission(
		ctx,
		UpdatePresentationSubmissionParams{
			EventID: event.ID, SessionID: presentation.ID, AccountID: second.ID,
			ExpectedRevision: updated.Revision,
			Speaker:          "Too late", Now: deadline,
		},
	)
	if !errors.Is(err, ErrPresentationSubmissionClosed) {
		t.Fatalf("Presentation deadline update error = %v", err)
	}
	rollbackSubmissionTestCommand(t, closed)

	client.SessionRun.Create().
		SetSessionID(presentation.ID).
		SetActualStart(deadline).
		SetSnapshotJSON(`{"title":"Assigned Presentation"}`).
		SaveX(ctx)
	client.ReopenWindow.Create().
		SetEventID(event.ID).
		SetTargetType("Presentation").
		SetTargetID(presentation.ID).
		SetReason("Speaker correction").
		SetExpiresAt(deadline.Add(time.Hour)).
		SetCreatedByAccountID(first.ID).
		SetCreatedAt(deadline).
		SaveX(ctx)

	afterStart := beginSubmissionTestCommand(t, installation, ctx)
	_, err = afterStart.UpdatePresentationSubmission(
		ctx,
		UpdatePresentationSubmissionParams{
			EventID: event.ID, SessionID: presentation.ID, AccountID: second.ID,
			ExpectedRevision: updated.Revision,
			Speaker:          "Corrected after start", PublicDetails: "Reopened", Now: deadline.Add(time.Minute),
		},
	)
	if !errors.Is(err, ErrPresentationSubmissionClosed) {
		t.Fatalf("Presentation update after Actual Start error = %v", err)
	}
	rollbackSubmissionTestCommand(t, afterStart)

	for index := 1; index <= 2; index++ {
		upload := beginSubmissionTestCommand(t, installation, ctx)
		version, uploadErr := upload.SaveAttachmentVersion(
			ctx,
			SaveAttachmentVersionParams{
				Authorization: UploadAuthorization{
					EventID: event.ID, TargetType: UploadTargetPresentation, TargetID: presentation.ID,
				},
				Name: "slides", OriginalFilename: "slides.pdf",
				MediaType: "application/pdf", SizeBytes: int64(index),
				SHA256: "digest", StorageKey: "sha256/di/digest",
				UploaderType: "Account", UploaderID: second.ID,
				ReleaseEligibility: AttachmentReleasePublic,
				Now:                deadline.Add(time.Duration(index) * time.Minute),
			},
		)
		if uploadErr != nil || version.Version != index {
			t.Fatalf("Presentation Attachment Version %d = %+v, %v", index, version, uploadErr)
		}
		commitSubmissionTestCommand(t, upload)
	}

	wrongUpload := beginSubmissionTestCommand(t, installation, ctx)
	_, err = wrongUpload.SaveAttachmentVersion(
		ctx,
		SaveAttachmentVersionParams{
			Authorization: UploadAuthorization{
				EventID: event.ID, TargetType: UploadTargetPresentation, TargetID: presentation.ID,
			},
			Name: "stolen", OriginalFilename: "stolen.pdf",
			SizeBytes: 1, SHA256: "other", StorageKey: "sha256/ot/other",
			UploaderType: "Account", UploaderID: first.ID,
			ReleaseEligibility: AttachmentReleasePublic,
			Now:                deadline.Add(3 * time.Minute),
		},
	)
	if !errors.Is(err, ErrUploadTargetNotFound) {
		t.Fatalf("replaced Account Presentation upload error = %v", err)
	}
	rollbackSubmissionTestCommand(t, wrongUpload)
}
