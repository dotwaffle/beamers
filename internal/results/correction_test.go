package results

import (
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/store"
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
						Placed: []PublicResultEntry{{
							EntryID: 2, Name: "Beta", Placement: 1,
						}},
						Unplaced: []PublicResultEntry{{
							EntryID: 1, Name: "Alpha",
						}},
						Awards: []PublicResultsAward{},
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

func TestBuildCorrectedResultsPublicationRejectsConcealedPublicDetails(t *testing.T) {
	order := []ResultItemRef{
		{Kind: ResultItemCompetition, CompetitionSessionID: 10, DisplayOrder: 1},
		{Kind: ResultItemEventAward, AwardKey: "community", DisplayOrder: 2},
	}
	tests := map[string]func([]PublicResultsItem){
		"removed stable entry": func(items []PublicResultsItem) {
			items[0].Competition.Unplaced[0].EntryID = 99
		},
		"removed public score": func(items []PublicResultsItem) {
			items[0].Competition.Placed[0].Score = ""
		},
		"removed public message": func(items []PublicResultsItem) {
			items[0].Competition.Unplaced[0].Message = ""
		},
		"blank public title": func(items []PublicResultsItem) {
			items[0].Competition.Title = " "
		},
		"removed award recipient": func(items []PublicResultsItem) {
			items[1].Award.Recipients = []string{"Beta"}
		},
		"mismatched item kind": func(items []PublicResultsItem) {
			items[0].Kind = ResultItemNoPublicResults
		},
		"multiple item arms": func(items []PublicResultsItem) {
			items[0].Award = &PublicResultsAward{
				Key: "extra", Name: "Extra", Recipients: []string{"Alpha"},
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			current := correctionTestPublication()
			current.Items[0].Competition.Placed[0].Score = "10"
			current.Items[0].Competition.Unplaced[0].Message = "Unplaced"
			proposed, err := clonePublicResultsPublication(current)
			if err != nil {
				t.Fatalf("clone public Results: %v", err)
			}
			mutate(proposed.Items)
			_, err = BuildCorrectedResultsPublication(
				current,
				order,
				CorrectionProposal{
					PublicationOrder: order,
					Items:            proposed.Items,
					Template:         DefaultResultsTextTemplate(),
					CrewReason:       "Attempt to conceal public information.",
				},
				time.Now(),
			)
			if !errors.Is(err, ErrResultsCorrection) {
				t.Fatalf("concealing Results Correction error = %v", err)
			}
		})
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
					Placed: []PublicResultEntry{{
						EntryID: 1, Name: "Alpha", Placement: 1,
					}},
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

func TestCorrectedPublicationOrderPreservesUnreleasedItems(t *testing.T) {
	locked := prizegivingItemRefInputs([]ResultItemRef{
		{Kind: ResultItemCompetition, CompetitionSessionID: 10, DisplayOrder: 1},
		{Kind: ResultItemCompetition, CompetitionSessionID: 20, DisplayOrder: 2},
		{Kind: ResultItemEventAward, AwardKey: "community", DisplayOrder: 3},
	})
	corrected := prizegivingItemRefInputs([]ResultItemRef{
		{Kind: ResultItemEventAward, AwardKey: "community", DisplayOrder: 1},
		{Kind: ResultItemCompetition, CompetitionSessionID: 10, DisplayOrder: 2},
	})
	merged := correctedPublicationOrder(locked, corrected)
	want := []store.PrizegivingResultItemRef{
		corrected[0],
		locked[1],
		corrected[1],
	}
	for index := range want {
		want[index].DisplayOrder = index + 1
		if merged[index] != want[index] {
			t.Fatalf("merged publication order[%d] = %+v, want %+v", index, merged[index], want[index])
		}
	}
}

func TestCorrectionFromStoreFailsClosedOnMalformedRevision(t *testing.T) {
	if _, err := correctionFromStore(store.ResultsCorrection{}); err != nil {
		t.Fatalf("empty Results Correction error = %v", err)
	}
	if _, err := correctionFromStore(store.ResultsCorrection{
		Revision: 1, ItemsJSON: "{",
	}); !errors.Is(err, ErrCorrectionTransition) {
		t.Fatalf("malformed Results Correction error = %v", err)
	}
}

func TestPreservePublishedResultsMatchesCorrectedItemsByIdentity(t *testing.T) {
	correction := &PublicResultsCorrection{PreviousRevision: 1}
	frozen := PublicResultsPublication{
		Event:      PublicResultsEvent{Name: "Frozen Event"},
		EventTitle: "Frozen Event",
		Correction: correction,
		Items: []PublicResultsItem{
			{
				Kind: ResultItemEventAward,
				Award: &PublicResultsAward{
					Key: "community", Name: "Corrected Community",
				},
			},
			{
				Kind: ResultItemCompetition,
				Competition: &PublicCompetitionResults{
					SessionID: 11, Title: "Corrected Final",
				},
			},
		},
	}
	model := PublicResultsPublication{
		Event: PublicResultsEvent{Name: "Current Event"},
		Items: []PublicResultsItem{
			{Kind: ResultItemEventAward, Award: &PublicResultsAward{Name: "Old Community"}},
			{Kind: ResultItemCompetitionAward, Award: &PublicResultsAward{Name: "Jury"}},
			{
				Kind: ResultItemCompetition,
				Competition: &PublicCompetitionResults{
					SessionID: 11, Title: "Old Final",
				},
			},
		},
	}
	preservePublishedResults(
		&model,
		frozen,
		[]ResultItemRef{
			{Kind: ResultItemEventAward, AwardKey: "community", DisplayOrder: 1},
			{Kind: ResultItemCompetition, CompetitionSessionID: 11, DisplayOrder: 2},
		},
		[]ResultItemRef{
			{Kind: ResultItemEventAward, AwardKey: "community", DisplayOrder: 1},
			{
				Kind:                 ResultItemCompetitionAward,
				CompetitionSessionID: 11,
				AwardKey:             "jury",
				DisplayOrder:         2,
			},
			{Kind: ResultItemCompetition, CompetitionSessionID: 11, DisplayOrder: 3},
		},
	)
	if model.Event.Name != "Frozen Event" ||
		model.Correction != correction ||
		model.Items[0].Award.Name != "Corrected Community" ||
		model.Items[1].Award.Name != "Jury" ||
		model.Items[2].Competition.Title != "Corrected Final" {
		t.Fatalf("continued corrected public Results = %+v", model)
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
					Placed: []PublicResultEntry{{
						EntryID: 1, Name: "Alpha", Placement: 1,
					}},
					Unplaced: []PublicResultEntry{{
						EntryID: 2, Name: "Beta",
					}},
					Awards: []PublicResultsAward{},
				},
			},
			{
				Kind: ResultItemEventAward,
				Award: &PublicResultsAward{
					Key: "community", Name: "Community",
					Recipients: []string{"Alpha"},
				},
			},
		},
	}
}
