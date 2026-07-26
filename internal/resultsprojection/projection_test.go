package resultsprojection

import (
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	resultsv1 "github.com/dotwaffle/beamers/gen/beamers/results/v1"
	"github.com/dotwaffle/beamers/internal/prizegivingvalue"
	"github.com/dotwaffle/beamers/internal/results"
	"github.com/dotwaffle/beamers/internal/store"
)

func TestDraftProjectsEverySharedFieldFromBothSources(t *testing.T) {
	readyAt := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	createdAt := readyAt.Add(-time.Hour)
	duration := 3 * time.Second
	nanos := int64(duration)
	domain := results.Draft{
		ID: 1, EventID: 2, SessionID: 3, Revision: 4,
		Disposition: results.Publish, NoPublicReason: "crew",
		PublicExplanation: "public",
		Score: results.ScorePolicy{
			Type: results.Duration, Visibility: results.ScorePublic,
			Unit: "seconds", Precision: 2, Requirement: results.ScoreRequired,
			Interpretation: results.LowerWins,
		},
		Standings: []results.Standing{{
			EntryID: 5, Standing: results.Placed, Placement: 1,
			DisplayOrder: 6, Score: results.ScoreValue{Duration: &duration},
		}},
		Awards: []results.Award{{
			Key: "jury", Name: "Jury", Promoted: true, DisplayOrder: 7,
			Recipients: []results.AwardRecipient{
				{EntryID: 8}, {DisplayName: "Guest"},
			},
		}},
		Ready: true, ReadyByAccountID: 9, ReadyAt: readyAt,
		CreatedByAccountID: 10, CreatedAt: createdAt,
	}
	stored := store.CompetitionResultsDraft{
		ID: 1, EventID: 2, SessionID: 3, Revision: 4,
		Disposition: "Publish", NoPublicCrewReason: "crew",
		PublicExplanation: "public", ScoreType: "Duration",
		ScoreVisibility: "Public", ScoreUnit: "seconds", ScorePrecision: 2,
		ScoreRequirement: "Required", ScoreInterpretation: "LowerWins",
		Standings: []store.CompetitionResultStanding{{
			EntryID: 5, Standing: "Placed", Placement: 1,
			DisplayOrder: 6, DurationScoreNanos: &nanos,
		}},
		Awards: []store.CompetitionAward{{
			Key: "jury", Name: "Jury", Promoted: true, DisplayOrder: 7,
			Recipients: []store.AwardRecipientInput{
				{EntryID: 8}, {DisplayName: "Guest"},
			},
		}},
		Ready: true, ReadyByAccountID: 9, ReadyAt: readyAt,
		CreatedByAccountID: 10, CreatedAt: createdAt,
	}

	projected, err := Draft(&domain)
	if err != nil {
		t.Fatalf("project domain Draft: %v", err)
	}
	fromStore, err := StoredDraft(&stored)
	if err != nil {
		t.Fatalf("project stored Draft: %v", err)
	}
	if !proto.Equal(projected, fromStore) {
		t.Fatalf("source projections differ:\ndomain: %v\nstore:  %v", projected, fromStore)
	}
	if projected.GetId() != 1 || projected.GetEventId() != 2 ||
		projected.GetSessionId() != 3 || projected.GetRevision() != 4 ||
		projected.GetNoPublicCrewReason() != "crew" ||
		projected.GetPublicExplanation() != "public" ||
		projected.GetScore().GetUnit() != "seconds" ||
		projected.GetScore().GetPrecision() != 2 ||
		!projected.GetReady() || projected.GetReadyByAccountId() != 9 ||
		!projected.GetReadyAt().AsTime().Equal(readyAt) ||
		projected.GetCreatedByAccountId() != 10 ||
		!projected.GetCreatedAt().AsTime().Equal(createdAt) {
		t.Fatalf("Draft scalar fields = %v", projected)
	}
	standing := projected.GetStandings()[0]
	if standing.GetEntryId() != 5 || standing.GetPlacement() != 1 ||
		standing.GetDisplayOrder() != 6 ||
		standing.GetScore().GetDuration().AsDuration() != duration {
		t.Fatalf("Standing fields = %v", standing)
	}
	award := projected.GetAwards()[0]
	if award.GetKey() != "jury" || award.GetName() != "Jury" ||
		!award.GetPromoted() || award.GetDisplayOrder() != 7 ||
		award.GetRecipients()[0].GetEntryId() != 8 ||
		award.GetRecipients()[1].GetDisplayName() != "Guest" {
		t.Fatalf("Award fields = %v", award)
	}

	eventAward := results.EventAward{
		Award: results.Award{
			Key: "event", Name: "Event Award", DisplayOrder: 11,
			Recipients: []results.AwardRecipient{{DisplayName: "Volunteer"}},
		},
		ReleasePath: results.AwardReleasePath{
			Kind: results.PrizegivingRelease, PrizegivingSessionID: 12,
		},
	}
	projectedEventAward, err := EventAward(&eventAward)
	if err != nil {
		t.Fatalf("project Event Award: %v", err)
	}
	storedEventAward, err := StoredEventAward(&store.EventAward{
		Key: "event", Name: "Event Award", DisplayOrder: 11,
		Recipients: []store.AwardRecipientInput{{DisplayName: "Volunteer"}},
		ReleasePath: store.AwardReleasePath{
			Kind: "Prizegiving", PrizegivingSessionID: 12,
		},
	})
	if err != nil {
		t.Fatalf("project stored Event Award: %v", err)
	}
	if !proto.Equal(projectedEventAward, storedEventAward) ||
		projectedEventAward.GetRecipients()[0].GetDisplayName() != "Volunteer" ||
		projectedEventAward.GetReleasePath().GetPrizegivingSessionId() != 12 {
		t.Fatalf("Event Award fields = %v", projectedEventAward)
	}

	item, err := Item(results.ResultItem{
		Kind: results.ResultItemCompetitionAward, CompetitionSessionID: 13,
		AwardKey: "jury", DisplayOrder: 14, RevealMethod: results.RevealSequentialPodium,
	})
	if err != nil {
		t.Fatalf("project Result Item: %v", err)
	}
	if item.GetKind() != resultsv1.ResultItemKind_RESULT_ITEM_KIND_COMPETITION_AWARD ||
		item.GetCompetitionSessionId() != 13 || item.GetAwardKey() != "jury" ||
		item.GetDisplayOrder() != 14 ||
		item.GetRevealMethod() != resultsv1.RevealMethod_REVEAL_METHOD_SEQUENTIAL_PODIUM {
		t.Fatalf("Result Item fields = %v", item)
	}
	itemRef, err := StoredItemRef(&store.PrizegivingResultItemRef{
		Kind: prizegivingvalue.ItemEventAward, CompetitionSessionID: 15,
		AwardKey: "event", DisplayOrder: 16,
	})
	if err != nil {
		t.Fatalf("project stored Result Item reference: %v", err)
	}
	if itemRef.GetKind() != resultsv1.ResultItemKind_RESULT_ITEM_KIND_EVENT_AWARD ||
		itemRef.GetCompetitionSessionId() != 15 || itemRef.GetAwardKey() != "event" ||
		itemRef.GetDisplayOrder() != 16 {
		t.Fatalf("Result Item reference fields = %v", itemRef)
	}
}

