package displays

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/displayviews"
	"github.com/dotwaffle/beamers/internal/publictime"
	"github.com/dotwaffle/beamers/internal/stagetimer"
	"github.com/dotwaffle/beamers/internal/themevalue"
)

func TestDisplayThemeUsesResolvedEventTheme(t *testing.T) {
	t.Parallel()

	config := themevalue.DefaultConfig()
	config.BrandAsset = themevalue.BrandAssetSignal
	config.BackgroundColor = "#112233"
	config.SurfaceColor = "#223344"
	config.AccentColor = "#ffdf6e"
	config.TextColor = "#ffffff"
	config.Background = themevalue.BackgroundNebula
	config.Typeface = themevalue.TypefaceDemoscene
	config.Transition = themevalue.TransitionFade
	config.Motion = themevalue.MotionStill

	config.LiveColor = "#ff7b72"
	config.DangerColor = "#ff8fa3"

	theme := eventDisplayTheme(config, "Revision")
	if theme.Branding != "Revision" ||
		theme.BrandAsset != themevalue.BrandAssetSignal ||
		theme.ForegroundColor != "#ffffff" ||
		theme.BackgroundColor != "#112233" ||
		theme.SurfaceColor != "#223344" ||
		theme.SignalColor != "#ffdf6e" ||
		theme.LiveColor != "#ff7b72" ||
		theme.DangerColor != "#ff8fa3" ||
		theme.Background != displayviews.BackgroundNebula ||
		theme.Font != displayviews.FontDemoscene ||
		theme.Transition != displayviews.TransitionNone {
		t.Fatalf("Display Event Theme = %+v", theme)
	}
}

func TestDisplaySessionKickerAndLifecycleBadge(t *testing.T) {
	t.Parallel()

	live := Session{Lifecycle: "Live", DisplayPresentedStart: "2099-08-21 10:00"}
	if got := displaySessionKicker(live); got != "NOW" {
		t.Errorf("kicker for Live Session = %q, want NOW", got)
	}
	if !displayLifecycleBadge(live) {
		t.Error("Live Session should carry a lifecycle badge")
	}

	upcoming := Session{Lifecycle: "Scheduled", DisplayPresentedStart: "2099-08-21 10:00"}
	if got := displaySessionKicker(upcoming); got != "UP NEXT · 2099-08-21 10:00" {
		t.Errorf("kicker for Scheduled Session = %q, want UP NEXT with its start time", got)
	}
	if displayLifecycleBadge(upcoming) {
		t.Error("Scheduled Session should not carry a lifecycle badge")
	}

	canceled := Session{Lifecycle: "Canceled"}
	if !displayLifecycleBadge(canceled) {
		t.Error("Canceled Session should carry a lifecycle badge")
	}
}

func TestEmergencyAlertKeepsCertifiedPresentationOutsideEventTheme(t *testing.T) {
	t.Parallel()

	configuration := displayviews.DefaultConfiguration()
	configuration.Theme.BackgroundColor = "#112233"
	composition, err := displayviews.Compose("", true, configuration)
	if err != nil {
		t.Fatalf("compose themed Display: %v", err)
	}
	var output strings.Builder
	err = DisplayPage(Snapshot{
		ProtocolVersion: "beamers.display.v1",
		AssetVersion:    "test",
		Display:         Display{Name: "Display"},
		Composition:     composition,
		EmergencyAlert:  &DisplayOverride{Text: "Evacuate"},
	}).Render(t.Context(), &output)
	if err != nil {
		t.Fatalf("render Emergency Alert: %v", err)
	}
	if !strings.Contains(
		output.String(),
		`<main class="display-view emergency-alert display-override-replace" data-override-kind="EmergencyAlert">`,
	) || strings.Contains(
		output.String(),
		`class="display-view emergency-alert display-override-replace" style=`,
	) {
		t.Fatalf("Emergency Alert inherited Event Theme: %s", output.String())
	}
}

