package votingvalue

import "testing"

func TestAggregateAppliesMissingAndNeutralSelfVotes(t *testing.T) {
	entries := []EligibleEntry{
		{EntryID: 10, SubmitterAccountID: 1},
		{EntryID: 20},
	}
	ballots := []Ballot{{
		AccountID: 1,
		Scores:    map[int]int{10: 5},
	}}
	allowed := Aggregate(entries, ballots, "Allowed")
	neutral := Aggregate(entries, ballots, "Neutral")
	if allowed[0].Total != 5 ||
		allowed[1].Total != 3 ||
		neutral[0].Total != 3 ||
		neutral[1].Total != 3 {
		t.Fatalf("aggregate allowed=%+v neutral=%+v", allowed, neutral)
	}
}
