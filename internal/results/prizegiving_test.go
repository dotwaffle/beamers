package results

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestDefaultPrizegivingOrderUsesPlannedTimeAndAwardsLast(t *testing.T) {
	early := PrizegivingCompetitionOrderSource{
		SessionID: 12, PlannedStart: time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC),
		Draft: Draft{
			SessionID: 12, Disposition: Publish,
			Awards: []Award{{
				Key: "judges-choice", Promoted: true, DisplayOrder: 1,
			}},
		},
	}
	late := PrizegivingCompetitionOrderSource{
		SessionID: 11, PlannedStart: time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC),
		Draft: Draft{SessionID: 11, Disposition: NoPublicResults},
	}
	sequence, publicationOrder := BuildDefaultPrizegivingOrder(
		PrizegivingDefaultOrderInput{
			Competitions: []PrizegivingCompetitionOrderSource{late, early},
			EventAwards: []EventAward{{
				Award: Award{Key: "community", DisplayOrder: 1},
			}},
		},
	)
	if len(sequence) != 4 ||
		sequence[0].CompetitionSessionID != early.SessionID ||
		sequence[1].Kind != ResultItemCompetitionAward ||
		sequence[2].Kind != ResultItemNoPublicResults ||
		sequence[3].Kind != ResultItemEventAward ||
		!reflect.DeepEqual(
			publicationOrder,
			[]ResultItemRef{
				sequence[0].Ref(1),
				sequence[1].Ref(2),
				sequence[2].Ref(3),
				sequence[3].Ref(4),
			},
		) {
		t.Fatalf(
			"default sequence=%+v publication=%+v",
			sequence,
			publicationOrder,
		)
	}
}

func TestPrizegivingTakeAndRevealPreserveLockedTruth(t *testing.T) {
	now := time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name       string
		method     RevealMethod
		wantStatus ResultItemStageStatus
		wantTime   time.Duration
	}{
		{
			name: "static", method: RevealStatic,
			wantStatus: ResultItemRevealed,
		},
		{
			name: "sequential podium", method: RevealSequentialPodium,
			wantStatus: ResultItemRevealing, wantTime: 3 * time.Second,
		},
		{
			name: "animated score bars", method: RevealAnimatedScoreBars,
			wantStatus: ResultItemRevealing, wantTime: 5 * time.Second,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			locked := LockedResultItem{
				ResultItem: ResultItem{
					Kind: ResultItemCompetition, CompetitionSessionID: 11,
					DisplayOrder: 1, RevealMethod: test.method,
				},
				RevealSeed: 73,
			}
			taken, err := TakePrizegivingResultItem(locked, ResultItemStageState{}, now)
			if err != nil {
				t.Fatalf("Take Result Item: %v", err)
			}
			if taken.Status != ResultItemTaken || taken.TakenAt != now {
				t.Fatalf("Taken Result Item = %+v", taken)
			}

			revealed, presentation, err := StartPrizegivingReveal(locked, taken, now)
			if err != nil {
				t.Fatalf("start Result Reveal: %v", err)
			}
			if revealed.Status != test.wantStatus ||
				revealed.RevealDuration != test.wantTime ||
				presentation.Item != locked ||
				presentation.Method != test.method ||
				presentation.ReducedMotionMethod != RevealStatic ||
				presentation.RevealSeed != 73 {
				t.Fatalf(
					"Reveal state=%+v presentation=%+v",
					revealed,
					presentation,
				)
			}
			if test.wantTime == 0 && revealed.RevealCompletedAt != now {
				t.Fatalf("Static Reveal completion = %v, want %v", revealed.RevealCompletedAt, now)
			}
		})
	}
}

func TestPrizegivingRevealCompletionAndSkipActionsAreMonotonic(t *testing.T) {
	startedAt := time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)
	locked := LockedResultItem{
		ResultItem: ResultItem{
			Kind: ResultItemCompetition, CompetitionSessionID: 11,
			DisplayOrder: 1, RevealMethod: RevealSequentialPodium,
		},
		RevealSeed: 73,
	}
	taken, err := TakePrizegivingResultItem(locked, ResultItemStageState{}, startedAt)
	if err != nil {
		t.Fatalf("Take Result Item: %v", err)
	}
	revealing, _, err := StartPrizegivingReveal(locked, taken, startedAt)
	if err != nil {
		t.Fatalf("start Result Reveal: %v", err)
	}
	if _, err = CompletePrizegivingReveal(
		locked,
		revealing,
		startedAt.Add(2*time.Second),
	); !errors.Is(err, ErrResultRevealRunning) {
		t.Fatalf("early Reveal completion error = %v", err)
	}
	revealed, err := CompletePrizegivingReveal(
		locked,
		revealing,
		startedAt.Add(3*time.Second),
	)
	if err != nil {
		t.Fatalf("complete Result Reveal: %v", err)
	}
	if revealed.Status != ResultItemRevealed ||
		revealed.RevealCompletedAt != startedAt.Add(3*time.Second) {
		t.Fatalf("completed Result Reveal = %+v", revealed)
	}
	replayedState, replay, err := ReplayPrizegivingReveal(
		locked,
		revealed,
		startedAt.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("Replay Result Reveal: %v", err)
	}
	if replayedState != revealed || replay.Item != locked ||
		replay.RevealSeed != locked.RevealSeed ||
		replay.StartedAt != startedAt.Add(time.Minute) {
		t.Fatalf("Replay state=%+v presentation=%+v", replayedState, replay)
	}

	skipFinal, err := SkipPrizegivingResultToFinal(
		locked,
		taken,
		startedAt.Add(time.Second),
	)
	if err != nil {
		t.Fatalf("Skip Result to Final: %v", err)
	}
	if skipFinal.Status != ResultItemRevealed ||
		skipFinal.RevealCompletedAt != startedAt.Add(time.Second) {
		t.Fatalf("Skip to Final state = %+v", skipFinal)
	}
	skipped, err := SkipPrizegivingResultFromStage(
		locked,
		ResultItemStageState{},
		startedAt,
	)
	if err != nil {
		t.Fatalf("Skip Result from Stage: %v", err)
	}
	if skipped.Status != ResultItemSkipped {
		t.Fatalf("Skip from Stage state = %+v", skipped)
	}
}

