package results

import (
	"errors"
	"testing"
	"time"
)

func TestBuildCorrectedResultsPublicationPreservesCoverageAndAllowsReorder(t *testing.T) {
	now := time.Date(2026, 8, 21, 16, 0, 0, 0, time.UTC)
	current := correctionTestPublication()
	order := []ResultItemRef{
		{Kind: ResultItemCompetition, CompetitionSessionID: 10, DisplayOrder: 1},
		{Kind: ResultItemEventAward, AwardKey: "community", DisplayOrder: 2},
	}
	proposedOrder := []ResultItemRef{
		{Kind: ResultItemEventAward, AwardKey: "community", DisplayOrder: 1},
		{Kind: ResultItemCompetition, CompetitionSessionID: 10, DisplayOrder: 2},
	}
	corrected, err := BuildCorrectedResultsPublication(
		current,
		order,
		CorrectionProposal{
			PublicationOrder: proposedOrder,
			Items: []PublicResultsItem{
				current.Items[1],
				{
					Kind: ResultItemCompetition,
					Competition: &PublicCompetitionResults{
						SessionID: 10, Title: "Corrected Final",
						Placed:   []PublicResultEntry{{Name: "Beta", Placement: 1}},
						Unplaced: []PublicResultEntry{{Name: "Alpha"}},
						Awards:   []PublicResultsAward{},
					},
				},
			},
			Template:   DefaultResultsTextTemplate(),
			CrewReason: "The announced placement was transposed.",
			PublicNote: "Placements corrected after review.",
		},
		now,
	)
	if err != nil {
		t.Fatalf("build Results Correction: %v", err)
	}
	if corrected.Revision != 4 ||
		corrected.Correction == nil ||
		corrected.Correction.PreviousRevision != 3 ||
		corrected.Correction.Note != "Placements corrected after review." ||
		corrected.Items[0].Award == nil ||
		corrected.Items[1].Competition.Title != "Corrected Final" {
		t.Fatalf("corrected Results Publication = %+v", corrected)
	}
}

func TestBuildCorrectedResultsPublicationRejectsRetraction(t *testing.T) {
	current := correctionTestPublication()
	order := []ResultItemRef{
		{Kind: ResultItemCompetition, CompetitionSessionID: 10, DisplayOrder: 1},
		{Kind: ResultItemEventAward, AwardKey: "community", DisplayOrder: 2},
	}
	proposal := CorrectionProposal{
		PublicationOrder: order,
		Items: []PublicResultsItem{
			{
				Kind: ResultItemCompetition,
				Competition: &PublicCompetitionResults{
					SessionID: 10, Title: "Final",
					Placed: []PublicResultEntry{{Name: "Alpha", Placement: 1}},
				},
			},
			current.Items[1],
		},
		Template:   DefaultResultsTextTemplate(),
		CrewReason: "Remove a public participant.",
	}
	if _, err := BuildCorrectedResultsPublication(
		current,
		order,
		proposal,
		time.Now(),
	); !errors.Is(err, ErrResultsCorrection) {
		t.Fatalf("retracting Results Correction error = %v", err)
	}
	proposal.Items = current.Items
	proposal.CrewReason = " "
	if _, err := BuildCorrectedResultsPublication(
		current,
		order,
		proposal,
		time.Now(),
	); !errors.Is(err, ErrResultsCorrection) {
		t.Fatalf("reasonless Results Correction error = %v", err)
	}
}

func correctionTestPublication() PublicResultsPublication {
	return PublicResultsPublication{
		SchemaVersion: "1", Event: PublicResultsEvent{Name: "Demo"},
		EventTitle: "Demo", Revision: 3, Status: ResultsPublicationFinal,
		Items: []PublicResultsItem{
			{
				Kind: ResultItemCompetition,
				Competition: &PublicCompetitionResults{
					SessionID: 10, Title: "Final",
					Placed:   []PublicResultEntry{{Name: "Alpha", Placement: 1}},
					Unplaced: []PublicResultEntry{{Name: "Beta"}},
					Awards:   []PublicResultsAward{},
				},
			},
			{
				Kind: ResultItemEventAward,
				Award: &PublicResultsAward{
					Name: "Community", Recipients: []string{"Alpha"},
				},
			},
		},
	}
}
