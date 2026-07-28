package results

import (
	"testing"

	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/votingvalue"
)

func TestFollowsVotingTallyPreservesTiesAndOrdering(t *testing.T) {
	tally := store.VotingTally{Entries: []votingvalue.Entry{
		{EntryID: 10, Total: 8, Count: 2},
		{EntryID: 20, Total: 8, Count: 2},
		{EntryID: 30, Total: 4, Count: 2},
	}}
	eligible := []store.CompetitionResultsEligibleEntry{
		{ID: 10}, {ID: 20}, {ID: 30},
	}
	derived := []Standing{
		{EntryID: 10, Standing: Placed, Placement: 1, DisplayOrder: 1},
		{EntryID: 20, Standing: Placed, Placement: 1, DisplayOrder: 2},
		{EntryID: 30, Standing: Placed, Placement: 3, DisplayOrder: 3},
	}
	if !followsVotingTally(derived, tally, eligible) {
		t.Fatal("tally-derived tie was treated as an override")
	}
	derived[1].Placement = 2
	if followsVotingTally(derived, tally, eligible) {
		t.Fatal("resolved tie was not treated as an override")
	}
}
