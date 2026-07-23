package stagetimer

import (
	"testing"
	"time"
)

func TestNewFixedEndTargetsResolvedInstant(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 2, 7, 12, 3, 0, 0, time.UTC)
	timer, err := New(Spec{
		SessionID:    7,
		Policy:       FixedEnd,
		ActualStart:  start,
		PlannedStart: time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC),
		PlannedEnd:   time.Date(2026, 2, 7, 12, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("New Fixed End timer: %v", err)
	}
	if got, want := timer.Anchor, time.Date(2026, 2, 7, 12, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("anchor = %v, want %v", got, want)
	}
	if timer.Mode != Countdown {
		t.Errorf("mode = %q, want %q", timer.Mode, Countdown)
	}
}

func TestNewFixedDurationAddsPlannedDurationToActualStart(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 2, 7, 12, 3, 0, 0, time.UTC)
	timer, err := New(Spec{
		SessionID:    7,
		Policy:       FixedDuration,
		ActualStart:  start,
		PlannedStart: time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC),
		PlannedEnd:   time.Date(2026, 2, 7, 12, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("New Fixed Duration timer: %v", err)
	}
	if got, want := timer.Anchor, start.Add(30*time.Minute); !got.Equal(want) {
		t.Errorf("anchor = %v, want %v", got, want)
	}
	if timer.Mode != Countdown {
		t.Errorf("mode = %q, want %q", timer.Mode, Countdown)
	}
}

func TestNewManualEndUsesActualStartAsElapsedAnchor(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 2, 7, 12, 3, 0, 0, time.UTC)
	timer, err := New(Spec{
		SessionID:    7,
		Policy:       ManualEnd,
		ActualStart:  start,
		PlannedStart: time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC),
		PlannedEnd:   time.Date(2026, 2, 7, 12, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("New Manual End timer: %v", err)
	}
	if !timer.Anchor.Equal(start) {
		t.Errorf("anchor = %v, want Actual Start %v", timer.Anchor, start)
	}
	if timer.Mode != Elapsed {
		t.Errorf("mode = %q, want %q", timer.Mode, Elapsed)
	}
}

func TestFrameContinuesIntoPositiveOvertime(t *testing.T) {
	t.Parallel()

	timer := Timer{Mode: Countdown, Anchor: time.Date(2026, 2, 7, 12, 30, 0, 0, time.UTC)}
	before := FrameAt(timer, timer.Anchor.Add(-time.Second))
	subsecond := FrameAt(timer, timer.Anchor.Add(-250*time.Millisecond))
	atTarget := FrameAt(timer, timer.Anchor)
	after := FrameAt(timer, timer.Anchor.Add(time.Second))

	if before.Text != "00:01" || before.Overtime {
		t.Errorf("before target = %+v, want countdown 00:01", before)
	}
	if subsecond.Text != "00:01" || subsecond.Overtime {
		t.Errorf("subsecond before target = %+v, want countdown 00:01", subsecond)
	}
	if atTarget.Text != "00:00" || atTarget.Overtime {
		t.Errorf("at target = %+v, want countdown 00:00", atTarget)
	}
	if after.Text != "+00:01" || !after.Overtime {
		t.Errorf("after target = %+v, want overtime +00:01", after)
	}
}

func TestFrameShowsManualElapsedTime(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 2, 7, 12, 3, 0, 0, time.UTC)
	got := FrameAt(Timer{Mode: Elapsed, Anchor: start}, start.Add(2*time.Minute+9*time.Second))
	if got.Text != "02:09" || got.Overtime {
		t.Errorf("elapsed frame = %+v, want 02:09 without overtime", got)
	}
}

func TestResolveThresholdsUsesMostSpecificOverride(t *testing.T) {
	t.Parallel()

	event := []Threshold{{Remaining: 10 * time.Minute, Emphasis: Attention}}
	sessionType := map[string][]Threshold{
		"Presentation": {{Remaining: 5 * time.Minute, Emphasis: Attention}},
	}
	session := map[int][]Threshold{
		7: {{Remaining: time.Minute, Emphasis: Urgent}},
	}

	tests := []struct {
		name      string
		sessionID int
		typeName  string
		want      time.Duration
	}{
		{name: "Event", sessionID: 8, typeName: "Break", want: 10 * time.Minute},
		{name: "Session type", sessionID: 8, typeName: "Presentation", want: 5 * time.Minute},
		{name: "Session", sessionID: 7, typeName: "Presentation", want: time.Minute},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := ResolveThresholds(event, sessionType, session, test.typeName, test.sessionID)
			if len(got) != 1 || got[0].Remaining != test.want {
				t.Errorf("resolved thresholds = %+v, want remaining %v", got, test.want)
			}
		})
	}
}

func TestNewIgnoresThresholdLongerThanTargetDuration(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)
	timer, err := New(Spec{
		SessionID:    7,
		Policy:       FixedDuration,
		ActualStart:  start,
		PlannedStart: start,
		PlannedEnd:   start.Add(5 * time.Minute),
		Thresholds: []Threshold{
			{Remaining: 10 * time.Minute, Emphasis: Attention},
			{Remaining: time.Minute, Emphasis: Urgent},
		},
	})
	if err != nil {
		t.Fatalf("New timer: %v", err)
	}
	if len(timer.Thresholds) != 1 || timer.Thresholds[0].Remaining != time.Minute {
		t.Errorf("thresholds = %+v, want only one-minute threshold", timer.Thresholds)
	}
}

func TestNewFixedEndIgnoresThresholdLongerThanLateStartHorizon(t *testing.T) {
	t.Parallel()

	plannedStart := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)
	timer, err := New(Spec{
		SessionID:    7,
		Policy:       FixedEnd,
		ActualStart:  plannedStart.Add(15 * time.Minute),
		PlannedStart: plannedStart,
		PlannedEnd:   plannedStart.Add(30 * time.Minute),
		Thresholds: []Threshold{
			{Remaining: 20 * time.Minute, Emphasis: Attention},
			{Remaining: 5 * time.Minute, Emphasis: Urgent},
		},
	})
	if err != nil {
		t.Fatalf("New Fixed End timer: %v", err)
	}
	if len(timer.Thresholds) != 1 || timer.Thresholds[0].Remaining != 5*time.Minute {
		t.Errorf("thresholds = %+v, want only five-minute threshold", timer.Thresholds)
	}
}

func TestFrameAppliesThresholdEmphasisAccessibly(t *testing.T) {
	t.Parallel()

	target := time.Date(2026, 2, 7, 12, 30, 0, 0, time.UTC)
	timer := Timer{
		Mode:   Countdown,
		Anchor: target,
		Thresholds: []Threshold{
			{Remaining: 5 * time.Minute, Emphasis: Attention},
			{Remaining: time.Minute, Emphasis: Urgent},
		},
	}
	tests := []struct {
		name string
		now  time.Time
		want Emphasis
	}{
		{name: "Normal", now: target.Add(-6 * time.Minute), want: Normal},
		{name: "Attention", now: target.Add(-5 * time.Minute), want: Attention},
		{name: "Urgent", now: target.Add(-30 * time.Second), want: Urgent},
		{name: "Overtime remains urgent", now: target.Add(time.Second), want: Urgent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := FrameAt(timer, test.now).Emphasis; got != test.want {
				t.Errorf("emphasis = %q, want %q", got, test.want)
			}
		})
	}
}
