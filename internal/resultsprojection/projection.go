// Package resultsprojection projects Results values into transport messages.
package resultsprojection

import (
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	resultsv1 "github.com/dotwaffle/beamers/gen/beamers/results/v1"
	"github.com/dotwaffle/beamers/internal/results"
	"github.com/dotwaffle/beamers/internal/store"
)

// ErrUnknownValue means persisted or domain state cannot be represented safely.
var ErrUnknownValue = errors.New("unknown Results projection value")

// Draft projects one Results Draft.
func Draft(value *results.Draft) (*resultsv1.CompetitionResultsDraft, error) {
	if value == nil {
		return nil, nil
	}
	disposition, err := Disposition(string(value.Disposition))
	if err != nil {
		return nil, err
	}
	score, err := scorePolicy(value.Score)
	if err != nil {
		return nil, err
	}
	standings, err := standings(value.Standings)
	if err != nil {
		return nil, err
	}
	awards := competitionAwards(value.Awards)
	message := &resultsv1.CompetitionResultsDraft{
		Id: int64(value.ID), EventId: int64(value.EventID), SessionId: int64(value.SessionID),
		Revision: int64(value.Revision), Disposition: disposition,
		NoPublicCrewReason: value.NoPublicReason, PublicExplanation: value.PublicExplanation,
		Score: score, Standings: standings, Ready: value.Ready,
		ReadyByAccountId:   int64(value.ReadyByAccountID),
		CreatedByAccountId: int64(value.CreatedByAccountID),
		Awards:             awards,
	}
	setTimestamp(&message.ReadyAt, value.ReadyAt)
	setTimestamp(&message.CreatedAt, value.CreatedAt)
	return message, nil
}

// Drafts projects Results Drafts.
func Drafts(values []results.Draft) ([]*resultsv1.CompetitionResultsDraft, error) {
	projected := make([]*resultsv1.CompetitionResultsDraft, 0, len(values))
	for index := range values {
		draft, err := Draft(&values[index])
		if err != nil {
			return nil, err
		}
		projected = append(projected, draft)
	}
	return projected, nil
}

// StoredDraft projects one persisted Results Draft.
func StoredDraft(value *store.CompetitionResultsDraft) (*resultsv1.CompetitionResultsDraft, error) {
	if value == nil || value.ID == 0 {
		return nil, nil
	}
	found := results.Draft{
		ID: value.ID, EventID: value.EventID, SessionID: value.SessionID,
		Revision: value.Revision, Disposition: results.Disposition(value.Disposition),
		NoPublicReason: value.NoPublicCrewReason, PublicExplanation: value.PublicExplanation,
		Score: results.ScorePolicy{
			Type:       results.ScoreType(value.ScoreType),
			Visibility: results.ScoreVisibility(value.ScoreVisibility),
			Unit:       value.ScoreUnit, Precision: value.ScorePrecision,
			Requirement:    results.ScoreRequirement(value.ScoreRequirement),
			Interpretation: results.ScoreInterpretation(value.ScoreInterpretation),
		},
		Ready: value.Ready, ReadyByAccountID: value.ReadyByAccountID,
		ReadyAt: value.ReadyAt, CreatedByAccountID: value.CreatedByAccountID,
		CreatedAt: value.CreatedAt,
	}
	for _, standing := range value.Standings {
		score := results.ScoreValue{Decimal: standing.DecimalScore}
		if standing.DurationScoreNanos != nil {
			duration := time.Duration(*standing.DurationScoreNanos)
			score.Duration = &duration
		}
		found.Standings = append(found.Standings, results.Standing{
			EntryID: standing.EntryID, Standing: results.ResultStanding(standing.Standing),
			Placement: standing.Placement, DisplayOrder: standing.DisplayOrder, Score: score,
		})
	}
	for _, award := range value.Awards {
		found.Awards = append(found.Awards, results.Award{
			Key: award.Key, Name: award.Name,
			Recipients: storedRecipients(award.Recipients),
			Promoted:   award.Promoted, DisplayOrder: award.DisplayOrder,
		})
	}
	return Draft(&found)
}

