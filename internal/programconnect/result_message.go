package programconnect

import (
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	programv1 "github.com/dotwaffle/beamers/gen/beamers/program/v1"
	resultsv1 "github.com/dotwaffle/beamers/gen/beamers/results/v1"
	"github.com/dotwaffle/beamers/internal/prizegivingvalue"
	"github.com/dotwaffle/beamers/internal/resultsprojection"
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
func ProgramResultMessage(found *store.ProgramResult) (*programv1.ProgramResult, error) {
	if found == nil {
		return nil, nil
	}
	item, err := resultsprojection.StoredItemRef(&found.Ref)
	if err != nil {
		return nil, err
	}
	revealMethod, err := resultsprojection.RevealMethod(string(found.RevealMethod))
	if err != nil {
		return nil, err
	}
	reducedMotionRevealMethod, err := resultsprojection.RevealMethod(
		string(found.ReducedMotionRevealMethod),
	)
	if err != nil {
		return nil, err
	}
	competitionResults, err := resultsprojection.StoredDraft(&found.CompetitionResults)
	if err != nil {
		return nil, err
	}
	eventAward, err := resultsprojection.StoredEventAward(&found.EventAward)
	if err != nil {
		return nil, err
	}
	status, err := resultStageStatus(string(found.Status))
	if err != nil {
		return nil, err
	}
	release, err := resultReleaseState(string(found.Release))
	if err != nil {
		return nil, err
	}
	result := &programv1.ProgramResult{
		Item:                      item,
		RevealMethod:              revealMethod,
		ReducedMotionRevealMethod: reducedMotionRevealMethod,
		RevealSeed:                found.RevealSeed,
		Status:                    status,
		Release:                   release,
		Replay:                    found.Replay,
		CompetitionResults:        competitionResults,
		EventAward:                eventAward,
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
	return result, nil
}

func resultReleaseState(found string) (programv1.ResultReleaseState, error) {
	value, ok := map[string]programv1.ResultReleaseState{
		"Held":        programv1.ResultReleaseState_RESULT_RELEASE_STATE_HELD,
		"Ready":       programv1.ResultReleaseState_RESULT_RELEASE_STATE_READY,
		"CeremonyEnd": programv1.ResultReleaseState_RESULT_RELEASE_STATE_CEREMONY_END,
	}[found]
	if !ok {
		return 0, fmt.Errorf(
			"%w: Result Release State %q",
			resultsprojection.ErrUnknownValue,
			found,
		)
	}
	return value, nil
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

func resultStageStatus(found string) (programv1.ResultStageStatus, error) {
	value, ok := map[string]programv1.ResultStageStatus{
		"Pending":   programv1.ResultStageStatus_RESULT_STAGE_STATUS_PENDING,
		"Taken":     programv1.ResultStageStatus_RESULT_STAGE_STATUS_TAKEN,
		"Revealing": programv1.ResultStageStatus_RESULT_STAGE_STATUS_REVEALING,
		"Revealed":  programv1.ResultStageStatus_RESULT_STAGE_STATUS_REVEALED,
		"Skipped":   programv1.ResultStageStatus_RESULT_STAGE_STATUS_SKIPPED,
	}[found]
	if !ok {
		return 0, fmt.Errorf(
			"%w: Result Stage Status %q",
			resultsprojection.ErrUnknownValue,
			found,
		)
	}
	return value, nil
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
