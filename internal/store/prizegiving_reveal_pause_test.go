package store

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/prizegiving"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
	"github.com/dotwaffle/beamers/internal/prizegivingvalue"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestReplaceOverridePausesOnlyFullProgramChannelCoverage(t *testing.T) {
	client := openEntTestClient(t)
	installation := &SQLite{client: client}
	ctx := systemContext(t.Context())
	event := createSchemaTestEvent(t, client)
	client.Installation.Create().SetActiveEventID(event.ID).SaveX(ctx)
	client.Rundown.Create().SetEventID(event.ID).SaveX(ctx)
	location := client.Location.Create().SetEventID(event.ID).SaveX(ctx)
	ceremony := client.Session.Create().
		SetEventID(event.ID).
		SetLifecycle(session.LifecycleLive).
		SetProgramOutputKind(session.ProgramOutputKindResult).
		SetProgramOutputResult(prizegivingvalue.ProgramOutput{
			ItemRef: prizegivingvalue.ItemRef{
				Kind:         prizegivingvalue.ItemEventAward,
				AwardKey:     "community",
				DisplayOrder: 1,
			},
		}).
		SaveX(ctx)
	client.SessionPublishedVersion.Create().
		SetSessionID(ceremony.ID).
		SetPublishedRevision(1).
		SetTitle("Prizegiving").
		SetType(sessionpublishedversion.TypeCeremony).
		SetAudienceVisibility(sessionpublishedversion.AudienceVisibilityPublic).
		SetPlannedStart(time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)).
		SetPlannedEnd(time.Date(2026, 8, 21, 15, 0, 0, 0, time.UTC)).
		SetTimingPolicy(sessionpublishedversion.TimingPolicyFixedEnd).
		SetMinimumDurationSeconds(1800).
		SetStartBoundary(sessionpublishedversion.StartBoundaryHard).
		SetEndBoundary(sessionpublishedversion.EndBoundaryHard).
		AddLocationIDs(location.ID).
		SaveX(ctx)
	client.SessionRun.Create().
		SetSessionID(ceremony.ID).
		SetActualStart(time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)).
		SetSnapshotJSON(`{"type":"Ceremony","location_ids":[` +
			strconv.Itoa(location.ID) + `]}`).
		SaveX(ctx)
	startedAt := time.Date(2026, 8, 21, 14, 1, 0, 0, time.UTC)
	ref := prizegivingvalue.ItemRef{
		Kind: prizegivingvalue.ItemEventAward, AwardKey: "community",
		DisplayOrder: 1,
	}
	client.Prizegiving.Create().
		SetEventID(event.ID).
		SetCeremonySessionID(ceremony.ID).
		SetLocked(true).
		SetItemStates([]prizegivingvalue.StageState{{
			ItemRef: ref, Status: prizegivingvalue.StageRevealing,
			Release:         prizegivingvalue.ReleaseHeld,
			RevealStartedAt: startedAt, RevealDurationNanos: int64(5 * time.Second),
		}}).
		SetCreatedByAccountID(1).
		SaveX(ctx)
	displayIDs := make([]int, 0, 2)
	for index, groups := range [][]string{{"left"}, {"right"}} {
		display := client.Display.Create().
			SetName("Program " + strconv.Itoa(index+1)).
			SetEnrolledAt(startedAt).
			SaveX(ctx)
		displayIDs = append(displayIDs, display.ID)
		client.DisplayAssignment.Create().
			SetDisplayID(display.ID).
			SetEventID(event.ID).
			SetLocationID(location.ID).
			SetViewKey("competition-output").
			SetDisplayGroupKeys(groups).
			SaveX(ctx)
	}
	producerContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID:  1,
		EventRoles: map[int]viewer.Role{event.ID: viewer.Producer},
	})

	activatePriorityOverride(t, installation, producerContext, ActivatePriorityOverrideParams{
		EventID: event.ID,
		Target: DisplayOverrideTarget{
			Type: DisplayOverrideTargetProgramChannel,
			ID:   ceremony.ID,
		},
		Kind: DisplayOverrideUrgentNotice, Presentation: DisplayOverrideOverlay,
		Text: "Overlay", UntilCleared: true, Now: startedAt.Add(time.Second),
	})
	assertRevealPause(t, client, ceremony.ID, time.Time{}, 0)

	activatePriorityOverride(t, installation, producerContext, ActivatePriorityOverrideParams{
		EventID: event.ID,
		Target: DisplayOverrideTarget{
			Type: DisplayOverrideTargetDisplay,
			ID:   displayIDs[0],
		},
		Kind: DisplayOverrideUrgentNotice, Presentation: DisplayOverrideReplace,
		Text: "Partial", UntilCleared: true, Now: startedAt.Add(2 * time.Second),
	})
	assertRevealPause(t, client, ceremony.ID, time.Time{}, 0)

	full := activatePriorityOverride(
		t,
		installation,
		producerContext,
		ActivatePriorityOverrideParams{
			EventID: event.ID,
			Target: DisplayOverrideTarget{
				Type: DisplayOverrideTargetProgramChannel,
				ID:   ceremony.ID,
			},
			Kind: DisplayOverrideUrgentNotice, Presentation: DisplayOverrideReplace,
			Text: "Full", UntilCleared: true, Now: startedAt.Add(3 * time.Second),
		},
	)
	assertRevealPause(t, client, ceremony.ID, startedAt.Add(3*time.Second), 0)

	clearCommand := beginCommand(t, installation, producerContext)
	if _, err := clearCommand.ClearDisplayOverride(
		producerContext,
		event.ID,
		full.ID,
		full.Revision,
		startedAt.Add(13*time.Second),
		false,
	); err != nil {
		t.Fatalf("clear full Replace Override: %v", err)
	}
	if err := clearCommand.Commit(); err != nil {
		t.Fatalf("commit full Replace clear: %v", err)
	}
	assertRevealPause(t, client, ceremony.ID, time.Time{}, 10*time.Second)
	state := client.Prizegiving.Query().OnlyX(ctx).ItemStates[0]
	if early := state.EffectiveAt(startedAt.Add(14 * time.Second)); early.Status !=
		prizegivingvalue.StageRevealing {
		t.Fatalf("early resumed Reveal = %+v", early)
	}
	if complete := state.EffectiveAt(startedAt.Add(15 * time.Second)); complete.Status !=
		prizegivingvalue.StageRevealed {
		t.Fatalf("completed resumed Reveal = %+v", complete)
	}
	activatePriorityOverride(t, installation, producerContext, ActivatePriorityOverrideParams{
		EventID: event.ID,
		Target: DisplayOverrideTarget{
			Type: DisplayOverrideTargetProgramChannel,
			ID:   ceremony.ID,
		},
		Kind: DisplayOverrideUrgentNotice, Presentation: DisplayOverrideReplace,
		Text: "After completion", UntilCleared: true, Now: startedAt.Add(20 * time.Second),
	})
	assertRevealPause(t, client, ceremony.ID, time.Time{}, 10*time.Second)
	storedSession := client.Session.GetX(ctx, ceremony.ID)
	storedRun := storedSession.QueryRuns().OnlyX(ctx)
	if storedSession.ProgramOutputRevision != 2 ||
		storedSession.Lifecycle != session.LifecycleLive ||
		storedRun.ActualStart != time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC) ||
		!storedRun.ActualEnd.IsZero() {
		t.Fatalf(
			"Reveal Override changed Session timing: session=%+v run=%+v",
			storedSession,
			storedRun,
		)
	}
}

func activatePriorityOverride(
	t *testing.T,
	installation *SQLite,
	ctx context.Context,
	params ActivatePriorityOverrideParams,
) DisplayOverride {
	t.Helper()
	transaction := beginCommand(t, installation, ctx)
	activated, err := transaction.ActivatePriorityOverride(ctx, params)
	if err != nil {
		t.Fatalf("activate %s Override: %v", params.Presentation, err)
	}
	if err = transaction.Commit(); err != nil {
		t.Fatalf("commit %s Override: %v", params.Presentation, err)
	}
	return activated
}

func assertRevealPause(
	t *testing.T,
	client *ent.Client,
	ceremonyID int,
	pausedAt time.Time,
	pausedDuration time.Duration,
) {
	t.Helper()
	state := client.Prizegiving.Query().
		Where(prizegiving.CeremonySessionIDEQ(ceremonyID)).
		OnlyX(systemContext(t.Context())).
		ItemStates[0]
	if state.RevealPausedAt != pausedAt ||
		time.Duration(state.RevealPausedNanos) != pausedDuration {
		t.Fatalf("Reveal pause state = %+v", state)
	}
}
