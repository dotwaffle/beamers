package results

import (
	"reflect"
	"testing"
)

func TestPrizegivingPreflightLocksExactReviewedSources(t *testing.T) {
	competitionItem := ResultItem{
		Kind: ResultItemCompetition, CompetitionSessionID: 11,
		DisplayOrder: 1, RevealMethod: RevealSequentialPodium,
	}
	promotedAward := ResultItem{
		Kind: ResultItemCompetitionAward, CompetitionSessionID: 11,
		AwardKey: "judges-choice", DisplayOrder: 2, RevealMethod: RevealStatic,
	}
	eventAward := ResultItem{
		Kind: ResultItemEventAward, AwardKey: "community",
		DisplayOrder: 3, RevealMethod: RevealStatic,
	}
	input := PrizegivingPreflightInput{
		EventID:               3,
		CeremonySessionID:     7,
		PlanRevision:          4,
		CompetitionSessionIDs: []int{11},
		Sequence:              []ResultItem{competitionItem, promotedAward, eventAward},
		PublicationOrder: []ResultItemRef{
			eventAward.Ref(1), competitionItem.Ref(2), promotedAward.Ref(3),
		},
		Template: TextTemplate{
			Revision: 9,
			Source:   "{{.EventTitle}}\n",
		},
		Competitions: []PrizegivingCompetitionSource{{
			Draft: Draft{
				ID: 31, EventID: 3, SessionID: 11, Revision: 6,
				Disposition: Publish, Ready: true,
				Score: ScorePolicy{Type: None},
				Awards: []Award{{
					Key: "judges-choice", Name: "Judges' Choice",
					Recipients: []AwardRecipient{{DisplayName: "Ari"}},
					Promoted:   true, DisplayOrder: 1,
				}},
			},
		}},
		EventAwards: PrizegivingEventAwardsSource{
			DraftRevision: 8,
			PathRevision:  5,
			Ready:         true,
			Awards: []EventAward{{
				Award: Award{
					Key: "community", Name: "Community",
					Recipients:   []AwardRecipient{{DisplayName: "Volunteers"}},
					DisplayOrder: 1,
				},
				ReleasePath: AwardReleasePath{
					Kind: PrizegivingRelease, PrizegivingSessionID: 7,
				},
			}},
		},
	}

	locked, findings := BuildPrizegivingPreflight(input, "preflight-command")

	if len(findings) != 0 {
		t.Fatalf("Prizegiving Preflight findings = %+v", findings)
	}
	if locked.PlanRevision != 4 ||
		len(locked.CompetitionSources) != 1 ||
		locked.CompetitionSources[0].DraftRevision != 6 ||
		locked.EventAwardsDraftRevision != 8 ||
		locked.EventAwardsPathRevision != 5 ||
		locked.Template.Revision != 9 {
		t.Fatalf("locked Prizegiving sources = %+v", locked)
	}
	if len(locked.Sequence) != 3 ||
		locked.Sequence[0].RevealSeed == 0 ||
		locked.Sequence[0].RevealSeed == locked.Sequence[1].RevealSeed {
		t.Fatalf("locked Reveal Seeds = %+v", locked.Sequence)
	}
	if locked.PublicationOrder[0].Kind != ResultItemEventAward ||
		locked.Sequence[0].Kind != ResultItemCompetition {
		t.Fatalf(
			"independent order lost: sequence=%+v publication=%+v",
			locked.Sequence,
			locked.PublicationOrder,
		)
	}
}

func TestPrizegivingPreflightReportsEveryReleaseBlocker(t *testing.T) {
	input := PrizegivingPreflightInput{
		EventID:               3,
		CeremonySessionID:     7,
		PlanRevision:          2,
		CompetitionSessionIDs: []int{11, 12},
		Sequence: []ResultItem{
			{
				Kind: ResultItemCompetition, CompetitionSessionID: 11,
				DisplayOrder: 1, RevealMethod: RevealStatic,
			},
			{
				Kind: ResultItemCompetition, CompetitionSessionID: 12,
				DisplayOrder: 2, RevealMethod: "UnknownMethod",
			},
			{
				Kind: ResultItemEventAward, AwardKey: "community",
				DisplayOrder: 3, RevealMethod: RevealStatic,
			},
		},
		PublicationOrder: []ResultItemRef{
			{
				Kind: ResultItemCompetition, CompetitionSessionID: 11,
				DisplayOrder: 1,
			},
			{
				Kind: ResultItemCompetition, CompetitionSessionID: 12,
				DisplayOrder: 2,
			},
			{Kind: ResultItemEventAward, AwardKey: "community", DisplayOrder: 3},
		},
		Template: TextTemplate{
			Revision: 3,
			Source:   `{{call .Command}}`,
		},
		Competitions: []PrizegivingCompetitionSource{
			{
				Draft: Draft{
					ID: 31, EventID: 3, SessionID: 11, Revision: 4,
					Disposition: Pending, Score: ScorePolicy{Type: None},
				},
			},
			{
				Draft: Draft{
					ID: 32, EventID: 3, SessionID: 12, Revision: 5,
					Disposition: Publish, Ready: false,
					Score: ScorePolicy{
						Type: Decimal, Requirement: ScoreRequired,
					},
					Standings: []Standing{{
						EntryID: 41, Standing: Placed, Placement: 1,
						DisplayOrder: 1,
					}},
				},
				ResolutionRequired: true,
			},
		},
		EventAwards: PrizegivingEventAwardsSource{
			DraftRevision: 7,
			PathRevision:  2,
			Ready:         false,
			Awards: []EventAward{{
				Award: Award{
					Key: "community", Name: "Community",
					Recipients:   []AwardRecipient{{DisplayName: "Volunteers"}},
					DisplayOrder: 1,
				},
				ReleasePath: AwardReleasePath{
					Kind: PrizegivingRelease, PrizegivingSessionID: 7,
				},
			}},
		},
	}

	_, findings := BuildPrizegivingPreflight(input, "blocked")
	codes := make(map[string]bool, len(findings))
	for _, finding := range findings {
		codes[finding.Code] = true
	}
	for _, code := range []string{
		"pending_disposition",
		"results_not_ready",
		"resolution_required",
		"required_score_missing",
		"invalid_reveal_method",
		"unsafe_results_template",
		"event_awards_not_ready",
	} {
		if !codes[code] {
			t.Errorf("Prizegiving Preflight findings = %+v, missing %q", findings, code)
		}
	}
}