func TestDisplayPageRendersEveryConfiguredBuiltInRegion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		viewKey string
		standby bool
	}{
		{name: "Standby", standby: true},
		{name: "Event Overview", viewKey: displayviews.EventOverview},
		{name: "Location Signage", viewKey: displayviews.LocationSignage},
		{name: "Stage Timer", viewKey: displayviews.StageTimer},
		{name: "Competition Output", viewKey: displayviews.CompetitionOutput},
		{name: "Timeline", viewKey: displayviews.Timeline},
		{name: "Crew Overview", viewKey: displayviews.CrewOverview},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			composition, err := displayviews.Compose(
				test.viewKey,
				test.standby,
				displayviews.DefaultConfiguration(),
			)
			if err != nil {
				t.Fatalf("compose test Display: %v", err)
			}
			snapshot := Snapshot{
				ProtocolVersion: "beamers.display.v1",
				AssetVersion:    "test-asset",
				ServerTime:      time.Date(2099, 8, 21, 8, 0, 0, 0, time.UTC),
				Display:         Display{Name: "Test Display"},
				EventName:       "Test Event",
				LocationName:    "Main Hall",
				ViewKey:         test.viewKey,
				Standby:         test.standby,
				Composition:     composition,
			}
			var rendered strings.Builder
			if err := DisplayPage(snapshot).Render(context.Background(), &rendered); err != nil {
				t.Fatalf("render Display page: %v", err)
			}
			for _, region := range composition.Layout.Regions {
				want := fmt.Sprintf(
					`data-region=%q data-widget=%q data-persistent="%t"`,
					region.Name,
					region.Widget,
					region.Persistent,
				)
				if !strings.Contains(rendered.String(), want) {
					t.Errorf("Display page missing configured Region %q: %s", want, rendered.String())
				}
			}
		})
	}
}

func TestDisplayNowNextExcludesInactiveSessions(t *testing.T) {
	t.Parallel()

	now := time.Date(2099, 8, 21, 9, 0, 0, 0, time.UTC)
	sessions := []Session{
		{Title: "Canceled", Lifecycle: "Canceled"},
		{
			Title: "Ended", Lifecycle: "Ended",
			ForecastEnd: now.Add(-time.Hour),
		},
		{
			Title: "Past", Lifecycle: "Scheduled",
			ForecastEnd: now.Add(-time.Minute),
		},
		{Title: "Current", Lifecycle: "Live", ForecastEnd: now.Add(time.Hour)},
		{Title: "Next", Lifecycle: "Scheduled", ForecastEnd: now.Add(2 * time.Hour)},
	}
	got := displayNowNext(sessions, now)
	if len(got) != 2 || got[0].Title != "Current" || got[1].Title != "Next" {
		t.Errorf("Now/Next Sessions = %+v, want Current and Next", got)
	}
	if len(sessions) != 5 {
		t.Errorf("Now/Next filtering changed the full Display rotation: %+v", sessions)
	}
}

func TestDisplayPageServerRendersStageTimerState(t *testing.T) {
	t.Parallel()

	now := time.Date(2099, 8, 21, 8, 0, 30, 0, time.UTC)
	composition, err := displayviews.Compose(
		displayviews.StageTimer,
		false,
		displayviews.DefaultConfiguration(),
	)
	if err != nil {
		t.Fatalf("compose Stage Timer: %v", err)
	}
	snapshot := Snapshot{
		ProtocolVersion: "beamers.display.v1",
		AssetVersion:    "test-asset",
		ServerTime:      now,
		Display:         Display{Name: "Stage Right"},
		EventName:       "Test Event",
		ViewKey:         displayviews.StageTimer,
		Composition:     composition,
		StageTimer: &StageTimer{
			SessionID:                 42,
			Title:                     "Closing Keynote",
			Mode:                      stagetimer.Countdown,
			Anchor:                    now.Add(30 * time.Second),
			AdjustmentSeconds:         300,
			AdjustmentNoticeExpiresAt: now.Add(5 * time.Second),
			Thresholds: []stagetimer.Threshold{
				{Remaining: time.Minute, Emphasis: stagetimer.Urgent},
			},
			SpanStart: now.Add(-30 * time.Minute),
			SpanEnd:   now.Add(30 * time.Second),
		},
	}
	var rendered strings.Builder
	if err := DisplayPage(snapshot).Render(context.Background(), &rendered); err != nil {
		t.Fatalf("render Stage Timer page: %v", err)
	}
	for _, want := range []string{
		"Closing Keynote",
		"Remaining",
		"00:30",
		// The emphasis marks the Region, the same element client.js marks. On the
		// inner block it framed a different box before and after a reconnect.
		`data-widget="stage-timer" data-persistent="true" data-timer-emphasis="urgent"`,
		"Urgent",
		"Time adjusted: +5:00",
		"data-timer-adjustment-notice",
		// The bar carries the span the projection bounded, the same values the
		// streamed payload ships, so both renderers draw the same progress.
		`data-progress-start="2099-08-21T07:30:30Z"`,
		`data-progress-end="2099-08-21T08:01:00Z"`,
	} {
		if !strings.Contains(rendered.String(), want) {
			t.Errorf("Stage Timer page missing %q: %s", want, rendered.String())
		}
	}
}

