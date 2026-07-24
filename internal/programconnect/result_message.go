package programconnect

import (
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	programv1 "github.com/dotwaffle/beamers/gen/beamers/program/v1"
	resultsv1 "github.com/dotwaffle/beamers/gen/beamers/results/v1"
	"github.com/dotwaffle/beamers/internal/prizegivingvalue"
	"github.com/dotwaffle/beamers/internal/store"
)

func programResultFromMessage(found *programv1.ProgramResult) *store.ProgramResult {
	if found == nil || found.GetItem() == nil {
		return nil
	}
	return &store.ProgramResult{Ref: resultItemRefFromMessage(found.GetItem())}
}

// ProgramResultMessage projects exact locked Result truth to the shared
// Program Output contract.
func ProgramResultMessage(found *store.ProgramResult) *programv1.ProgramResult {
	if found == nil {
		return nil
	}
	result := &programv1.ProgramResult{
		Item:                      resultItemRefMessage(found.Ref),
		RevealMethod:              revealMethod(string(found.RevealMethod)),
		ReducedMotionRevealMethod: revealMethod(string(found.ReducedMotionRevealMethod)),
		RevealSeed:                found.RevealSeed,
		Status:                    resultStageStatus(string(found.Status)),
		Release:                   resultReleaseState(string(found.Release)),
		Replay:                    found.Replay,
		CompetitionResults:        competitionResultsDraftMessage(found.CompetitionResults),
		EventAward:                eventAwardMessage(found.EventAward),
	}
	for _, bar := range found.ScoreBars {
		result.ScoreBars = append(result.ScoreBars, &programv1.ProgramScoreBar{
			EntryId: int64(bar.EntryID), BasisPoints: bar.BasisPoints,
		})
	}
	setTimestamp(&result.TakenAt, found.TakenAt)
	setTimestamp(&result.RevealStartedAt, found.RevealStartedAt)
	setDuration(&result.RevealDuration, found.RevealDuration)
	setTimestamp(&result.RevealCompletedAt, found.RevealCompletedAt)
	setTimestamp(&result.SkippedAt, found.SkippedAt)
	setTimestamp(&result.PresentationStartedAt, found.PresentationStartedAt)
	setDuration(&result.PresentationDuration, found.PresentationDuration)
	return result
}

func resultReleaseState(found string) programv1.ResultReleaseState {
	return map[string]programv1.ResultReleaseState{
		"Held":        programv1.ResultReleaseState_RESULT_RELEASE_STATE_HELD,
		"Ready":       programv1.ResultReleaseState_RESULT_RELEASE_STATE_READY,
		"CeremonyEnd": programv1.ResultReleaseState_RESULT_RELEASE_STATE_CEREMONY_END,
	}[found]
}

func resultItemRefFromMessage(
	found *resultsv1.ResultItemRef,
) store.PrizegivingResultItemRef {
	return store.PrizegivingResultItemRef{
		Kind: prizegivingvalue.ItemKind(map[resultsv1.ResultItemKind]string{
			resultsv1.ResultItemKind_RESULT_ITEM_KIND_COMPETITION_RESULTS: "CompetitionResults",
			resultsv1.ResultItemKind_RESULT_ITEM_KIND_NO_PUBLIC_RESULTS:   "NoPublicResults",
			resultsv1.ResultItemKind_RESULT_ITEM_KIND_COMPETITION_AWARD:   "CompetitionAward",
			resultsv1.ResultItemKind_RESULT_ITEM_KIND_EVENT_AWARD:         "EventAward",
		}[found.GetKind()]),
		CompetitionSessionID: int(found.GetCompetitionSessionId()),
		AwardKey:             found.GetAwardKey(),
		DisplayOrder:         int(found.GetDisplayOrder()),
	}
}

