package resultsconnect

import (
	"errors"
	"testing"

	"google.golang.org/protobuf/types/known/durationpb"

	resultsv1 "github.com/dotwaffle/beamers/gen/beamers/results/v1"
	"github.com/dotwaffle/beamers/internal/results"
)

func TestScoreValueFromProtoRejectsDurationOutsideExactStorageRange(t *testing.T) {
	value := &resultsv1.ScoreValue{
		Value: &resultsv1.ScoreValue_Duration{
			Duration: &durationpb.Duration{Seconds: 315_576_000_000},
		},
	}
	if _, err := scoreValueFromProto(value); !errors.Is(err, results.ErrInvalidScore) {
		t.Fatalf("oversized Duration error = %v", err)
	}
}

func TestPrizegivingPlanFromProtoRejectsUnspecifiedEnums(t *testing.T) {
	_, err := resultItemsFromProto([]*resultsv1.ResultItem{{
		Kind: resultsv1.ResultItemKind_RESULT_ITEM_KIND_COMPETITION_RESULTS,
	}})
	if !errors.Is(err, results.ErrInvalidInput) {
		t.Fatalf("unspecified Reveal Method error = %v", err)
	}

	if mode := prizegivingPreviewModeFromProto(
		resultsv1.PrizegivingPreviewMode_PRIZEGIVING_PREVIEW_MODE_UNSPECIFIED,
	); mode != "" {
		t.Fatalf("unspecified Prizegiving Preview mode = %q", mode)
	}
}