// TestDisplayPageMatchesTheClientRendererForRotationAndRanking pins the entry
// document to the markup internal/displays/client.js produces. The two
// renderers draw the same Display: the server paints it first and the client
// repaints it after every reconnect, so any disagreement shows up as content
// that shifts once a kiosk reconnects.
func TestDisplayPageMatchesTheClientRendererForRotationAndRanking(t *testing.T) {
	t.Parallel()

	sessions := []Session{
		{
			Title:                 "Opening Keynote",
			Lifecycle:             "Live",
			PresentedStartLabel:   publictime.LabelActualStart,
			PresentedEndLabel:     publictime.LabelForecastEnd,
			PresentedStart:        time.Date(2099, 8, 21, 8, 0, 0, 0, time.UTC),
			PresentedEnd:          time.Date(2099, 8, 21, 9, 0, 0, 0, time.UTC),
			DisplayPresentedStart: "08:00",
			DisplayPresentedEnd:   "09:00",
		},
		{
			Title:                 "Second Session",
			Lifecycle:             "Scheduled",
			PresentedStartLabel:   publictime.LabelForecastStart,
			PresentedEndLabel:     publictime.LabelForecastEnd,
			PresentedStart:        time.Date(2099, 8, 21, 9, 30, 0, 0, time.UTC),
			PresentedEnd:          time.Date(2099, 8, 21, 10, 0, 0, 0, time.UTC),
			DisplayPresentedStart: "09:30",
			DisplayPresentedEnd:   "10:00",
		},
	}
	rendered := renderLocationSignage(t, sessions)

	// The client shows one rotation page and hides the rest. A server document
	// that showed every page at once would stack the whole Event on first paint
	// and then collapse when the client booted.
	if visible := strings.Count(rendered, `data-rotation-page data-slot="rotation"`); visible != 1 {
		t.Errorf("visible rotation pages = %d, want 1: %s", visible, rendered)
	}
	if collapsed := strings.Count(rendered, `data-rotation-page hidden data-slot="rotation"`); collapsed != 1 {
		t.Errorf("hidden rotation pages = %d, want 1: %s", collapsed, rendered)
	}
	for _, want := range []string{`data-slot="now"`, `data-slot="next"`} {
		if !strings.Contains(rendered, want) {
			t.Errorf("Now / Next Region missing %q: %s", want, rendered)
		}
	}

	// An empty rotation explains itself rather than leaving the Region blank.
	empty := renderLocationSignage(t, nil)
	if !strings.Contains(empty, "No public Event information is currently scheduled.") {
		t.Errorf("empty rotation Region has no explanation: %s", empty)
	}
}

// TestStageTimerBrandingOmitsTheLocationLine keeps the branding Region aligned
// with the client renderer, which reports the Location only on Event Overview.
// Location Signage carries its own Location Region, and a Stage Timer serves a
// presenter who is standing in the room already.
func TestStageTimerBrandingOmitsTheLocationLine(t *testing.T) {
	t.Parallel()

	composition, err := displayviews.Compose(
		displayviews.StageTimer,
		false,
		displayviews.DefaultConfiguration(),
	)
	if err != nil {
		t.Fatalf("compose Stage Timer Display: %v", err)
	}
	var rendered strings.Builder
	err = DisplayPage(Snapshot{
		ProtocolVersion: "beamers.display.v1",
		AssetVersion:    "test-asset",
		ServerTime:      time.Date(2099, 8, 21, 8, 0, 0, 0, time.UTC),
		Display:         Display{Name: "Test Display"},
		EventName:       "Test Event",
		LocationName:    "Main Hall",
		ViewKey:         displayviews.StageTimer,
		Composition:     composition,
	}).Render(context.Background(), &rendered)
	if err != nil {
		t.Fatalf("render Stage Timer Display: %v", err)
	}
	if strings.Contains(rendered.String(), "Location: Main Hall") {
		t.Errorf("Stage Timer branding reported the Location: %s", rendered.String())
	}
}

