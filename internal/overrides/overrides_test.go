package overrides

import (
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	_ "github.com/dotwaffle/beamers/ent/runtime"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/store/storetest"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestTechnicalDifficultiesRejectsDurationBeforeConversion(t *testing.T) {
	service := &Service{}
	_, err := service.ActivateTechnicalDifficulties(
		t.Context(),
		auth.Account{},
		TechnicalDifficultiesInput{
			EventID: 1, TargetGroupKey: "crew",
			DurationSeconds: int(^uint(0) >> 1),
		},
	)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("large Technical Difficulties duration error = %v", err)
	}
}

func TestEmergencyAlertDegradesWithoutOpeningOtherMutationPaths(t *testing.T) {
	now := time.Date(2026, time.July, 24, 11, 0, 0, 0, time.UTC)
	dataDir := t.TempDir()
	if err := store.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize Override storage: %v", err)
	}
	storage, err := store.Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open Override storage: %v", err)
	}
	service, err := New(t.Context(), storage, func() time.Time {
		return now
	})
	if err != nil {
		t.Fatalf("create Override service: %v", err)
	}
	const displayKey = "display-credential-hash"
	healthy := store.DisplaySnapshotState{
		Display:       store.Display{ID: 7, Name: "Main Hall"},
		ActiveEventID: 1,
		LocationID:    2,
		LocationName:  "Main Hall",
		ViewKey:       "event-overview",
		DisplayGroupKeys: []string{
			"venue",
		},
	}
	initialProjection, projectionErr := service.ProjectDisplaySnapshot(
		displayKey,
		healthy,
		nil,
	)
	if projectionErr != nil || initialProjection.EmergencyAlert != nil {
		t.Fatalf("cache healthy Display Snapshot = %+v, %v", initialProjection, projectionErr)
	}
	if closeErr := storage.Close(); closeErr != nil {
		t.Fatalf("fail Override storage: %v", closeErr)
	}
	producer := auth.Account{
		ID:         3,
		EventRoles: map[int]viewer.Role{1: viewer.Producer},
	}
	input := PriorityInput{
		EventID: 1,
		Target:  Target{Type: store.DisplayOverrideTargetEvent},
		Text:    "Evacuate using marked exits",
	}
	preview, err := service.PreviewEmergencyAlert(t.Context(), producer, input)
	if err != nil {
		t.Fatalf("preview degraded Emergency Alert: %v", err)
	}
	if !preview.Nondurable || len(preview.Displays) != 1 ||
		preview.Displays[0].ID != healthy.Display.ID {
		t.Fatalf("degraded Emergency preview = %+v", preview)
	}
	if preview.ConfirmationFingerprint ==
		store.DisplayOverridePreviewFingerprint(preview.Preview) {
		t.Fatal("degraded confirmation reused the durable fingerprint")
	}
	input.PreviewFingerprint = preview.ConfirmationFingerprint
	input.Confirmed = true
	input.ConfirmationMethod = "Keyboard"
	input.CommandID = "degraded-emergency"
	activated, err := service.ActivateEmergencyAlert(t.Context(), producer, input)
	if err != nil {
		t.Fatalf("activate degraded Emergency Alert: %v", err)
	}
	if !activated.Nondurable || activated.ID <= 0 || activated.Revision != 1 {
		t.Fatalf("degraded Emergency activation = %+v", activated)
	}
	replayed, err := service.ActivateEmergencyAlert(t.Context(), producer, input)
	if err != nil || !reflect.DeepEqual(replayed, activated) {
		t.Fatalf("replay degraded Emergency activation = %+v, %v", replayed, err)
	}
	secondActivation := input
	secondActivation.CommandID = "second-active-emergency"
	if _, secondErr := service.ActivateEmergencyAlert(
		t.Context(),
		producer,
		secondActivation,
	); !errors.Is(secondErr, ErrRevision) {
		t.Fatalf("second active degraded Emergency error = %v", secondErr)
	}
	conflicting := input
	conflicting.Text = "Different work"
	_, conflictErr := service.ActivateEmergencyAlert(
		t.Context(),
		producer,
		conflicting,
	)
	if !errors.Is(conflictErr, ErrCommandConflict) {
		t.Fatalf("conflicting degraded Emergency command error = %v", conflictErr)
	}
	projected, err := service.ProjectDisplaySnapshot(
		displayKey,
		store.DisplaySnapshotState{},
		errors.New("storage unavailable"),
	)
	if err != nil || projected.EmergencyAlert == nil ||
		projected.EmergencyAlert.ID != activated.ID {
		t.Fatalf("degraded Display Snapshot = %+v, %v", projected, err)
	}
	_, projectionErr = service.ProjectDisplaySnapshot(
		"new-display",
		healthy,
		nil,
	)
	if projectionErr == nil {
		t.Fatal("Display without a pre-failure snapshot expanded degraded targets")
	}

	_, ordinaryErr := service.SendStageMessage(
		t.Context(),
		producer,
		SendStageMessageInput{
			EventID: 1, Text: "not allowed", TargetGroupKey: "venue",
			UntilCleared: true, CommandID: "degraded-stage-message",
		},
	)
	if ordinaryErr == nil {
		t.Fatal("ordinary mutation succeeded without storage")
	}
	operatorWithoutCapability := auth.Account{
		ID:         4,
		EventRoles: map[int]viewer.Role{1: viewer.Operator},
		EventScopes: map[int]viewer.EventScope{
			1: {
				DisplayGroupKeys: map[string]struct{}{"venue": {}},
			},
		},
	}
	unauthorized := input
	unauthorized.CommandID = "unauthorized-emergency"
	_, unauthorizedErr := service.ActivateEmergencyAlert(
		t.Context(),
		operatorWithoutCapability,
		unauthorized,
	)
	if !errors.Is(unauthorizedErr, ErrScopeDenied) {
		t.Fatalf("unauthorized degraded Emergency error = %v", unauthorizedErr)
	}

	cleared, err := service.Clear(t.Context(), producer, ClearInput{
		EventID: 1, OverrideID: activated.ID, ExpectedRevision: activated.Revision,
		CommandID: "clear-degraded-emergency", Confirmed: true,
		ConfirmationMethod: "Keyboard",
	})
	if err != nil {
		t.Fatalf("clear degraded Emergency Alert: %v", err)
	}
	if !cleared.Nondurable || cleared.Revision != 2 || cleared.ClearedAt.IsZero() {
		t.Fatalf("cleared degraded Emergency Alert = %+v", cleared)
	}
	projected, err = service.ProjectDisplaySnapshot(
		displayKey,
		store.DisplaySnapshotState{},
		errors.New("storage unavailable"),
	)
	if err != nil || projected.EmergencyAlert != nil {
		t.Fatalf("Display Snapshot after degraded clear = %+v, %v", projected, err)
	}
}

