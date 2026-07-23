package displayconnect

import (
	"testing"
	"time"

	displayv1 "github.com/dotwaffle/beamers/gen/beamers/display/v1"
	"github.com/dotwaffle/beamers/internal/displays"
	"github.com/dotwaffle/beamers/internal/displaystream"
	"github.com/dotwaffle/beamers/internal/stagetimer"
)

func TestSnapshotMessageCarriesStageTimerContract(t *testing.T) {
	t.Parallel()

	anchor := time.Date(2026, 2, 7, 12, 30, 0, 0, time.UTC)
	forecastEnd := anchor.Add(15 * time.Minute)
	message := snapshotMessage(displays.Snapshot{
		StageTimer: &displays.StageTimer{
			SessionID:   42,
			Title:       "Closing Keynote",
			Mode:        stagetimer.Countdown,
			Anchor:      anchor,
			ForecastEnd: forecastEnd,
			Thresholds: []stagetimer.Threshold{
				{Remaining: 2 * time.Minute, Emphasis: stagetimer.Attention},
				{Remaining: 30 * time.Second, Emphasis: stagetimer.Urgent},
			},
		},
	}, displaystream.Cursor{}, "")

	timer := message.GetStageTimer()
	if timer.GetSessionId() != 42 || timer.GetTitle() != "Closing Keynote" {
		t.Fatalf("Stage Timer = %+v", timer)
	}
	if timer.GetMode() != displayv1.StageTimerMode_STAGE_TIMER_MODE_COUNTDOWN {
		t.Errorf("mode = %v, want countdown", timer.GetMode())
	}
	if got := timer.GetAnchor().AsTime(); !got.Equal(anchor) {
		t.Errorf("anchor = %v, want %v", got, anchor)
	}
	if got := timer.GetForecastEnd().AsTime(); !got.Equal(forecastEnd) {
		t.Errorf("Forecast End = %v, want %v", got, forecastEnd)
	}
	if len(timer.GetThresholds()) != 2 ||
		timer.GetThresholds()[0].GetRemainingSeconds() != 120 ||
		timer.GetThresholds()[1].GetEmphasis() != displayv1.TimerEmphasis_TIMER_EMPHASIS_URGENT {
		t.Errorf("thresholds = %+v", timer.GetThresholds())
	}
}
