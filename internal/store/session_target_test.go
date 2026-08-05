package store

import (
	"errors"
	"slices"
	"testing"

	"github.com/dotwaffle/beamers/internal/sessiontarget"
	"github.com/dotwaffle/beamers/internal/timingripple"
)

func TestTimingStateCollectsAffectedLanesOnce(t *testing.T) {
	state := timingState{Sessions: []timingripple.Session{
		{ID: 1, LaneIDs: []int{2, 1}},
		{ID: 2, LaneIDs: []int{2, 3}},
		{ID: 3, LaneIDs: []int{4}},
	}}
	found := state.affectedLaneIDs([]timingripple.Change{
		{SessionID: 1}, {SessionID: 2},
	})
	if !slices.Equal(found, []int{1, 2, 3}) {
		t.Fatalf("affectedLaneIDs() = %v, want [1 2 3]", found)
	}
}

// TestIsSessionTargetPreviewDomainErrorDistinguishesFailureKinds is a
// regression test: AdjustSessionTargetLaneScope must swallow only
// sessiontarget.Preview's own domain rejections, not the structural or
// opaque failures previewSessionTarget can return before ever calling
// Preview. Swallowing those too would let an authorize-phase failure that
// has nothing to do with Lane scope silently narrow the judged Lanes to the
// anchor alone, rather than aborting the command outright.
func TestIsSessionTargetPreviewDomainErrorDistinguishesFailureKinds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		err    error
		domain bool
	}{
		{name: "preset not configured", err: sessiontarget.ErrPresetNotConfigured, domain: true},
		{name: "target before now", err: sessiontarget.ErrTargetBeforeNow, domain: true},
		{name: "no countdown target", err: sessiontarget.ErrNoCountdownTarget, domain: true},
		{name: "event not active", err: ErrEventNotActive},
		{name: "session scope required", err: ErrSessionScopeRequired},
		{name: "session not found", err: ErrSessionNotFound},
		{name: "session lifecycle transition", err: ErrSessionLifecycleTransition},
		{name: "opaque store failure", err: opaqueError("load Adjust Target Session", errors.New("driver failure"))},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if found := isSessionTargetPreviewDomainError(testCase.err); found != testCase.domain {
				t.Fatalf("isSessionTargetPreviewDomainError(%v) = %t, want %t", testCase.err, found, testCase.domain)
			}
		})
	}
}
