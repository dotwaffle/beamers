package store

import (
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/ent/displayassignment"
	"github.com/dotwaffle/beamers/ent/displayoverridestate"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestStageMessageTargetsCrewAndReplacesCurrentMessage(t *testing.T) {
	client := openEntTestClient(t)
	installationStore := &SQLite{client: client}
	internalContext := systemContext(t.Context())
	event := createSchemaTestEvent(t, client)
	client.Installation.Create().SetActiveEventID(event.ID).SaveX(internalContext)
	location := client.Location.Create().
		SetEventID(event.ID).
		SaveX(internalContext)
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	crewDisplay := client.Display.Create().
		SetName("Stage").
		SetEnrolledAt(now).
		SaveX(internalContext)
	publicDisplay := client.Display.Create().
		SetName("Lobby").
		SetEnrolledAt(now).
		SaveX(internalContext)
	for _, assignment := range []struct {
		displayID int
		viewKey   string
	}{
		{displayID: crewDisplay.ID, viewKey: "stage-timer"},
		{displayID: publicDisplay.ID, viewKey: "event-overview"},
	} {
		client.DisplayAssignment.Create().
			SetDisplayID(assignment.displayID).
			SetEventID(event.ID).
			SetLocationID(location.ID).
			SetViewKey(assignment.viewKey).
			SetDisplayGroupKeys([]string{"stage-a", "stage-b"}).
			SaveX(internalContext)
	}
	producerContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID: 1, EventRoles: map[int]viewer.Role{event.ID: viewer.Producer},
	})
	configure := beginCommand(t, installationStore, producerContext)
	if _, err := configure.ConfigureStageMessages(
		producerContext,
		ConfigureStageMessagesParams{
			EventID: event.ID, ExpectedRevision: 0, DefaultDurationSeconds: 10,
			Presets: []StageMessagePreset{{
				Key: "wrap", Text: "Please wrap up", TargetGroupKey: "stage-a",
				DurationSeconds: 15, Emphasis: StageMessageAttention,
			}},
		},
	); err != nil {
		t.Fatalf("configure Stage Message preset: %v", err)
	}
	if err := configure.Commit(); err != nil {
		t.Fatalf("commit Stage Message preset: %v", err)
	}
	preview, err := installationStore.PreviewStageMessage(
		producerContext,
		ActivateStageMessageParams{
			EventID: event.ID, PresetKey: "wrap", UntilCleared: true, Now: now,
		},
	)
	if err != nil || len(preview.Displays) != 1 ||
		preview.Displays[0].ID != crewDisplay.ID {
		t.Fatalf("Stage Message preview = %+v, %v", preview, err)
	}
	firstTx := beginCommand(t, installationStore, producerContext)
	first, err := firstTx.ActivateStageMessage(
		producerContext,
		ActivateStageMessageParams{
			EventID: event.ID, PresetKey: "wrap", UntilCleared: true, Now: now,
		},
	)
	if err != nil {
		t.Fatalf("activate preset Stage Message: %v", err)
	}
	if err = firstTx.Commit(); err != nil {
		t.Fatalf("commit preset Stage Message: %v", err)
	}
	states := client.DisplayOverrideState.Query().AllX(internalContext)
	if len(states) != 1 || states[0].DisplayID != crewDisplay.ID ||
		states[0].OverrideID != first.ID {
		t.Fatalf("Stage Message states = %+v", states)
	}
	secondTx := beginCommand(t, installationStore, producerContext)
	second, err := secondTx.ActivateStageMessage(
		producerContext,
		ActivateStageMessageParams{
			EventID: event.ID, Text: "Stop now", TargetGroupKey: "stage-b",
			Emphasis: StageMessageUrgent, UntilCleared: true, Now: now.Add(time.Second),
		},
	)
	if err != nil {
		t.Fatalf("activate replacement Stage Message: %v", err)
	}
	if err = secondTx.Commit(); err != nil {
		t.Fatalf("commit replacement Stage Message: %v", err)
	}
	state := client.DisplayOverrideState.Query().Where(
		displayoverridestate.DisplayIDEQ(crewDisplay.ID),
	).OnlyX(internalContext)
	if state.OverrideID != second.ID {
		t.Fatalf("replacement Stage Message state = %+v", state)
	}
	crewAssignment := client.DisplayAssignment.Query().Where(
		displayassignment.DisplayIDEQ(crewDisplay.ID),
	).OnlyX(internalContext)
	crewAssignment.Update().
		SetDisplayGroupKeys([]string{"stage-a"}).
		SaveX(internalContext)
	thirdTx := beginCommand(t, installationStore, producerContext)
	third, err := thirdTx.ActivateStageMessage(
		producerContext,
		ActivateStageMessageParams{
			EventID: event.ID, Text: "New backstage message", TargetGroupKey: "stage-b",
			Emphasis: StageMessageAttention, UntilCleared: true, Now: now.Add(2 * time.Second),
		},
	)
	if err != nil {
		t.Fatalf("replace Stage Message while Display is outside group: %v", err)
	}
	if err = thirdTx.Commit(); err != nil {
		t.Fatalf("commit out-of-group replacement Stage Message: %v", err)
	}
	state = client.DisplayOverrideState.Query().Where(
		displayoverridestate.DisplayIDEQ(crewDisplay.ID),
	).OnlyX(internalContext)
	if state.OverrideID != second.ID {
		t.Fatalf("out-of-group replacement erased Stage Message floor: %+v", state)
	}
	clearTx := beginCommand(t, installationStore, producerContext)
	if _, err = clearTx.ClearDisplayOverride(
		producerContext, event.ID, third.ID, third.Revision, now.Add(3*time.Second),
	); err != nil {
		t.Fatalf("clear replacement Stage Message: %v", err)
	}
	if err = clearTx.Commit(); err != nil {
		t.Fatalf("commit cleared Stage Message: %v", err)
	}
	crewAssignment.Update().
		SetDisplayGroupKeys([]string{"stage-a", "stage-b"}).
		SaveX(internalContext)
	resync := beginCommand(t, installationStore, producerContext)
	if err = resync.syncDisplayOverridesForAssignment(
		producerContext,
		DisplayAssignment{
			DisplayID: crewDisplay.ID, EventID: event.ID, LocationID: location.ID,
			ViewKey: "stage-timer", DisplayGroupKeys: []string{"stage-a", "stage-b"},
		},
		now.Add(4*time.Second),
	); err != nil {
		t.Fatalf("resync cleared replacement Stage Message: %v", err)
	}
	if err = resync.Commit(); err != nil {
		t.Fatalf("commit Stage Message resync: %v", err)
	}
	state = client.DisplayOverrideState.Query().Where(
		displayoverridestate.DisplayIDEQ(crewDisplay.ID),
	).OnlyX(internalContext)
	if state.OverrideID != second.ID {
		t.Fatalf("clearing replacement revealed queued Stage Message; state = %+v", state)
	}
}

