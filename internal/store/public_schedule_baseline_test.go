package store

import (
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/ent/publicschedulebaseline"
	"github.com/dotwaffle/beamers/ent/rundown"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestCapturePublicScheduleBaselineCommitsAllPublicSessions(t *testing.T) {
	installationStore := openEventTestInstallation(t)
	now := time.Date(2026, time.July, 23, 18, 0, 0, 0, time.UTC)
	administrator := bootstrapEventTestAdministrator(t, installationStore, now)
	ctx := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID: administrator.ID, Administrator: true,
	})
	event := createEventTestEvent(t, installationStore, ctx, administrator.ID, "FOSDEM 2026", now)
	forecastStart := now.Add(24 * time.Hour)
	publicSessionID := createBaselineTestPublishedSession(
		t,
		installationStore,
		event.ID,
		"Opening keynote",
		"Public",
		forecastStart,
	)
	createBaselineTestPublishedSession(
		t,
		installationStore,
		event.ID,
		"Crew briefing",
		"CrewOnly",
		forecastStart.Add(-time.Hour),
	)
	installationStore.client.Rundown.Update().
		Where(rundown.EventIDEQ(event.ID)).
		SetPublishedRevision(1).
		SaveX(systemContext(t.Context()))

	state, err := installationStore.LoadPublicScheduleBaselineState(ctx, event.ID)
	if err != nil {
		t.Fatalf("load baseline state: %v", err)
	}
	if state.EventName != "FOSDEM 2026" || state.Active || state.Captured ||
		state.PublishedRevision != 1 || len(state.Sessions) != 1 {
		t.Fatalf("baseline state = %+v, want one non-Active uncaptured Public Session", state)
	}

	transaction, err := installationStore.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin baseline capture: %v", err)
	}
	result, err := transaction.CapturePublicScheduleBaseline(
		ctx,
		PublicScheduleBaselineCaptureParams{
			EventID: event.ID, ExpectedPublishedRevision: 1, Now: now,
		},
	)
	if err != nil {
		t.Fatalf("capture baseline: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit baseline: %v", err)
	}
	if result.EventID != event.ID || result.PublishedRevision != 1 ||
		result.SessionCount != 1 || !result.CapturedAt.Equal(now) {
		t.Fatalf("capture result = %+v, want one Session at revision 1", result)
	}

	baseline := installationStore.client.PublicScheduleBaseline.Query().
		Where(publicschedulebaseline.EventIDEQ(event.ID)).
		WithEntries().
		OnlyX(systemContext(t.Context()))
	if len(baseline.Edges.Entries) != 1 {
		t.Fatalf("baseline entries = %d, want 1", len(baseline.Edges.Entries))
	}
	entry := baseline.Edges.Entries[0]
	if entry.SessionID != publicSessionID || !entry.ForecastStart.Equal(forecastStart) ||
		entry.SourcePublishedRevision != 1 {
		t.Fatalf("baseline entry = %+v, want Session %d at %v", entry, publicSessionID, forecastStart)
	}
}

func TestCapturePublicScheduleBaselineAllowsEmptyEvent(t *testing.T) {
	installationStore := openEventTestInstallation(t)
	now := time.Date(2026, time.July, 23, 18, 0, 0, 0, time.UTC)
	administrator := bootstrapEventTestAdministrator(t, installationStore, now)
	ctx := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID: administrator.ID, Administrator: true,
	})
	event := createEventTestEvent(t, installationStore, ctx, administrator.ID, "Empty Event", now)

	transaction, err := installationStore.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin empty baseline capture: %v", err)
	}
	result, err := transaction.CapturePublicScheduleBaseline(
		ctx,
		PublicScheduleBaselineCaptureParams{EventID: event.ID, Now: now},
	)
	if err != nil {
		t.Fatalf("capture empty baseline: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit empty baseline: %v", err)
	}
	if result.SessionCount != 0 {
		t.Fatalf("empty baseline Session count = %d, want 0", result.SessionCount)
	}
}

func TestCapturePublicScheduleBaselineRejectsStaleAndRepeatedCapture(t *testing.T) {
	installationStore := openEventTestInstallation(t)
	now := time.Date(2026, time.July, 23, 18, 0, 0, 0, time.UTC)
	administrator := bootstrapEventTestAdministrator(t, installationStore, now)
	ctx := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID: administrator.ID, Administrator: true,
	})
	event := createEventTestEvent(t, installationStore, ctx, administrator.ID, "Revision Event", now)

	stale, err := installationStore.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin stale baseline capture: %v", err)
	}
	if _, captureErr := stale.CapturePublicScheduleBaseline(
		ctx,
		PublicScheduleBaselineCaptureParams{
			EventID: event.ID, ExpectedPublishedRevision: 1, Now: now,
		},
	); !errors.Is(captureErr, ErrPublicScheduleBaselineRevision) {
		t.Fatalf("stale capture error = %v, want %v", captureErr, ErrPublicScheduleBaselineRevision)
	}
	if rollbackErr := stale.Rollback(); rollbackErr != nil {
		t.Fatalf("rollback stale baseline capture: %v", rollbackErr)
	}

	first, err := installationStore.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin first baseline capture: %v", err)
	}
	if _, captureErr := first.CapturePublicScheduleBaseline(
		ctx,
		PublicScheduleBaselineCaptureParams{EventID: event.ID, Now: now},
	); captureErr != nil {
		t.Fatalf("capture first baseline: %v", captureErr)
	}
	if commitErr := first.Commit(); commitErr != nil {
		t.Fatalf("commit first baseline: %v", commitErr)
	}

	repeated, err := installationStore.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin repeated baseline capture: %v", err)
	}
	if _, captureErr := repeated.CapturePublicScheduleBaseline(
		ctx,
		PublicScheduleBaselineCaptureParams{EventID: event.ID, Now: now.Add(time.Minute)},
	); !errors.Is(captureErr, ErrPublicScheduleBaselineExists) {
		t.Fatalf("repeated capture error = %v, want %v", captureErr, ErrPublicScheduleBaselineExists)
	}
	if err := repeated.Rollback(); err != nil {
		t.Fatalf("rollback repeated baseline capture: %v", err)
	}
	if count := installationStore.client.PublicScheduleBaseline.Query().
		CountX(systemContext(t.Context())); count != 1 {
		t.Fatalf("baseline count = %d, want 1", count)
	}
}

func createBaselineTestPublishedSession(
	t *testing.T,
	installationStore *SQLite,
	eventID int,
	title string,
	visibility string,
	plannedStart time.Time,
) int {
	t.Helper()
	ctx := systemContext(t.Context())
	identity := installationStore.client.Session.Create().SetEventID(eventID).SaveX(ctx)
	installationStore.client.SessionPublishedVersion.Create().
		SetSessionID(identity.ID).
		SetPublishedRevision(1).
		SetTitle(title).
		SetType("Presentation").
		SetAudienceVisibility(sessionpublishedversion.AudienceVisibility(visibility)).
		SetPlannedStart(plannedStart).
		SetPlannedEnd(plannedStart.Add(time.Hour)).
		SetTimingPolicy("FixedEnd").
		SetMinimumDurationSeconds(3600).
		SetStartBoundary("Soft").
		SetEndBoundary("Soft").
		SaveX(ctx)
	return identity.ID
}