func TestRecoverEndsPreviewOnlyDegradation(t *testing.T) {
	dataDir := t.TempDir()
	if err := store.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize preview recovery storage: %v", err)
	}
	storage, err := store.Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open preview recovery storage: %v", err)
	}
	service, err := New(t.Context(), storage, time.Now)
	if err != nil {
		t.Fatalf("create preview recovery service: %v", err)
	}
	_, err = service.ProjectDisplaySnapshot("preview-display", store.DisplaySnapshotState{
		Display:       store.Display{ID: 8, Name: "Preview Display"},
		ActiveEventID: 1,
		LocationID:    3,
		ViewKey:       "event-overview",
	}, nil)
	if err != nil {
		t.Fatalf("cache preview recovery Display: %v", err)
	}
	if closeErr := storage.Close(); closeErr != nil {
		t.Fatalf("fail preview recovery storage: %v", closeErr)
	}
	actor := auth.Account{
		ID:         4,
		EventRoles: map[int]viewer.Role{1: viewer.Producer},
	}
	if _, err = service.PreviewEmergencyAlert(t.Context(), actor, PriorityInput{
		EventID: 1,
		Target:  Target{Type: store.DisplayOverrideTargetEvent},
		Text:    "Preview without activation",
	}); err != nil {
		t.Fatalf("preview degraded Emergency: %v", err)
	}
	if !service.Degraded() {
		t.Fatal("failed preview did not enter degraded operation")
	}

	recoveredStorage, err := store.Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("reopen preview recovery storage: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := recoveredStorage.Close(); closeErr != nil {
			t.Errorf("close preview recovery storage: %v", closeErr)
		}
	})
	service.storage = recoveredStorage
	databasePath := filepath.Join(dataDir, "beamers.db")
	if err = storetest.FailCommandEvidence(t.Context(), databasePath); err != nil {
		t.Fatalf("retain preview recovery failure: %v", err)
	}
	recovered, err := service.Recover(t.Context())
	if err == nil || recovered {
		t.Fatalf("recover while evidence remains unavailable = %t, %v", recovered, err)
	}
	if !service.Degraded() {
		t.Fatal("failed recovery cleared preview-only degradation")
	}
	if err = storetest.AllowCommandEvidence(t.Context(), databasePath); err != nil {
		t.Fatalf("restore preview recovery storage: %v", err)
	}
	recovered, err = service.Recover(t.Context())
	if err != nil || recovered {
		t.Fatalf("recover preview-only degradation = %t, %v", recovered, err)
	}
	if service.Degraded() {
		t.Fatal("preview-only degradation remained after storage recovery")
	}
}

