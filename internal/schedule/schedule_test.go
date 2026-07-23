package schedule

import (
	"errors"
	"testing"
	"time"

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

func TestFormatEventTimeUsesEventLocale(t *testing.T) {
	zone, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("load timezone: %v", err)
	}
	value := time.Date(2026, 8, 22, 13, 5, 0, 0, zone)

	if got := formatEventTime(value, "en-US"); got != "Aug 22, 2026 1:05 PM CEST" {
		t.Errorf("en-US Event time = %q", got)
	}
	if got := formatEventTime(value, "de-DE"); got != "22 Aug 2026 13:05 CEST" {
		t.Errorf("de-DE Event time = %q", got)
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
		{ID: 2, ForecastStart: time.Date(2026, 10, 25, 1, 15, 0, 0, time.UTC)},
		{ID: 1, ForecastStart: time.Date(2026, 10, 25, 0, 45, 0, 0, time.UTC)},
	}
	sortScheduleSessions(sessions)
	if sessions[0].ID != 1 || sessions[1].ID != 2 {
		t.Errorf("fallback Session order = %d, %d", sessions[0].ID, sessions[1].ID)
	}
}

func TestFilterScheduleSessionsMatchesEverySelectedDimension(t *testing.T) {
	sessions := []store.PublicScheduleSession{
		{
			ID: 1, ForecastStart: time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC),
			LocationIDs: []int{1}, LaneIDs: []int{2}, TrackIDs: []int{3},
		},
		{
			ID: 2, ForecastStart: time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC),
			LocationIDs: []int{1}, LaneIDs: []int{4}, TrackIDs: []int{3},
		},
	}
	filtered := filterScheduleSessions(sessions, Filter{
		Day: "2026-08-21", LocationID: 1, LaneID: 2, TrackID: 3,
	}, func(item store.PublicScheduleSession) string {
		return item.ForecastStart.Format(time.DateOnly)
	})
	if len(filtered) != 1 || filtered[0].ID != 1 {
		t.Fatalf("filtered Schedule Sessions = %+v", filtered)
	}
}

func TestPublicActualTimeUsesCommunicatedTimeWithinTolerance(t *testing.T) {
	communicated := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	actual := communicated.Add(90 * time.Second)

	if got := publicActualTime(actual, communicated, 30*time.Minute); !got.Equal(communicated) {
		t.Errorf("normalized public Actual Time = %s; want %s", got, communicated)
	}
	if got := publicActualTime(actual, communicated, 10*time.Minute); !got.Equal(actual) {
		t.Errorf("short Session public Actual Time = %s; want exact %s", got, actual)
	}
	if got := publicActualTime(actual.Add(time.Minute), communicated, 30*time.Minute); !got.Equal(actual.Add(time.Minute)) {
		t.Errorf("late public Actual Time = %s; want exact %s", got, actual.Add(time.Minute))
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
