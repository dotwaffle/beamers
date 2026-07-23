package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/attachment"
	"github.com/dotwaffle/beamers/ent/attachmentversion"
	"github.com/dotwaffle/beamers/ent/competitionentry"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestAttachmentReleasePolicyEligibilityHoldAndCue(t *testing.T) {
	client := openEntTestClient(t)
	installationStore := &SQLite{client: client}
	fixtureContext := systemContext(t.Context())
	event := createSchemaTestEvent(t, client)
	client.Installation.Create().SetActiveEventID(event.ID).SaveX(fixtureContext)
	competition := client.Session.Create().
		SetEventID(event.ID).
		SetLifecycle(session.LifecycleLive).
		SaveX(fixtureContext)
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	client.SessionPublishedVersion.Create().
		SetSessionID(competition.ID).
		SetPublishedRevision(1).
		SetTitle("Release Competition").
		SetType(sessionpublishedversion.TypeCompetition).
		SetAudienceVisibility(sessionpublishedversion.AudienceVisibilityPublic).
		SetPlannedStart(now).
		SetPlannedEnd(now.Add(time.Hour)).
		SetTimingPolicy(sessionpublishedversion.TimingPolicyFixedEnd).
		SetMinimumDurationSeconds(1800).
		SetStartBoundary(sessionpublishedversion.StartBoundaryHard).
		SetEndBoundary(sessionpublishedversion.EndBoundaryHard).
		SetSubmissionDeadline(now.Add(2 * time.Hour)).
		SaveX(fixtureContext)
	included := client.CompetitionEntry.Create().
		SetEventID(event.ID).
		SetCompetitionSessionID(competition.ID).
		SetName("Included").
		SetDisposition(competitionentry.DispositionIncluded).
		SaveX(fixtureContext)
	pending := client.CompetitionEntry.Create().
		SetEventID(event.ID).
		SetCompetitionSessionID(competition.ID).
		SetName("Pending").
		SetDisposition(competitionentry.DispositionPending).
		SaveX(fixtureContext)
	disqualified := client.CompetitionEntry.Create().
		SetEventID(event.ID).
		SetCompetitionSessionID(competition.ID).
		SetName("Disqualified").
		SetDisposition(competitionentry.DispositionIncluded).
		SetResultDisposition(competitionentry.ResultDispositionDisqualified).
		SetReleaseHold(true).
		SaveX(fixtureContext)
	withheld := client.CompetitionEntry.Create().
		SetEventID(event.ID).
		SetCompetitionSessionID(competition.ID).
		SetName("Withheld").
		SetDisposition(competitionentry.DispositionIncluded).
		SetPresentationStatus(competitionentry.PresentationStatusNotPresented).
		SetResultDisposition(competitionentry.ResultDispositionWithheld).
		SetReleaseHold(true).
		SaveX(fixtureContext)
	unresolved := client.CompetitionEntry.Create().
		SetEventID(event.ID).
		SetCompetitionSessionID(competition.ID).
		SetName("Unresolved").
		SetDisposition(competitionentry.DispositionIncluded).
		SetPresentationStatus(competitionentry.PresentationStatusNotPresented).
		SetResolutionRequired(true).
		SaveX(fixtureContext)
	publicVersion := createReleaseVersion(t, client, fixtureContext, event.ID, included.ID, "public", "Public")
	crewVersion := createReleaseVersion(t, client, fixtureContext, event.ID, included.ID, "crew", "CrewOnly")
	pendingVersion := createReleaseVersion(t, client, fixtureContext, event.ID, pending.ID, "pending", "Public")
	disqualifiedVersion := createReleaseVersion(
		t, client, fixtureContext, event.ID, disqualified.ID, "disqualified", "Public",
	)
	withheldVersion := createReleaseVersion(
		t, client, fixtureContext, event.ID, withheld.ID, "withheld", "Public",
	)
	_ = crewVersion
	_ = pendingVersion
	_ = withheldVersion
	producerContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID: 1, EventRoles: map[int]viewer.Role{event.ID: viewer.Producer},
	})

	if released := releasedVersionIDs(t, installationStore); len(released) != 0 {
		t.Fatalf("On Ended released while Live = %v", released)
	}
	competition.Update().SetLifecycle(session.LifecycleEnded).SaveX(fixtureContext)
	if released := releasedVersionIDs(t, installationStore); !slices.Equal(released, []int{publicVersion.ID}) {
		t.Fatalf("On Ended releases = %v, want [%d]", released, publicVersion.ID)
	}

	configure := beginCommand(t, installationStore, producerContext)
	configured, err := configure.ConfigureCompetitionAttachmentRelease(
		producerContext,
		ConfigureCompetitionAttachmentReleaseParams{
			EventID: event.ID, SessionID: competition.ID,
			ExpectedRevision: 0, Policy: AttachmentReleaseOnEventCue, Override: true,
		},
	)
	if err != nil {
		t.Fatalf("configure Competition release override: %v", err)
	}
	if err = configure.Commit(); err != nil {
		t.Fatalf("commit Competition release override: %v", err)
	}
	if !configured.Override || configured.Policy != AttachmentReleaseOnEventCue {
		t.Fatalf("Competition release override = %+v", configured)
	}
	if released := releasedVersionIDs(t, installationStore); len(released) != 0 {
		t.Fatalf("cue-governed files released early = %v", released)
	}

	blocked := beginCommand(t, installationStore, producerContext)
	if _, err = blocked.FireEventAttachmentReleaseCue(
		producerContext, event.ID, 0, now,
	); !errors.Is(err, ErrAttachmentReleaseCueBlocked) {
		t.Fatalf("blocked Event Release Cue error = %v", err)
	}
	if err = blocked.Rollback(); err != nil {
		t.Fatalf("roll back blocked Event Release Cue: %v", err)
	}
	unresolved.Update().SetResolutionRequired(false).SaveX(fixtureContext)
	cue := beginCommand(t, installationStore, producerContext)
	fired, err := cue.FireEventAttachmentReleaseCue(producerContext, event.ID, 0, now)
	if err != nil {
		t.Fatalf("fire Event Release Cue: %v", err)
	}
	if err = cue.Commit(); err != nil {
		t.Fatalf("commit Event Release Cue: %v", err)
	}
	if fired.CueAt.IsZero() || fired.Revision != 1 {
		t.Fatalf("Event Release Cue = %+v", fired)
	}
	if released := releasedVersionIDs(t, installationStore); !slices.Equal(released, []int{publicVersion.ID}) {
		t.Fatalf("cue releases = %v, want [%d]", released, publicVersion.ID)
	}

	hold := beginCommand(t, installationStore, producerContext)
	held, err := hold.SetAttachmentVersionRelease(
		producerContext,
		SetAttachmentVersionReleaseParams{
			EventID: event.ID, VersionID: publicVersion.ID,
			ExpectedRevision: 0, Hold: true,
		},
	)
	if err != nil {
		t.Fatalf("apply Version Release Hold: %v", err)
	}
	if err = hold.Commit(); err != nil {
		t.Fatalf("commit Version Release Hold: %v", err)
	}
	if !held.ReleaseHold || len(releasedVersionIDs(t, installationStore)) != 0 {
		t.Fatalf("Version Release Hold did not disable public access: %+v", held)
	}
	lift := beginCommand(t, installationStore, producerContext)
	if _, err = lift.SetAttachmentVersionRelease(
		producerContext,
		SetAttachmentVersionReleaseParams{
			EventID: event.ID, VersionID: publicVersion.ID,
			ExpectedRevision: 1, Hold: false,
		},
	); err != nil {
		t.Fatalf("lift Version Release Hold: %v", err)
	}
	if err = lift.Commit(); err != nil {
		t.Fatalf("commit lifted Version Release Hold: %v", err)
	}
	entryLift := beginCommand(t, installationStore, producerContext)
	if _, err = entryLift.SetCompetitionEntryReleaseHold(
		producerContext,
		SetCompetitionEntryReleaseHoldParams{
			EventID: event.ID, SessionID: competition.ID, EntryID: disqualified.ID,
			ExpectedRevision: disqualified.Revision, CrewReason: "review complete",
		},
	); err != nil {
		t.Fatalf("lift disqualified Entry Release Hold: %v", err)
	}
	if err = entryLift.Commit(); err != nil {
		t.Fatalf("commit lifted Entry Release Hold: %v", err)
	}
	if released := releasedVersionIDs(t, installationStore); !slices.Equal(
		released, []int{publicVersion.ID, disqualifiedVersion.ID},
	) {
		t.Fatalf("lifted releases = %v", released)
	}
}

