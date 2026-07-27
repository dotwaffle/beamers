package server

import (
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

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
