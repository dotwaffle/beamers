package overrides

import (
	"context"
	"errors"
	"maps"
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

func TestCanceledEmergencyActivationDoesNotEnterDegradedMode(t *testing.T) {
	dataDir := t.TempDir()
	if err := store.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize canceled Emergency storage: %v", err)
	}
	storage, err := store.Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open canceled Emergency storage: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := storage.Close(); closeErr != nil {
			t.Errorf("close canceled Emergency storage: %v", closeErr)
		}
	})
	service, err := New(t.Context(), storage, time.Now, nil)
	if err != nil {
		t.Fatalf("create canceled Emergency service: %v", err)
	}
	input := PriorityInput{
		EventID:   1,
		Target:    Target{Type: store.DisplayOverrideTargetEvent},
		Text:      "Evacuate using marked exits",
		Confirmed: true, ConfirmationMethod: "Keyboard",
		PreviewFingerprint: "canceled-preview", CommandID: "canceled-emergency",
	}
	for _, test := range []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		want error
	}{
		{
			name: "canceled",
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				return ctx, func() {}
			},
			want: context.Canceled,
		},
		{
			name: "deadline",
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
			},
			want: context.DeadlineExceeded,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := test.ctx()
			defer cancel()
			if _, activateErr := service.ActivateEmergencyAlert(
				ctx,
				auth.Account{ID: 1},
				input,
			); !errors.Is(activateErr, test.want) {
				t.Fatalf("Emergency activation error = %v, want %v", activateErr, test.want)
			}
			if service.Degraded() {
				t.Fatal("canceled Emergency activation entered degraded mode")
			}
		})
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
	notifications := 0
	service, err := New(t.Context(), storage, func() time.Time {
		return now
	}, func() {
		notifications++
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
	durableFingerprint, err := store.DisplayOverridePreviewFingerprint(preview.Preview)
	if err != nil {
		t.Fatalf("fingerprint degraded Emergency preview: %v", err)
	}
	if preview.ConfirmationFingerprint == durableFingerprint {
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
	if notifications != 2 {
		t.Fatalf("degraded Emergency notifications = %d, want 2", notifications)
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
	if notifications != 2 {
		t.Fatalf("rejected degraded Emergency notifications = %d, want 2", notifications)
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
	if notifications != 3 {
		t.Fatalf("degraded Emergency clear notifications = %d, want 3", notifications)
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
	service, err := New(t.Context(), storage, time.Now, nil)
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
	secondActor, err := authentication.CreateAccount(
		t.Context(),
		session.Account,
		"Recovery Operator",
		"another correct horse battery staple",
		"create-recovery-operator",
	)
	if err != nil {
		t.Fatalf("create recovery Operator: %v", err)
	}
	eventService, err := events.New(storage, func() time.Time {
		return now
	}, nil, nil)
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
	}, nil)
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
	rejected := input
	rejected.PreviewFingerprint = preview.ConfirmationFingerprint
	rejected.CommandID = "reject-degraded-emergency"
	for range 2 {
		if _, rejectedErr := service.ActivateEmergencyAlert(
			t.Context(),
			actor,
			rejected,
		); !errors.Is(rejectedErr, ErrInvalidInput) {
			t.Fatalf("reject degraded Emergency: %v", rejectedErr)
		}
	}
	crossActorConflict := rejected
	crossActorConflict.Text = "Different actor work"
	for range 2 {
		if _, conflictErr := service.ActivateEmergencyAlert(
			t.Context(),
			secondActor,
			crossActorConflict,
		); !errors.Is(conflictErr, ErrCommandConflict) {
			t.Fatalf("cross-actor degraded Emergency conflict: %v", conflictErr)
		}
	}
	preexistingConflict := rejected
	preexistingConflict.CommandID = "create-recovery-operator"
	for range 2 {
		if _, rejectedErr := service.ActivateEmergencyAlert(
			t.Context(),
			actor,
			preexistingConflict,
		); !errors.Is(rejectedErr, ErrInvalidInput) {
			t.Fatalf("queue preexisting receipt conflict: %v", rejectedErr)
		}
	}
	stale := input
	stale.PreviewFingerprint = "stale-fingerprint"
	stale.Confirmed = true
	stale.ConfirmationMethod = "Keyboard"
	stale.CommandID = "reject-stale-degraded-emergency"
	for range 2 {
		if _, staleErr := service.ActivateEmergencyAlert(
			t.Context(),
			actor,
			stale,
		); !errors.Is(staleErr, ErrRevision) {
			t.Fatalf("reject stale degraded Emergency: %v", staleErr)
		}
	}
	unauthorizedActor := actor
	unauthorizedActor.Administrator = false
	unauthorizedActor.EventRoles = map[int]viewer.Role{event.ID: viewer.Operator}
	unauthorized := input
	unauthorized.PreviewFingerprint = preview.ConfirmationFingerprint
	unauthorized.Confirmed = true
	unauthorized.ConfirmationMethod = "Keyboard"
	unauthorized.CommandID = "reject-unauthorized-degraded-emergency"
	for range 2 {
		if _, unauthorizedErr := service.ActivateEmergencyAlert(
			t.Context(),
			unauthorizedActor,
			unauthorized,
		); !errors.Is(unauthorizedErr, ErrScopeDenied) {
			t.Fatalf("reject unauthorized degraded Emergency: %v", unauthorizedErr)
		}
	}
	input.PreviewFingerprint = preview.ConfirmationFingerprint
	input.Confirmed = true
	input.ConfirmationMethod = "Keyboard"
	input.CommandID = "recover-emergency"
	activated, err := service.ActivateEmergencyAlert(t.Context(), actor, input)
	if err != nil {
		t.Fatalf("activate recovery Emergency: %v", err)
	}
	conflicting := input
	conflicting.Text = "Different emergency work"
	for range 2 {
		if _, conflictErr := service.ActivateEmergencyAlert(
			t.Context(),
			actor,
			conflicting,
		); !errors.Is(conflictErr, ErrCommandConflict) {
			t.Fatalf("conflict degraded Emergency: %v", conflictErr)
		}
	}
	rejectedClear := ClearInput{
		EventID: event.ID, OverrideID: activated.ID,
		ExpectedRevision: activated.Revision,
		CommandID:        "reject-degraded-emergency-clear",
	}
	for range 2 {
		if _, rejectedErr := service.Clear(
			t.Context(),
			actor,
			rejectedClear,
		); !errors.Is(rejectedErr, ErrInvalidInput) {
			t.Fatalf("reject degraded Emergency clear: %v", rejectedErr)
		}
	}
	conflictingClear := rejectedClear
	conflictingClear.ExpectedRevision++
	for range 2 {
		if _, conflictErr := service.Clear(
			t.Context(),
			actor,
			conflictingClear,
		); !errors.Is(conflictErr, ErrCommandConflict) {
			t.Fatalf("conflict degraded Emergency clear: %v", conflictErr)
		}
	}
	staleClear := rejectedClear
	staleClear.ExpectedRevision++
	staleClear.CommandID = "reject-stale-degraded-emergency-clear"
	staleClear.Confirmed = true
	staleClear.ConfirmationMethod = "Keyboard"
	for range 2 {
		if _, staleErr := service.Clear(
			t.Context(),
			actor,
			staleClear,
		); !errors.Is(staleErr, ErrRevision) {
			t.Fatalf("reject stale degraded Emergency clear: %v", staleErr)
		}
	}
	unauthorizedClear := rejectedClear
	unauthorizedClear.CommandID = "reject-unauthorized-degraded-emergency-clear"
	unauthorizedClear.Confirmed = true
	unauthorizedClear.ConfirmationMethod = "Keyboard"
	for range 2 {
		if _, unauthorizedErr := service.Clear(
			t.Context(),
			unauthorizedActor,
			unauthorizedClear,
		); !errors.Is(unauthorizedErr, ErrScopeDenied) {
			t.Fatalf("reject unauthorized degraded Emergency clear: %v", unauthorizedErr)
		}
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
	if _, rejectedErr := service.ActivateEmergencyAlert(
		t.Context(),
		actor,
		rejected,
	); !errors.Is(rejectedErr, ErrInvalidInput) {
		t.Fatalf("replay recovered rejected Emergency: %v", rejectedErr)
	}
	if _, staleErr := service.ActivateEmergencyAlert(
		t.Context(),
		actor,
		stale,
	); !errors.Is(staleErr, ErrRevision) {
		t.Fatalf("replay recovered stale Emergency: %v", staleErr)
	}
	if _, unauthorizedErr := service.ActivateEmergencyAlert(
		t.Context(),
		unauthorizedActor,
		unauthorized,
	); !errors.Is(unauthorizedErr, ErrScopeDenied) {
		t.Fatalf("replay recovered unauthorized Emergency: %v", unauthorizedErr)
	}
	if _, rejectedErr := service.Clear(
		t.Context(),
		actor,
		rejectedClear,
	); !errors.Is(rejectedErr, ErrInvalidInput) {
		t.Fatalf("replay recovered rejected Emergency clear: %v", rejectedErr)
	}
	if _, staleErr := service.Clear(
		t.Context(),
		actor,
		staleClear,
	); !errors.Is(staleErr, ErrRevision) {
		t.Fatalf("replay recovered stale Emergency clear: %v", staleErr)
	}
	if _, unauthorizedErr := service.Clear(
		t.Context(),
		unauthorizedActor,
		unauthorizedClear,
	); !errors.Is(unauthorizedErr, ErrScopeDenied) {
		t.Fatalf("replay recovered unauthorized Emergency clear: %v", unauthorizedErr)
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
	rejectedReasons := map[string]int{
		"override_invalid_input":     1,
		"override_revision_conflict": 1,
		"override_scope_denied":      1,
		"command_id_conflict":        3,
	}
	clearReasons := maps.Clone(rejectedReasons)
	clearReasons["command_id_conflict"] = 1
	for _, entry := range audit {
		if entry.Action == "ActivateEmergencyAlert" {
			emergencyEntries++
			if entry.Outcome == "Rejected" {
				rejectedReasons[entry.Reason]--
			}
		}
		if entry.Action == "ClearDisplayOverride" &&
			entry.Outcome == "Rejected" {
			clearReasons[entry.Reason]--
		}
	}
	if emergencyEntries != 7 {
		t.Fatalf(
			"recovered Emergency Audit Entries = %d, want 7",
			emergencyEntries,
		)
	}
	for reason, remaining := range rejectedReasons {
		if remaining != 0 {
			t.Errorf(
				"recovered Emergency rejection %q remaining = %d",
				reason,
				remaining,
			)
		}
	}
	for reason, remaining := range clearReasons {
		if remaining != 0 {
			t.Errorf(
				"recovered Emergency clear rejection %q remaining = %d",
				reason,
				remaining,
			)
		}
	}

	nextService, err := New(t.Context(), recoveredStorage, func() time.Time {
		return now.Add(time.Hour)
	}, nil)
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