func createReleaseVersion(
	t *testing.T,
	client *ent.Client,
	ctx context.Context,
	eventID, entryID int,
	name, eligibility string,
) *ent.AttachmentVersion {
	t.Helper()
	logical := client.Attachment.Create().
		SetEventID(eventID).
		SetOwnerType(attachment.OwnerTypeEntry).
		SetOwnerID(entryID).
		SetName(name).
		SaveX(ctx)
	return client.AttachmentVersion.Create().
		SetAttachmentID(logical.ID).
		SetVersion(1).
		SetOriginalFilename(name + ".txt").
		SetSizeBytes(1).
		SetSha256("00").
		SetStorageKey(name).
		SetUploaderType(attachmentversion.UploaderTypeCrew).
		SetUploaderID(1).
		SetFinal(true).
		SetReleaseEligibility(attachmentversion.ReleaseEligibility(eligibility)).
		SaveX(ctx)
}

func releasedVersionIDs(t *testing.T, installationStore *SQLite) []int {
	t.Helper()
	released, err := installationStore.LoadReleasedAttachmentVersions(t.Context())
	if err != nil {
		t.Fatalf("load released Attachment Versions: %v", err)
	}
	result := make([]int, 0, len(released))
	for _, version := range released {
		result = append(result, version.ID)
	}
	return result
}
