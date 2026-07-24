package prizegivingvalue

import (
	"slices"
	"sort"
	"time"
)

// RevealCoverageInterval records when one Replace Override covered Displays.
type RevealCoverageInterval struct {
	DisplayIDs []int
	StartedAt  time.Time
	EndedAt    time.Time
}

// RevealCoverageTransition changes whether every Program consumer is covered.
type RevealCoverageTransition struct {
	At      time.Time
	Covered bool
}

// ReconcileRevealCoverage derives full-coverage transitions after the last
// durable Reveal pause observation.
func ReconcileRevealCoverage(
	consumerIDs []int,
	intervals []RevealCoverageInterval,
	pausedAt, now time.Time,
) []RevealCoverageTransition {
	if len(consumerIDs) == 0 {
		if pausedAt.IsZero() {
			return nil
		}
		return []RevealCoverageTransition{{At: now}}
	}
	coveredAt := func(at time.Time) bool {
		for _, consumerID := range consumerIDs {
			covered := slices.ContainsFunc(
				intervals,
				func(interval RevealCoverageInterval) bool {
					return !interval.StartedAt.After(at) &&
						(interval.EndedAt.IsZero() || interval.EndedAt.After(at)) &&
						slices.Contains(interval.DisplayIDs, consumerID)
				},
			)
			if !covered {
				return false
			}
		}
		return true
	}
	if pausedAt.IsZero() {
		if !coveredAt(now) {
			return nil
		}
		return []RevealCoverageTransition{{At: now, Covered: true}}
	}
	boundaries := []time.Time{now}
	for _, interval := range intervals {
		for _, boundary := range []time.Time{interval.StartedAt, interval.EndedAt} {
			if boundary.After(pausedAt) && !boundary.After(now) {
				boundaries = append(boundaries, boundary)
			}
		}
	}
	sort.Slice(boundaries, func(first, second int) bool {
		return boundaries[first].Before(boundaries[second])
	})
	transitions := make([]RevealCoverageTransition, 0, len(boundaries))
	currentlyCovered := true
	var previous time.Time
	for _, boundary := range boundaries {
		if boundary.Equal(previous) {
			continue
		}
		previous = boundary
		covered := coveredAt(boundary)
		if covered != currentlyCovered {
			transitions = append(transitions, RevealCoverageTransition{
				At: boundary, Covered: covered,
			})
			currentlyCovered = covered
		}
	}
	return transitions
}

// ReconcileRevealCoverageState applies coverage transitions without allowing a
// Reveal that completed during an uncovered interval to restart.
func ReconcileRevealCoverageState(
	state StageState,
	transitions []RevealCoverageTransition,
	now time.Time,
) StageState {
	for _, transition := range transitions {
		if transition.Covered {
			state = state.EffectiveAt(transition.At)
			if state.Status != StageRevealing {
				return state
			}
		}
		state = state.WithRevealPaused(transition.Covered, transition.At)
	}
	return state.EffectiveAt(now)
}
