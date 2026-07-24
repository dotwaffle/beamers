package programconnect

import (
	"testing"
	"time"

	"connectrpc.com/connect"

	programv1 "github.com/dotwaffle/beamers/gen/beamers/program/v1"
	resultsv1 "github.com/dotwaffle/beamers/gen/beamers/results/v1"
	"github.com/dotwaffle/beamers/internal/prizegivingvalue"
	"github.com/dotwaffle/beamers/internal/programcontrol"
	"github.com/dotwaffle/beamers/internal/store"
)

func TestProgramResultMessagePreservesPublicRevealContract(t *testing.T) {
	startedAt := time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)
	message := ProgramResultMessage(&store.ProgramResult{
		Ref: store.PrizegivingResultItemRef{
			Kind:                 prizegivingvalue.ItemCompetitionResults,
			CompetitionSessionID: 17,
			DisplayOrder:         2,
		},
		RevealMethod:              prizegivingvalue.RevealSequentialPodium,
		ReducedMotionRevealMethod: prizegivingvalue.RevealStatic,
		RevealSeed:                73,
		Status:                    prizegivingvalue.StageRevealing,
		Release:                   prizegivingvalue.ReleaseHeld,
		RevealStartedAt:           startedAt,
		RevealDuration:            3 * time.Second,
		ScoreBars: []store.ProgramScoreBar{{
			EntryID: 4, BasisPoints: 7500,
		}},
	})
	if message.GetItem().GetKind() !=
		resultsv1.ResultItemKind_RESULT_ITEM_KIND_COMPETITION_RESULTS ||
		message.GetItem().GetCompetitionSessionId() != 17 ||
		message.GetRevealMethod() !=
			resultsv1.RevealMethod_REVEAL_METHOD_SEQUENTIAL_PODIUM ||
		message.GetReducedMotionRevealMethod() !=
			resultsv1.RevealMethod_REVEAL_METHOD_STATIC_RESULT ||
		message.GetStatus() !=
			programv1.ResultStageStatus_RESULT_STAGE_STATUS_REVEALING ||
		message.GetRelease() !=
			programv1.ResultReleaseState_RESULT_RELEASE_STATE_HELD ||
		message.GetRevealDuration().AsDuration() != 3*time.Second ||
		len(message.GetScoreBars()) != 1 ||
		message.GetScoreBars()[0].GetBasisPoints() != 7500 {
		t.Fatalf("Program Result message = %+v", message)
	}
}

func TestProgramResultRequestMappingAndErrorCode(t *testing.T) {
	item := programItemFromMessage(&programv1.ProgramItem{
		Kind: programv1.ProgramItemKind_PROGRAM_ITEM_KIND_RESULT,
		Result: &programv1.ProgramResult{
			Item: &resultsv1.ResultItemRef{
				Kind:                 resultsv1.ResultItemKind_RESULT_ITEM_KIND_COMPETITION_AWARD,
				CompetitionSessionId: 17,
				AwardKey:             "jury",
				DisplayOrder:         2,
			},
		},
	})
	if item.Result == nil ||
		item.Result.Ref.Kind != prizegivingvalue.ItemCompetitionAward ||
		item.Result.Ref.CompetitionSessionID != 17 ||
		item.Result.Ref.AwardKey != "jury" ||
		item.Result.Ref.DisplayOrder != 2 {
		t.Fatalf("Program Result request item = %+v", item)
	}
	if code := connect.CodeOf(connectError(programcontrol.ErrResultTransition)); code !=
		connect.CodeFailedPrecondition {
		t.Fatalf("Result transition code = %v", code)
	}
}
