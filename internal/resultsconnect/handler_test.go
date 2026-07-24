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
