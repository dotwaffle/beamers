package sessiontarget

import (
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/timingripple"
)

func TestPreviewAcceptsPresetAndReportsDownstreamOverlap(t *testing.T) {
	now := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)
	state := State{
		SessionID: 7, Revision: 3, CurrentTarget: now.Add(20 * time.Minute),
		Presets: []time.Duration{5 * time.Minute, -2 * time.Minute},
		Timing:  targetTiming(now, now.Add(20*time.Minute), now.Add(22*time.Minute)),
	}

	preview, err := Preview(state, Adjustment{Duration: 5 * time.Minute, Preset: true}, now)
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if got, want := preview.ProposedTarget, now.Add(25*time.Minute); !got.Equal(want) {
		t.Fatalf("ProposedTarget = %v, want %v", got, want)
	}
	if len(preview.Effects) != 1 || preview.Effects[0].SessionID != 8 ||
		preview.Effects[0].CurrentOverlap != 0 ||
		preview.Effects[0].ProposedOverlap != 0 ||
		!preview.Effects[0].ProposedForecastStart.Equal(now.Add(25*time.Minute)) ||
		!preview.Effects[0].ProposedForecastEnd.Equal(now.Add(55*time.Minute)) {
		t.Fatalf("Effects = %#v, want Session 8 shifted by 3 minutes", preview.Effects)
	}
	if preview.Fingerprint == "" {
		t.Fatal("Fingerprint is empty")
	}
}

func TestPreviewAcceptsCustomNegativeAdjustment(t *testing.T) {
	now := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)
	preview, err := Preview(State{
		SessionID: 7, Revision: 3, CurrentTarget: now.Add(20 * time.Minute),
	}, Adjustment{Duration: -90 * time.Second}, now)
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if got, want := preview.ProposedTarget, now.Add(18*time.Minute+30*time.Second); !got.Equal(want) {
		t.Fatalf("ProposedTarget = %v, want %v", got, want)
	}
}

func TestPreviewReportsResolvedDownstreamOverlap(t *testing.T) {
	now := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)
	preview, err := Preview(State{
		SessionID: 7, Revision: 3, CurrentTarget: now.Add(25 * time.Minute),
		Timing: targetTiming(now, now.Add(25*time.Minute), now.Add(22*time.Minute)),
	}, Adjustment{Duration: -5 * time.Minute}, now)
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if len(preview.Effects) != 1 || preview.Effects[0].CurrentOverlap != 3*time.Minute ||
		preview.Effects[0].ProposedOverlap != 0 {
		t.Fatalf("Effects = %#v, want resolved 3 minute overlap", preview.Effects)
	}
}

func TestPreviewRejectsUnknownPreset(t *testing.T) {
	now := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)
	_, err := Preview(State{
		SessionID: 7, Revision: 3, CurrentTarget: now.Add(20 * time.Minute),
		Presets: []time.Duration{5 * time.Minute},
	}, Adjustment{Duration: 10 * time.Minute, Preset: true}, now)
	if !errors.Is(err, ErrPresetNotConfigured) {
		t.Fatalf("Preview() error = %v, want ErrPresetNotConfigured", err)
	}
}

func TestPreviewRejectsSubsecondAdjustment(t *testing.T) {
	now := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)
	_, err := Preview(State{
		SessionID: 7, Revision: 3, CurrentTarget: now.Add(20 * time.Minute),
	}, Adjustment{Duration: 500 * time.Millisecond}, now)
	if err == nil {
		t.Fatal("Preview() accepted a subsecond adjustment")
	}
}

func TestPreviewRejectsManualEndSession(t *testing.T) {
	now := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)
	_, err := Preview(State{
		SessionID: 7, Revision: 3, CurrentTarget: now.Add(20 * time.Minute),
		TimingPolicy: "ManualEnd",
	}, Adjustment{Duration: 5 * time.Minute}, now)
	if !errors.Is(err, ErrNoCountdownTarget) {
		t.Fatalf("Preview() error = %v, want ErrNoCountdownTarget", err)
	}
}