func TestPrizegivingEndListsEveryUnresolvedResultItem(t *testing.T) {
	now := time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)
	noPublic := LockedResultItem{
		ResultItem: ResultItem{
			Kind: ResultItemNoPublicResults, CompetitionSessionID: 12,
			DisplayOrder: 2, RevealMethod: RevealStatic,
		},
		RevealSeed: 74,
	}
	finalOnTake, err := TakePrizegivingResultItem(
		noPublic,
		ResultItemStageState{},
		now,
	)
	if err != nil {
		t.Fatalf("Take No Public Results: %v", err)
	}
	if finalOnTake.Status != ResultItemRevealed {
		t.Fatalf("No Public Results Take = %+v", finalOnTake)
	}
	pending := ResultItemRef{
		Kind: ResultItemCompetition, CompetitionSessionID: 11, DisplayOrder: 1,
	}
	unresolved := UnresolvedPrizegivingResultItems(
		[]LockedResultItem{
			{ResultItem: ResultItem{
				Kind: pending.Kind, CompetitionSessionID: pending.CompetitionSessionID,
				DisplayOrder: 1, RevealMethod: RevealStatic,
			}},
			noPublic,
		},
		[]ResultItemStageState{finalOnTake},
	)
	if !reflect.DeepEqual(unresolved, []ResultItemRef{pending}) {
		t.Fatalf("unresolved Result Items = %+v", unresolved)
	}
}

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

	sourceDraft := Draft{
		ID: 31, SessionID: 11,
		Standings: []Standing{{EntryID: 41, Placement: 1}},
	}
	sourceAwards := []EventAward{{
		Award: Award{
			Key:        "community",
			Recipients: []AwardRecipient{{DisplayName: "Volunteers"}},
		},
	}}
	preview, err := projectPrizegivingPreview(
		plan,
		[]Draft{sourceDraft},
		sourceAwards,
		PrizegivingPreviewModePreview,
	)
	if err != nil {
		t.Fatalf("project Results Preview: %v", err)
	}
	rehearsal, err := projectPrizegivingPreview(
		plan,
		[]Draft{sourceDraft},
		sourceAwards,
		PrizegivingPreviewModeRehearsal,
	)
	if err != nil {
		t.Fatalf("project Results rehearsal: %v", err)
	}

	if preview.Watermark != prizegivingPreviewWatermark ||
		rehearsal.Watermark != prizegivingPreviewWatermark ||
		preview.Plan.Lock.Sequence[0].RevealSeed !=
			rehearsal.Plan.Lock.Sequence[0].RevealSeed ||
		preview.CompetitionResults[0].Standings[0].Placement != 1 ||
		preview.EventAwards[0].Recipients[0].DisplayName != "Volunteers" {
		t.Fatalf("Preview=%+v rehearsal=%+v", preview, rehearsal)
	}
	preview.CompetitionResults[0].Standings[0].Placement = 2
	preview.EventAwards[0].Recipients[0].DisplayName = "Changed"
	if sourceDraft.Standings[0].Placement != 1 ||
		sourceAwards[0].Recipients[0].DisplayName != "Volunteers" {
		t.Fatal("Preview retained mutable source aliases")
	}
	if !reflect.DeepEqual(plan, before) {
		t.Fatalf("Preview mutated Prizegiving plan: before=%+v after=%+v", before, plan)
	}
}

func TestPrizegivingPreflightDoesNotRequireScoresForNoPublicResults(t *testing.T) {
	item := ResultItem{
		Kind: ResultItemNoPublicResults, CompetitionSessionID: 11,
		DisplayOrder: 1, RevealMethod: RevealStatic,
	}
	_, findings := BuildPrizegivingPreflight(PrizegivingPreflightInput{
		EventID: 3, CeremonySessionID: 7, PlanRevision: 1,
		CompetitionSessionIDs: []int{11},
		Sequence:              []ResultItem{item},
		PublicationOrder:      []ResultItemRef{item.Ref(1)},
		Template:              TextTemplate{Revision: 1, Source: "{{.EventTitle}}\n"},
		Competitions: []PrizegivingCompetitionSource{{Draft: Draft{
			ID: 31, EventID: 3, SessionID: 11, Revision: 2,
			Disposition: NoPublicResults,
			Score: ScorePolicy{
				Type: Decimal, Requirement: ScoreRequired,
			},
		}}},
	}, "no-public")
	for _, finding := range findings {
		if finding.Code == "required_score_missing" {
			t.Fatalf("No Public Results findings = %+v", findings)
		}
	}
}
