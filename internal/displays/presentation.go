package displays

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/dotwaffle/beamers/internal/displayviews"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/publictime"
	"github.com/dotwaffle/beamers/internal/stagetimer"
	"github.com/dotwaffle/beamers/internal/themevalue"
)

func displayPageClass(snapshot Snapshot) string {
	theme := snapshot.Composition.Theme
	return strings.Join([]string{
		"display-view",
		"display-layout-" + snapshot.Composition.Layout.Key,
		"display-font-" + theme.Font,
		"display-background-" + theme.Background,
		"display-transition-" + theme.Transition,
	}, " ")
}

func displayBranding(snapshot Snapshot) string {
	prefix := ""
	if snapshot.Composition.Theme.BrandAsset == themevalue.BrandAssetSignal {
		prefix = "◆ "
	}
	if snapshot.Composition.Theme.Branding != "" {
		return prefix + snapshot.Composition.Theme.Branding
	}
	if snapshot.EventName != "" {
		return prefix + snapshot.EventName
	}
	return prefix + "Beamers"
}

func displayPageTitle(snapshot Snapshot) string {
	switch {
	case snapshot.Standby:
		return snapshot.Display.Name + " · Standby"
	case snapshot.ViewKey == "location-signage":
		return snapshot.LocationName + " · Location Signage"
	case snapshot.ViewKey == "event-overview":
		return snapshot.Display.Name + " · Event Overview"
	default:
		return snapshot.Display.Name + " · " + snapshot.EventName
	}
}

func displayPersistent(persistent bool) string {
	return strconv.FormatBool(persistent)
}

func stageMessageLabel(emphasis string) string {
	switch emphasis {
	case "Attention":
		return "Attention:"
	case "Urgent":
		return "Urgent:"
	default:
		return "Stage message:"
	}
}

// displayRegionAttributes marks a Region with the Stage Timer's emphasis.
//
// internal/displays/client.js sets these on the Region, but the entry document
// used to set them on a wrapper inside it. The emphasis rules match either
// element, so the outline framed the timer block on first paint and the whole
// Region after a reconnect. The wrapper is gone and both renderers now mark the
// Region.
func displayRegionAttributes(snapshot Snapshot, widget string) templ.Attributes {
	if widget != displayviews.WidgetStageTimer {
		return nil
	}
	timer, ok := displayStageTimer(snapshot)
	if !ok {
		return nil
	}
	return templ.Attributes{
		"data-timer-emphasis": timer.Emphasis,
		"data-timer-overtime": displayPersistent(timer.Overtime),
	}
}

// projectTimeline positions Sessions on their Event-day axes. Geometry is
// projected once so the entry document and browser renderer cannot disagree.
func projectTimeline(sessions []Session, zone *time.Location, boundary string) error {
	if zone == nil {
		zone = time.UTC
	}
	type dayProjection struct {
		laneEnds []time.Time
		indices  []int
	}
	days := make(map[string]*dayProjection)
	for index := range sessions {
		session := &sessions[index]
		if session.ForecastStart.IsZero() || session.ForecastEnd.IsZero() {
			return errors.New("timeline Session span is required")
		}
		localStart := session.ForecastStart.In(zone)
		dayDate := localStart
		dayStart, err := events.ResolveDayBoundary(dayDate, zone, boundary)
		if err != nil {
			return err
		}
		if localStart.Before(dayStart) {
			dayDate = dayDate.AddDate(0, 0, -1)
			dayStart, err = events.ResolveDayBoundary(dayDate, zone, boundary)
			if err != nil {
				return err
			}
		}
		dayEnd, err := events.ResolveDayBoundary(dayDate.AddDate(0, 0, 1), zone, boundary)
		if err != nil {
			return err
		}
		spanStart := maxTime(session.ForecastStart, dayStart)
		spanEnd := minTime(session.ForecastEnd, dayEnd)
		if !spanEnd.After(spanStart) {
			spanEnd = spanStart.Add(time.Minute)
		}
		dayDuration := dayEnd.Sub(dayStart)
		session.Timeline.Day = dayStart.Format(time.DateOnly)
		session.Timeline.Offset = timelineBasisPoints(spanStart.Sub(dayStart), dayDuration, 0)
		session.Timeline.Width = timelineBasisPoints(spanEnd.Sub(spanStart), dayDuration, 1)
		if session.Timeline.Offset+session.Timeline.Width > 10000 {
			session.Timeline.Width = 10000 - session.Timeline.Offset
		}
		day := days[session.Timeline.Day]
		if day == nil {
			day = &dayProjection{}
			days[session.Timeline.Day] = day
		}
		session.Timeline.Lane = len(day.laneEnds)
		for lane, laneEnd := range day.laneEnds {
			if !spanStart.Before(laneEnd) {
				session.Timeline.Lane = lane
				day.laneEnds[lane] = spanEnd
				break
			}
		}
		if session.Timeline.Lane == len(day.laneEnds) {
			day.laneEnds = append(day.laneEnds, spanEnd)
		}
		day.indices = append(day.indices, index)
	}
	for _, day := range days {
		for _, index := range day.indices {
			sessions[index].Timeline.LaneCount = len(day.laneEnds)
		}
	}
	return nil
}

