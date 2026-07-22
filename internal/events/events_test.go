package events

import (
	"errors"
	"testing"
	"time"
)

func TestEventDayBoundaryDefaultsToMidnight(t *testing.T) {
	input := CreateInput{
		Name: "Revision 2026", PlannedStartDate: "2026-08-21", PlannedEndDate: "2026-08-23",
		Timezone: "Europe/Berlin", EventLocale: "de-DE",
	}
	validated, err := validateCreateInput(input)
	if err != nil {
		t.Fatalf("validate Event defaults: %v", err)
	}
	if validated.EventDayBoundary != "00:00" {
		t.Errorf("default Event Day Boundary = %q, want 00:00", validated.EventDayBoundary)
	}
}

func TestEventTimezoneRejectsHostLocalConfiguration(t *testing.T) {
	input := CreateInput{
		Name: "Revision 2026", PlannedStartDate: "2026-08-21", PlannedEndDate: "2026-08-23",
		Timezone: "Local", EventLocale: "de-DE",
	}
	_, err := validateCreateInput(input)
	var validation *ValidationError
	ok := errors.As(err, &validation)
	if !ok || validation.Field != "timezone" {
		t.Fatalf("Local timezone error = %v, want actionable timezone validation", err)
	}
}

func TestResolveDayBoundaryUsesFirstValidTimeAfterGap(t *testing.T) {
	location, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("load test timezone: %v", err)
	}
	resolved, err := ResolveDayBoundary(
		time.Date(2026, time.March, 29, 0, 0, 0, 0, time.UTC), location, "02:30",
	)
	if err != nil {
		t.Fatalf("resolve gap boundary: %v", err)
	}
	if got := resolved.In(location).Format(time.RFC3339); got != "2026-03-29T03:00:00+02:00" {
		t.Errorf("gap boundary = %s, want 2026-03-29T03:00:00+02:00", got)
	}
}

func TestResolveDayBoundaryUsesLaterRepeatedOccurrence(t *testing.T) {
	location, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("load test timezone: %v", err)
	}
	resolved, err := ResolveDayBoundary(
		time.Date(2026, time.October, 25, 0, 0, 0, 0, time.UTC), location, "02:30",
	)
	if err != nil {
		t.Fatalf("resolve repeated boundary: %v", err)
	}
	if got := resolved.In(location).Format(time.RFC3339); got != "2026-10-25T02:30:00+01:00" {
		t.Errorf("repeated boundary = %s, want 2026-10-25T02:30:00+01:00", got)
	}
}