func resultItemRefMessage(
	found store.PrizegivingResultItemRef,
) *resultsv1.ResultItemRef {
	return &resultsv1.ResultItemRef{
		Kind: map[string]resultsv1.ResultItemKind{
			"CompetitionResults": resultsv1.ResultItemKind_RESULT_ITEM_KIND_COMPETITION_RESULTS,
			"NoPublicResults":    resultsv1.ResultItemKind_RESULT_ITEM_KIND_NO_PUBLIC_RESULTS,
			"CompetitionAward":   resultsv1.ResultItemKind_RESULT_ITEM_KIND_COMPETITION_AWARD,
			"EventAward":         resultsv1.ResultItemKind_RESULT_ITEM_KIND_EVENT_AWARD,
		}[string(found.Kind)],
		CompetitionSessionId: int64(found.CompetitionSessionID),
		AwardKey:             found.AwardKey,
		DisplayOrder:         int32(found.DisplayOrder), //nolint:gosec // Locked Result Items are bounded.
	}
}

func revealMethod(found string) resultsv1.RevealMethod {
	return map[string]resultsv1.RevealMethod{
		"StaticResult":      resultsv1.RevealMethod_REVEAL_METHOD_STATIC_RESULT,
		"SequentialPodium":  resultsv1.RevealMethod_REVEAL_METHOD_SEQUENTIAL_PODIUM,
		"AnimatedScoreBars": resultsv1.RevealMethod_REVEAL_METHOD_ANIMATED_SCORE_BARS,
	}[found]
}

func resultStageStatus(found string) programv1.ResultStageStatus {
	return map[string]programv1.ResultStageStatus{
		"Pending":   programv1.ResultStageStatus_RESULT_STAGE_STATUS_PENDING,
		"Taken":     programv1.ResultStageStatus_RESULT_STAGE_STATUS_TAKEN,
		"Revealing": programv1.ResultStageStatus_RESULT_STAGE_STATUS_REVEALING,
		"Revealed":  programv1.ResultStageStatus_RESULT_STAGE_STATUS_REVEALED,
		"Skipped":   programv1.ResultStageStatus_RESULT_STAGE_STATUS_SKIPPED,
	}[found]
}

func competitionResultsDraftMessage(
	found store.CompetitionResultsDraft,
) *resultsv1.CompetitionResultsDraft {
	if found.ID == 0 {
		return nil
	}
	result := &resultsv1.CompetitionResultsDraft{
		Id: int64(found.ID), EventId: int64(found.EventID),
		SessionId: int64(found.SessionID), Revision: int64(found.Revision),
		Disposition: map[string]resultsv1.ResultsDisposition{
			"Pending":         resultsv1.ResultsDisposition_RESULTS_DISPOSITION_PENDING,
			"Publish":         resultsv1.ResultsDisposition_RESULTS_DISPOSITION_PUBLISH,
			"NoPublicResults": resultsv1.ResultsDisposition_RESULTS_DISPOSITION_NO_PUBLIC_RESULTS,
		}[found.Disposition],
		NoPublicCrewReason: found.NoPublicCrewReason,
		PublicExplanation:  found.PublicExplanation,
		Score: &resultsv1.ScorePolicy{
			Type: map[string]resultsv1.ScoreType{
				"None":     resultsv1.ScoreType_SCORE_TYPE_NONE,
				"Decimal":  resultsv1.ScoreType_SCORE_TYPE_DECIMAL,
				"Duration": resultsv1.ScoreType_SCORE_TYPE_DURATION,
			}[found.ScoreType],
			Visibility: map[string]resultsv1.ScoreVisibility{
				"Public":   resultsv1.ScoreVisibility_SCORE_VISIBILITY_PUBLIC,
				"CrewOnly": resultsv1.ScoreVisibility_SCORE_VISIBILITY_CREW_ONLY,
			}[found.ScoreVisibility],
			Unit:      found.ScoreUnit,
			Precision: int32(found.ScorePrecision), //nolint:gosec // Validated before storage.
			Requirement: map[string]resultsv1.ScoreRequirement{
				"Optional": resultsv1.ScoreRequirement_SCORE_REQUIREMENT_OPTIONAL,
				"Required": resultsv1.ScoreRequirement_SCORE_REQUIREMENT_REQUIRED,
			}[found.ScoreRequirement],
			Interpretation: map[string]resultsv1.ScoreInterpretation{
				"HigherWins":    resultsv1.ScoreInterpretation_SCORE_INTERPRETATION_HIGHER_WINS,
				"LowerWins":     resultsv1.ScoreInterpretation_SCORE_INTERPRETATION_LOWER_WINS,
				"Informational": resultsv1.ScoreInterpretation_SCORE_INTERPRETATION_INFORMATIONAL,
			}[found.ScoreInterpretation],
		},
		Ready:              found.Ready,
		ReadyByAccountId:   int64(found.ReadyByAccountID),
		CreatedByAccountId: int64(found.CreatedByAccountID),
		Awards:             competitionAwardsMessage(found.Awards),
	}
	for _, standing := range found.Standings {
		value := &resultsv1.CompetitionResultStanding{
			EntryId: int64(standing.EntryID),
			Standing: map[string]resultsv1.ResultStanding{
				"Placed":   resultsv1.ResultStanding_RESULT_STANDING_PLACED,
				"Unplaced": resultsv1.ResultStanding_RESULT_STANDING_UNPLACED,
			}[standing.Standing],
			DisplayOrder: int32(standing.DisplayOrder), //nolint:gosec // Results are bounded.
		}
		if standing.Placement > 0 {
			placement := int64(standing.Placement)
			value.Placement = &placement
		}
		switch {
		case standing.DecimalScore != nil:
			value.Score = &resultsv1.ScoreValue{
				Value: &resultsv1.ScoreValue_Decimal{Decimal: *standing.DecimalScore},
			}
		case standing.DurationScoreNanos != nil:
			value.Score = &resultsv1.ScoreValue{
				Value: &resultsv1.ScoreValue_Duration{
					Duration: durationpb.New(time.Duration(*standing.DurationScoreNanos)),
				},
			}
		}
		result.Standings = append(result.Standings, value)
	}
	setTimestamp(&result.ReadyAt, found.ReadyAt)
	setTimestamp(&result.CreatedAt, found.CreatedAt)
	return result
}

