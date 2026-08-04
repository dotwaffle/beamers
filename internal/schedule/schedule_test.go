package schedule

import (
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/publictime"
	"github.com/dotwaffle/beamers/internal/store"
)

func TestGroupedEventDayUsesEventDayBoundary(t *testing.T) {
	zone, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("load timezone: %v", err)
	}

	tests := []struct {
		name    string
		start   time.Time
		wantDay string
	}{
		{
			name:    "before boundary",
			start:   time.Date(2026, 8, 22, 1, 0, 0, 0, zone),
			wantDay: "2026-08-21",
		},
		{
			name:    "after boundary",
			start:   time.Date(2026, 8, 22, 8, 0, 0, 0, zone),
			wantDay: "2026-08-22",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			day, dayErr := groupedEventDay(test.start, zone, "06:00")
			if dayErr != nil {
				t.Fatalf("group Event day: %v", dayErr)
			}
			if day != test.wantDay {
				t.Errorf("group Event day = %q; want %q", day, test.wantDay)
			}
		})
	}
}

func TestGroupScheduleDaysMarksOnlyCalendarDateTransition(t *testing.T) {
	sessions := []Session{
		{Title: "Before midnight", EventDay: "2026-08-21", LocalDate: "2026-08-21"},
		{Title: "After midnight", EventDay: "2026-08-21", LocalDate: "2026-08-22"},
		{Title: "Still after midnight", EventDay: "2026-08-21", LocalDate: "2026-08-22"},
	}
	days := groupScheduleDays(sessions)
	if len(days) != 1 || len(days[0].Sessions) != 3 {
		t.Fatalf("Schedule days = %+v", days)
	}
	if days[0].Sessions[0].CalendarDateRollover || !days[0].Sessions[1].CalendarDateRollover ||
		days[0].Sessions[2].CalendarDateRollover {
		t.Errorf("calendar date rollover markers = %+v", days[0].Sessions)
	}
}

func TestGroupScheduleDaysMarksFirstVisibleRollover(t *testing.T) {
	sessions := []Session{
		{Title: "After midnight", EventDay: "2026-08-21", LocalDate: "2026-08-22"},
		{Title: "Still after midnight", EventDay: "2026-08-21", LocalDate: "2026-08-22"},
	}
	days := groupScheduleDays(sessions)
	if len(days) != 1 || len(days[0].Sessions) != 2 ||
		!days[0].Sessions[0].CalendarDateRollover || days[0].Sessions[1].CalendarDateRollover {
		t.Errorf("first visible rollover markers = %+v", days)
	}
}

func TestProjectedTimePointIncludesCompactClock(t *testing.T) {
	displayZone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load display timezone: %v", err)
	}
	eventZone, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("load Event timezone: %v", err)
	}
	value := time.Date(2026, 8, 22, 13, 5, 0, 0, eventZone)

	// The Schedule card time column needs a bare "HH:MM" per zone rather than
	// the full "2026-08-22 07:05 EDT" sentence used elsewhere on the page, so
	// dual-timezone presentation stays compact instead of stacking two
	// sentence-length strings per Session.
	point := projectedTimePoint("Start", value, displayZone, eventZone)
	if point.Clock != "07:05" {
		t.Errorf("display-zone Clock = %q, want %q", point.Clock, "07:05")
	}
	if point.EventClock != "13:05" {
		t.Errorf("Event-zone EventClock = %q, want %q", point.EventClock, "13:05")
	}
}

func TestTimeKindDistinguishesActualFromForecast(t *testing.T) {
	// A Live Session presents a normalized Actual Start beside a Forecast
	// End (publictime.Live). The Schedule card's compact time column must
	// keep that distinction visible instead of erasing it for brevity.
	tests := []struct {
		label string
		want  string
	}{
		{"Forecast Start", "Forecast"},
		{"Forecast End", "Forecast"},
		{"Actual Start", "Actual"},
		{"Actual End", "Actual"},
		{"Last Forecast Start", "Forecast"},
		{"Last Forecast End", "Forecast"},
	}
	for _, test := range tests {
		if got := timeKind(test.label); got != test.want {
			t.Errorf("timeKind(%q) = %q, want %q", test.label, got, test.want)
		}
	}
}