// EventAwardsDraft projects one Event Awards Draft.
func EventAwardsDraft(value *results.EventAwardsDraft) (*resultsv1.EventAwardsDraft, error) {
	if value == nil {
		return nil, nil
	}
	awards, err := EventAwards(value.Awards)
	if err != nil {
		return nil, err
	}
	states := make([]*resultsv1.EventAwardPathState, 0, len(value.PathStates))
	for _, state := range value.PathStates {
		path, pathErr := awardReleasePath(
			string(state.ReleasePath.Kind),
			state.ReleasePath.PrizegivingSessionID,
		)
		if pathErr != nil {
			return nil, pathErr
		}
		found := &resultsv1.EventAwardPathState{
			ReleasePath: path, Revision: int64(state.Revision),
			Ready: state.Ready, ReadyByAccountId: int64(state.ReadyByAccountID),
		}
		setTimestamp(&found.ReadyAt, state.ReadyAt)
		states = append(states, found)
	}
	message := &resultsv1.EventAwardsDraft{
		Id: int64(value.ID), EventId: int64(value.EventID), Revision: int64(value.Revision),
		Awards: awards, PathStates: states,
		CreatedByAccountId: int64(value.CreatedByAccountID),
	}
	setTimestamp(&message.CreatedAt, value.CreatedAt)
	return message, nil
}

// EventAwards projects Event Awards.
func EventAwards(values []results.EventAward) ([]*resultsv1.EventAward, error) {
	projected := make([]*resultsv1.EventAward, 0, len(values))
	for index := range values {
		award, err := EventAward(&values[index])
		if err != nil {
			return nil, err
		}
		projected = append(projected, award)
	}
	return projected, nil
}

// EventAward projects one Event Award.
func EventAward(value *results.EventAward) (*resultsv1.EventAward, error) {
	if value == nil {
		return nil, nil
	}
	path, err := awardReleasePath(string(value.ReleasePath.Kind), value.ReleasePath.PrizegivingSessionID)
	if err != nil {
		return nil, err
	}
	return &resultsv1.EventAward{
		Key: value.Key, Name: value.Name, Recipients: awardRecipients(value.Recipients),
		DisplayOrder: int32(value.DisplayOrder), //nolint:gosec // Award count is bounded.
		ReleasePath:  path,
	}, nil
}

// StoredEventAward projects one persisted Event Award.
func StoredEventAward(value *store.EventAward) (*resultsv1.EventAward, error) {
	if value == nil || value.Key == "" {
		return nil, nil
	}
	found := results.EventAward{
		Award: results.Award{
			Key: value.Key, Name: value.Name,
			Recipients:   storedRecipients(value.Recipients),
			DisplayOrder: value.DisplayOrder,
		},
		ReleasePath: results.AwardReleasePath{
			Kind:                 results.AwardReleasePathKind(value.ReleasePath.Kind),
			PrizegivingSessionID: value.ReleasePath.PrizegivingSessionID,
		},
	}
	return EventAward(&found)
}

// Item projects one staged Result Item.
func Item(value results.ResultItem) (*resultsv1.ResultItem, error) {
	kind, err := ItemKind(string(value.Kind))
	if err != nil {
		return nil, err
	}
	reveal, err := RevealMethod(string(value.RevealMethod))
	if err != nil {
		return nil, err
	}
	return &resultsv1.ResultItem{
		Kind: kind, CompetitionSessionId: int64(value.CompetitionSessionID),
		AwardKey:     value.AwardKey,
		DisplayOrder: int32(value.DisplayOrder), //nolint:gosec // Result Items are bounded.
		RevealMethod: reveal,
	}, nil
}

// Items projects staged Result Items.
func Items(values []results.ResultItem) ([]*resultsv1.ResultItem, error) {
	projected := make([]*resultsv1.ResultItem, 0, len(values))
	for _, value := range values {
		item, err := Item(value)
		if err != nil {
			return nil, err
		}
		projected = append(projected, item)
	}
	return projected, nil
}

// ItemRef projects one Result Item reference.
func ItemRef(value *results.ResultItemRef) (*resultsv1.ResultItemRef, error) {
	if value == nil {
		return nil, nil
	}
	return itemRef(string(value.Kind), value.CompetitionSessionID, value.AwardKey, value.DisplayOrder)
}

