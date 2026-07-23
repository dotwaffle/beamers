package timingripple

import (
	"testing"
	"time"
)

func TestAdjustTargetRipplesOnlyAnchorLanes(t *testing.T) {
	at := timeline()
	plan, err := Calculate([]Session{
		session(1, at, at.Add(time.Hour), []int{1}),
		session(2, at.Add(time.Hour), at.Add(2*time.Hour), []int{1}),
		session(3, at.Add(time.Hour), at.Add(2*time.Hour), []int{2}),
	}, AdjustTarget{SessionID: 1, TargetEnd: at.Add(time.Hour + 10*time.Minute)})
	if err != nil {
		t.Fatalf("Calculate() error = %v", err)
	}
	assertChange(t, plan, 1, at, at.Add(time.Hour+10*time.Minute))
	assertChange(t, plan, 2, at.Add(time.Hour+10*time.Minute), at.Add(2*time.Hour+10*time.Minute))
	assertNoChange(t, plan, 3)
}

func TestAdjustTargetDoesNotNormalizeEarlierOverlap(t *testing.T) {
	at := timeline()
	plan, err := Calculate([]Session{
		session(1, at, at.Add(70*time.Minute), []int{1}),
		session(2, at.Add(time.Hour), at.Add(2*time.Hour), []int{1}),
		session(3, at.Add(2*time.Hour), at.Add(3*time.Hour), []int{1}),
	}, AdjustTarget{SessionID: 2, TargetEnd: at.Add(2*time.Hour + 5*time.Minute)})
	if err != nil {
		t.Fatalf("Calculate() error = %v", err)
	}
	assertNoChange(t, plan, 1)
	assertChange(t, plan, 2, at.Add(time.Hour), at.Add(2*time.Hour+5*time.Minute))
	assertChange(t, plan, 3, at.Add(2*time.Hour+5*time.Minute), at.Add(3*time.Hour+5*time.Minute))
}

func TestAdjustTargetCompressesBeforeRippling(t *testing.T) {
	at := timeline()
	next := session(2, at.Add(time.Hour), at.Add(2*time.Hour), []int{1})
	next.MinimumDuration = 55 * time.Minute
	plan, err := Calculate([]Session{
		session(1, at, at.Add(time.Hour), []int{1}),
		next,
		session(3, at.Add(2*time.Hour), at.Add(3*time.Hour), []int{1}),
	}, AdjustTarget{SessionID: 1, TargetEnd: at.Add(time.Hour + 10*time.Minute)})
	if err != nil {
		t.Fatalf("Calculate() error = %v", err)
	}
	assertChange(t, plan, 2, at.Add(time.Hour+10*time.Minute), at.Add(2*time.Hour+5*time.Minute))
	assertChange(t, plan, 3, at.Add(2*time.Hour+5*time.Minute), at.Add(3*time.Hour+5*time.Minute))
}

func TestAdjustTargetCanCompressExplicitZeroMinimumDuration(t *testing.T) {
	at := timeline()
	next := session(2, at.Add(time.Hour), at.Add(2*time.Hour), []int{1})
	next.MinimumDuration = 0
	plan, err := Calculate([]Session{
		session(1, at, at.Add(time.Hour), []int{1}), next,
	}, AdjustTarget{SessionID: 1, TargetEnd: at.Add(time.Hour + 10*time.Minute)})
	if err != nil {
		t.Fatalf("Calculate() error = %v", err)
	}
	assertChange(t, plan, 2, at.Add(time.Hour+10*time.Minute), at.Add(2*time.Hour))
}

func TestAdjustTargetDoesNotMoveHardBoundary(t *testing.T) {
	at := timeline()
	next := session(2, at.Add(time.Hour), at.Add(2*time.Hour), []int{1})
	next.StartBoundary = Hard
	plan, err := Calculate([]Session{
		session(1, at, at.Add(time.Hour), []int{1}), next,
	}, AdjustTarget{SessionID: 1, TargetEnd: at.Add(time.Hour + 10*time.Minute)})
	if err != nil {
		t.Fatalf("Calculate() error = %v", err)
	}
	assertNoChange(t, plan, 2)
	if len(plan.HardCollisions) != 1 || plan.HardCollisions[0].SessionID != 2 ||
		plan.HardCollisions[0].Overlap != 10*time.Minute {
		t.Fatalf("HardCollisions = %#v", plan.HardCollisions)
	}
}

