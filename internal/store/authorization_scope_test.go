package store

import (
	"strconv"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
	"github.com/dotwaffle/beamers/internal/authz"
	"github.com/dotwaffle/beamers/internal/viewer"
)

// programChannelOverrideFixture builds one Program Channel (a Ceremony
// Session driving competition-output at a Location) fed by one Display
// tagged with a Display Group key, and returns the target and a function to
// repoint the Display to a different Display Group.
func programChannelOverrideFixture(
	t *testing.T,
	client *ent.Client,
	groupKey string,
) (eventID int, target DisplayOverrideTarget, repoint func(newGroupKey string)) {
	t.Helper()
	ctx := hostMaintenanceContext(t.Context())
	event := createSchemaTestEvent(t, client)
	client.Rundown.Create().SetEventID(event.ID).SaveX(ctx)
	location := client.Location.Create().SetEventID(event.ID).SaveX(ctx)
	now := time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)
	ceremony := client.Session.Create().
		SetEventID(event.ID).
		SetLifecycle(session.LifecycleLive).
		SaveX(ctx)
	client.SessionPublishedVersion.Create().
		SetSessionID(ceremony.ID).
		SetPublishedRevision(1).
		SetTitle("Prizegiving").
		SetType(sessionpublishedversion.TypeCompetition).
		SetAudienceVisibility(sessionpublishedversion.AudienceVisibilityPublic).
		SetPlannedStart(now).
		SetPlannedEnd(now.Add(time.Hour)).
		SetTimingPolicy(sessionpublishedversion.TimingPolicyFixedEnd).
		SetMinimumDurationSeconds(1800).
		SetStartBoundary(sessionpublishedversion.StartBoundaryHard).
		SetEndBoundary(sessionpublishedversion.EndBoundaryHard).
		AddLocationIDs(location.ID).
		SaveX(ctx)
	client.SessionRun.Create().
		SetSessionID(ceremony.ID).
		SetActualStart(now).
		SetSnapshotJSON(`{"type":"Ceremony","location_ids":[` + strconv.Itoa(location.ID) + `]}`).
		SaveX(ctx)
	display := client.Display.Create().SetName("Program").SetEnrolledAt(now).SaveX(ctx)
	assignment := client.DisplayAssignment.Create().
		SetDisplayID(display.ID).
		SetEventID(event.ID).
		SetLocationID(location.ID).
		SetViewKey("competition-output").
		SetDisplayGroupKeys([]string{groupKey}).
		SaveX(ctx)
	return event.ID, DisplayOverrideTarget{Type: DisplayOverrideTargetProgramChannel, ID: ceremony.ID},
		func(newGroupKey string) {
			assignment.Update().SetDisplayGroupKeys([]string{newGroupKey}).SaveX(ctx)
		}
}

func operatorGrantedDisplayGroup(eventID, accountID int, groupKey string) viewer.Identity {
	return viewer.Identity{
		AccountID:  accountID,
		EventRoles: map[int]viewer.Role{eventID: viewer.Operator},
		EventScopes: map[int]viewer.EventScope{
			eventID: {DisplayGroupKeys: map[string]struct{}{groupKey: {}}},
		},
	}
}

func evaluateActivateUrgentNotice(t *testing.T, identity viewer.Identity, facts authz.Facts) error {
	t.Helper()
	return authz.Evaluate(authz.Request{
		Identity: identity, Authenticated: true,
		Action: "ActivateUrgentNotice", Facts: facts,
	})
}

// TestDisplayGroupGrantCoversProgramChannelFeedingItsDisplays proves D6's
// second requirement: an Operator whose grant is a real Display Group key
// can operate a Program Channel Override once the target resolves to the
// Display Groups of the Displays the channel actually feeds, rather than a
// synthetic key nobody can be granted.
func TestDisplayGroupGrantCoversProgramChannelFeedingItsDisplays(t *testing.T) {
	client := openEntTestClient(t)
	ctx := hostMaintenanceContext(t.Context())
	eventID, target, _ := programChannelOverrideFixture(t, client, "left")

	facts, err := DisplayOverrideTargetScope(ctx, client, eventID, target)
	if err != nil {
		t.Fatalf("resolve Program Channel scope: %v", err)
	}

	granted := operatorGrantedDisplayGroup(eventID, 2, "left")
	if err := evaluateActivateUrgentNotice(t, granted, facts); err != nil {
		t.Fatalf(
			"Operator granted the feeding Display Group was refused: %v", err,
		)
	}

	ungranted := operatorGrantedDisplayGroup(eventID, 3, "right")
	if err := evaluateActivateUrgentNotice(t, ungranted, facts); err == nil {
		t.Fatalf("Operator granted an unrelated Display Group was admitted")
	}
}

// TestProgramChannelOverrideGrantDoesNotSurviveRepointing proves D6's first
// requirement: repointing a Program Channel to a different Display Group's
// consuming Displays changes who may override it. No synthetic key is
// grandfathered across the repoint.
func TestProgramChannelOverrideGrantDoesNotSurviveRepointing(t *testing.T) {
	client := openEntTestClient(t)
	ctx := hostMaintenanceContext(t.Context())
	eventID, target, repoint := programChannelOverrideFixture(t, client, "left")

	before, err := DisplayOverrideTargetScope(ctx, client, eventID, target)
	if err != nil {
		t.Fatalf("resolve Program Channel scope before repointing: %v", err)
	}
	leftOperator := operatorGrantedDisplayGroup(eventID, 2, "left")
	rightOperator := operatorGrantedDisplayGroup(eventID, 3, "right")
	if evalErr := evaluateActivateUrgentNotice(t, leftOperator, before); evalErr != nil {
		t.Fatalf("left-group Operator refused before repointing: %v", evalErr)
	}
	if evalErr := evaluateActivateUrgentNotice(t, rightOperator, before); evalErr == nil {
		t.Fatalf("right-group Operator admitted before repointing")
	}

	repoint("right")

	after, err := DisplayOverrideTargetScope(ctx, client, eventID, target)
	if err != nil {
		t.Fatalf("resolve Program Channel scope after repointing: %v", err)
	}
	if evalErr := evaluateActivateUrgentNotice(t, leftOperator, after); evalErr == nil {
		t.Fatalf("left-group Operator retained authority after repointing")
	}
	if evalErr := evaluateActivateUrgentNotice(t, rightOperator, after); evalErr != nil {
		t.Fatalf("right-group Operator refused after repointing: %v", evalErr)
	}
}