// ItemRefs projects Result Item references.
func ItemRefs(values []results.ResultItemRef) ([]*resultsv1.ResultItemRef, error) {
	projected := make([]*resultsv1.ResultItemRef, 0, len(values))
	for index := range values {
		item, err := ItemRef(&values[index])
		if err != nil {
			return nil, err
		}
		projected = append(projected, item)
	}
	return projected, nil
}

// StoredItemRef projects one persisted Result Item reference.
func StoredItemRef(value *store.PrizegivingResultItemRef) (*resultsv1.ResultItemRef, error) {
	if value == nil {
		return nil, nil
	}
	return itemRef(string(value.Kind), value.CompetitionSessionID, value.AwardKey, value.DisplayOrder)
}

// ItemKind projects one Result Item kind.
func ItemKind(value string) (resultsv1.ResultItemKind, error) {
	found, ok := map[string]resultsv1.ResultItemKind{
		"CompetitionResults": resultsv1.ResultItemKind_RESULT_ITEM_KIND_COMPETITION_RESULTS,
		"NoPublicResults":    resultsv1.ResultItemKind_RESULT_ITEM_KIND_NO_PUBLIC_RESULTS,
		"CompetitionAward":   resultsv1.ResultItemKind_RESULT_ITEM_KIND_COMPETITION_AWARD,
		"EventAward":         resultsv1.ResultItemKind_RESULT_ITEM_KIND_EVENT_AWARD,
	}[value]
	if !ok {
		return 0, unknown("Result Item kind", value)
	}
	return found, nil
}

// RevealMethod projects one Results Reveal Method.
func RevealMethod(value string) (resultsv1.RevealMethod, error) {
	found, ok := map[string]resultsv1.RevealMethod{
		"StaticResult":      resultsv1.RevealMethod_REVEAL_METHOD_STATIC_RESULT,
		"SequentialPodium":  resultsv1.RevealMethod_REVEAL_METHOD_SEQUENTIAL_PODIUM,
		"AnimatedScoreBars": resultsv1.RevealMethod_REVEAL_METHOD_ANIMATED_SCORE_BARS,
	}[value]
	if !ok {
		return 0, unknown("Reveal Method", value)
	}
	return found, nil
}

// Disposition projects one Results disposition.
func Disposition(value string) (resultsv1.ResultsDisposition, error) {
	found, ok := map[string]resultsv1.ResultsDisposition{
		"Pending":         resultsv1.ResultsDisposition_RESULTS_DISPOSITION_PENDING,
		"Publish":         resultsv1.ResultsDisposition_RESULTS_DISPOSITION_PUBLISH,
		"NoPublicResults": resultsv1.ResultsDisposition_RESULTS_DISPOSITION_NO_PUBLIC_RESULTS,
	}[value]
	if !ok {
		return 0, unknown("Results disposition", value)
	}
	return found, nil
}

// ReleasePolicy projects one Results Release Policy.
func ReleasePolicy(value string) (resultsv1.ResultsReleasePolicy, error) {
	return enum("Results Release Policy", value, map[string]resultsv1.ResultsReleasePolicy{
		"AllAtCue":            resultsv1.ResultsReleasePolicy_RESULTS_RELEASE_POLICY_ALL_AT_CUE,
		"ProgressiveOnReveal": resultsv1.ResultsReleasePolicy_RESULTS_RELEASE_POLICY_PROGRESSIVE_ON_REVEAL,
		"AtCeremonyEnd":       resultsv1.ResultsReleasePolicy_RESULTS_RELEASE_POLICY_AT_CEREMONY_END,
		"Standalone":          resultsv1.ResultsReleasePolicy_RESULTS_RELEASE_POLICY_STANDALONE,
	})
}

// PreviewMode projects one Prizegiving Preview Mode.
func PreviewMode(value string) (resultsv1.PrizegivingPreviewMode, error) {
	return enum("Prizegiving Preview Mode", value, map[string]resultsv1.PrizegivingPreviewMode{
		"Preview":   resultsv1.PrizegivingPreviewMode_PRIZEGIVING_PREVIEW_MODE_PREVIEW,
		"Rehearsal": resultsv1.PrizegivingPreviewMode_PRIZEGIVING_PREVIEW_MODE_REHEARSAL,
	})
}

