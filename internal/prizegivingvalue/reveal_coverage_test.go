package prizegivingvalue

import (
	"testing"
	"time"
)

func TestReconcileRevealCoveragePreservesUncoveredGaps(t *testing.T) {
	startedAt := time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)
	consumers := []int{1, 2}
	intervals := []RevealCoverageInterval{
		{
			DisplayIDs: []int{1},
			StartedAt:  startedAt,
		},
		{
			DisplayIDs: []int{2},
			StartedAt:  startedAt,
			EndedAt:    startedAt.Add(5 * time.Second),
		},
		{
			DisplayIDs: []int{2},
			StartedAt:  startedAt.Add(10 * time.Second),
		},
	}
	transitions := ReconcileRevealCoverage(
		consumers,
		intervals,
		startedAt,
		startedAt.Add(10*time.Second),
	)
	want := []RevealCoverageTransition{
		{At: startedAt.Add(5 * time.Second)},
		{At: startedAt.Add(10 * time.Second), Covered: true},
	}
	if len(transitions) != len(want) {
		t.Fatalf("coverage transitions = %+v, want %+v", transitions, want)
	}
	for index := range want {
		if transitions[index] != want[index] {
			t.Fatalf("coverage transitions = %+v, want %+v", transitions, want)
		}
	}

	state := ReconcileRevealCoverageState(StageState{
		Status:         StageRevealing,
		RevealPausedAt: startedAt,
	}, transitions, startedAt.Add(10*time.Second))
	if state.RevealPausedAt != startedAt.Add(10*time.Second) ||
		time.Duration(state.RevealPausedNanos) != 5*time.Second {
		t.Fatalf("reconciled Reveal pause = %+v", state)
	}
}

func TestReconcileRevealCoverageDoesNotRestartCompletionInsideGap(t *testing.T) {
	startedAt := time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)
	state := ReconcileRevealCoverageState(
		StageState{
			Status:              StageRevealing,
			Release:             ReleaseHeld,
			RevealStartedAt:     startedAt,
			RevealDurationNanos: int64(3 * time.Second),
			RevealPausedAt:      startedAt,
		},
		[]RevealCoverageTransition{
			{At: startedAt.Add(5 * time.Second)},
			{At: startedAt.Add(10 * time.Second), Covered: true},
		},
		startedAt.Add(10*time.Second),
	)
	if state.Status != StageRevealed ||
		state.Release != ReleaseReady ||
		state.RevealCompletedAt != startedAt.Add(8*time.Second) ||
		!state.RevealPausedAt.IsZero() ||
		time.Duration(state.RevealPausedNanos) != 5*time.Second {
		t.Fatalf("Reveal completed inside coverage gap = %+v", state)
	}
}

func TestReconcileRevealCoverageRequiresEveryConsumer(t *testing.T) {
	now := time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name      string
		consumers []int
		intervals []RevealCoverageInterval
		covered   bool
	}{
		{
			name:      "full union",
			consumers: []int{1, 2},
			intervals: []RevealCoverageInterval{
				{DisplayIDs: []int{1}, StartedAt: now},
				{DisplayIDs: []int{2}, StartedAt: now},
			},
			covered: true,
		},
		{
			name:      "partial",
			consumers: []int{1, 2},
			intervals: []RevealCoverageInterval{{
				DisplayIDs: []int{1}, StartedAt: now,
			}},
		},
		{
			name: "no consumers",
			intervals: []RevealCoverageInterval{{
				DisplayIDs: []int{1}, StartedAt: now,
			}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			transitions := ReconcileRevealCoverage(
				test.consumers,
				test.intervals,
				time.Time{},
				now,
			)
			got := len(transitions) == 1 && transitions[0].Covered
			if got != test.covered {
				t.Fatalf("coverage transitions = %+v", transitions)
			}
		})
	}
}
