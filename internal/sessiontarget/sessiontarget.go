// Package sessiontarget decides live Session target adjustments.
package sessiontarget

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strconv"
	"time"
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
	Downstream    []DownstreamSession
}

// DownstreamSession is one later shared-resource Session relevant to the preview.
type DownstreamSession struct {
	SessionID     int
	ForecastStart time.Time
	ForecastEnd   time.Time
	StartBoundary string
}

// Adjustment selects a configured preset or custom signed duration.
type Adjustment struct {
	Duration time.Duration
	Preset   bool
}

// Effect reports one overlap introduced by the proposed target.
type Effect struct {
	SessionID       int
	CurrentOverlap  time.Duration
	ProposedOverlap time.Duration
}

// Result is the complete deterministic preview shown before confirmation.
type Result struct {
	CurrentTarget                    time.Time
	ProposedTarget                   time.Time
	Adjustment                       time.Duration
	Effects                          []Effect
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
	for _, downstream := range state.Downstream {
		if downstream.SessionID <= 0 || downstream.ForecastStart.IsZero() ||
			downstream.ForecastEnd.Before(downstream.ForecastStart) {
			return Result{}, errors.New("downstream Session timing is invalid")
		}
		currentOverlap := max(state.CurrentTarget.Sub(downstream.ForecastStart), 0)
		proposedOverlap := max(proposed.Sub(downstream.ForecastStart), 0)
		if currentOverlap != proposedOverlap {
			result.Effects = append(result.Effects, Effect{
				SessionID:       downstream.SessionID,
				CurrentOverlap:  currentOverlap,
				ProposedOverlap: proposedOverlap,
			})
			if proposedOverlap > 0 && downstream.StartBoundary == HardBoundary {
				result.RequiresHardBoundaryConfirmation = true
			}
		}
	}
	result.Fingerprint = fingerprint(state, result)
	return result, nil
}

func configuredPreset(presets []time.Duration, adjustment time.Duration) bool {
	return slices.Contains(presets, adjustment)
}

func fingerprint(state State, result Result) string {
	hash := sha256.New()
	writeFingerprint(hash.Write, strconv.Itoa(state.SessionID))
	writeFingerprint(hash.Write, strconv.Itoa(state.Revision))
	writeFingerprint(hash.Write, result.CurrentTarget.UTC().Format(time.RFC3339Nano))
	writeFingerprint(hash.Write, result.ProposedTarget.UTC().Format(time.RFC3339Nano))
	writeFingerprint(hash.Write, strconv.FormatInt(int64(result.Adjustment), 10))
	writeFingerprint(hash.Write, strconv.FormatBool(result.RequiresHardBoundaryConfirmation))
	for _, downstream := range state.Downstream {
		writeFingerprint(hash.Write, strconv.Itoa(downstream.SessionID))
		writeFingerprint(hash.Write, downstream.ForecastStart.UTC().Format(time.RFC3339Nano))
		writeFingerprint(hash.Write, downstream.ForecastEnd.UTC().Format(time.RFC3339Nano))
		writeFingerprint(hash.Write, downstream.StartBoundary)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func writeFingerprint(write func([]byte) (int, error), value string) {
	_, _ = write([]byte(strconv.Itoa(len(value))))
	_, _ = write([]byte{':'})
	_, _ = write([]byte(value))
}