func TestAdjustTargetCompressesToMinimumBeforeHardEndCollision(t *testing.T) {
	at := timeline()
	next := session(2, at.Add(time.Hour), at.Add(2*time.Hour), []int{1})
	next.MinimumDuration = 55 * time.Minute
	next.EndBoundary = Hard
	plan, err := Calculate([]Session{
		session(1, at, at.Add(time.Hour), []int{1}), next,
	}, AdjustTarget{SessionID: 1, TargetEnd: at.Add(time.Hour + 10*time.Minute)})
	if err != nil {
		t.Fatalf("Calculate() error = %v", err)
	}
	assertChange(t, plan, 2, at.Add(time.Hour+5*time.Minute), at.Add(2*time.Hour))
	if len(plan.HardCollisions) != 1 || plan.HardCollisions[0].SessionID != 2 ||
		plan.HardCollisions[0].Overlap != 5*time.Minute {
		t.Fatalf("HardCollisions = %#v", plan.HardCollisions)
	}
}

func TestAdjustTargetSynchronizesSharedSessionAndItsLanes(t *testing.T) {
	at := timeline()
	plan, err := Calculate([]Session{
		session(1, at, at.Add(time.Hour), []int{1}),
		session(2, at.Add(time.Hour), at.Add(2*time.Hour), []int{1, 2}),
		session(3, at.Add(2*time.Hour), at.Add(3*time.Hour), []int{2}),
		session(4, at.Add(time.Hour), at.Add(2*time.Hour), []int{3}),
	}, AdjustTarget{SessionID: 1, TargetEnd: at.Add(time.Hour + 10*time.Minute)})
	if err != nil {
		t.Fatalf("Calculate() error = %v", err)
	}
	assertChange(t, plan, 2, at.Add(time.Hour+10*time.Minute), at.Add(2*time.Hour+10*time.Minute))
	assertChange(t, plan, 3, at.Add(2*time.Hour+10*time.Minute), at.Add(3*time.Hour+10*time.Minute))
	assertNoChange(t, plan, 4)
}

func TestAdjustTargetReportsLocationCollisionFromMovedSession(t *testing.T) {
	at := timeline()
	anchor := session(1, at, at.Add(time.Hour), []int{1})
	anchor.LocationIDs = []int{10}
	moved := session(2, at.Add(time.Hour), at.Add(2*time.Hour), []int{1})
	moved.LocationIDs = []int{20}
	occupied := session(3, at.Add(2*time.Hour), at.Add(3*time.Hour), []int{2})
	occupied.LocationIDs = []int{20}
	occupied.StartBoundary = Hard
	plan, err := Calculate(
		[]Session{anchor, moved, occupied},
		AdjustTarget{SessionID: 1, TargetEnd: at.Add(time.Hour + 5*time.Minute)},
	)
	if err != nil {
		t.Fatalf("Calculate() error = %v", err)
	}
	assertEffectOverlap(t, plan, 2, 5*time.Minute)
	assertEffectOverlap(t, plan, 3, 5*time.Minute)
	if len(plan.HardCollisions) != 1 ||
		plan.HardCollisions[0].SessionID != 3 {
		t.Fatalf("HardCollisions = %#v, want Session 3", plan.HardCollisions)
	}
}

func TestAdjustTargetReportsChangedLocationPairWithSameMaximumOverlap(t *testing.T) {
	at := timeline()
	anchor := session(1, at, at.Add(time.Hour), []int{1})
	moved := session(2, at.Add(time.Hour), at.Add(2*time.Hour), []int{1})
	moved.LocationIDs = []int{20}
	hard := session(3, at.Add(2*time.Hour), at.Add(3*time.Hour), []int{2})
	hard.LocationIDs = []int{20}
	hard.StartBoundary = Hard
	existing := session(
		4,
		at.Add(2*time.Hour+50*time.Minute),
		at.Add(3*time.Hour),
		[]int{3},
	)
	existing.LocationIDs = []int{20}
	plan, err := Calculate(
		[]Session{anchor, moved, hard, existing},
		AdjustTarget{SessionID: 1, TargetEnd: at.Add(time.Hour + 10*time.Minute)},
	)
	if err != nil {
		t.Fatalf("Calculate() error = %v", err)
	}
	for _, effect := range plan.Effects {
		if effect.SessionID == 3 {
			if effect.CurrentOverlap != 10*time.Minute ||
				effect.ProposedOverlap != 10*time.Minute {
				t.Fatalf("Session 3 effect = %#v", effect)
			}
			if len(plan.HardCollisions) != 1 ||
				plan.HardCollisions[0].SessionID != 3 {
				t.Fatalf("HardCollisions = %#v, want Session 3", plan.HardCollisions)
			}
			return
		}
	}
	t.Fatalf("Session 3 changed occupancy pair is absent: %#v", plan.Effects)
}

