package events

import (
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/command"
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
	if validated.EntryDefaultDisposition != "Pending" {
		t.Errorf("default Entry disposition = %q, want Pending", validated.EntryDefaultDisposition)
	}
	if got := validated.TargetAdjustmentPresetsSeconds; len(got) != 3 ||
		got[0] != -300 || got[1] != 300 || got[2] != 600 {
		t.Errorf("default Adjust Target presets = %v, want [-300 300 600]", got)
	}
}

func TestEventEntryDefaultDispositionAcceptsIncluded(t *testing.T) {
	input := CreateInput{
		Name: "Revision 2026", PlannedStartDate: "2026-08-21", PlannedEndDate: "2026-08-23",
		Timezone: "Europe/Berlin", EventLocale: "de-DE", EntryDefaultDisposition: "Included",
	}
	validated, err := validateCreateInput(input)
	if err != nil || validated.EntryDefaultDisposition != "Included" {
		t.Fatalf("Included Entry default = %+v, %v", validated, err)
	}
}

func TestEventEntryDefaultDispositionRejectsUnsupportedValue(t *testing.T) {
	input := CreateInput{
		Name: "Revision 2026", PlannedStartDate: "2026-08-21", PlannedEndDate: "2026-08-23",
		Timezone: "Europe/Berlin", EventLocale: "de-DE", EntryDefaultDisposition: "Rejected",
	}
	_, err := validateCreateInput(input)
	var validation *ValidationError
	if !errors.As(err, &validation) || validation.Field != "entry_default_disposition" {
		t.Fatalf("unsupported Entry default error = %v", err)
	}
}

func TestEventTargetAdjustmentPresetsRejectInvalidValues(t *testing.T) {
	input := CreateInput{
		Name: "Revision 2026", PlannedStartDate: "2026-08-21", PlannedEndDate: "2026-08-23",
		Timezone: "Europe/Berlin", EventLocale: "de-DE",
		TargetAdjustmentPresetsSeconds: []int{300, 0},
	}
	_, err := validateCreateInput(input)
	var validation *ValidationError
	if !errors.As(err, &validation) || validation.Field != "target_adjustment_presets_seconds" {
		t.Fatalf("preset validation error = %v", err)
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

func TestEventGrantScopesValidateRoleBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		input     GrantInput
		wantField string
	}{
		{
			name:      "Producer explicit scope",
			input:     GrantInput{AccountID: 2, Role: "Producer", LaneIDs: []int{1}},
			wantField: "role",
		},
		{
			name:      "Observer live scope",
			input:     GrantInput{AccountID: 2, Role: "Observer", DisplayGroupKeys: []string{"stage"}},
			wantField: "role",
		},
		{
			name:      "Observer mutation capability",
			input:     GrantInput{AccountID: 2, Role: "Observer", Capabilities: []string{"ManageResults"}},
			wantField: "capabilities",
		},
		{
			name:      "unknown capability",
			input:     GrantInput{AccountID: 2, Role: "Operator", Capabilities: []string{"Unknown"}},
			wantField: "capabilities",
		},
		{
			name:      "invalid Display Group key",
			input:     GrantInput{AccountID: 2, Role: "Operator", DisplayGroupKeys: []string{"stage left"}},
			wantField: "display_group_keys",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := validateGrantInput(test.input)
			var validation *ValidationError
			if !errors.As(err, &validation) || validation.Field != test.wantField {
				t.Fatalf("validation error = %v, want field %q", err, test.wantField)
			}
		})
	}
}

func TestEventGrantScopesNormalizeStableOrder(t *testing.T) {
	validated, err := validateGrantInput(GrantInput{
		AccountID:        2,
		Role:             "Operator",
		LaneIDs:          []int{3, 1},
		DisplayGroupKeys: []string{"stage:right", "stage:left"},
		Capabilities:     []string{"ViewResults", "EmergencyAlert"},
	})
	if err != nil {
		t.Fatalf("validate scoped Grant: %v", err)
	}
	if got := validated.LaneIDs; got[0] != 1 || got[1] != 3 {
		t.Errorf("Lane IDs = %v, want [1 3]", got)
	}
	if got := validated.DisplayGroupKeys; got[0] != "stage:left" || got[1] != "stage:right" {
		t.Errorf("Display Group keys = %v, want stable order", got)
	}
	if got := validated.Capabilities; got[0] != "EmergencyAlert" || got[1] != "ViewResults" {
		t.Errorf("capabilities = %v, want stable order", got)
	}
}

func TestUnscopedEventGrantRetainsLegacyPayloadHash(t *testing.T) {
	input := GrantInput{AccountID: 2, Role: "Operator", CommandID: "grant-opal"}
	want := command.PayloadHash("1", "2", "Operator")
	got, err := grantPayloadHash(1, input)
	if err != nil {
		t.Fatalf("hash unscoped Grant payload: %v", err)
	}
	if got != want {
		t.Errorf("unscoped Grant payload hash = %q, want legacy hash %q", got, want)
	}
}

func TestScopedEventGrantPayloadHashUsesSetOrder(t *testing.T) {
	first := GrantInput{
		AccountID: 2, Role: "Operator",
		LaneIDs: []int{3, 1}, DisplayGroupKeys: []string{"right", "left"},
		Capabilities: []string{"ViewResults", "EmergencyAlert"},
	}
	second := GrantInput{
		AccountID: 2, Role: "Operator",
		LaneIDs: []int{1, 3}, DisplayGroupKeys: []string{"left", "right"},
		Capabilities: []string{"EmergencyAlert", "ViewResults"},
	}
	firstHash, err := grantPayloadHash(1, first)
	if err != nil {
		t.Fatalf("hash first scoped Grant payload: %v", err)
	}
	secondHash, err := grantPayloadHash(1, second)
	if err != nil {
		t.Fatalf("hash second scoped Grant payload: %v", err)
	}
	if firstHash != secondHash {
		t.Errorf("equivalent scoped Grant hashes differ: %q != %q", firstHash, secondHash)
	}
}