func TestRecoverPersistsDegradedEmergencyEvidenceExactlyOnce(t *testing.T) {
	now := time.Date(2026, time.July, 24, 13, 0, 0, 0, time.UTC)
	dataDir := t.TempDir()
	if err := store.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize recovery storage: %v", err)
	}
	storage, err := store.Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open recovery storage: %v", err)
	}
	authentication, err := auth.New(storage, auth.DefaultConfig())
	if err != nil {
		t.Fatalf("create authentication service: %v", err)
	}
	bootstrap, err := authentication.IssueBootstrap(t.Context())
	if err != nil {
		t.Fatalf("issue bootstrap: %v", err)
	}
	session, err := authentication.BootstrapAdministrator(
		t.Context(),
		bootstrap,
		"Ada Admin",
		"correct horse battery staple",
	)
	if err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	eventService, err := events.New(storage, func() time.Time {
		return now
	})
	if err != nil {
		t.Fatalf("create Event service: %v", err)
	}
	event, err := eventService.Create(t.Context(), session.Account, events.CreateInput{
		Name: "Recovery Event", PlannedStartDate: "2026-08-21",
		PlannedEndDate: "2026-08-23", Timezone: "Europe/Berlin",
		EventLocale: "de-DE", EventDayBoundary: "06:00",
		CommandID: "create-recovery-event",
	})
	if err != nil {
		t.Fatalf("create recovery Event: %v", err)
	}
	actor := session.Account
	actor.EventRoles = map[int]viewer.Role{event.ID: viewer.Producer}
	service, err := New(t.Context(), storage, func() time.Time {
		return now
	})
	if err != nil {
		t.Fatalf("create Override service: %v", err)
	}
	const displayKey = "recovery-display"
	_, err = service.ProjectDisplaySnapshot(displayKey, store.DisplaySnapshotState{
		Display:       store.Display{ID: 9, Name: "Recovery Display"},
		ActiveEventID: event.ID,
		LocationID:    4,
		ViewKey:       "event-overview",
	}, nil)
	if err != nil {
		t.Fatalf("cache recovery Display: %v", err)
	}
	if closeErr := storage.Close(); closeErr != nil {
		t.Fatalf("fail recovery storage: %v", closeErr)
	}
	input := PriorityInput{
		EventID: event.ID,
		Target:  Target{Type: store.DisplayOverrideTargetEvent},
		Text:    "Evacuate using marked exits",
	}
	preview, err := service.PreviewEmergencyAlert(t.Context(), actor, input)
	if err != nil {
		t.Fatalf("preview recovery Emergency: %v", err)
	}
	input.PreviewFingerprint = preview.ConfirmationFingerprint
	input.Confirmed = true
	input.ConfirmationMethod = "Keyboard"
	input.CommandID = "recover-emergency"
	activated, err := service.ActivateEmergencyAlert(t.Context(), actor, input)
	if err != nil {
		t.Fatalf("activate recovery Emergency: %v", err)
	}

	recoveredStorage, err := store.Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("reopen recovered storage: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := recoveredStorage.Close(); closeErr != nil {
			t.Errorf("close recovered storage: %v", closeErr)
		}
	})
	service.storage = recoveredStorage
	type recoveryResult struct {
		recovered bool
		err       error
	}
	results := make([]recoveryResult, 2)
	var wait sync.WaitGroup
	for index := range results {
		wait.Go(func() {
			results[index].recovered, results[index].err = service.Recover(t.Context())
		})
	}
	wait.Wait()
	recoveryCount := 0
	for _, result := range results {
		if result.err != nil {
			t.Fatalf("recover degraded Emergency: %v", result.err)
		}
		if result.recovered {
			recoveryCount++
		}
	}
	if recoveryCount != 1 {
		t.Fatalf("successful degraded Emergency recoveries = %d, want 1", recoveryCount)
	}
	recovered, err := service.Recover(t.Context())
	if err != nil || recovered {
		t.Fatalf("repeat degraded Emergency recovery = %t, %v", recovered, err)
	}
	replayed, err := service.ActivateEmergencyAlert(t.Context(), actor, input)
	if err != nil || replayed.ID != activated.ID {
		t.Fatalf("replay recovered Emergency = %+v, %v", replayed, err)
	}
	recoveredAuthentication, err := auth.New(recoveredStorage, auth.DefaultConfig())
	if err != nil {
		t.Fatalf("create recovered authentication service: %v", err)
	}
	audit, err := recoveredAuthentication.ListAuditEntries(t.Context(), actor)
	if err != nil {
		t.Fatalf("list recovered Audit Entries: %v", err)
	}
	emergencyEntries := 0
	for _, entry := range audit {
		if entry.Action == "ActivateEmergencyAlert" {
			emergencyEntries++
		}
	}
	if emergencyEntries != 1 {
		t.Fatalf("recovered Emergency Audit Entries = %d, want 1", emergencyEntries)
	}

	nextService, err := New(t.Context(), recoveredStorage, func() time.Time {
		return now.Add(time.Hour)
	})
	if err != nil {
		t.Fatalf("create service after recovered Emergency: %v", err)
	}
	if nextService.nextDegradedID != activated.ID {
		t.Fatalf(
			"next degraded Emergency ID floor = %d, want %d",
			nextService.nextDegradedID, activated.ID,
		)
	}
	healthyActivated := Override{ID: activated.ID + 1}
	healthyActivated, err = nextService.activateDurably(func() (Override, error) {
		return healthyActivated, nil
	})
	if err != nil {
		t.Fatalf("record healthy Override allocation: %v", err)
	}
	if nextService.nextDegradedID != healthyActivated.ID {
		t.Fatalf(
			"healthy Override ID floor = %d, want %d",
			nextService.nextDegradedID, healthyActivated.ID,
		)
	}
	_, err = nextService.ProjectDisplaySnapshot(displayKey, store.DisplaySnapshotState{
		Display:       store.Display{ID: 9, Name: "Recovery Display"},
		ActiveEventID: event.ID,
		LocationID:    4,
		ViewKey:       "event-overview",
	}, nil)
	if err != nil {
		t.Fatalf("cache next-incident Display: %v", err)
	}
	nextService.degraded = true
	nextInput := PriorityInput{
		EventID: event.ID,
		Target:  Target{Type: store.DisplayOverrideTargetEvent},
		Text:    "Second storage incident",
	}
	nextPreview, err := nextService.PreviewEmergencyAlert(t.Context(), actor, nextInput)
	if err != nil {
		t.Fatalf("preview second degraded Emergency: %v", err)
	}
	nextInput.PreviewFingerprint = nextPreview.ConfirmationFingerprint
	nextInput.Confirmed = true
	nextInput.ConfirmationMethod = "Keyboard"
	nextInput.CommandID = "second-degraded-emergency"
	nextActivated, err := nextService.ActivateEmergencyAlert(t.Context(), actor, nextInput)
	if err != nil {
		t.Fatalf("activate second degraded Emergency: %v", err)
	}
	if nextActivated.ID != healthyActivated.ID+1 {
		t.Fatalf(
			"second degraded Emergency ID = %d, want %d",
			nextActivated.ID, healthyActivated.ID+1,
		)
	}
}