func TestEarlierTargetDoesNotImplicitlyPullForward(t *testing.T) {
	at := timeline()
	plan, err := Calculate([]Session{
		session(1, at, at.Add(time.Hour), []int{1}),
		session(2, at.Add(time.Hour), at.Add(2*time.Hour), []int{1}),
	}, AdjustTarget{SessionID: 1, TargetEnd: at.Add(50 * time.Minute)})
	if err != nil {
		t.Fatalf("Calculate() error = %v", err)
	}
	assertNoChange(t, plan, 2)
}

func TestPullForwardMovesOnlyEligibleSoftBoundaries(t *testing.T) {
	at := timeline()
	hard := session(3, at.Add(2*time.Hour), at.Add(3*time.Hour), []int{1})
	hard.StartBoundary = Hard
	plan, err := Calculate([]Session{
		session(1, at, at.Add(time.Hour), []int{1}),
		session(2, at.Add(time.Hour), at.Add(2*time.Hour), []int{1}),
		hard,
	}, PullForward{SessionID: 1, ActualEnd: at.Add(50 * time.Minute)})
	if err != nil {
		t.Fatalf("Calculate() error = %v", err)
	}
	assertChange(t, plan, 2, at.Add(50*time.Minute), at.Add(110*time.Minute))
	assertNoChange(t, plan, 3)
}

func TestPullForwardAfterForecastEndIsNoOp(t *testing.T) {
	at := timeline()
	plan, err := Calculate([]Session{
		session(1, at, at.Add(time.Hour), []int{1}),
		session(2, at.Add(time.Hour), at.Add(2*time.Hour), []int{1}),
	}, PullForward{SessionID: 1, ActualEnd: at.Add(70 * time.Minute)})
	if err != nil {
		t.Fatalf("Calculate() error = %v", err)
	}
	if len(plan.Changes) != 0 {
		t.Fatalf("Changes = %#v, want none", plan.Changes)
	}
}

func TestPullForwardKeepsSharedSessionAfterEveryLanePredecessor(t *testing.T) {
	at := timeline()
	plan, err := Calculate([]Session{
		session(1, at, at.Add(time.Hour), []int{1}),
		session(4, at, at.Add(55*time.Minute), []int{2}),
		session(2, at.Add(time.Hour), at.Add(2*time.Hour), []int{1, 2}),
	}, PullForward{SessionID: 1, ActualEnd: at.Add(50 * time.Minute)})
	if err != nil {
		t.Fatalf("Calculate() error = %v", err)
	}
	assertChange(t, plan, 2, at.Add(55*time.Minute), at.Add(115*time.Minute))
}

func TestPullForwardReportsLocationCollisionFromMovedSession(t *testing.T) {
	at := timeline()
	anchor := session(1, at, at.Add(time.Hour), []int{1})
	moved := session(2, at.Add(time.Hour), at.Add(2*time.Hour), []int{1})
	moved.LocationIDs = []int{20}
	occupied := session(3, at.Add(30*time.Minute), at.Add(time.Hour), []int{2})
	occupied.LocationIDs = []int{20}
	plan, err := Calculate(
		[]Session{anchor, moved, occupied},
		PullForward{SessionID: 1, ActualEnd: at.Add(50 * time.Minute)},
	)
	if err != nil {
		t.Fatalf("Calculate() error = %v", err)
	}
	assertEffectOverlap(t, plan, 2, 10*time.Minute)
	assertEffectOverlap(t, plan, 3, 10*time.Minute)
}