func TestProjectionEnumsAreExhaustive(t *testing.T) {
	for value, want := range map[string]resultsv1.ResultItemKind{
		"CompetitionResults": resultsv1.ResultItemKind_RESULT_ITEM_KIND_COMPETITION_RESULTS,
		"NoPublicResults":    resultsv1.ResultItemKind_RESULT_ITEM_KIND_NO_PUBLIC_RESULTS,
		"CompetitionAward":   resultsv1.ResultItemKind_RESULT_ITEM_KIND_COMPETITION_AWARD,
		"EventAward":         resultsv1.ResultItemKind_RESULT_ITEM_KIND_EVENT_AWARD,
	} {
		got, err := ItemKind(value)
		if err != nil || got != want {
			t.Errorf("ItemKind(%q) = %v, %v; want %v", value, got, err, want)
		}
	}
	for value, want := range map[string]resultsv1.RevealMethod{
		"StaticResult":      resultsv1.RevealMethod_REVEAL_METHOD_STATIC_RESULT,
		"SequentialPodium":  resultsv1.RevealMethod_REVEAL_METHOD_SEQUENTIAL_PODIUM,
		"AnimatedScoreBars": resultsv1.RevealMethod_REVEAL_METHOD_ANIMATED_SCORE_BARS,
	} {
		got, err := RevealMethod(value)
		if err != nil || got != want {
			t.Errorf("RevealMethod(%q) = %v, %v; want %v", value, got, err, want)
		}
	}
	for value, want := range map[string]resultsv1.ResultsDisposition{
		"Pending":         resultsv1.ResultsDisposition_RESULTS_DISPOSITION_PENDING,
		"Publish":         resultsv1.ResultsDisposition_RESULTS_DISPOSITION_PUBLISH,
		"NoPublicResults": resultsv1.ResultsDisposition_RESULTS_DISPOSITION_NO_PUBLIC_RESULTS,
	} {
		got, err := Disposition(value)
		if err != nil || got != want {
			t.Errorf("Disposition(%q) = %v, %v; want %v", value, got, err, want)
		}
	}
	for value, want := range map[string]resultsv1.ResultsReleasePolicy{
		"AllAtCue":            resultsv1.ResultsReleasePolicy_RESULTS_RELEASE_POLICY_ALL_AT_CUE,
		"ProgressiveOnReveal": resultsv1.ResultsReleasePolicy_RESULTS_RELEASE_POLICY_PROGRESSIVE_ON_REVEAL,
		"AtCeremonyEnd":       resultsv1.ResultsReleasePolicy_RESULTS_RELEASE_POLICY_AT_CEREMONY_END,
		"Standalone":          resultsv1.ResultsReleasePolicy_RESULTS_RELEASE_POLICY_STANDALONE,
	} {
		got, err := ReleasePolicy(value)
		if err != nil || got != want {
			t.Errorf("ReleasePolicy(%q) = %v, %v; want %v", value, got, err, want)
		}
	}
	for value, want := range map[string]resultsv1.PrizegivingPreviewMode{
		"Preview":   resultsv1.PrizegivingPreviewMode_PRIZEGIVING_PREVIEW_MODE_PREVIEW,
		"Rehearsal": resultsv1.PrizegivingPreviewMode_PRIZEGIVING_PREVIEW_MODE_REHEARSAL,
	} {
		got, err := PreviewMode(value)
		if err != nil || got != want {
			t.Errorf("PreviewMode(%q) = %v, %v; want %v", value, got, err, want)
		}
	}
	for value, want := range map[string]resultsv1.ResultsPublicationScope{
		"Prizegiving": resultsv1.ResultsPublicationScope_RESULTS_PUBLICATION_SCOPE_PRIZEGIVING,
		"Standalone":  resultsv1.ResultsPublicationScope_RESULTS_PUBLICATION_SCOPE_STANDALONE,
		"EventAwards": resultsv1.ResultsPublicationScope_RESULTS_PUBLICATION_SCOPE_EVENT_AWARDS,
	} {
		got, err := PublicationScope(value)
		if err != nil || got != want {
			t.Errorf("PublicationScope(%q) = %v, %v; want %v", value, got, err, want)
		}
	}
	for value, want := range map[string]resultsv1.ResultsCorrectionStatus{
		"Draft":     resultsv1.ResultsCorrectionStatus_RESULTS_CORRECTION_STATUS_DRAFT,
		"Ready":     resultsv1.ResultsCorrectionStatus_RESULTS_CORRECTION_STATUS_READY,
		"Published": resultsv1.ResultsCorrectionStatus_RESULTS_CORRECTION_STATUS_PUBLISHED,
	} {
		got, err := CorrectionStatus(value)
		if err != nil || got != want {
			t.Errorf("CorrectionStatus(%q) = %v, %v; want %v", value, got, err, want)
		}
	}
	for value, want := range map[string]resultsv1.ResultsPublicationStatus{
		"Partial": resultsv1.ResultsPublicationStatus_RESULTS_PUBLICATION_STATUS_PARTIAL,
		"Final":   resultsv1.ResultsPublicationStatus_RESULTS_PUBLICATION_STATUS_FINAL,
	} {
		got, err := PublicationStatus(value)
		if err != nil || got != want {
			t.Errorf("PublicationStatus(%q) = %v, %v; want %v", value, got, err, want)
		}
	}

	scoreCases := []struct {
		name  string
		score results.ScorePolicy
		check func(*resultsv1.ScorePolicy) bool
	}{
		{"none", results.ScorePolicy{Type: results.None, Visibility: results.ScorePublic, Requirement: results.ScoreOptional, Interpretation: results.HigherWins}, func(value *resultsv1.ScorePolicy) bool {
			return value.GetType() == resultsv1.ScoreType_SCORE_TYPE_NONE
		}},
		{"decimal", results.ScorePolicy{Type: results.Decimal, Visibility: results.ScoreCrewOnly, Requirement: results.ScoreRequired, Interpretation: results.LowerWins}, func(value *resultsv1.ScorePolicy) bool {
			return value.GetType() == resultsv1.ScoreType_SCORE_TYPE_DECIMAL &&
				value.GetVisibility() == resultsv1.ScoreVisibility_SCORE_VISIBILITY_CREW_ONLY &&
				value.GetRequirement() == resultsv1.ScoreRequirement_SCORE_REQUIREMENT_REQUIRED &&
				value.GetInterpretation() == resultsv1.ScoreInterpretation_SCORE_INTERPRETATION_LOWER_WINS
		}},
		{"duration", results.ScorePolicy{Type: results.Duration, Visibility: results.ScorePublic, Requirement: results.ScoreOptional, Interpretation: results.Informational}, func(value *resultsv1.ScorePolicy) bool {
			return value.GetType() == resultsv1.ScoreType_SCORE_TYPE_DURATION &&
				value.GetInterpretation() == resultsv1.ScoreInterpretation_SCORE_INTERPRETATION_INFORMATIONAL
		}},
	}
	for _, test := range scoreCases {
		projected, err := Draft(&results.Draft{Disposition: results.Pending, Score: test.score})
		if err != nil || !test.check(projected.GetScore()) {
			t.Errorf("%s Score Policy = %v, %v", test.name, projected.GetScore(), err)
		}
	}

	for value, want := range map[results.ResultStanding]resultsv1.ResultStanding{
		results.Placed:   resultsv1.ResultStanding_RESULT_STANDING_PLACED,
		results.Unplaced: resultsv1.ResultStanding_RESULT_STANDING_UNPLACED,
	} {
		projected, err := Draft(&results.Draft{
			Disposition: results.Pending,
			Score: results.ScorePolicy{
				Type: results.None, Visibility: results.ScorePublic,
				Requirement: results.ScoreOptional, Interpretation: results.HigherWins,
			},
			Standings: []results.Standing{{Standing: value}},
		})
		if err != nil || projected.GetStandings()[0].GetStanding() != want {
			t.Errorf("Result Standing %q = %v, %v", value, projected, err)
		}
	}

	for value, want := range map[results.AwardReleasePathKind]resultsv1.AwardReleasePathKind{
		results.StandaloneRelease:  resultsv1.AwardReleasePathKind_AWARD_RELEASE_PATH_KIND_STANDALONE,
		results.PrizegivingRelease: resultsv1.AwardReleasePathKind_AWARD_RELEASE_PATH_KIND_PRIZEGIVING,
	} {
		projected, err := EventAward(&results.EventAward{
			Award:       results.Award{Key: "award"},
			ReleasePath: results.AwardReleasePath{Kind: value},
		})
		if err != nil || projected.GetReleasePath().GetKind() != want {
			t.Errorf("Award Release Path %q = %v, %v", value, projected, err)
		}
	}
}

