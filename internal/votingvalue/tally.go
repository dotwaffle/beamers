// Package votingvalue defines persisted aggregate Voting Tally values.
package votingvalue

import (
	"slices"
	"sort"
)

// Entry is one Entry's aggregate score without Account-level Vote data.
type Entry struct {
	EntryID int `json:"entry_id"`
	Total   int `json:"total"`
	Count   int `json:"count"`
}

// EligibleEntry is one Entry included in a close-time Tally.
type EligibleEntry struct {
	EntryID, SubmitterAccountID int
}

// Ballot is one participating Account's explicit scores.
type Ballot struct {
	AccountID int
	Scores    map[int]int
}

// Aggregate applies missing-score and frozen Self-Vote rules.
func Aggregate(
	entries []EligibleEntry,
	ballots []Ballot,
	selfVotePolicy string,
) []Entry {
	result := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		total := 0
		for _, ballot := range ballots {
			value := ballot.Scores[entry.EntryID]
			if value == 0 ||
				selfVotePolicy == "Neutral" &&
					entry.SubmitterAccountID == ballot.AccountID {
				value = 3
			}
			total += value
		}
		result = append(result, Entry{
			EntryID: entry.EntryID, Total: total, Count: len(ballots),
		})
	}
	return result
}

// Placement is one tally-derived competition ranking.
type Placement struct {
	EntryID      int
	Placement    int
	DisplayOrder int
}

// Placements ranks eligible Entries by total while preserving tally order for ties.
func Placements(entries []Entry, eligibleEntryIDs []int) []Placement {
	eligible := make(map[int]struct{}, len(eligibleEntryIDs))
	for _, entryID := range eligibleEntryIDs {
		eligible[entryID] = struct{}{}
	}
	ranked := slices.Clone(entries)
	sort.SliceStable(ranked, func(first, second int) bool {
		return ranked[first].Total > ranked[second].Total
	})
	result := make([]Placement, 0, len(ranked))
	previousTotal := 0
	placement := 0
	for _, entry := range ranked {
		if _, ok := eligible[entry.EntryID]; !ok {
			continue
		}
		if len(result) == 0 || entry.Total != previousTotal {
			placement = len(result) + 1
		}
		result = append(result, Placement{
			EntryID: entry.EntryID, Placement: placement,
			DisplayOrder: len(result) + 1,
		})
		previousTotal = entry.Total
	}
	return result
}