func TestPullForwardMovesOnlySoftEdgeOfHardEndSession(t *testing.T) {
	at := timeline()
	next := session(2, at.Add(time.Hour), at.Add(2*time.Hour), []int{1})
	next.EndBoundary = Hard
	plan, err := Calculate([]Session{
		session(1, at, at.Add(time.Hour), []int{1}), next,
	}, PullForward{SessionID: 1, ActualEnd: at.Add(50 * time.Minute)})
	if err != nil {
		t.Fatalf("Calculate() error = %v", err)
	}
	assertChange(t, plan, 2, at.Add(50*time.Minute), at.Add(2*time.Hour))
}

func TestCalculateRejectsRepeatedLaneMembership(t *testing.T) {
	at := timeline()
	_, err := Calculate([]Session{
		session(1, at, at.Add(time.Hour), []int{1, 1}),
	}, AdjustTarget{SessionID: 1, TargetEnd: at.Add(50 * time.Minute)})
	if err == nil {
		t.Fatal("Calculate() accepted repeated Lane membership")
	}
}

func TestAdjustTargetAtSessionCapacity(t *testing.T) {
	const sessionCount = 25_000
	at := timeline()
	sessions := make([]Session, 0, sessionCount)
	for index := range sessionCount {
		start := at.Add(time.Duration(index) * time.Minute)
		sessions = append(sessions, session(index+1, start, start.Add(time.Minute), []int{1}))
	}
	plan, err := Calculate(sessions, AdjustTarget{
		SessionID: 1, TargetEnd: at.Add(time.Minute + time.Second),
	})
	if err != nil {
		t.Fatalf("Calculate() error = %v", err)
	}
	if len(plan.Changes) != sessionCount {
		t.Fatalf("len(Changes) = %d, want %d", len(plan.Changes), sessionCount)
	}
	last := plan.Changes[len(plan.Changes)-1]
	wantEnd := at.Add(sessionCount*time.Minute + time.Second)
	if !last.ForecastEnd.Equal(wantEnd) {
		t.Fatalf("last Forecast End = %v, want %v", last.ForecastEnd, wantEnd)
	}
}

func TestFingerprintDistinguishesLaneAndLocationMemberships(t *testing.T) {
	at := timeline()
	first := session(1, at, at.Add(time.Hour), []int{1, 2})
	second := session(1, at, at.Add(time.Hour), []int{1})
	second.LocationIDs = []int{2}
	action := AdjustTarget{SessionID: 1, TargetEnd: at.Add(50 * time.Minute)}
	if Fingerprint([]Session{first}, action, 0) ==
		Fingerprint([]Session{second}, action, 0) {
		t.Fatal("Fingerprint conflated Lane and Location memberships")
	}
}

func timeline() time.Time {
	return time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
}

func session(id int, start, end time.Time, lanes []int) Session {
	return Session{
		ID: id, PlannedStart: start, PlannedEnd: end,
		ForecastStart: start, ForecastEnd: end,
		MinimumDuration: end.Sub(start),
		StartBoundary:   Soft, EndBoundary: Soft, LaneIDs: lanes,
	}
}

func assertChange(t *testing.T, plan Plan, id int, start, end time.Time) {
	t.Helper()
	for _, change := range plan.Changes {
		if change.SessionID == id {
			if !change.ForecastStart.Equal(start) || !change.ForecastEnd.Equal(end) {
				t.Fatalf("Session %d change = %#v, want %v-%v", id, change, start, end)
			}
			return
		}
	}
	t.Fatalf("Session %d has no change in %#v", id, plan.Changes)
}

func assertNoChange(t *testing.T, plan Plan, id int) {
	t.Helper()
	for _, change := range plan.Changes {
		if change.SessionID == id {
			t.Fatalf("Session %d unexpectedly changed: %#v", id, change)
		}
	}
}

func assertEffectOverlap(t *testing.T, plan Plan, id int, overlap time.Duration) {
	t.Helper()
	for _, effect := range plan.Effects {
		if effect.SessionID == id {
			if effect.ProposedOverlap != overlap {
				t.Fatalf(
					"Session %d ProposedOverlap = %v, want %v",
					id, effect.ProposedOverlap, overlap,
				)
			}
			return
		}
	}
	t.Fatalf("Session %d has no effect in %#v", id, plan.Effects)
}