func TestFormatEventTimeIsLocaleIndependent(t *testing.T) {
	zone, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("load timezone: %v", err)
	}
	value := time.Date(2026, 8, 22, 13, 5, 0, 0, zone)

	// The format no longer follows the locale. A 12-hour clock and a month-first
	// date are exactly what an attendee reading a Schedule for another country
	// misreads, so every Event renders the same unambiguous instant.
	const want = "2026-08-22 13:05 CEST"
	if got := formatEventTime(value); got != want {
		t.Errorf("Event time = %q, want %q", got, want)
	}
}

func TestScheduleLanguageUsesContentLanguageThenEventLocale(t *testing.T) {
	if got := scheduleLanguage("fr", "de-DE"); got != "fr" {
		t.Errorf("content language override = %q", got)
	}
	if got := scheduleLanguage("", "de-DE"); got != "de-DE" {
		t.Errorf("Event Locale language fallback = %q", got)
	}
}

func TestSortScheduleSessionsUsesAbsoluteFallbackOrder(t *testing.T) {
	sessions := []store.PublicScheduleSession{
		{ID: 2, PublicTime: publictime.Facts{Forecast: publictime.Range{
			Start: time.Date(2026, 10, 25, 1, 15, 0, 0, time.UTC),
		}}},
		{ID: 1, PublicTime: publictime.Facts{Forecast: publictime.Range{
			Start: time.Date(2026, 10, 25, 0, 45, 0, 0, time.UTC),
		}}},
	}
	sortScheduleSessions(sessions)
	if sessions[0].ID != 1 || sessions[1].ID != 2 {
		t.Errorf("fallback Session order = %d, %d", sessions[0].ID, sessions[1].ID)
	}
}

func TestFilterScheduleSessionsMatchesEverySelectedDimension(t *testing.T) {
	sessions := []store.PublicScheduleSession{
		{
			ID: 1, PublicTime: publictime.Facts{Forecast: publictime.Range{
				Start: time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC),
			}},
			LocationIDs: []int{1}, LaneIDs: []int{2}, TrackIDs: []int{3},
		},
		{
			ID: 2, PublicTime: publictime.Facts{Forecast: publictime.Range{
				Start: time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC),
			}},
			LocationIDs: []int{1}, LaneIDs: []int{4}, TrackIDs: []int{3},
		},
	}
	filtered := filterScheduleSessions(sessions, Filter{
		Day: "2026-08-21", LocationID: 1, LaneID: 2, TrackID: 3,
	}, func(item store.PublicScheduleSession) string {
		return item.PublicTime.Forecast.Start.Format(time.DateOnly)
	})
	if len(filtered) != 1 || filtered[0].ID != 1 {
		t.Fatalf("filtered Schedule Sessions = %+v", filtered)
	}
}

func TestValidateFilterRejectsMalformedValuesBehindServiceSeam(t *testing.T) {
	for _, filter := range []Filter{
		{LocationID: -1},
		{LaneID: -1},
		{TrackID: -1},
		{Day: "not-a-date"},
		{ViewerTimezone: "not/a/timezone"},
	} {
		if _, err := validateFilter(filter); !errors.Is(err, ErrInvalidFilter) {
			t.Errorf("validate Filter %+v = %v; want ErrInvalidFilter", filter, err)
		}
	}
}

func TestSnapshotPathsPreserveFiltersAndStreamCursor(t *testing.T) {
	snapshot := Snapshot{
		EventSlug:      "revision",
		ViewerTimezone: "America/New_York",
		Filter: Filter{
			Day: "2026-08-21", LocationID: 1, LaneID: 2, TrackID: 3,
		},
		StreamID: "stream one", StreamPosition: 7,
	}
	if got := snapshot.SchedulePath(); got !=
		"/events/revision/schedule?day=2026-08-21&lane=2&location=1&time_zone=America%2FNew_York&track=3" {
		t.Errorf("Schedule path = %q", got)
	}
	if got := snapshot.EventsPath(); got != "/schedule/events?after=7&stream_id=stream+one" {
		t.Errorf("Schedule Events path = %q", got)
	}
}
