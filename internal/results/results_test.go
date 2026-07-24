package results

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestReviewAcceptsCompetitionRankingAndExplicitUnplacedOrder(t *testing.T) {
	draft := Draft{
		Disposition: Publish,
		Score: ScorePolicy{
			Type: None,
		},
		Standings: []Standing{
			{EntryID: 11, Standing: Placed, Placement: 1, DisplayOrder: 1},
			{EntryID: 12, Standing: Placed, Placement: 2, DisplayOrder: 2},
			{EntryID: 13, Standing: Placed, Placement: 2, DisplayOrder: 3},
			{EntryID: 14, Standing: Placed, Placement: 4, DisplayOrder: 4},
			{EntryID: 15, Standing: Unplaced, DisplayOrder: 5},
		},
	}
	entries := []EligibleEntry{
		{ID: 11, LockedOrder: 1},
		{ID: 12, LockedOrder: 2},
		{ID: 13, LockedOrder: 3},
		{ID: 14, LockedOrder: 4},
		{ID: 15, LockedOrder: 5},
	}

	if err := Review(draft, entries); err != nil {
		t.Fatalf("review valid Results Draft: %v", err)
	}

	draft.Standings[3].Placement = 3
	if err := Review(draft, entries); !errors.Is(err, ErrCompetitionRanking) {
		t.Fatalf("review non-competition ranking error = %v", err)
	}
}

func TestReviewRequiresUnplacedEntriesInLockedOrder(t *testing.T) {
	draft := Draft{
		Disposition: Publish,
		Score:       ScorePolicy{Type: None},
		Standings: []Standing{
			{EntryID: 21, Standing: Placed, Placement: 1, DisplayOrder: 1},
			{EntryID: 23, Standing: Unplaced, DisplayOrder: 2},
			{EntryID: 22, Standing: Unplaced, DisplayOrder: 3},
		},
	}
	entries := []EligibleEntry{
		{ID: 21, LockedOrder: 1},
		{ID: 22, LockedOrder: 2},
		{ID: 23, LockedOrder: 3},
	}

	if err := Review(draft, entries); !errors.Is(err, ErrUnplacedOrder) {
		t.Fatalf("review reordered Unplaced Entries error = %v", err)
	}
}

func TestValidateDraftAcceptsExplicitNoPublicResultsWithoutStandings(t *testing.T) {
	draft := Draft{
		Disposition:    NoPublicResults,
		NoPublicReason: "Judging could not be completed",
		Score:          ScorePolicy{Type: None},
	}
	if err := ValidateDraft(draft); err != nil {
		t.Fatalf("validate No Public Results: %v", err)
	}

	draft.NoPublicReason = ""
	if err := ValidateDraft(draft); !errors.Is(err, ErrCrewReasonRequired) {
		t.Fatalf("validate No Public Results without Crew Reason error = %v", err)
	}
}

func TestReviewRequiresExactScoresWithoutDerivingPlacement(t *testing.T) {
	firstScore := "9.50"
	draft := Draft{
		Disposition: Publish,
		Score: ScorePolicy{
			Type: Decimal, Visibility: ScorePublic, Unit: "points",
			Precision: 2, Requirement: ScoreRequired, Interpretation: HigherWins,
		},
		Standings: []Standing{
			{
				EntryID: 41, Standing: Placed, Placement: 1, DisplayOrder: 1,
				Score: ScoreValue{Decimal: &firstScore},
			},
			{EntryID: 42, Standing: Placed, Placement: 2, DisplayOrder: 2},
		},
	}
	entries := []EligibleEntry{{ID: 41, LockedOrder: 1}, {ID: 42, LockedOrder: 2}}

	if err := Review(draft, entries); !errors.Is(err, ErrScoreRequired) {
		t.Fatalf("review missing required Score error = %v", err)
	}

	secondScore := "10.00"
	draft.Standings[1].Score.Decimal = &secondScore
	if err := Review(draft, entries); err != nil {
		t.Fatalf("review score/Placement contradiction: %v", err)
	}

	invalidScore := "1e1"
	draft.Standings[1].Score.Decimal = &invalidScore
	if err := Review(draft, entries); !errors.Is(err, ErrInvalidScore) {
		t.Fatalf("review non-decimal Score error = %v", err)
	}

	negativeDuration := -time.Second
	draft.Score.Type = Duration
	draft.Score.Unit = "seconds"
	draft.Standings[0].Score = ScoreValue{Duration: &negativeDuration}
	draft.Standings[1].Score = ScoreValue{Duration: &negativeDuration}
	if err := Review(draft, entries); !errors.Is(err, ErrInvalidScore) {
		t.Fatalf("review negative Duration Score error = %v", err)
	}
}

func TestValidateDraftRejectsScoresBeyondStorageBounds(t *testing.T) {
	decimal := strings.Repeat("1", 201)
	draft := Draft{
		Disposition: Publish,
		Score: ScorePolicy{
			Type: Decimal, Visibility: ScorePublic,
			Unit: strings.Repeat("u", 101), Precision: 2,
			Requirement: ScoreOptional, Interpretation: Informational,
		},
		Standings: []Standing{{
			EntryID: 1, Standing: Placed, Placement: 1, DisplayOrder: 1,
			Score: ScoreValue{Decimal: &decimal},
		}},
	}
	if err := ValidateDraft(draft); !errors.Is(err, ErrInvalidScore) {
		t.Fatalf("oversized Score policy error = %v", err)
	}
}