func renderLocationSignage(t *testing.T, sessions []Session) string {
	t.Helper()

	composition, err := displayviews.Compose(
		displayviews.LocationSignage,
		false,
		displayviews.DefaultConfiguration(),
	)
	if err != nil {
		t.Fatalf("compose Location Signage Display: %v", err)
	}
	var rendered strings.Builder
	err = DisplayPage(Snapshot{
		ProtocolVersion: "beamers.display.v1",
		AssetVersion:    "test-asset",
		ServerTime:      time.Date(2099, 8, 21, 8, 0, 0, 0, time.UTC),
		Display:         Display{Name: "Test Display"},
		EventName:       "Test Event",
		LocationName:    "Main Hall",
		ViewKey:         displayviews.LocationSignage,
		Composition:     composition,
		Sessions:        sessions,
	}).Render(context.Background(), &rendered)
	if err != nil {
		t.Fatalf("render Location Signage Display: %v", err)
	}
	return rendered.String()
}

// TestTimelineSizesBlocksByTheirSpan pins the proportional geometry ADR 0058
// asks for. Equal-width blocks would communicate order only, which the existing
// rotation Regions already do.
func TestTimelineSizesBlocksByTheirSpan(t *testing.T) {
	t.Parallel()

	composition, err := displayviews.Compose(
		displayviews.Timeline,
		false,
		displayviews.DefaultConfiguration(),
	)
	if err != nil {
		t.Fatalf("compose Timeline Display: %v", err)
	}
	var rendered strings.Builder
	err = DisplayPage(Snapshot{
		ProtocolVersion: "beamers.display.v1",
		AssetVersion:    "test-asset",
		ServerTime:      time.Date(2099, 8, 21, 8, 0, 0, 0, time.UTC),
		Display:         Display{Name: "Test Display"},
		EventName:       "Test Event",
		LocationName:    "Main Hall",
		ViewKey:         displayviews.Timeline,
		Composition:     composition,
		Sessions: []Session{
			{
				Title:                 "Short Break",
				Lifecycle:             "Scheduled",
				ForecastStart:         time.Date(2099, 8, 21, 8, 0, 0, 0, time.UTC),
				ForecastEnd:           time.Date(2099, 8, 21, 8, 15, 0, 0, time.UTC),
				PresentedStart:        time.Date(2099, 8, 21, 8, 0, 0, 0, time.UTC),
				PresentedStartLabel:   publictime.LabelForecastStart,
				DisplayPresentedStart: "08:00",
				Timeline: TimelineGeometry{
					Day: "2099-08-21", Offset: 0, Width: 1042, Lane: 0, LaneCount: 1,
				},
			},
			{
				Title:                 "Long Keynote",
				Lifecycle:             "Scheduled",
				ForecastStart:         time.Date(2099, 8, 21, 8, 15, 0, 0, time.UTC),
				ForecastEnd:           time.Date(2099, 8, 21, 9, 45, 0, 0, time.UTC),
				PresentedStart:        time.Date(2099, 8, 21, 8, 15, 0, 0, time.UTC),
				PresentedStartLabel:   publictime.LabelForecastStart,
				DisplayPresentedStart: "08:15",
				Timeline: TimelineGeometry{
					Day: "2099-08-21", Offset: 1042, Width: 6250, Lane: 0, LaneCount: 1,
				},
			},
			{
				Unavailable:         true,
				AvailabilityMessage: "Location unavailable until Aug 21, 2099 10:15 UTC",
				ForecastStart:       time.Date(2099, 8, 21, 9, 45, 0, 0, time.UTC),
				ForecastEnd:         time.Date(2099, 8, 21, 10, 15, 0, 0, time.UTC),
				Timeline: TimelineGeometry{
					Day: "2099-08-21", Offset: 7292, Width: 2083, Lane: 0, LaneCount: 1,
				},
			},
		},
	}).Render(context.Background(), &rendered)
	if err != nil {
		t.Fatalf("render Timeline Display: %v", err)
	}
	page := rendered.String()

	// Each block carries its offset and width on the Event-day axis.
	for _, want := range []string{
		"2099-08-21",
		"--display-offset:0",
		"--display-width:1042",
		"--display-offset:1042",
		"--display-width:6250",
		"--display-offset:7292",
		"--display-width:2083",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("Timeline missing geometry %q: %s", want, page)
		}
	}

	// A suppressed span reports that it is taken and until when, never the
	// Session occupying it.
	if !strings.Contains(page, "Location unavailable until Aug 21, 2099 10:15 UTC") {
		t.Errorf("Timeline did not report the unavailable span: %s", page)
	}
	if !strings.Contains(page, "data-unavailable") {
		t.Errorf("Timeline did not mark the unavailable span: %s", page)
	}
}