func TestStageMessageConfigurationReportsCorruptPresetJSON(t *testing.T) {
	client := openEntTestClient(t)
	installationStore := &SQLite{client: client}
	internalContext := systemContext(t.Context())
	event := createSchemaTestEvent(t, client)
	client.Event.UpdateOne(event).
		SetStageMessagePresets("{").
		SetStageMessageConfigurationRevision(1).
		SaveX(internalContext)
	producerContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID: 1, EventRoles: map[int]viewer.Role{event.ID: viewer.Producer},
	})
	transaction := beginCommand(t, installationStore, producerContext)
	_, err := transaction.ConfigureStageMessages(
		producerContext,
		ConfigureStageMessagesParams{
			EventID: event.ID, ExpectedRevision: 0, DefaultDurationSeconds: 10,
		},
	)
	if err == nil || errors.Is(err, ErrStageMessageConfigurationRevision) {
		t.Fatalf("corrupt Stage Message presets error = %v", err)
	}
}

func TestTechnicalDifficultiesCanTargetPublicAndCrewDisplays(t *testing.T) {
	client := openEntTestClient(t)
	installationStore := &SQLite{client: client}
	internalContext := systemContext(t.Context())
	event := createSchemaTestEvent(t, client)
	client.Installation.Create().SetActiveEventID(event.ID).SaveX(internalContext)
	location := client.Location.Create().SetEventID(event.ID).SaveX(internalContext)
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	for _, viewKey := range []string{"stage-timer", "event-overview"} {
		display := client.Display.Create().
			SetName(viewKey).
			SetEnrolledAt(now).
			SaveX(internalContext)
		client.DisplayAssignment.Create().
			SetDisplayID(display.ID).
			SetEventID(event.ID).
			SetLocationID(location.ID).
			SetViewKey(viewKey).
			SetDisplayGroupKeys([]string{"venue"}).
			SaveX(internalContext)
	}
	producerContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID: 1, EventRoles: map[int]viewer.Role{event.ID: viewer.Producer},
	})
	transaction := beginCommand(t, installationStore, producerContext)
	activated, err := transaction.ActivateTechnicalDifficulties(
		producerContext,
		ActivateTechnicalDifficultiesParams{
			EventID: event.ID, TargetGroupKey: "venue",
			UntilCleared: true, Now: now,
		},
	)
	if err != nil {
		t.Fatalf("activate Technical Difficulties: %v", err)
	}
	if err = transaction.Commit(); err != nil {
		t.Fatalf("commit Technical Difficulties: %v", err)
	}
	if activated.Kind != DisplayOverrideTechnicalDifficulties {
		t.Fatalf("Technical Difficulties = %+v", activated)
	}
	if count := client.DisplayOverrideState.Query().CountX(internalContext); count != 2 {
		t.Fatalf("Technical Difficulties states = %d, want 2", count)
	}
	replacementTx := beginCommand(t, installationStore, producerContext)
	replacement, err := replacementTx.ActivateTechnicalDifficulties(
		producerContext,
		ActivateTechnicalDifficultiesParams{
			EventID: event.ID, TargetGroupKey: "venue", Text: "New message",
			UntilCleared: true, Now: now.Add(time.Second),
		},
	)
	if err != nil {
		t.Fatalf("activate replacement Technical Difficulties: %v", err)
	}
	if err = replacementTx.Commit(); err != nil {
		t.Fatalf("commit replacement Technical Difficulties: %v", err)
	}
	clearTx := beginCommand(t, installationStore, producerContext)
	if _, err = clearTx.ClearDisplayOverride(
		producerContext, event.ID, replacement.ID, replacement.Revision, now.Add(2*time.Second),
	); err != nil {
		t.Fatalf("clear replacement Technical Difficulties: %v", err)
	}
	if err = clearTx.Commit(); err != nil {
		t.Fatalf("commit cleared Technical Difficulties: %v", err)
	}
	assignment := client.DisplayAssignment.Query().FirstX(internalContext)
	var snapshot DisplaySnapshotState
	if err = loadCurrentDisplayOverrides(
		internalContext, client, assignment, now.Add(3*time.Second), &snapshot,
	); err != nil {
		t.Fatalf("load restored Technical Difficulties: %v", err)
	}
	if snapshot.TechnicalDifficulties == nil ||
		snapshot.TechnicalDifficulties.ID != activated.ID {
		t.Fatalf("restored Technical Difficulties = %+v", snapshot.TechnicalDifficulties)
	}
}

func TestSameCursorAcknowledgmentAllowsOnlyOverrideExpiry(t *testing.T) {
	current := DisplayAcknowledgment{
		ProtocolVersion: "v1", AssetVersion: "asset", StreamID: "stream",
		StreamPosition: 3, ActiveEventID: 1, ActivationGeneration: 2,
		PublishedRevision: 4, StageMessageID: 7, StageMessageRevision: 1,
		TechnicalDifficultiesID: 8, TechnicalDifficultiesRevision: 2,
	}
	expired := current
	expired.StageMessageID, expired.StageMessageRevision = 0, 0
	if !sameStateWithExpiredOverrides(current, expired) {
		t.Fatal("same-cursor Stage Message expiry was rejected")
	}
	if sameStateWithExpiredOverrides(expired, current) {
		t.Fatal("same-cursor stale Stage Message was accepted after expiry")
	}
	changed := current
	changed.StageMessageID = 9
	if sameStateWithExpiredOverrides(current, changed) {
		t.Fatal("same-cursor replacement Stage Message was accepted")
	}
}
