package store

import (
	"testing"
	"time"

	"github.com/dotwaffle/beamers/ent/competitionentry"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
)

func TestPublicCompetitionEntriesFollowCanonicalOrder(t *testing.T) {
	client := openEntTestClient(t)
	installation := &SQLite{client: client}
	ctx := systemContext(t.Context())
	event := createSchemaTestEvent(t, client)
	client.Installation.Create().SetActiveEventID(event.ID).SaveX(ctx)
	competition := client.Session.Create().
		SetEventID(event.ID).
		SetEntryOrderPolicy(session.EntryOrderPolicyManualOrder).
		SaveX(ctx)
	client.SessionPublishedVersion.Create().
		SetSessionID(competition.ID).
		SetPublishedRevision(1).
		SetTitle("Ordered Competition").
		SetType(sessionpublishedversion.TypeCompetition).
		SetAudienceVisibility(sessionpublishedversion.AudienceVisibilityPublic).
		SetPlannedStart(time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)).
		SetPlannedEnd(time.Date(2026, 8, 21, 13, 0, 0, 0, time.UTC)).
		SetTimingPolicy(sessionpublishedversion.TimingPolicyFixedEnd).
		SetMinimumDurationSeconds(1800).
		SetStartBoundary(sessionpublishedversion.StartBoundaryHard).
		SetEndBoundary(sessionpublishedversion.EndBoundaryHard).
		SetSubmissionDeadline(time.Date(2026, 8, 21, 11, 0, 0, 0, time.UTC)).
		SaveX(ctx)
	first := client.CompetitionEntry.Create().
		SetEventID(event.ID).
		SetCompetitionSessionID(competition.ID).
		SetName("First submitted").
		SetDisposition(competitionentry.DispositionIncluded).
		SaveX(ctx)
	second := client.CompetitionEntry.Create().
		SetEventID(event.ID).
		SetCompetitionSessionID(competition.ID).
		SetName("First publicly").
		SetDisposition(competitionentry.DispositionIncluded).
		SaveX(ctx)
	competition.Update().SetEntryOrderManualIds([]int{second.ID, first.ID}).SaveX(ctx)

	public, err := installation.LoadPublicSchedule(t.Context())
	if err != nil {
		t.Fatalf("load public Competition: %v", err)
	}
	if len(public.Sessions) != 1 ||
		len(public.Sessions[0].CompetitionEntries) != 2 ||
		public.Sessions[0].CompetitionEntries[0].ID != second.ID ||
		public.Sessions[0].CompetitionEntries[1].ID != first.ID {
		t.Fatalf("public Competition order = %+v", public.Sessions)
	}
}