func TestTimelineRendersNowLineGridlinesLaneLabelsAndLiveFill(t *testing.T) {
	t.Parallel()

	composition, err := displayviews.Compose(
		displayviews.Timeline,
		false,
		displayviews.DefaultConfiguration(),
	)
	if err != nil {
		t.Fatalf("compose Timeline Display: %v", err)
	}
	nowOffset := 2500
	var rendered strings.Builder
	err = DisplayPage(Snapshot{
		ProtocolVersion: "beamers.display.v1",
		AssetVersion:    "test-asset",
		ServerTime:      time.Date(2099, 8, 21, 8, 0, 0, 0, time.UTC),
		Display:         Display{Name: "Test Display"},
		EventName:       "Test Event",
		LocationName:    "Main Hall",
		ViewKey:         displayviews.Timeline,
		Composition:     composition,
		Sessions: []Session{
			{
				Title:                 "Opening Keynote",
				Lifecycle:             "Live",
				ForecastStart:         time.Date(2099, 8, 21, 8, 0, 0, 0, time.UTC),
				ForecastEnd:           time.Date(2099, 8, 21, 9, 0, 0, 0, time.UTC),
				PresentedStart:        time.Date(2099, 8, 21, 8, 0, 0, 0, time.UTC),
				PresentedStartLabel:   publictime.LabelActualStart,
				DisplayPresentedStart: "08:00",
				Timeline: TimelineGeometry{
					Day: "2099-08-21", Offset: 0, Width: 5000, Lane: 0, LaneCount: 2,
					NowOffset: &nowOffset,
					Gridlines: []TimelineGridline{
						{Offset: 417, Label: "07:00"},
						{Offset: 833, Label: "08:00"},
					},
				},
			},
			{
				Title:                 "Second Session",
				Lifecycle:             "Scheduled",
				ForecastStart:         time.Date(2099, 8, 21, 9, 0, 0, 0, time.UTC),
				ForecastEnd:           time.Date(2099, 8, 21, 10, 0, 0, 0, time.UTC),
				PresentedStart:        time.Date(2099, 8, 21, 9, 0, 0, 0, time.UTC),
				PresentedStartLabel:   publictime.LabelForecastStart,
				DisplayPresentedStart: "09:00",
				Timeline: TimelineGeometry{
					Day: "2099-08-21", Offset: 5000, Width: 5000, Lane: 1, LaneCount: 2,
					NowOffset: &nowOffset,
					Gridlines: []TimelineGridline{
						{Offset: 417, Label: "07:00"},
						{Offset: 833, Label: "08:00"},
					},
				},
			},
		},
	}).Render(context.Background(), &rendered)
	if err != nil {
		t.Fatalf("render Timeline Display: %v", err)
	}
	page := rendered.String()

	// The now-line is absolutely positioned via the projected offset, once
	// per Event day, not once per Session.
	if count := strings.Count(page, `class="display-timeline-now"`); count != 1 {
		t.Errorf("now-line count = %d, want 1: %s", count, page)
	}
	if !strings.Contains(page, "--display-offset:2500") {
		t.Errorf("now-line did not carry its projected offset: %s", page)
	}

	// Hour gridlines are faint and labeled, and appear once per Event day.
	for _, want := range []string{
		`class="display-timeline-gridline"`,
		"<span>07:00</span>",
		"<span>08:00</span>",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("Timeline missing gridline markup %q: %s", want, page)
		}
	}
	if count := strings.Count(page, `class="display-timeline-gridline"`); count != 2 {
		t.Errorf("gridline count = %d, want 2: %s", count, page)
	}

	// Small Lane labels, one per visual overlap lane.
	if !strings.Contains(page, `class="display-timeline-lane-label"`) {
		t.Errorf("Timeline missing Lane labels: %s", page)
	}
	if !strings.Contains(page, "Lane 1") || !strings.Contains(page, "Lane 2") {
		t.Errorf("Timeline Lane labels did not name both lanes: %s", page)
	}

	// The Live block carries its lifecycle so CSS can fill it in the
	// Theme's signal color, distinct from a Scheduled block.
	if !strings.Contains(page, `data-lifecycle="Live"`) {
		t.Errorf("Live Timeline block missing data-lifecycle: %s", page)
	}
}