func TestProjectionPreservesNilAndRejectsUnknownValues(t *testing.T) {
	if draft, err := Draft(nil); err != nil || draft != nil {
		t.Errorf("Draft(nil) = %v, %v", draft, err)
	}
	if draft, err := StoredDraft(nil); err != nil || draft != nil {
		t.Errorf("StoredDraft(nil) = %v, %v", draft, err)
	}
	if draft, err := StoredDraft(&store.CompetitionResultsDraft{}); err != nil || draft != nil {
		t.Errorf("StoredDraft(zero) = %v, %v", draft, err)
	}
	if award, err := EventAward(nil); err != nil || award != nil {
		t.Errorf("EventAward(nil) = %v, %v", award, err)
	}
	if award, err := StoredEventAward(nil); err != nil || award != nil {
		t.Errorf("StoredEventAward(nil) = %v, %v", award, err)
	}
	if item, err := ItemRef(nil); err != nil || item != nil {
		t.Errorf("ItemRef(nil) = %v, %v", item, err)
	}

	invalidDraft := validDraft()
	invalidDraft.Disposition = "mystery"
	assertUnknown(t, func() error {
		_, err := Draft(&invalidDraft)
		return err
	})
	invalidDraft = validDraft()
	invalidDraft.Score.Type = "mystery"
	assertUnknown(t, func() error {
		_, err := Draft(&invalidDraft)
		return err
	})
	for name, mutate := range map[string]func(*results.Draft){
		"visibility":  func(value *results.Draft) { value.Score.Visibility = "mystery" },
		"requirement": func(value *results.Draft) { value.Score.Requirement = "mystery" },
		"interpretation": func(value *results.Draft) {
			value.Score.Interpretation = "mystery"
		},
	} {
		t.Run("unknown score "+name, func(t *testing.T) {
			value := validDraft()
			mutate(&value)
			assertUnknown(t, func() error {
				_, err := Draft(&value)
				return err
			})
		})
	}
	invalidDraft = validDraft()
	invalidDraft.Standings = []results.Standing{{Standing: "mystery"}}
	assertUnknown(t, func() error {
		_, err := Draft(&invalidDraft)
		return err
	})
	assertUnknown(t, func() error {
		_, err := Item(results.ResultItem{
			Kind: results.ResultItemCompetition, RevealMethod: "mystery",
		})
		return err
	})
	assertUnknown(t, func() error {
		_, err := StoredDraft(&store.CompetitionResultsDraft{
			ID: 1, Disposition: "Pending", ScoreType: "mystery",
			ScoreVisibility: "Public", ScoreRequirement: "Optional",
			ScoreInterpretation: "HigherWins",
		})
		return err
	})
	assertUnknown(t, func() error {
		_, err := StoredEventAward(&store.EventAward{
			Key: "award", ReleasePath: store.AwardReleasePath{Kind: "mystery"},
		})
		return err
	})
	for name, projection := range map[string]func() error{
		"item kind":          func() error { _, err := ItemKind("mystery"); return err },
		"reveal method":      func() error { _, err := RevealMethod("mystery"); return err },
		"release policy":     func() error { _, err := ReleasePolicy("mystery"); return err },
		"preview mode":       func() error { _, err := PreviewMode("mystery"); return err },
		"publication scope":  func() error { _, err := PublicationScope("Event"); return err },
		"correction status":  func() error { _, err := CorrectionStatus("mystery"); return err },
		"publication status": func() error { _, err := PublicationStatus("mystery"); return err },
	} {
		t.Run("unknown "+name, func(t *testing.T) {
			assertUnknown(t, projection)
		})
	}
}

func validDraft() results.Draft {
	return results.Draft{
		Disposition: results.Pending,
		Score: results.ScorePolicy{
			Type: results.None, Visibility: results.ScorePublic,
			Requirement: results.ScoreOptional, Interpretation: results.HigherWins,
		},
	}
}

func assertUnknown(t *testing.T, projection func() error) {
	t.Helper()
	if err := projection(); !errors.Is(err, ErrUnknownValue) {
		t.Errorf("projection error = %v, want ErrUnknownValue", err)
	}
}
