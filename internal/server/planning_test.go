package server

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/displayviews"
	"github.com/dotwaffle/beamers/internal/rundown"
)

func TestPlanningLocalTimeRequiresValidExplicitOccurrence(t *testing.T) {
	if _, err := rundown.ResolveLocalDateTime(
		"2026-03-29T02:30",
		"Europe/Berlin",
		"",
	); err == nil {
		t.Fatal("nonexistent local time was accepted")
	}
	if _, err := rundown.ResolveLocalDateTime(
		"2026-10-25T02:30",
		"Europe/Berlin",
		"",
	); err == nil {
		t.Fatal("ambiguous local time was accepted without an occurrence")
	}
	earlier, err := rundown.ResolveLocalDateTime(
		"2026-10-25T02:30",
		"Europe/Berlin",
		"Earlier",
	)
	if err != nil {
		t.Fatalf("resolve earlier occurrence: %v", err)
	}
	later, err := rundown.ResolveLocalDateTime(
		"2026-10-25T02:30",
		"Europe/Berlin",
		"Later",
	)
	if err != nil {
		t.Fatalf("resolve later occurrence: %v", err)
	}
	if !later.After(earlier) {
		t.Fatalf("occurrences = %v then %v, want increasing instants", earlier, later)
	}
	switched, err := planningFormTime(
		"2026-10-25T02:30",
		"Europe/Berlin",
		"Later",
		earlier,
	)
	if err != nil {
		t.Fatalf("switch occurrence: %v", err)
	}
	if !switched.Equal(later) {
		t.Fatalf("switched occurrence = %v, want %v", switched, later)
	}
}

func TestDisplaySettingsInputDistinguishesEmptyOverridesFromInheritance(t *testing.T) {
	const sessionID = 42
	draft := rundown.DraftRundown{
		Sessions: []rundown.CrewSession{{
			ID: sessionID, Title: "Opening Keynote", Type: rundown.SessionPresentation,
		}},
	}
	values := url.Values{
		"rotation_seconds":        {"15"},
		"expected_event_revision": {"1"},
		"command_id":              {"configure-empty-overrides"},
		"session_type.Presentation.threshold_override": {"true"},
		"session.42.threshold_override":                {"true"},
	}
	input, err := displaySettingsInput(values, displayviews.DefaultConfiguration(), draft)
	if err != nil {
		t.Fatalf("parse explicit empty overrides: %v", err)
	}
	typeThresholds, typeExists := input.SessionTypeTimerThresholds["Presentation"]
	sessionThresholds, sessionExists := input.SessionTimerThresholds[sessionID]
	if !typeExists || typeThresholds == nil || !sessionExists || sessionThresholds == nil {
		t.Fatalf(
			"explicit empty overrides = type (%t, %#v), Session (%t, %#v)",
			typeExists,
			typeThresholds,
			sessionExists,
			sessionThresholds,
		)
	}

	delete(values, "session_type.Presentation.threshold_override")
	delete(values, "session.42.threshold_override")
	inherited, err := displaySettingsInput(values, input.Configuration, draft)
	if err != nil {
		t.Fatalf("parse inherited thresholds: %v", err)
	}
	if _, exists := inherited.SessionTypeTimerThresholds["Presentation"]; exists {
		t.Error("unchecked Session type retained an override")
	}
	if _, exists := inherited.SessionTimerThresholds[sessionID]; exists {
		t.Error("unchecked Session retained an override")
	}
}

func TestDisplaySettingsInputReportsDomainThresholdRules(t *testing.T) {
	t.Parallel()
	repeated := make([]string, 0, 17)
	emphases := make([]string, 0, 17)
	for row := range 17 {
		repeated = append(repeated, strconv.Itoa(row+1))
		emphases = append(emphases, displayviews.EmphasisUrgent)
	}
	tests := []struct {
		name     string
		seconds  []string
		emphasis []string
		fieldID  string
		message  string
	}{
		{
			name: "unparsable seconds", seconds: []string{"soon"},
			emphasis: []string{displayviews.EmphasisUrgent},
			fieldID:  "timer_threshold-seconds-0",
			message:  "must be a whole number of seconds",
		},
		{
			name: "seconds below the domain range", seconds: []string{"0"},
			emphasis: []string{displayviews.EmphasisUrgent},
			fieldID:  "timer_threshold-seconds-0",
			message:  "remaining seconds must be between 1 and 86400",
		},
		{
			name: "seconds above the domain range", seconds: []string{"86401"},
			emphasis: []string{displayviews.EmphasisUrgent},
			fieldID:  "timer_threshold-seconds-0",
			message:  "remaining seconds must be between 1 and 86400",
		},
		{
			name: "unsupported emphasis", seconds: []string{"60"},
			emphasis: []string{"panic"},
			fieldID:  "timer_threshold-emphasis-0",
			message:  "emphasis must be attention or urgent",
		},
		{
			name: "repeated remaining seconds", seconds: []string{"60", "60"},
			emphasis: []string{displayviews.EmphasisAttention, displayviews.EmphasisUrgent},
			fieldID:  "timer_threshold-seconds-1",
			message:  "must not repeat remaining seconds",
		},
		{
			name:     "blank rows keep the submitted row number",
			seconds:  []string{"", "0"},
			emphasis: []string{"", displayviews.EmphasisUrgent},
			fieldID:  "timer_threshold-seconds-1",
			message:  "remaining seconds must be between 1 and 86400",
		},
		{
			name:    "more thresholds than the domain allows",
			seconds: repeated, emphasis: emphases,
			fieldID: "timer_threshold-seconds-16",
			message: "must contain at most 16 thresholds",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			values := url.Values{
				"rotation_seconds":         {"15"},
				"expected_event_revision":  {"1"},
				"timer_threshold_seconds":  test.seconds,
				"timer_threshold_emphasis": test.emphasis,
			}
			_, err := displaySettingsInput(
				values,
				displayviews.DefaultConfiguration(),
				rundown.DraftRundown{},
			)
			var inputError *displaySettingsInputError
			if !errors.As(err, &inputError) {
				t.Fatalf("displaySettingsInput() error = %v, want input error", err)
			}
			if inputError.fieldID != test.fieldID {
				t.Errorf("field ID = %q, want %q", inputError.fieldID, test.fieldID)
			}
			if inputError.message != test.message {
				t.Errorf("message = %q, want %q", inputError.message, test.message)
			}
		})
	}
}