// PublicationScope projects one Results Publication Scope.
func PublicationScope(value string) (resultsv1.ResultsPublicationScope, error) {
	return enum("Results Publication Scope", value, map[string]resultsv1.ResultsPublicationScope{
		"Prizegiving": resultsv1.ResultsPublicationScope_RESULTS_PUBLICATION_SCOPE_PRIZEGIVING,
		"Standalone":  resultsv1.ResultsPublicationScope_RESULTS_PUBLICATION_SCOPE_STANDALONE,
		"EventAwards": resultsv1.ResultsPublicationScope_RESULTS_PUBLICATION_SCOPE_EVENT_AWARDS,
	})
}

// CorrectionStatus projects one Results Correction Status.
func CorrectionStatus(value string) (resultsv1.ResultsCorrectionStatus, error) {
	return enum("Results Correction Status", value, map[string]resultsv1.ResultsCorrectionStatus{
		"Draft":     resultsv1.ResultsCorrectionStatus_RESULTS_CORRECTION_STATUS_DRAFT,
		"Ready":     resultsv1.ResultsCorrectionStatus_RESULTS_CORRECTION_STATUS_READY,
		"Published": resultsv1.ResultsCorrectionStatus_RESULTS_CORRECTION_STATUS_PUBLISHED,
	})
}

// PublicationStatus projects one Results Publication Status.
func PublicationStatus(value string) (resultsv1.ResultsPublicationStatus, error) {
	return enum("Results Publication Status", value, map[string]resultsv1.ResultsPublicationStatus{
		"Partial": resultsv1.ResultsPublicationStatus_RESULTS_PUBLICATION_STATUS_PARTIAL,
		"Final":   resultsv1.ResultsPublicationStatus_RESULTS_PUBLICATION_STATUS_FINAL,
	})
}

func scorePolicy(value results.ScorePolicy) (*resultsv1.ScorePolicy, error) {
	scoreType, err := enum("Score Type", string(value.Type), map[string]resultsv1.ScoreType{
		"None": resultsv1.ScoreType_SCORE_TYPE_NONE, "Decimal": resultsv1.ScoreType_SCORE_TYPE_DECIMAL,
		"Duration": resultsv1.ScoreType_SCORE_TYPE_DURATION,
	})
	if err != nil {
		return nil, err
	}
	visibility, err := enum("Score Visibility", string(value.Visibility), map[string]resultsv1.ScoreVisibility{
		"Public":   resultsv1.ScoreVisibility_SCORE_VISIBILITY_PUBLIC,
		"CrewOnly": resultsv1.ScoreVisibility_SCORE_VISIBILITY_CREW_ONLY,
	})
	if err != nil {
		return nil, err
	}
	requirement, err := enum("Score Requirement", string(value.Requirement), map[string]resultsv1.ScoreRequirement{
		"Optional": resultsv1.ScoreRequirement_SCORE_REQUIREMENT_OPTIONAL,
		"Required": resultsv1.ScoreRequirement_SCORE_REQUIREMENT_REQUIRED,
	})
	if err != nil {
		return nil, err
	}
	interpretation, err := enum(
		"Score Interpretation",
		string(value.Interpretation),
		map[string]resultsv1.ScoreInterpretation{
			"HigherWins":    resultsv1.ScoreInterpretation_SCORE_INTERPRETATION_HIGHER_WINS,
			"LowerWins":     resultsv1.ScoreInterpretation_SCORE_INTERPRETATION_LOWER_WINS,
			"Informational": resultsv1.ScoreInterpretation_SCORE_INTERPRETATION_INFORMATIONAL,
		},
	)
	if err != nil {
		return nil, err
	}
	return &resultsv1.ScorePolicy{
		Type: scoreType, Visibility: visibility, Unit: value.Unit,
		Precision:   int32(value.Precision), //nolint:gosec // Validated before projection.
		Requirement: requirement, Interpretation: interpretation,
	}, nil
}

