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
		producerContext, event.ID, third.ID, third.Revision, now.Add(3*time.Second), false,
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
		producerContext, event.ID, replacement.ID, replacement.Revision, now.Add(2*time.Second), false,
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

func TestCommandEvidenceProbeRollsBackAllRows(t *testing.T) {
	client := openEntTestClient(t)
	installationStore := &SQLite{client: client}
	internalContext := systemContext(t.Context())
	if err := installationStore.ProbeCommandEvidence(
		t.Context(),
		time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC),
	); err != nil {
		t.Fatalf("probe command evidence: %v", err)
	}
	if receipts := client.CommandReceipt.Query().CountX(internalContext); receipts != 0 {
		t.Fatalf("probe Command Receipts = %d, want 0", receipts)
	}
	if audits := client.AuditEntry.Query().CountX(internalContext); audits != 0 {
		t.Fatalf("probe Audit Entries = %d, want 0", audits)
	}
}

func TestPersistDegradedEmergencyAlertKeepsProcessIdentityAndClearOrder(t *testing.T) {
	client := openEntTestClient(t)
	installationStore := &SQLite{client: client}
	internalContext := systemContext(t.Context())
	event := createSchemaTestEvent(t, client)
	account := client.Account.Create().
		SetName("Ada Admin").
		SetNormalizedName("ada admin").
		SetAdministrator(true).
		SaveX(internalContext)
	now := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)
	activated := DisplayOverride{
		ID: 1_000_000_001, EventID: event.ID,
		TargetGroupKey:     "event",
		Target:             DisplayOverrideTarget{Type: DisplayOverrideTargetEvent},
		Kind:               DisplayOverrideEmergencyAlert,
		Presentation:       DisplayOverrideReplace,
		Text:               "Evacuate using marked exits",
		Emphasis:           StageMessageNormal,
		UntilCleared:       true,
		Revision:           1,
		CreatedByAccountID: account.ID,
		CreatedAt:          now,
		Nondurable:         true,
	}
	activateTx := beginCommand(t, installationStore, internalContext)
	persisted, err := activateTx.PersistDegradedEmergencyAlert(
		internalContext,
		activated,
	)
	if err != nil {
		t.Fatalf("persist degraded Emergency activation: %v", err)
	}
	if commitErr := activateTx.Commit(); commitErr != nil {
		t.Fatalf("commit degraded Emergency activation: %v", commitErr)
	}
	if persisted.ID != activated.ID || persisted.Revision != activated.Revision ||
		persisted.Nondurable {
		t.Fatalf("persisted degraded Emergency activation = %+v", persisted)
	}

	cleared := activated
	cleared.Revision = 2
	cleared.ClearedAt = now.Add(time.Minute)
	clearTx := beginCommand(t, installationStore, internalContext)
	persisted, err = clearTx.PersistDegradedEmergencyAlert(
		internalContext,
		cleared,
	)
	if err != nil {
		t.Fatalf("persist degraded Emergency clear: %v", err)
	}
	if err := clearTx.Commit(); err != nil {
		t.Fatalf("commit degraded Emergency clear: %v", err)
	}
	if persisted.ID != activated.ID || persisted.Revision != 2 ||
		!persisted.ClearedAt.Equal(cleared.ClearedAt) || persisted.Nondurable {
		t.Fatalf("persisted degraded Emergency clear = %+v", persisted)
	}
	if count := client.DisplayOverride.Query().CountX(internalContext); count != 1 {
		t.Fatalf("persisted degraded Emergency rows = %d, want 1", count)
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

func TestPriorityOverridesResolveTargetsAndRequireEmergencyConfirmation(t *testing.T) {
	client := openEntTestClient(t)
	installationStore := &SQLite{client: client}
	internalContext := systemContext(t.Context())
	event := createSchemaTestEvent(t, client)
	client.Installation.Create().SetActiveEventID(event.ID).SaveX(internalContext)
	location := client.Location.Create().SetEventID(event.ID).SaveX(internalContext)
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	crewDisplay := client.Display.Create().SetName("Crew").SetEnrolledAt(now).SaveX(internalContext)
	publicDisplay := client.Display.Create().SetName("Public").SetEnrolledAt(now).SaveX(internalContext)
	for _, assignment := range []struct {
		displayID int
		viewKey   string
	}{
		{crewDisplay.ID, "stage-timer"},
		{publicDisplay.ID, "event-overview"},
	} {
		client.DisplayAssignment.Create().
			SetDisplayID(assignment.displayID).
			SetEventID(event.ID).
			SetLocationID(location.ID).
			SetViewKey(assignment.viewKey).
			SetDisplayGroupKeys([]string{"venue"}).
			SaveX(internalContext)
	}
	producerContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID: 1, EventRoles: map[int]viewer.Role{event.ID: viewer.Producer},
	})
	for _, test := range []struct {
		target DisplayOverrideTarget
		want   int
	}{
		{DisplayOverrideTarget{Type: DisplayOverrideTargetEvent}, 2},
		{DisplayOverrideTarget{Type: DisplayOverrideTargetPublic}, 1},
		{DisplayOverrideTarget{Type: DisplayOverrideTargetCrew}, 1},
		{DisplayOverrideTarget{Type: DisplayOverrideTargetLocation, ID: location.ID}, 2},
		{DisplayOverrideTarget{Type: DisplayOverrideTargetDisplayGroup, Key: "venue"}, 2},
		{DisplayOverrideTarget{Type: DisplayOverrideTargetDisplay, ID: crewDisplay.ID}, 1},
	} {
		preview, err := installationStore.PreviewPriorityOverride(
			producerContext,
			ActivatePriorityOverrideParams{
				EventID: event.ID, Target: test.target, Kind: DisplayOverrideUrgentNotice,
				Presentation: DisplayOverrideOverlay, Text: "Notice",
				UntilCleared: true, Now: now,
			},
		)
		if err != nil || len(preview.Displays) != test.want {
			t.Fatalf("preview target %+v = %+v, %v; want %d Displays", test.target, preview, err, test.want)
		}
	}
	urgentTx := beginCommand(t, installationStore, producerContext)
	urgent, err := urgentTx.ActivatePriorityOverride(
		producerContext,
		ActivatePriorityOverrideParams{
			EventID: event.ID, Target: DisplayOverrideTarget{Type: DisplayOverrideTargetEvent},
			Kind: DisplayOverrideUrgentNotice, Presentation: DisplayOverrideOverlay,
			Text: "Urgent", UntilCleared: true, Now: now,
		},
	)
	if err != nil {
		t.Fatalf("activate Urgent Notice: %v", err)
	}
	if err = urgentTx.Commit(); err != nil {
		t.Fatalf("commit Urgent Notice: %v", err)
	}
	emergencyParams := ActivatePriorityOverrideParams{
		EventID: event.ID, Target: DisplayOverrideTarget{Type: DisplayOverrideTargetEvent},
		Kind: DisplayOverrideEmergencyAlert, Presentation: DisplayOverrideReplace,
		Text: "Evacuate", UntilCleared: true, Now: now.Add(time.Second),
	}
	emergencyPreview, err := installationStore.PreviewPriorityOverride(
		producerContext, emergencyParams,
	)
	if err != nil {
		t.Fatalf("preview Emergency Alert: %v", err)
	}
	emergencyParams.ConfirmationFingerprint = DisplayOverridePreviewFingerprint(emergencyPreview)
	addedDisplay := client.Display.Create().
		SetName("Added after preview").
		SetEnrolledAt(now).
		SaveX(internalContext)
	client.DisplayAssignment.Create().
		SetDisplayID(addedDisplay.ID).
		SetEventID(event.ID).
		SetLocationID(location.ID).
		SetViewKey("event-overview").
		SaveX(internalContext)
	staleTx := beginCommand(t, installationStore, producerContext)
	if _, err = staleTx.ActivatePriorityOverride(
		producerContext, emergencyParams,
	); !errors.Is(err, ErrDisplayOverrideRevision) {
		t.Fatalf("stale Emergency target confirmation error = %v", err)
	}
	_ = staleTx.Rollback()
	emergencyPreview, err = installationStore.PreviewPriorityOverride(
		producerContext, emergencyParams,
	)
	if err != nil {
		t.Fatalf("refresh Emergency Alert preview: %v", err)
	}
	emergencyParams.ConfirmationFingerprint = DisplayOverridePreviewFingerprint(emergencyPreview)
	emergencyTx := beginCommand(t, installationStore, producerContext)
	emergency, err := emergencyTx.ActivatePriorityOverride(
		producerContext,
		emergencyParams,
	)
	if err != nil {
		t.Fatalf("activate Emergency Alert: %v", err)
	}
	if err = emergencyTx.Commit(); err != nil {
		t.Fatalf("commit Emergency Alert: %v", err)
	}
	assignment := client.DisplayAssignment.Query().Where(
		displayassignment.DisplayIDEQ(publicDisplay.ID),
	).OnlyX(internalContext)
	var snapshot DisplaySnapshotState
	if err = loadCurrentDisplayOverrides(
		internalContext, client, assignment, now.Add(2*time.Second), &snapshot,
	); err != nil {
		t.Fatalf("load priority Overrides: %v", err)
	}
	if snapshot.UrgentNotice == nil || snapshot.UrgentNotice.ID != urgent.ID ||
		snapshot.EmergencyAlert == nil || snapshot.EmergencyAlert.ID != emergency.ID {
		t.Fatalf("priority Override stack = %+v", snapshot)
	}
	unconfirmed := beginCommand(t, installationStore, producerContext)
	if _, err = unconfirmed.ClearDisplayOverride(
		producerContext, event.ID, emergency.ID, emergency.Revision,
		now.Add(3*time.Second), false,
	); !errors.Is(err, ErrDisplayOverrideInput) {
		t.Fatalf("unconfirmed Emergency clear error = %v", err)
	}
	_ = unconfirmed.Rollback()
	confirmed := beginCommand(t, installationStore, producerContext)
	if _, err = confirmed.ClearDisplayOverride(
		producerContext, event.ID, emergency.ID, emergency.Revision,
		now.Add(3*time.Second), true,
	); err != nil {
		t.Fatalf("confirmed Emergency clear: %v", err)
	}
	if err = confirmed.Commit(); err != nil {
		t.Fatalf("commit Emergency clear: %v", err)
	}
	snapshot = DisplaySnapshotState{}
	if err = loadCurrentDisplayOverrides(
		internalContext, client, assignment, now.Add(4*time.Second), &snapshot,
	); err != nil || snapshot.EmergencyAlert != nil ||
		snapshot.UrgentNotice == nil || snapshot.UrgentNotice.ID != urgent.ID {
		t.Fatalf("underlying Urgent Notice after Emergency clear = %+v, %v", snapshot, err)
	}
}
