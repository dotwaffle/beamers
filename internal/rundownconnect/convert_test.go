package rundownconnect

import (
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	rundownv1 "github.com/dotwaffle/beamers/gen/beamers/rundown/v1"
)

func TestSessionDraftDefaultsOmittedMinimumDurationToPlannedDuration(t *testing.T) {
	start := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	converted, err := sessionDraft(&rundownv1.SessionDraft{
		PlannedStart: timestamppb.New(start), PlannedEnd: timestamppb.New(start.Add(time.Hour)),
	})
	if err != nil {
		t.Fatalf("sessionDraft() error = %v", err)
	}
	if converted.MinimumDuration != time.Hour {
		t.Fatalf("MinimumDuration = %v, want 1h", converted.MinimumDuration)
	}
}

func TestSessionDraftPreservesExplicitZeroMinimumDuration(t *testing.T) {
	start := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	converted, err := sessionDraft(&rundownv1.SessionDraft{
		PlannedStart: timestamppb.New(start), PlannedEnd: timestamppb.New(start.Add(time.Hour)),
		MinimumDuration: durationpb.New(0),
	})
	if err != nil {
		t.Fatalf("sessionDraft() error = %v", err)
	}
	if converted.MinimumDuration != 0 {
		t.Fatalf("MinimumDuration = %v, want 0", converted.MinimumDuration)
	}
}