func TestPrizegivingPreflightRequiresExactItemsInBothOrders(t *testing.T) {
	score := "9.5"
	input := PrizegivingPreflightInput{
		EventID:               3,
		CeremonySessionID:     7,
		PlanRevision:          2,
		CompetitionSessionIDs: []int{11},
		Sequence: []ResultItem{{
			Kind: ResultItemCompetition, CompetitionSessionID: 11,
			DisplayOrder: 1, RevealMethod: RevealAnimatedScoreBars,
		}},
		PublicationOrder: []ResultItemRef{
			{
				Kind: ResultItemCompetition, CompetitionSessionID: 11,
				DisplayOrder: 1,
			},
			{
				Kind: ResultItemCompetition, CompetitionSessionID: 11,
				DisplayOrder: 2,
			},
		},
		Template: TextTemplate{Revision: 1, Source: "{{.EventTitle}}\n"},
		Competitions: []PrizegivingCompetitionSource{{
			Draft: Draft{
				ID: 31, EventID: 3, SessionID: 11, Revision: 4,
				Disposition: Publish, Ready: true,
				Score: ScorePolicy{
					Type: Decimal, Requirement: ScoreOptional,
				},
				Standings: []Standing{{
					EntryID: 41, Standing: Placed, Placement: 1,
					DisplayOrder: 1, Score: ScoreValue{Decimal: &score},
				}},
				Awards: []Award{{
					Key: "judges-choice", Name: "Judges' Choice",
					Recipients: []AwardRecipient{{DisplayName: "Ari"}},
					Promoted:   true, DisplayOrder: 1,
				}},
			},
		}},
	}

	_, findings := BuildPrizegivingPreflight(input, "mismatched-items")
	codes := make(map[string]bool, len(findings))
	for _, finding := range findings {
		codes[finding.Code] = true
	}
	if !codes["results_sequence_invalid"] {
		t.Errorf("Prizegiving Preflight findings = %+v, missing sequence mismatch", findings)
	}
	if !codes["publication_order_invalid"] {
		t.Errorf("Prizegiving Preflight findings = %+v, missing publication mismatch", findings)
	}

	input.Sequence = []ResultItem{
		{
			Kind: ResultItemCompetition, CompetitionSessionID: 11,
			DisplayOrder: 1, RevealMethod: RevealSequentialPodium,
		},
		{
			Kind: ResultItemCompetitionAward, CompetitionSessionID: 11,
			AwardKey: "judges-choice", DisplayOrder: 2,
			RevealMethod: RevealAnimatedScoreBars,
		},
	}
	input.PublicationOrder = []ResultItemRef{
		input.Sequence[1].Ref(1),
		input.Sequence[0].Ref(2),
	}
	_, findings = BuildPrizegivingPreflight(input, "invalid-method-source")
	codes = make(map[string]bool, len(findings))
	for _, finding := range findings {
		codes[finding.Code] = true
	}
	if !codes["invalid_reveal_method"] {
		t.Errorf("Prizegiving Preflight findings = %+v, missing method/source mismatch", findings)
	}
}

func TestPrizegivingPreviewAndRehearsalAreWatermarkedAndSideEffectFree(t *testing.T) {
	plan := PrizegivingPlan{
		ID: 5, EventID: 3, CeremonySessionID: 7, Revision: 4, Locked: true,
		Lock: PrizegivingPreflightLock{
			PlanRevision: 4,
			Sequence: []LockedResultItem{{
				ResultItem: ResultItem{
					Kind: ResultItemCompetition, CompetitionSessionID: 11,
					DisplayOrder: 1, RevealMethod: RevealStatic,
				},
				RevealSeed: 42,
			}},
		},
	}
	before := plan

	preview, err := projectPrizegivingPreview(plan, PrizegivingPreviewModePreview)
	if err != nil {
		t.Fatalf("project Results Preview: %v", err)
	}
	rehearsal, err := projectPrizegivingPreview(
		plan,
		PrizegivingPreviewModeRehearsal,
	)
	if err != nil {
		t.Fatalf("project Results rehearsal: %v", err)
	}

	if preview.Watermark != prizegivingPreviewWatermark ||
		rehearsal.Watermark != prizegivingPreviewWatermark ||
		preview.Plan.Lock.Sequence[0].RevealSeed !=
			rehearsal.Plan.Lock.Sequence[0].RevealSeed {
		t.Fatalf("Preview=%+v rehearsal=%+v", preview, rehearsal)
	}
	if !reflect.DeepEqual(plan, before) {
		t.Fatalf("Preview mutated Prizegiving plan: before=%+v after=%+v", before, plan)
	}
}
