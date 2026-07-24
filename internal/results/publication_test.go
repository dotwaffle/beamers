package results

import "testing"

func TestProgressiveResultsPublicationIsMonotonicAndOmitsHeldItems(t *testing.T) {
	competition := ResultItemRef{
		Kind: ResultItemCompetition, CompetitionSessionID: 11, DisplayOrder: 1,
	}
	award := ResultItemRef{
		Kind: ResultItemCompetitionAward, CompetitionSessionID: 11,
		AwardKey: "jury", DisplayOrder: 2,
	}
	input := PublicationInput{
		Policy: ResultsProgressiveOnReveal,
		Order:  []ResultItemRef{competition, award},
		States: []ResultItemStageState{
			{Ref: competition, Status: ResultItemRevealed, Release: ResultReleaseReady},
			{Ref: award, Status: ResultItemTaken, Release: ResultReleaseHeld},
		},
	}
	first, changed, err := AdvancePublication(input)
	if err != nil {
		t.Fatalf("advance progressive Results Publication: %v", err)
	}
	if !changed ||
		first.Revision != 1 ||
		first.Status != ResultsPublicationPartial ||
		len(first.Items) != 1 ||
		first.Items[0] != competition {
		t.Fatalf("first progressive Results Publication = %+v", first)
	}

	input.Current = first
	repeated, changed, err := AdvancePublication(input)
	if err != nil {
		t.Fatalf("repeat progressive Results Publication: %v", err)
	}
	if changed || repeated.Revision != first.Revision {
		t.Fatalf("repeated progressive Results Publication = %+v, changed %t", repeated, changed)
	}

	input.States[1] = ResultItemStageState{
		Ref: award, Status: ResultItemRevealed, Release: ResultReleaseReady,
	}
	second, changed, err := AdvancePublication(input)
	if err != nil {
		t.Fatalf("advance complete progressive set: %v", err)
	}
	if !changed ||
		second.Revision != 2 ||
		second.Status != ResultsPublicationPartial ||
		len(second.Items) != 2 {
		t.Fatalf("complete progressive set before Ceremony End = %+v", second)
	}

	input.Current = second
	input.CeremonyEnded = true
	final, changed, err := AdvancePublication(input)
	if err != nil {
		t.Fatalf("finalize progressive Results Publication: %v", err)
	}
	if !changed ||
		final.Revision != 3 ||
		final.Status != ResultsPublicationFinal ||
		len(final.Items) != 2 {
		t.Fatalf("final progressive Results Publication = %+v", final)
	}
}

func TestAtomicResultsPublicationPoliciesReleaseOnlyAtTheirTrigger(t *testing.T) {
	item := ResultItemRef{
		Kind: ResultItemCompetition, CompetitionSessionID: 11, DisplayOrder: 1,
	}
	skippedAward := ResultItemRef{
		Kind: ResultItemCompetitionAward, CompetitionSessionID: 11,
		AwardKey: "jury", DisplayOrder: 2,
	}
	states := []ResultItemStageState{
		{Ref: item, Status: ResultItemRevealed, Release: ResultReleaseReady},
		{Ref: skippedAward, Status: ResultItemSkipped, Release: ResultReleaseCeremonyEnd},
	}
	tests := []struct {
		name   string
		input  PublicationInput
		status PublicationStatus
	}{
		{
			name: "all at cue",
			input: PublicationInput{
				Policy: ResultsAllAtCue, Order: []ResultItemRef{item, skippedAward},
				States: states, CueFired: true,
			},
			status: ResultsPublicationFinal,
		},
		{
			name: "at ceremony end",
			input: PublicationInput{
				Policy: ResultsAtCeremonyEnd, Order: []ResultItemRef{item, skippedAward},
				States: states, CeremonyEnded: true,
			},
			status: ResultsPublicationFinal,
		},
		{
			name: "standalone",
			input: PublicationInput{
				Policy: ResultsStandalone, Order: []ResultItemRef{item},
				States: states[:1], StandaloneRelease: true,
			},
			status: ResultsPublicationFinal,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := test.input
			before.CueFired = false
			before.CeremonyEnded = false
			before.StandaloneRelease = false
			if publication, changed, err := AdvancePublication(before); err != nil ||
				changed ||
				publication.Revision != 0 {
				t.Fatalf("publication before trigger = %+v, %t, %v", publication, changed, err)
			}
			publication, changed, err := AdvancePublication(test.input)
			if err != nil {
				t.Fatalf("advance atomic Results Publication: %v", err)
			}
			if !changed ||
				publication.Revision != 1 ||
				publication.Status != test.status ||
				len(publication.Items) != len(test.input.Order) {
				t.Fatalf("atomic Results Publication = %+v", publication)
			}
		})
	}
}

func TestResultsPublicationNeverRetractsPreviouslyReleasedItems(t *testing.T) {
	first := ResultItemRef{
		Kind: ResultItemCompetition, CompetitionSessionID: 11, DisplayOrder: 1,
	}
	second := ResultItemRef{
		Kind: ResultItemEventAward, AwardKey: "community", DisplayOrder: 2,
	}
	current := Publication{
		Revision: 2, Status: ResultsPublicationPartial,
		Items: []ResultItemRef{first, second},
	}
	next, changed, err := AdvancePublication(PublicationInput{
		Policy: ResultsProgressiveOnReveal,
		Order:  []ResultItemRef{first, second},
		States: []ResultItemStageState{{
			Ref: first, Status: ResultItemRevealed, Release: ResultReleaseReady,
		}},
		Current: current,
	})
	if err != nil {
		t.Fatalf("advance from incomplete observed state: %v", err)
	}
	if changed || next.Revision != current.Revision || len(next.Items) != 2 {
		t.Fatalf("regressed Results Publication = %+v, changed %t", next, changed)
	}
}
