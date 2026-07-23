package displays

import (
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/displayviews"
	"github.com/dotwaffle/beamers/internal/store"
)

func TestProjectStageTimerUsesLiveSessionAtAssignedLocation(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 2, 7, 12, 3, 0, 0, time.UTC)
	found := store.DisplaySnapshotState{
		LocationID: 7,
		ViewKey:    displayviews.StageTimer,
		Sessions: []store.DisplaySessionState{
			{
				ID: 1, Lifecycle: "Live", AudienceVisibility: "Public", LocationIDs: []int{8},
				Type: "Presentation", TimingPolicy: "FixedEnd",
				RunPlannedStart: start.Add(-3 * time.Minute),
				RunPlannedEnd:   start.Add(27 * time.Minute),
				ForecastEnd:     start.Add(27 * time.Minute),
				ActualStart:     start,
			},
			{
				ID: 2, Title: "Closing Keynote", TimerTitle: "Closing Keynote", Lifecycle: "Live",
				AudienceVisibility: "Public", LocationIDs: []int{7},
				Type: "Presentation", TimingPolicy: "FixedDuration",
				RunPlannedStart: start.Add(-3 * time.Minute),
				RunPlannedEnd:   start.Add(27 * time.Minute),
				ForecastEnd:     start.Add(30 * time.Minute),
				ActualStart:     start,
			},
		},
	}
	configuration := displayviews.DefaultConfiguration()
	configuration.SessionTypeTimerThresholds = map[string][]displayviews.TimerThreshold{
		"Presentation": {{RemainingSeconds: 120, Emphasis: displayviews.EmphasisAttention}},
	}

	timer, ok, err := projectStageTimer(found, configuration)
	if err != nil {
		t.Fatalf("project Stage Timer: %v", err)
	}
	if !ok {
		t.Fatal("project Stage Timer returned no timer")
	}
	if timer.SessionID != 2 || timer.Title != "Closing Keynote" {
		t.Errorf("timer Session = %+v, want Closing Keynote (2)", timer)
	}
	if got, want := timer.Anchor, start.Add(30*time.Minute); !got.Equal(want) {
		t.Errorf("timer anchor = %v, want %v", got, want)
	}
	if got, want := timer.ForecastEnd, start.Add(30*time.Minute); !got.Equal(want) {
		t.Errorf("timer Forecast End = %v, want %v", got, want)
	}
	if len(timer.Thresholds) != 1 || timer.Thresholds[0].Remaining != 2*time.Minute {
		t.Errorf("timer thresholds = %+v, want Session-type override", timer.Thresholds)
	}
}

func TestProjectStageTimerRequiresLiveSession(t *testing.T) {
	t.Parallel()

	found := store.DisplaySnapshotState{
		LocationID: 7,
		ViewKey:    displayviews.StageTimer,
		Sessions: []store.DisplaySessionState{{
			ID: 2, Lifecycle: "Ended", LocationIDs: []int{7},
		}},
	}
	if _, ok, err := projectStageTimer(found, displayviews.DefaultConfiguration()); err != nil || ok {
		t.Errorf("project Stage Timer = ok %t, error %v; want no timer", ok, err)
	}
}

func TestProjectStageTimerIncludesCrewOnlyLiveSessionWithoutAudienceProjection(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)
	found := store.DisplaySnapshotState{
		LocationID: 7,
		ViewKey:    displayviews.StageTimer,
		Sessions: []store.DisplaySessionState{{
			ID: 2, Lifecycle: "Live", AudienceVisibility: "CrewOnly",
			TimerTitle:  "Private Soundcheck",
			LocationIDs: []int{7}, Type: "Presentation", TimingPolicy: "FixedEnd",
			RunPlannedStart: start, RunPlannedEnd: start.Add(time.Hour),
			ActualStart: start,
		}},
	}
	timer, ok, err := projectStageTimer(found, displayviews.DefaultConfiguration())
	if err != nil || !ok {
		t.Fatalf("project CrewOnly Stage Timer = ok %t, error %v", ok, err)
	}
	if timer.Title != "Private Soundcheck" {
		t.Errorf("CrewOnly timer title = %q, want private crew title", timer.Title)
	}
}

func TestProjectStageTimerSelectsMostRecentlyStartedSessionDeterministically(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)
	found := store.DisplaySnapshotState{
		LocationID: 7,
		ViewKey:    displayviews.StageTimer,
		Sessions: []store.DisplaySessionState{
			{
				ID: 9, Title: "Earlier", Lifecycle: "Live",
				AudienceVisibility: "Public", LocationIDs: []int{7},
				Type: "Presentation", TimingPolicy: "FixedEnd",
				RunPlannedStart: start, RunPlannedEnd: start.Add(time.Hour),
				ActualStart: start,
			},
			{
				ID: 4, Title: "Current", TimerTitle: "Current", Lifecycle: "Live",
				AudienceVisibility: "Public", LocationIDs: []int{7},
				Type: "Presentation", TimingPolicy: "FixedEnd",
				RunPlannedStart: start, RunPlannedEnd: start.Add(time.Hour),
				ActualStart: start.Add(time.Minute),
			},
		},
	}
	timer, ok, err := projectStageTimer(found, displayviews.DefaultConfiguration())
	if err != nil || !ok {
		t.Fatalf("project Stage Timer = ok %t, error %v", ok, err)
	}
	if timer.SessionID != 4 {
		t.Errorf("timer Session ID = %d, want most recently started Session 4", timer.SessionID)
	}
}

func TestProjectStageTimerIgnoresLongInheritedThreshold(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)
	found := store.DisplaySnapshotState{
		LocationID: 7,
		ViewKey:    displayviews.StageTimer,
		Sessions: []store.DisplaySessionState{{
			ID: 2, Lifecycle: "Live", AudienceVisibility: "Public", LocationIDs: []int{7},
			Type: "Break", TimingPolicy: "FixedEnd",
			RunPlannedStart: start, RunPlannedEnd: start.Add(2 * time.Minute),
			ActualStart: start,
		}},
	}
	timer, ok, err := projectStageTimer(found, displayviews.DefaultConfiguration())
	if err != nil || !ok {
		t.Fatalf("project Stage Timer = ok %t, error %v", ok, err)
	}
	if len(timer.Thresholds) != 1 || timer.Thresholds[0].Remaining != time.Minute {
		t.Errorf("timer thresholds = %+v, want only one-minute threshold", timer.Thresholds)
	}
}
