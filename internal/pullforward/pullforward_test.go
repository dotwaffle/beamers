package pullforward

import (
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/timingripple"
)

func TestPreviewMovesEligibleLaterSessions(t *testing.T) {
	at := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	result, err := Preview(State{
		SessionID: 1, Revision: 2, ActualEnd: at.Add(50 * time.Minute),
		Timing: []timingripple.Session{
			session(1, at, at.Add(time.Hour)),
			session(2, at.Add(time.Hour), at.Add(2*time.Hour)),
		},
	})
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if result.Fingerprint == "" || len(result.Changes) != 1 ||
		result.Changes[0].SessionID != 2 ||
		!result.Changes[0].ForecastStart.Equal(at.Add(50*time.Minute)) ||
		!result.Changes[0].ForecastEnd.Equal(at.Add(110*time.Minute)) {
		t.Fatalf("Preview() = %#v", result)
	}
}

func TestPreviewFingerprintChangesWithTiming(t *testing.T) {
	at := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	state := State{
		SessionID: 1, Revision: 2, ActualEnd: at.Add(50 * time.Minute),
		Timing: []timingripple.Session{
			session(1, at, at.Add(time.Hour)),
			session(2, at.Add(time.Hour), at.Add(2*time.Hour)),
		},
	}
	first, err := Preview(state)
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	state.Timing[1].ForecastStart = state.Timing[1].ForecastStart.Add(time.Minute)
	second, err := Preview(state)
	if err != nil {
		t.Fatalf("Preview() changed timing error = %v", err)
	}
	if first.Fingerprint == second.Fingerprint {
		t.Fatal("Fingerprint did not change with timing state")
	}
}

func session(id int, start, end time.Time) timingripple.Session {
	return timingripple.Session{
		ID: id, PlannedStart: start, PlannedEnd: end,
		ForecastStart: start, ForecastEnd: end,
		MinimumDuration: end.Sub(start),
		StartBoundary:   timingripple.Soft, EndBoundary: timingripple.Soft,
		LaneIDs: []int{1},
	}
}
