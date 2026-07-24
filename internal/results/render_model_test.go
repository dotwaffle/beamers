package results

import (
	"testing"
	"time"
)

func TestBuildPublicResultsModelAppliesVisibilityAndPublicationOrder(t *testing.T) {
	model, err := BuildPublicResultsModel(PublicResultsSource{
		EventName:   "Demo",
		Revision:    4,
		Status:      ResultsPublicationPartial,
		PublishedAt: time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC),
		Items: []PublicResultsSourceItem{
			{
				Ref: ResultItemRef{
					Kind:                 ResultItemNoPublicResults,
					CompetitionSessionID: 12,
					DisplayOrder:         1,
				},
				CompetitionTitle: "Private Final",
			},
			{
				Ref: ResultItemRef{
					Kind:                 ResultItemCompetition,
					CompetitionSessionID: 11,
					DisplayOrder:         2,
				},
				CompetitionTitle: "Open Final",
				Score: ScorePolicy{
					Type: Decimal, Visibility: ScoreCrewOnly, Unit: "pts",
					Precision: 2,
				},
				Entries: []PublicResultsSourceEntry{
					{
						Name: "Aurora", ResultDisposition: "Eligible",
						Standing: Placed, Placement: 1, DisplayOrder: 1,
						DecimalScore: "12.50",
					},
					{
						Name: "Borealis", ResultDisposition: "Eligible",
						Standing: Unplaced, LockedOrder: 2, DecimalScore: "11.00",
					},
					{
						Name: "Cygnus", ResultDisposition: "Disqualified",
						LockedOrder: 3, PublicDisqualificationMessage: "Missed checkpoint",
					},
					{Name: "Hidden", ResultDisposition: "Withheld", LockedOrder: 4},
				},
				Awards: []PublicResultsSourceAward{{
					Name: "Audience Choice", Recipients: []string{"Aurora"},
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("build public Results model: %v", err)
	}
	if len(model.Items) != 2 ||
		model.Items[0].NoPublicResults == nil ||
		model.Items[0].NoPublicResults.Explanation != "No results published." ||
		model.Items[1].Competition == nil {
		t.Fatalf("ordered public Results model = %+v", model)
	}
	competition := model.Items[1].Competition
	if len(competition.Placed) != 1 ||
		competition.Placed[0].Score != "" ||
		len(competition.Unplaced) != 1 ||
		len(competition.Disqualified) != 1 ||
		competition.Disqualified[0].Message != "Missed checkpoint" ||
		len(competition.Awards) != 1 {
		t.Fatalf("public Competition model = %+v", competition)
	}
}

func TestBuildPublicResultsModelFormatsPublicScoresAndIndependentAwards(t *testing.T) {
	duration := 90*time.Second + 250*time.Millisecond
	model, err := BuildPublicResultsModel(PublicResultsSource{
		EventName: "Demo", Revision: 2, Status: ResultsPublicationFinal,
		Items: []PublicResultsSourceItem{
			{
				Ref: ResultItemRef{
					Kind:                 ResultItemCompetition,
					CompetitionSessionID: 11,
					DisplayOrder:         1,
				},
				CompetitionTitle: "Timed Final",
				Score: ScorePolicy{
					Type: Duration, Visibility: ScorePublic,
					Unit: "s", Precision: 3,
				},
				Entries: []PublicResultsSourceEntry{{
					Name: "Aurora", ResultDisposition: "Eligible",
					Standing: Placed, Placement: 1, DisplayOrder: 1,
					DurationScore: &duration,
				}},
			},
			{
				Ref: ResultItemRef{
					Kind: ResultItemEventAward, AwardKey: "community",
					DisplayOrder: 2,
				},
				Award: &PublicResultsSourceAward{
					Name: "Community Award", Recipients: []string{"Borealis"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("build scored public Results model: %v", err)
	}
	if got := model.Items[0].Competition.Placed[0].Score; got != "90.250 s" {
		t.Fatalf("public Duration score = %q", got)
	}
	if model.Items[1].Award == nil ||
		model.Items[1].Award.Name != "Community Award" {
		t.Fatalf("independent Award = %+v", model.Items[1])
	}
}