func standings(values []results.Standing) ([]*resultsv1.CompetitionResultStanding, error) {
	projected := make([]*resultsv1.CompetitionResultStanding, 0, len(values))
	for _, value := range values {
		standing, err := enum("Result Standing", string(value.Standing), map[string]resultsv1.ResultStanding{
			"Placed":   resultsv1.ResultStanding_RESULT_STANDING_PLACED,
			"Unplaced": resultsv1.ResultStanding_RESULT_STANDING_UNPLACED,
		})
		if err != nil {
			return nil, err
		}
		found := &resultsv1.CompetitionResultStanding{
			EntryId: int64(value.EntryID), Standing: standing,
			DisplayOrder: int32(value.DisplayOrder), //nolint:gosec // Results are bounded.
			Score:        scoreValue(value.Score),
		}
		if value.Placement > 0 {
			placement := int64(value.Placement)
			found.Placement = &placement
		}
		projected = append(projected, found)
	}
	return projected, nil
}

func scoreValue(value results.ScoreValue) *resultsv1.ScoreValue {
	switch {
	case value.Decimal != nil:
		return &resultsv1.ScoreValue{
			Value: &resultsv1.ScoreValue_Decimal{Decimal: *value.Decimal},
		}
	case value.Duration != nil:
		return &resultsv1.ScoreValue{
			Value: &resultsv1.ScoreValue_Duration{Duration: durationpb.New(*value.Duration)},
		}
	default:
		return nil
	}
}

func competitionAwards(values []results.Award) []*resultsv1.CompetitionAward {
	awards := make([]*resultsv1.CompetitionAward, 0, len(values))
	for _, value := range values {
		awards = append(awards, &resultsv1.CompetitionAward{
			Key: value.Key, Name: value.Name, Recipients: awardRecipients(value.Recipients),
			Promoted:     value.Promoted,
			DisplayOrder: int32(value.DisplayOrder), //nolint:gosec // Award count is bounded.
		})
	}
	return awards
}

func awardRecipients(values []results.AwardRecipient) []*resultsv1.AwardRecipient {
	recipients := make([]*resultsv1.AwardRecipient, 0, len(values))
	for _, value := range values {
		found := &resultsv1.AwardRecipient{}
		if value.EntryID > 0 {
			found.Recipient = &resultsv1.AwardRecipient_EntryId{EntryId: int64(value.EntryID)}
		} else {
			found.Recipient = &resultsv1.AwardRecipient_DisplayName{DisplayName: value.DisplayName}
		}
		recipients = append(recipients, found)
	}
	return recipients
}

func storedRecipients(values []store.AwardRecipientInput) []results.AwardRecipient {
	recipients := make([]results.AwardRecipient, 0, len(values))
	for _, value := range values {
		recipients = append(recipients, results.AwardRecipient{
			EntryID: value.EntryID, DisplayName: value.DisplayName,
		})
	}
	return recipients
}

func awardReleasePath(kind string, sessionID int) (*resultsv1.AwardReleasePath, error) {
	projected, err := enum("Award Release Path", kind, map[string]resultsv1.AwardReleasePathKind{
		"Standalone":  resultsv1.AwardReleasePathKind_AWARD_RELEASE_PATH_KIND_STANDALONE,
		"Prizegiving": resultsv1.AwardReleasePathKind_AWARD_RELEASE_PATH_KIND_PRIZEGIVING,
	})
	if err != nil {
		return nil, err
	}
	return &resultsv1.AwardReleasePath{
		Kind: projected, PrizegivingSessionId: int64(sessionID),
	}, nil
}

func itemRef(kind string, sessionID int, awardKey string, displayOrder int) (*resultsv1.ResultItemRef, error) {
	projected, err := ItemKind(kind)
	if err != nil {
		return nil, err
	}
	return &resultsv1.ResultItemRef{
		Kind: projected, CompetitionSessionId: int64(sessionID), AwardKey: awardKey,
		DisplayOrder: int32(displayOrder), //nolint:gosec // Result Items are bounded.
	}, nil
}

func enum[T ~int32](name, value string, values map[string]T) (T, error) {
	found, ok := values[value]
	if !ok {
		return 0, unknown(name, value)
	}
	return found, nil
}

func unknown(name, value string) error {
	return fmt.Errorf("%w: %s %q", ErrUnknownValue, name, value)
}

func setTimestamp(target **timestamppb.Timestamp, value time.Time) {
	if !value.IsZero() {
		*target = timestamppb.New(value)
	}
}
