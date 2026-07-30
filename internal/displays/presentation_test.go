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

	theme := eventDisplayTheme(config, "Revision")
	if theme.Branding != "Revision" ||
		theme.BrandAsset != themevalue.BrandAssetSignal ||
		theme.ForegroundColor != "#ffffff" ||
		theme.BackgroundColor != "#112233" ||
		theme.AccentColor != "#223344" ||
		theme.SignalColor != "#ffdf6e" ||
		theme.Background != displayviews.BackgroundNebula ||
		theme.Font != displayviews.FontDemoscene ||
		theme.Transition != displayviews.TransitionNone {
		t.Fatalf("Display Event Theme = %+v", theme)
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

func TestDisplayNowNextExcludesCanceledSessions(t *testing.T) {
	t.Parallel()

	sessions := []Session{
		{Title: "Canceled", Lifecycle: "Canceled"},
		{Title: "Current", Lifecycle: "Live"},
		{Title: "Next", Lifecycle: "Scheduled"},
	}
	got := displayNowNext(sessions)
	if len(got) != 2 || got[0].Title != "Current" || got[1].Title != "Next" {
		t.Errorf("Now/Next Sessions = %+v, want Current and Next", got)
	}
	if len(sessions) != 3 {
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
