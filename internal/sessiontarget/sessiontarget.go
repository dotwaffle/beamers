// Package sessiontarget decides live Session target adjustments.
package sessiontarget

import (
	"errors"
	"slices"
	"time"

	"github.com/dotwaffle/beamers/internal/timingripple"
)

const (
	// HardBoundary identifies timing that cannot move without explicit confirmation.
	HardBoundary  = "Hard"
	maxAdjustment = 24 * time.Hour
)

var (
	// ErrPresetNotConfigured means a preset adjustment is not configured for the Event.
	ErrPresetNotConfigured = errors.New("target adjustment preset is not configured")
	// ErrTargetBeforeNow means the proposed target has already passed.
	ErrTargetBeforeNow = errors.New("target is before current server time; use End Now")
	// ErrNoCountdownTarget means the Session's Timing Policy has no target to adjust.
	ErrNoCountdownTarget = errors.New("manual end Session has no countdown target")
)

// State is the current input to one preview decision.
type State struct {
	SessionID     int
	Revision      int
	CurrentTarget time.Time
	EndBoundary   string
	TimingPolicy  string
	Presets       []time.Duration
	Timing        []timingripple.Session
}

// Adjustment selects a configured preset or custom signed duration.
type Adjustment struct {
	Duration time.Duration
	Preset   bool
}

// Effect reports one overlap introduced by the proposed target.
type Effect = timingripple.Effect

// Result is the complete deterministic preview shown before confirmation.
type Result struct {
	CurrentTarget                    time.Time
	ProposedTarget                   time.Time
	Adjustment                       time.Duration
	Effects                          []Effect
	Changes                          []timingripple.Change
	RequiresHardBoundaryConfirmation bool
	Fingerprint                      string
}

// Preview validates an adjustment and reports its downstream timing effects.
func Preview(state State, adjustment Adjustment, now time.Time) (Result, error) {
	if state.SessionID <= 0 || state.Revision < 0 || state.CurrentTarget.IsZero() {
		return Result{}, errors.New("live Session target state is invalid")
	}
	if state.TimingPolicy == "ManualEnd" {
		return Result{}, ErrNoCountdownTarget
	}
	if adjustment.Duration == 0 || adjustment.Duration%time.Second != 0 ||
		adjustment.Duration < -maxAdjustment ||
		adjustment.Duration > maxAdjustment {
		return Result{}, errors.New("target adjustment must use whole seconds and be non-zero and no more than 24 hours")
	}
	if adjustment.Preset && !configuredPreset(state.Presets, adjustment.Duration) {
		return Result{}, ErrPresetNotConfigured
	}
	proposed := state.CurrentTarget.Add(adjustment.Duration)
	if proposed.Before(now) {
		return Result{}, ErrTargetBeforeNow
	}
	result := Result{
		CurrentTarget:                    state.CurrentTarget,
		ProposedTarget:                   proposed,
		Adjustment:                       adjustment.Duration,
		RequiresHardBoundaryConfirmation: state.EndBoundary == HardBoundary,
	}
	action := timingripple.AdjustTarget{SessionID: state.SessionID, TargetEnd: proposed}
	if len(state.Timing) > 0 {
		plan, err := timingripple.Calculate(state.Timing, action)
		if err != nil {
			return Result{}, err
		}
		result.Changes = plan.Changes
		for _, effect := range plan.Effects {
			if effect.SessionID == state.SessionID {
				continue
			}
			result.Effects = append(result.Effects, effect)
		}
		result.RequiresHardBoundaryConfirmation = result.RequiresHardBoundaryConfirmation ||
			len(plan.HardCollisions) > 0
	}
	result.Fingerprint = timingripple.Fingerprint(state.Timing, action, state.Revision)
	return result, nil
}

func configuredPreset(presets []time.Duration, adjustment time.Duration) bool {
	return slices.Contains(presets, adjustment)
}