func TestTimelineProjectionUsesEventDaysAndOverlapLanes(t *testing.T) {
	t.Parallel()

	zone, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("load timezone: %v", err)
	}
	sessions := []Session{
		{
			ForecastStart: time.Date(2099, 8, 21, 8, 0, 0, 0, time.UTC),
			ForecastEnd:   time.Date(2099, 8, 21, 9, 0, 0, 0, time.UTC),
		},
		{
			ForecastStart: time.Date(2099, 8, 21, 8, 30, 0, 0, time.UTC),
			ForecastEnd:   time.Date(2099, 8, 21, 9, 30, 0, 0, time.UTC),
		},
		{
			ForecastStart: time.Date(2099, 8, 22, 4, 0, 0, 0, time.UTC),
			ForecastEnd:   time.Date(2099, 8, 22, 5, 0, 0, 0, time.UTC),
		},
	}
	farPast := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := projectTimeline(sessions, farPast, zone, "06:00"); err != nil {
		t.Fatalf("project Timeline: %v", err)
	}

	if sessions[0].Timeline.Day != "2099-08-21" ||
		sessions[0].Timeline.Offset != 1667 ||
		sessions[0].Timeline.Width != 417 {
		t.Errorf("first Session geometry = %+v", sessions[0])
	}
	if sessions[0].Timeline.Lane != 0 || sessions[1].Timeline.Lane != 1 ||
		sessions[0].Timeline.LaneCount != 2 || sessions[1].Timeline.LaneCount != 2 {
		t.Errorf("overlap lanes = %+v, %+v", sessions[0], sessions[1])
	}
	if sessions[2].Timeline.Day != "2099-08-22" || sessions[2].Timeline.Offset != 0 {
		t.Errorf("boundary Session geometry = %+v", sessions[2])
	}
}

func TestTimelineProjectionComputesNowLineAndGridlines(t *testing.T) {
	t.Parallel()

	zone, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("load timezone: %v", err)
	}
	sessions := []Session{
		{
			ForecastStart: time.Date(2099, 8, 21, 8, 0, 0, 0, time.UTC),
			ForecastEnd:   time.Date(2099, 8, 21, 9, 0, 0, 0, time.UTC),
		},
		{
			ForecastStart: time.Date(2099, 8, 22, 4, 0, 0, 0, time.UTC),
			ForecastEnd:   time.Date(2099, 8, 22, 5, 0, 0, 0, time.UTC),
		},
	}
	// 10:00 Europe/Berlin (08:00 UTC) on 2099-08-21, the same instant as the
	// first Session's ForecastStart, so NowOffset must equal its Offset.
	now := time.Date(2099, 8, 21, 8, 0, 0, 0, time.UTC)
	if err := projectTimeline(sessions, now, zone, "06:00"); err != nil {
		t.Fatalf("project Timeline: %v", err)
	}

	if sessions[0].Timeline.NowOffset == nil || *sessions[0].Timeline.NowOffset != 1667 {
		t.Errorf("first Event day NowOffset = %v, want 1667", sessions[0].Timeline.NowOffset)
	}
	if sessions[1].Timeline.NowOffset != nil {
		t.Errorf("second Event day NowOffset = %v, want nil (now falls outside it)", *sessions[1].Timeline.NowOffset)
	}
	// DayStart and DayEnd let a Display client re-derive the now-line from
	// its own synchronized clock between snapshots; Europe/Berlin sits two
	// hours ahead of UTC in August, so the 06:00 local boundary is 04:00 UTC.
	wantDayStart := time.Date(2099, 8, 21, 4, 0, 0, 0, time.UTC)
	wantDayEnd := time.Date(2099, 8, 22, 4, 0, 0, 0, time.UTC)
	if !sessions[0].Timeline.DayStart.Equal(wantDayStart) || !sessions[0].Timeline.DayEnd.Equal(wantDayEnd) {
		t.Errorf(
			"first Event day bounds = [%v, %v), want [%v, %v)",
			sessions[0].Timeline.DayStart, sessions[0].Timeline.DayEnd, wantDayStart, wantDayEnd,
		)
	}

	// The first Event day spans 06:00 to 06:00 local, so its hour gridlines
	// start at 07:00 and run hourly to 05:00 the next local day.
	gridlines := sessions[0].Timeline.Gridlines
	if len(gridlines) != 23 {
		t.Fatalf("gridline count = %d, want 23: %+v", len(gridlines), gridlines)
	}
	if gridlines[0].Label != "07:00" || gridlines[0].Offset != 417 {
		t.Errorf("first gridline = %+v, want {417 07:00}", gridlines[0])
	}
	if gridlines[len(gridlines)-1].Label != "05:00" {
		t.Errorf("last gridline label = %q, want 05:00", gridlines[len(gridlines)-1].Label)
	}
	// The second Event day gets its own gridlines projected the same way,
	// even with a single Session and no NowOffset.
	if len(sessions[1].Timeline.Gridlines) != 23 {
		t.Errorf(
			"second Event day gridline count = %d, want 23: %+v",
			len(sessions[1].Timeline.Gridlines), sessions[1].Timeline.Gridlines,
		)
	}
}