func TestDisplaySessionTypesCoverEveryRundownSessionType(t *testing.T) {
	t.Parallel()
	for _, sessionType := range []rundown.SessionType{
		rundown.SessionPresentation,
		rundown.SessionCompetition,
		rundown.SessionBreak,
		rundown.SessionActivity,
		rundown.SessionCeremony,
		rundown.SessionPerformance,
		rundown.SessionHold,
	} {
		if !displayviews.ValidSessionType(string(sessionType)) ||
			!slices.Contains(displaySessionTypes(), string(sessionType)) {
			t.Errorf("Session type %q is missing from the Display settings form", sessionType)
		}
	}
	if len(displaySessionTypes()) != 7 {
		t.Errorf("Display Session types = %v, want exactly the Rundown types", displaySessionTypes())
	}
}

func TestSessionFormTargetsOnlyFactsChangedFromViewedDraft(t *testing.T) {
	start := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	base := rundown.CrewSession{
		ID: 1, Title: "Viewed title", Speaker: "Viewed speaker",
		Type: rundown.SessionPresentation, AudienceVisibility: rundown.AudiencePublic,
		PlannedStart: start, PlannedEnd: start.Add(time.Hour),
		TimingPolicy: rundown.TimingFixedEnd, MinimumDuration: 15 * time.Minute,
		StartBoundary: rundown.BoundarySoft, EndBoundary: rundown.BoundarySoft,
		LaneIDs: []int{1}, LocationIDs: []int{1},
	}
	current := base
	current.Title = "Concurrent title"
	form := url.Values{
		"session_title":        {base.Title},
		"session_speaker":      {"My speaker"},
		"session_type":         {string(base.Type)},
		"audience_visibility":  {string(base.AudienceVisibility)},
		"planned_start":        {"2026-07-26T10:00"},
		"planned_end":          {"2026-07-26T11:00"},
		"timing_policy":        {string(base.TimingPolicy)},
		"minimum_duration":     {base.MinimumDuration.String()},
		"start_boundary":       {string(base.StartBoundary)},
		"end_boundary":         {string(base.EndBoundary)},
		"session_lane_ids":     {"1"},
		"session_location_ids": {"1"},
	}
	input, err := sessionFormInput(
		&http.Request{Form: form},
		1,
		"UTC",
		&current,
		&base,
		false,
		false,
		false,
	)
	if err != nil {
		t.Fatalf("parse stale independent edit: %v", err)
	}
	if !slices.Equal(input.UpdateFields, []string{"speaker"}) {
		t.Fatalf("update fields = %v, want speaker only", input.UpdateFields)
	}
	form["session_lane_ids"] = []string{"2"}
	input, err = sessionFormInput(
		&http.Request{Form: form}, 1, "UTC", &current, &base,
		false, false, false,
	)
	if err != nil {
		t.Fatalf("parse membership edit: %v", err)
	}
	if !slices.Equal(input.UpdateFields, []string{"speaker", "add_lanes", "remove_lanes"}) ||
		len(input.AddLanes) != 1 || input.AddLanes[0].ID != 2 ||
		len(input.RemoveLanes) != 1 || input.RemoveLanes[0].ID != 1 {
		t.Fatalf("membership edit = %+v", input)
	}
}

func TestPlanningFormErrorsTargetSessionRelationships(t *testing.T) {
	for _, test := range []struct {
		field, target, want string
	}{
		{"sessions.lanes", "", "draft-session-lane-id"},
		{"sessions.locations", "", "draft-session-location-id"},
		{"sessions.tracks", "", "draft-session-track-id"},
		{"sessions.locations", "42", "session-42-session-location-ids"},
		{"sessions.tracks", "42", "session-42-session-track-ids"},
	} {
		t.Run(test.field+"/"+test.target, func(t *testing.T) {
			values := url.Values{}
			if test.target != "" {
				values.Set("session_id", test.target)
			}
			_, formErrors := planningFormErrors(&rundown.ValidationError{
				Field: test.field, Message: "invalid relationship",
			}, values)
			if len(formErrors) != 1 || formErrors[0].FieldID != test.want {
				t.Fatalf("errors = %#v, want field %q", formErrors, test.want)
			}
		})
	}
}

func TestPlanningFormErrorsTargetImportProposals(t *testing.T) {
	for _, action := range []string{"csv-import", "icalendar-import"} {
		t.Run(action, func(t *testing.T) {
			_, formErrors := planningFormErrors(&rundown.ValidationError{
				Field: "proposal_ids", Message: "must select a proposal",
			}, url.Values{"action": {action}})
			want := strings.TrimSuffix(action, "-import") + "-proposal-ids"
			if len(formErrors) != 1 || formErrors[0].FieldID != want {
				t.Fatalf("errors = %#v, want field %q", formErrors, want)
			}
		})
	}
}