func TestPreviewRejectsTargetBeforeNowAndDirectsEndNow(t *testing.T) {
	now := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)
	_, err := Preview(State{
		SessionID: 7, Revision: 3, CurrentTarget: now.Add(time.Minute),
	}, Adjustment{Duration: -2 * time.Minute}, now)
	if !errors.Is(err, ErrTargetBeforeNow) {
		t.Fatalf("Preview() error = %v, want ErrTargetBeforeNow", err)
	}
	if err == nil || err.Error() != "target is before current server time; use End Now" {
		t.Fatalf("Preview() error text = %q", err)
	}
}

func TestPreviewRequiresHardBoundaryConfirmation(t *testing.T) {
	now := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)
	preview, err := Preview(State{
		SessionID: 7, Revision: 3, CurrentTarget: now.Add(20 * time.Minute),
		EndBoundary: HardBoundary,
	}, Adjustment{Duration: 5 * time.Minute}, now)
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if !preview.RequiresHardBoundaryConfirmation {
		t.Fatal("RequiresHardBoundaryConfirmation = false")
	}
}

func TestPreviewReportsLocationCollisionWithoutCrossLaneRipple(t *testing.T) {
	now := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)
	anchor := targetTiming(now, now.Add(20*time.Minute), now.Add(22*time.Minute))[0]
	anchor.LocationIDs = []int{10}
	other := timingripple.Session{
		ID: 8, PlannedStart: now.Add(22 * time.Minute), PlannedEnd: now.Add(52 * time.Minute),
		ForecastStart: now.Add(22 * time.Minute), ForecastEnd: now.Add(52 * time.Minute),
		StartBoundary: timingripple.Hard, EndBoundary: timingripple.Soft,
		LaneIDs: []int{2}, LocationIDs: []int{10},
	}
	preview, err := Preview(State{
		SessionID: 7, Revision: 3, CurrentTarget: now.Add(20 * time.Minute),
		Timing: []timingripple.Session{anchor, other},
	}, Adjustment{Duration: 5 * time.Minute}, now)
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if len(preview.Changes) != 1 || len(preview.Effects) != 1 ||
		preview.Effects[0].SessionID != 8 ||
		preview.Effects[0].ProposedOverlap != 3*time.Minute ||
		!preview.Effects[0].ProposedForecastStart.Equal(other.ForecastStart) ||
		!preview.RequiresHardBoundaryConfirmation {
		t.Fatalf("Preview() = %#v", preview)
	}
}

func TestPreviewFingerprintChangesWithDownstreamContext(t *testing.T) {
	now := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)
	state := State{
		SessionID: 7, Revision: 3, CurrentTarget: now.Add(20 * time.Minute),
		Timing: targetTiming(now, now.Add(20*time.Minute), now.Add(22*time.Minute)),
	}
	first, err := Preview(state, Adjustment{Duration: 5 * time.Minute}, now)
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	state.Timing[1].ForecastStart = state.Timing[1].ForecastStart.Add(time.Minute)
	second, err := Preview(state, Adjustment{Duration: 5 * time.Minute}, now)
	if err != nil {
		t.Fatalf("Preview() changed context error = %v", err)
	}
	if first.Fingerprint == second.Fingerprint {
		t.Fatal("Fingerprint did not change with downstream context")
	}
}

func targetTiming(now, target, downstreamStart time.Time) []timingripple.Session {
	return []timingripple.Session{
		{
			ID: 7, PlannedStart: now.Add(-10 * time.Minute), PlannedEnd: now.Add(20 * time.Minute),
			ForecastStart: now.Add(-10 * time.Minute), ForecastEnd: target,
			MinimumDuration: 30 * time.Minute,
			StartBoundary:   timingripple.Soft, EndBoundary: timingripple.Soft, LaneIDs: []int{1},
		},
		{
			ID: 8, PlannedStart: now.Add(22 * time.Minute), PlannedEnd: now.Add(52 * time.Minute),
			ForecastStart: downstreamStart, ForecastEnd: downstreamStart.Add(30 * time.Minute),
			MinimumDuration: 30 * time.Minute,
			StartBoundary:   timingripple.Soft, EndBoundary: timingripple.Soft, LaneIDs: []int{1},
		},
	}
}
