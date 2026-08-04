package displayconnect

import (
	"testing"
	"time"

	displayv1 "github.com/dotwaffle/beamers/gen/beamers/display/v1"
	"github.com/dotwaffle/beamers/internal/displays"
	"github.com/dotwaffle/beamers/internal/displaystream"
	"github.com/dotwaffle/beamers/internal/displayviews"
	"github.com/dotwaffle/beamers/internal/publictime"
	"github.com/dotwaffle/beamers/internal/stagetimer"
)

func TestSessionMessageCarriesPublicTimePresentation(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 2, 7, 12, 30, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	message := sessionMessage(displays.Session{
		PresentedStart: start, PresentedEnd: end,
		PresentedStartLabel: publictime.LabelActualStart,
		PresentedEndLabel:   publictime.LabelForecastEnd,
		Timeline: displays.TimelineGeometry{
			Day: "2026-02-07", Offset: 1250, Width: 2500, Lane: 1, LaneCount: 2,
		},
	})

	if got := message.GetPresentedStart().AsTime(); !got.Equal(start) {
		t.Errorf("presented start = %v, want %v", got, start)
	}
	if got := message.GetPresentedEnd().AsTime(); !got.Equal(end) {
		t.Errorf("presented end = %v, want %v", got, end)
	}
	if message.GetPresentedStartLabel() != "Actual Start" ||
		message.GetPresentedEndLabel() != "Forecast End" {
		t.Errorf("presented labels = %q, %q", message.GetPresentedStartLabel(), message.GetPresentedEndLabel())
	}
	if message.GetTimelineDay() != "2026-02-07" ||
		message.GetTimelineOffset() != 1250 ||
		message.GetTimelineWidth() != 2500 ||
		message.GetTimelineLane() != 1 ||
		message.GetTimelineLaneCount() != 2 {
		t.Errorf("Timeline geometry = %+v", message)
	}
}

func TestSessionMessageCarriesTimelineNowLineOnlyWhenProjected(t *testing.T) {
	t.Parallel()

	dayStart := time.Date(2026, 2, 7, 0, 0, 0, 0, time.UTC)
	dayEnd := dayStart.AddDate(0, 0, 1)
	nowOffset := 3333
	withNow := sessionMessage(displays.Session{
		Timeline: displays.TimelineGeometry{
			Day: "2026-02-07", NowOffset: &nowOffset, DayStart: dayStart, DayEnd: dayEnd,
		},
	})
	if withNow.GetTimelineNowOffset() != 3333 {
		t.Errorf("TimelineNowOffset = %d, want 3333", withNow.GetTimelineNowOffset())
	}
	if got := withNow.GetTimelineDayStart().AsTime(); !got.Equal(dayStart) {
		t.Errorf("TimelineDayStart = %v, want %v", got, dayStart)
	}
	if got := withNow.GetTimelineDayEnd().AsTime(); !got.Equal(dayEnd) {
		t.Errorf("TimelineDayEnd = %v, want %v", got, dayEnd)
	}

	// A Session whose Event day the server time falls outside carries no
	// now-line, and must not leak day bounds a client has no use for.
	withoutNow := sessionMessage(displays.Session{
		Timeline: displays.TimelineGeometry{Day: "2026-02-08", DayStart: dayStart, DayEnd: dayEnd},
	})
	if withoutNow.TimelineNowOffset != nil {
		t.Errorf("TimelineNowOffset = %v, want nil", withoutNow.TimelineNowOffset)
	}
	if withoutNow.TimelineDayStart != nil || withoutNow.TimelineDayEnd != nil {
		t.Errorf(
			"Timeline day bounds = [%v, %v), want unset",
			withoutNow.TimelineDayStart, withoutNow.TimelineDayEnd,
		)
	}
}

func TestSnapshotMessageCarriesStageTimerContract(t *testing.T) {
	t.Parallel()

	anchor := time.Date(2026, 2, 7, 12, 30, 0, 0, time.UTC)
	forecastEnd := anchor.Add(15 * time.Minute)
	noticeExpires := anchor.Add(5 * time.Second)
	spanStart := anchor.Add(-30 * time.Minute)
	message, err := snapshotMessage(displays.Snapshot{
		StageTimer: &displays.StageTimer{
			SessionID:                 42,
			Title:                     "Closing Keynote",
			Mode:                      stagetimer.Countdown,
			Anchor:                    anchor,
			ForecastEnd:               forecastEnd,
			AdjustmentSeconds:         300,
			AdjustmentNoticeExpiresAt: noticeExpires,
			SpanStart:                 spanStart,
			SpanEnd:                   anchor,
			Thresholds: []stagetimer.Threshold{
				{Remaining: 2 * time.Minute, Emphasis: stagetimer.Attention},
				{Remaining: 30 * time.Second, Emphasis: stagetimer.Urgent},
			},
		},
	}, displaystream.Cursor{}, "")
	if err != nil {
		t.Fatalf("project Display snapshot: %v", err)
	}

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
	if timer.GetAdjustmentSeconds() != 300 ||
		!timer.GetAdjustmentNoticeExpiresAt().AsTime().Equal(noticeExpires) {
		t.Errorf("adjustment notice = %+v", timer)
	}
	if got := timer.GetSpanStart().AsTime(); !got.Equal(spanStart) {
		t.Errorf("span start = %v, want %v", got, spanStart)
	}
	if got := timer.GetSpanEnd().AsTime(); !got.Equal(anchor) {
		t.Errorf("span end = %v, want %v", got, anchor)
	}
	if len(timer.GetThresholds()) != 2 ||
		timer.GetThresholds()[0].GetRemainingSeconds() != 120 ||
		timer.GetThresholds()[1].GetEmphasis() != displayv1.TimerEmphasis_TIMER_EMPHASIS_URGENT {
		t.Errorf("thresholds = %+v", timer.GetThresholds())
	}
}

func TestCompositionMessageCarriesLifecycleInks(t *testing.T) {
	t.Parallel()

	message := compositionMessage(displayviews.Composition{
		Theme: displayviews.Theme{
			LiveColor: "#ff7b72", DangerColor: "#ff8fa3",
		},
	})

	theme := message.GetTheme()
	if theme.GetLiveColor() != "#ff7b72" {
		t.Errorf("live color = %q, want #ff7b72", theme.GetLiveColor())
	}
	if theme.GetDangerColor() != "#ff8fa3" {
		t.Errorf("danger color = %q, want #ff8fa3", theme.GetDangerColor())
	}
}
