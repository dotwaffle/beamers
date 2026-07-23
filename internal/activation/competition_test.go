package activation

import (
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/store"
)

func TestValidPublishedSessionAcceptsCompetitionConfiguration(t *testing.T) {
	plannedStart := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	if !validPublishedSession(store.PublishedSession{
		ID: 1, Title: "Demo Competition", Type: "Competition", AudienceVisibility: "Public",
		PlannedStart: plannedStart, PlannedEnd: plannedStart.Add(time.Hour),
		TimingPolicy: "FixedEnd", MinimumDurationSeconds: 1800,
		StartBoundary: "Hard", EndBoundary: "Hard",
		SubmissionDeadline:      plannedStart.Add(-30 * time.Minute),
		EntryDefaultDisposition: "Included",
	}) {
		t.Fatal("valid published Competition failed Activation validation")
	}
}