func competitionAwardsMessage(
	found []store.CompetitionAward,
) []*resultsv1.CompetitionAward {
	result := make([]*resultsv1.CompetitionAward, 0, len(found))
	for _, award := range found {
		result = append(result, &resultsv1.CompetitionAward{
			Key: award.Key, Name: award.Name,
			Recipients:   awardRecipientsMessage(award.Recipients),
			Promoted:     award.Promoted,
			DisplayOrder: int32(award.DisplayOrder), //nolint:gosec // Awards are bounded.
		})
	}
	return result
}

func eventAwardMessage(found store.EventAward) *resultsv1.EventAward {
	if found.Key == "" {
		return nil
	}
	return &resultsv1.EventAward{
		Key: found.Key, Name: found.Name,
		Recipients:   awardRecipientsMessage(found.Recipients),
		DisplayOrder: int32(found.DisplayOrder), //nolint:gosec // Awards are bounded.
		ReleasePath: &resultsv1.AwardReleasePath{
			Kind: map[string]resultsv1.AwardReleasePathKind{
				"Standalone":  resultsv1.AwardReleasePathKind_AWARD_RELEASE_PATH_KIND_STANDALONE,
				"Prizegiving": resultsv1.AwardReleasePathKind_AWARD_RELEASE_PATH_KIND_PRIZEGIVING,
			}[found.ReleasePath.Kind],
			PrizegivingSessionId: int64(found.ReleasePath.PrizegivingSessionID),
		},
	}
}

func awardRecipientsMessage(
	found []store.AwardRecipientInput,
) []*resultsv1.AwardRecipient {
	result := make([]*resultsv1.AwardRecipient, 0, len(found))
	for _, recipient := range found {
		value := &resultsv1.AwardRecipient{}
		if recipient.EntryID > 0 {
			value.Recipient = &resultsv1.AwardRecipient_EntryId{
				EntryId: int64(recipient.EntryID),
			}
		} else {
			value.Recipient = &resultsv1.AwardRecipient_DisplayName{
				DisplayName: recipient.DisplayName,
			}
		}
		result = append(result, value)
	}
	return result
}

func setTimestamp(target **timestamppb.Timestamp, value time.Time) {
	if !value.IsZero() {
		*target = timestamppb.New(value)
	}
}

func setDuration(target **durationpb.Duration, value time.Duration) {
	if value != 0 {
		*target = durationpb.New(value)
	}
}
