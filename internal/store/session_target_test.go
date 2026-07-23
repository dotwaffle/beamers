package store

import (
	"slices"
	"testing"

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
