// Package stagetimer derives renderer-neutral live timer state.
package stagetimer

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"time"
)

// Policy determines the live timer's target behavior.
type Policy string

const (
	// FixedEnd counts down to one resolved absolute instant.
	FixedEnd Policy = "FixedEnd"
	// FixedDuration preserves planned elapsed duration from Actual Start.
	FixedDuration Policy = "FixedDuration"
	// ManualEnd counts elapsed time from Actual Start.
	ManualEnd Policy = "ManualEnd"
)

// Mode controls whether the timer counts toward or away from its anchor.
type Mode string

const (
	// Countdown counts down to an anchor and then up in overtime.
	Countdown Mode = "countdown"
	// Elapsed counts up from an anchor.
	Elapsed Mode = "elapsed"
)

// Emphasis identifies an accessible visual urgency level.
type Emphasis string

const (
	// Normal is the baseline timer presentation.
	Normal Emphasis = "normal"
	// Attention indicates a nearing target.
	Attention Emphasis = "attention"
	// Urgent indicates an imminent or exceeded target.
	Urgent Emphasis = "urgent"
)

// Threshold changes emphasis at one remaining duration.
type Threshold struct {
	Remaining time.Duration
	Emphasis  Emphasis
}

// Spec contains the authoritative Session Run facts used to create a timer.
type Spec struct {
	SessionID    int
	Policy       Policy
	ActualStart  time.Time
	PlannedStart time.Time
	PlannedEnd   time.Time
	TargetEnd    time.Time
	Thresholds   []Threshold
}

// Timer is a renderer-neutral clock anchored to authoritative Session state.
type Timer struct {
	SessionID  int
	Mode       Mode
	Anchor     time.Time
	Thresholds []Threshold
}

// Frame is one timer rendering decision.
type Frame struct {
	Text     string
	Emphasis Emphasis
	Overtime bool
}

// New derives a timer from the immutable Session Run Snapshot.
func New(spec Spec) (Timer, error) {
	if spec.SessionID <= 0 {
		return Timer{}, errors.New("session ID must be positive")
	}
	if spec.ActualStart.IsZero() {
		return Timer{}, errors.New("actual start is required")
	}
	duration := spec.PlannedEnd.Sub(spec.PlannedStart)
	if duration <= 0 {
		return Timer{}, errors.New("planned end must be after planned start")
	}
	timer := Timer{SessionID: spec.SessionID}
	thresholdDuration := duration
	switch spec.Policy {
	case FixedEnd:
		timer.Mode = Countdown
		timer.Anchor = spec.TargetEnd
		if timer.Anchor.IsZero() {
			timer.Anchor = spec.PlannedEnd
		}
		thresholdDuration = timer.Anchor.Sub(spec.ActualStart)
	case FixedDuration:
		timer.Mode = Countdown
		timer.Anchor = spec.TargetEnd
		if timer.Anchor.IsZero() {
			timer.Anchor = spec.ActualStart.Add(duration)
		}
	case ManualEnd:
		timer.Mode = Elapsed
		timer.Anchor = spec.ActualStart
	default:
		return Timer{}, errors.New("unsupported Timing Policy")
	}
	if timer.Mode == Countdown {
		timer.Thresholds = validThresholds(spec.Thresholds, thresholdDuration)
	}
	return timer, nil
}

// ResolveThresholds returns Session, Session-type, or Event thresholds in precedence order.
func ResolveThresholds(
	event []Threshold,
	sessionTypes map[string][]Threshold,
	sessions map[int][]Threshold,
	sessionType string,
	sessionID int,
) []Threshold {
	if thresholds, ok := sessions[sessionID]; ok {
		return slices.Clone(thresholds)
	}
	if thresholds, ok := sessionTypes[sessionType]; ok {
		return slices.Clone(thresholds)
	}
	return slices.Clone(event)
}

// FrameAt derives display text, overtime, and accessible emphasis at an instant.
func FrameAt(timer Timer, now time.Time) Frame {
	if timer.Mode == Elapsed {
		return Frame{Text: formatDuration(max(now.Sub(timer.Anchor), 0), false), Emphasis: Normal}
	}
	remaining := timer.Anchor.Sub(now)
	frame := Frame{Text: formatDuration(max(remaining, 0), true), Emphasis: Normal}
	if remaining < 0 {
		frame.Text = "+" + formatDuration(-remaining, false)
		frame.Overtime = true
	}
	for _, threshold := range timer.Thresholds {
		if remaining <= threshold.Remaining {
			frame.Emphasis = threshold.Emphasis
		}
	}
	return frame
}

func validThresholds(thresholds []Threshold, duration time.Duration) []Threshold {
	valid := make([]Threshold, 0, len(thresholds))
	for _, threshold := range thresholds {
		if threshold.Remaining <= 0 || threshold.Remaining > duration {
			continue
		}
		switch threshold.Emphasis {
		case Normal:
			continue
		case Attention, Urgent:
			// Valid threshold emphasis.
		default:
			continue
		}
		valid = append(valid, threshold)
	}
	slices.SortFunc(valid, func(first, second Threshold) int {
		return cmp.Compare(second.Remaining, first.Remaining)
	})
	return valid
}

func formatDuration(duration time.Duration, roundUp bool) string {
	seconds := int64(duration / time.Second)
	if roundUp && duration%time.Second != 0 {
		seconds++
	}
	return fmt.Sprintf("%02d:%02d", seconds/60, seconds%60)
}