func TestTimelineProjectionUsesCanonicalDSTBoundary(t *testing.T) {
	t.Parallel()

	zone, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("load timezone: %v", err)
	}
	sessions := []Session{
		{
			ForecastStart: time.Date(2026, 3, 29, 1, 30, 0, 0, time.UTC),
			ForecastEnd:   time.Date(2026, 3, 29, 2, 30, 0, 0, time.UTC),
		},
	}
	farPast := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := projectTimeline(sessions, farPast, zone, "02:30"); err != nil {
		t.Fatalf("project Timeline: %v", err)
	}
	if sessions[0].Timeline.Day != "2026-03-29" || sessions[0].Timeline.Offset != 213 {
		t.Errorf("DST-gap Timeline geometry = %+v", sessions[0])
	}
}

func TestDisplayClockStartsInEventTimezone(t *testing.T) {
	t.Parallel()

	composition, err := displayviews.Compose(
		displayviews.EventOverview,
		false,
		displayviews.DefaultConfiguration(),
	)
	if err != nil {
		t.Fatalf("compose Display: %v", err)
	}
	var rendered strings.Builder
	err = DisplayPage(Snapshot{
		ProtocolVersion: "beamers.display.v1",
		AssetVersion:    "test-asset",
		ServerTime:      time.Date(2099, 8, 21, 8, 0, 0, 0, time.UTC),
		EventTimezone:   "Europe/Berlin",
		Composition:     composition,
	}).Render(context.Background(), &rendered)
	if err != nil {
		t.Fatalf("render Display: %v", err)
	}
	if !strings.Contains(rendered.String(), `data-display-clock datetime="2099-08-21T08:00:00Z">10:00</time>`) {
		t.Errorf("Display clock did not start in Event timezone: %s", rendered.String())
	}
}

// TestClientAcceptsEveryBuiltInLayoutKey guards a cross-language duplication.
// client.js validates the Layout key against its own allowlist, so a View added
// to internal/displayviews but not to that list composes on the server and then
// throws in the client, leaving the Display stuck on its last frame. The list
// cannot be shared across languages, so it is pinned instead.
func TestClientAcceptsEveryBuiltInLayoutKey(t *testing.T) {
	t.Parallel()

	source := string(ClientJavaScript)
	start := strings.Index(source, "display-layout-${controlledToken(")
	if start < 0 {
		t.Fatal("client.js no longer validates the Layout key against an allowlist")
	}
	end := strings.Index(source[start:], "])}")
	if end < 0 {
		t.Fatal("client.js Layout key allowlist is unterminated")
	}
	allowlist := source[start : start+end]

	// Standby is a Layout without being an assignable View.
	for _, key := range append(displayviews.Normal(), "standby") {
		if !strings.Contains(allowlist, `"`+key+`"`) {
			t.Errorf("client.js rejects built-in Layout %q: %s", key, allowlist)
		}
	}
}
