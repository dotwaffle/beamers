package schedule

import (
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