func timelineBasisPoints(duration, dayDuration time.Duration, minimum int) int {
	if dayDuration <= 0 {
		return minimum
	}
	result := int(math.Round(float64(duration) / float64(dayDuration) * 10000))
	return max(minimum, min(result, 10000))
}

func minTime(first, second time.Time) time.Time {
	if first.Before(second) {
		return first
	}
	return second
}

func maxTime(first, second time.Time) time.Time {
	if first.After(second) {
		return first
	}
	return second
}

// displayTimelineStyle carries only bounded projected integers, never Event
// content, into CSS custom properties.
func displayTimelineStyle(session Session) templ.SafeCSS {
	return templ.SafeCSS(strings.Join([]string{
		"--display-offset:" + strconv.Itoa(session.Timeline.Offset),
		"--display-width:" + strconv.Itoa(session.Timeline.Width),
		"--display-lane:" + strconv.Itoa(session.Timeline.Lane),
		"--display-lanes:" + strconv.Itoa(max(1, session.Timeline.LaneCount)),
	}, ";"))
}

type displayTimelineDay struct {
	Label     string
	LaneCount int
	Sessions  []Session
}

func displayTimelineDays(sessions []Session) []displayTimelineDay {
	var days []displayTimelineDay
	for _, session := range sessions {
		if len(days) == 0 || days[len(days)-1].Label != session.Timeline.Day {
			days = append(days, displayTimelineDay{Label: session.Timeline.Day})
		}
		days[len(days)-1].LaneCount = max(
			days[len(days)-1].LaneCount,
			session.Timeline.LaneCount,
		)
		days[len(days)-1].Sessions = append(days[len(days)-1].Sessions, session)
	}
	return days
}

func displayTimelineDayStyle(day displayTimelineDay) templ.SafeCSS {
	return templ.SafeCSS("--display-lanes:" + strconv.Itoa(max(1, day.LaneCount)))
}

func displayClockTime(snapshot Snapshot) string {
	zone := time.UTC
	if snapshot.EventTimezone != "" {
		if found, err := time.LoadLocation(snapshot.EventTimezone); err == nil {
			zone = found
		}
	}
	return snapshot.ServerTime.In(zone).Format("15:04")
}

// displayProgressMeasurable reports whether a span can carry an elapsed bar. An
// unmeasurable span renders no bar at all rather than an empty one, which would
// claim the Session had not started.
func displayProgressMeasurable(start, end time.Time) bool {
	return !start.IsZero() && !end.IsZero() && end.After(start)
}

// displayProgressFraction is how far now sits between two instants, clamped.
func displayProgressFraction(start, end, now time.Time) float64 {
	if !displayProgressMeasurable(start, end) {
		return 0
	}
	elapsed := now.Sub(start).Seconds() / end.Sub(start).Seconds()
	return math.Min(1, math.Max(0, elapsed))
}

func displayProgressStyle(start, end, now time.Time) templ.SafeCSS {
	return templ.SafeCSS(
		"--display-progress:" +
			strconv.FormatFloat(displayProgressFraction(start, end, now), 'f', 4, 64),
	)
}

func displayProgressPercent(start, end, now time.Time) string {
	return strconv.Itoa(int(math.Round(displayProgressFraction(start, end, now) * 100)))
}

// displaySessionKicker labels a Now / Next card the same way client.js does:
// NOW only claims a Session that is actually Live, otherwise the card names
// when the Session is expected instead of implying it is already running.
func displaySessionKicker(session Session) string {
	if session.Lifecycle == "Live" {
		return "NOW"
	}
	return "UP NEXT · " + session.DisplayPresentedStart
}

// displayLifecycleBadge reports whether a Session earns a lifecycle badge.
// Scheduled and Ended are the unmarked default state and would only add noise.
func displayLifecycleBadge(session Session) bool {
	return session.Lifecycle == "Live" || session.Lifecycle == "Canceled"
}

// displayNowNextSlot ranks the two Sessions a Now / Next Region shows. The rank
// drives presentation only: read from across a room, two equally weighted
// Sessions leave the viewer working out which one is actually running.
func displayNowNextSlot(index int) string {
	if index == 0 {
		return "now"
	}
	return "next"
}

func displayNowNext(sessions []Session, now time.Time) []Session {
	result := make([]Session, 0, len(sessions))
	for _, session := range sessions {
		if session.Lifecycle == "Canceled" || session.Lifecycle == "Ended" {
			continue
		}
		if session.Lifecycle != "Live" &&
			!session.ForecastEnd.IsZero() &&
			!session.ForecastEnd.After(now) {
			continue
		}
		result = append(result, session)
	}
	return result
}

type stageTimerPresentation struct {
	Title                     string
	Direction                 string
	Text                      string
	Emphasis                  string
	EmphasisLabel             string
	Anchor                    time.Time
	ForecastEnd               time.Time
	DisplayForecastEnd        string
	AdjustmentNotice          string
	AdjustmentNoticeExpiresAt time.Time
	Overtime                  bool
	// SpanStart and SpanEnd carry the span projectStageTimerSpan bounded, so
	// the elapsed bar can report progress.
	SpanStart time.Time
	SpanEnd   time.Time
}

func displayStageTimer(snapshot Snapshot) (stageTimerPresentation, bool) {
	if snapshot.StageTimer == nil {
		return stageTimerPresentation{}, false
	}
	frame := stagetimer.FrameAt(stagetimer.Timer{
		SessionID:  snapshot.StageTimer.SessionID,
		Mode:       snapshot.StageTimer.Mode,
		Anchor:     snapshot.StageTimer.Anchor,
		Thresholds: snapshot.StageTimer.Thresholds,
	}, snapshot.ServerTime)
	direction := "Remaining"
	if snapshot.StageTimer.Mode == stagetimer.Elapsed {
		direction = "Elapsed"
	} else if frame.Overtime {
		direction = "Overtime"
	}
	emphasis := string(frame.Emphasis)
	label := ""
	if emphasis != string(stagetimer.Normal) {
		label = strings.ToUpper(emphasis[:1]) + emphasis[1:]
	}
	var forecastEnd time.Time
	var displayForecastEnd string
	if snapshot.StageTimer.Mode == stagetimer.Elapsed && !snapshot.StageTimer.ForecastEnd.IsZero() {
		forecastEnd = snapshot.StageTimer.ForecastEnd
		zone := time.UTC
		if snapshot.EventTimezone != "" {
			if found, err := time.LoadLocation(snapshot.EventTimezone); err == nil {
				zone = found
			}
		}
		displayForecastEnd = forecastEnd.In(zone).Format(publictime.TimeLayout)
	}
	return stageTimerPresentation{
		SpanStart: snapshot.StageTimer.SpanStart, SpanEnd: snapshot.StageTimer.SpanEnd,
		Title: snapshot.StageTimer.Title, Direction: direction,
		Text: frame.Text, Emphasis: emphasis, EmphasisLabel: label,
		Anchor: snapshot.StageTimer.Anchor, ForecastEnd: forecastEnd,
		DisplayForecastEnd:        displayForecastEnd,
		AdjustmentNotice:          timerAdjustmentNotice(snapshot.StageTimer.AdjustmentSeconds),
		AdjustmentNoticeExpiresAt: snapshot.StageTimer.AdjustmentNoticeExpiresAt,
		Overtime:                  frame.Overtime,
	}, true
}

func timerAdjustmentNotice(seconds int) string {
	if seconds == 0 {
		return ""
	}
	sign := "+"
	if seconds < 0 {
		sign = "-"
		seconds = -seconds
	}
	return fmt.Sprintf("Time adjusted: %s%d:%02d", sign, seconds/60, seconds%60)
}

func displayThemeStyle(snapshot Snapshot) templ.SafeCSS {
	theme := snapshot.Composition.Theme
	alpha := theme.ScrimOpacity * 255 / 100
	// Every interpolated value passed displayviews validation before the
	// Snapshot was created. Keeping the complete declaration here prevents
	// Event content from becoming CSS syntax.
	return templ.SafeCSS(fmt.Sprintf(
		"--display-foreground:%s;--display-background:%s;--display-surface:%s;"+
			"--display-signal:%s;--display-live:%s;--display-danger:%s;"+
			"--display-scrim-layer:%s%02x",
		theme.ForegroundColor,
		theme.BackgroundColor,
		theme.SurfaceColor,
		theme.SignalColor,
		theme.LiveColor,
		theme.DangerColor,
		theme.ScrimColor,
		alpha,
	))
}

func eventDisplayTheme(config themevalue.Config, branding string) displayviews.Theme {
	background := displayviews.BackgroundSolid
	if config.Background == themevalue.BackgroundNebula {
		background = displayviews.BackgroundNebula
	}
	font := displayviews.FontSans
	if config.Typeface == themevalue.TypefaceDemoscene {
		font = displayviews.FontDemoscene
	}
	transition := config.Transition
	if config.Motion == themevalue.MotionStill {
		transition = displayviews.TransitionNone
	}
	return displayviews.Theme{
		Branding: branding, BrandAsset: config.BrandAsset,
		ForegroundColor: config.TextColor, BackgroundColor: config.BackgroundColor,
		SurfaceColor: config.SurfaceColor, Background: background,
		SignalColor: config.AccentColor,
		LiveColor:   config.LiveColor, DangerColor: config.DangerColor,
		ScrimColor: "#000000", ScrimOpacity: 85,
		Font: font, Transition: transition,
	}
}

func displayThemeVariant(viewKey string, standby bool) string {
	if standby {
		return themevalue.VariantStandby
	}
	switch viewKey {
	case displayviews.CompetitionOutput,
		displayviews.EventOverview,
		displayviews.LocationSignage,
		displayviews.Timeline,
		displayviews.CrewOverview:
		return viewKey
	default:
		return ""
	}
}
