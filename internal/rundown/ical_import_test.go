package rundown

import (
	"strings"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/store"
)

func TestICalendarTimeResolutionRequiresDSTOccurrenceChoice(t *testing.T) {
	property := icalProperty{value: "20261025T023000", params: map[string]string{"TZID": "Europe/Brussels"}}
	_, _, err := resolveICalendarTime(property, "", "", "UTC", "")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous iCalendar time error = %v", err)
	}
	earlier, _, err := resolveICalendarTime(property, "", "", "UTC", "Earlier")
	if err != nil {
		t.Fatalf("resolve earlier occurrence: %v", err)
	}
	later, _, err := resolveICalendarTime(property, "", "", "UTC", "Later")
	if err != nil {
		t.Fatalf("resolve later occurrence: %v", err)
	}
	if later.Sub(earlier) != time.Hour {
		t.Errorf("ambiguous occurrences differ by %s, want 1h", later.Sub(earlier))
	}
}

func TestICalendarPreviewAllowsIndependentOccurrenceChoices(t *testing.T) {
	state := store.CSVImportState{
		Timezone: "Europe/Brussels", Sessions: map[string]store.CSVImportSession{},
		LaneIDs: map[string][]int{"Main": {1}}, LocationIDs: map[string][]int{}, TrackIDs: map[string][]int{},
	}
	data := "BEGIN:VCALENDAR\nBEGIN:VEVENT\nUID:mixed\n" +
		"DTSTART;TZID=Europe/Brussels:20261025T024500\n" +
		"DTEND;TZID=Europe/Brussels:20261025T021500\n" +
		"SUMMARY:Fallback session\nLOCATION:Main\nEND:VEVENT\nEND:VCALENDAR\n"
	preview, err := formICalendarImportPreview(state, ICalendarImportPreviewInput{
		Data: data,
		Choices: []ICalendarOccurrenceChoice{
			{UID: "mixed", Property: "DTSTART", Occurrence: "Earlier"},
			{UID: "mixed", Property: "DTEND", Occurrence: "Later"},
		},
	})
	if err != nil || len(preview.Proposals) != 1 || preview.Proposals[0].Classification != CSVProposalAddition {
		t.Fatalf("mixed occurrence preview = %+v, %v", preview, err)
	}
	if preview.Proposals[0].draft.PlannedEnd.Sub(preview.Proposals[0].draft.PlannedStart) != 30*time.Minute {
		t.Errorf("mixed occurrence duration = %s", preview.Proposals[0].draft.PlannedEnd.Sub(preview.Proposals[0].draft.PlannedStart))
	}
}

func TestICalendarTimeResolutionRejectsGapAndWarnsForFloating(t *testing.T) {
	gap := icalProperty{value: "20260329T023000", params: map[string]string{"TZID": "Europe/Brussels"}}
	_, _, err := resolveICalendarTime(gap, "", "", "UTC", "")
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("nonexistent iCalendar time error = %v", err)
	}
	floating := icalProperty{value: "20260201T090000", params: map[string]string{}}
	resolved, warnings, err := resolveICalendarTime(floating, "", "", "Europe/Brussels", "")
	if err != nil || len(warnings) != 1 || !strings.Contains(warnings[0], "floating") {
		t.Fatalf("floating iCalendar time = %v, %v, %v", resolved, warnings, err)
	}
}

func TestICalendarPreviewConsumesFOSDEMStyleEventExplicitly(t *testing.T) {
	state := store.CSVImportState{
		DraftRevision: 2, EventRevision: 3, Timezone: "Europe/Brussels",
		Sessions: map[string]store.CSVImportSession{},
		LaneIDs:  map[string][]int{"Janson": {11}}, LocationIDs: map[string][]int{"Janson": {21}},
		TrackIDs: map[string][]int{"Main Track": {31}},
	}
	data := "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"X-WR-TIMEZONE:Europe/Brussels\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:6895@fosdem-2026@fosdem.org\r\n" +
		"TZID:Europe-Brussels\r\n" +
		"DTSTART:20260201T090000\r\n" +
		"DTEND:20260201T095000\r\n" +
		"SUMMARY:Free as in Burned Out: Café & λ\r\n" +
		"DESCRIPTION:Line one\\nline two\r\n" +
		"CATEGORIES:Main Track\r\n" +
		"LOCATION:Janson\r\n" +
		"ATTENDEE;CN=Zoë Example:invalid:nomail\r\n" +
		"URL:https://fosdem.org/example\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	preview, err := formICalendarImportPreview(state, ICalendarImportPreviewInput{EventID: 1, Data: data})
	if err != nil {
		t.Fatalf("Preview FOSDEM-style iCalendar: %v", err)
	}
	if len(preview.Proposals) != 1 || preview.Proposals[0].Classification != CSVProposalAddition {
		t.Fatalf("FOSDEM-style proposal = %+v", preview.Proposals)
	}
	proposal := preview.Proposals[0]
	if proposal.draft.Title != "Free as in Burned Out: Café & λ" || proposal.draft.Speaker != "Zoë Example" ||
		proposal.draft.PublicDetails != "Line one\nline two" {
		t.Errorf("Unicode/text proposal = %+v", proposal.draft)
	}
	if !strings.Contains(strings.Join(preview.Warnings, " "), "unsupported TZID Europe-Brussels") ||
		!strings.Contains(strings.Join(preview.UnsupportedFields, " "), "URL") || len(preview.AppliedDefaults) == 0 {
		t.Errorf("iCalendar limitations = warnings %v unsupported %v defaults %v", preview.Warnings, preview.UnsupportedFields, preview.AppliedDefaults)
	}
}

func TestICalendarPreviewBlocksDuplicateUIDs(t *testing.T) {
	state := store.CSVImportState{
		Timezone: "UTC", Sessions: map[string]store.CSVImportSession{},
		LaneIDs: map[string][]int{}, LocationIDs: map[string][]int{}, TrackIDs: map[string][]int{},
	}
	event := "BEGIN:VEVENT\nUID:same\nDTSTART:20260101T100000Z\nDTEND:20260101T110000Z\nSUMMARY:Talk\nLOCATION:Missing\nEND:VEVENT\n"
	preview, err := formICalendarImportPreview(state, ICalendarImportPreviewInput{
		Data: "BEGIN:VCALENDAR\n" + event + event + "END:VCALENDAR\n",
	})
	if err != nil {
		t.Fatalf("Preview duplicate iCalendar UIDs: %v", err)
	}
	if !strings.Contains(strings.Join(preview.ValidationFailures, " "), "duplicate Import Reference") {
		t.Errorf("duplicate UID failures = %v", preview.ValidationFailures)
	}
}

func TestICalendarPreviewRejectsOversizedUID(t *testing.T) {
	state := store.CSVImportState{
		Timezone: "UTC", Sessions: map[string]store.CSVImportSession{},
		LaneIDs: map[string][]int{"Main": {1}}, LocationIDs: map[string][]int{}, TrackIDs: map[string][]int{},
	}
	data := "BEGIN:VCALENDAR\nBEGIN:VEVENT\nUID:" + strings.Repeat("x", maxImportReferenceLength+1) +
		"\nDTSTART:20260101T100000Z\nDTEND:20260101T110000Z\nSUMMARY:Talk\nLOCATION:Main\nEND:VEVENT\nEND:VCALENDAR\n"
	preview, err := formICalendarImportPreview(state, ICalendarImportPreviewInput{Data: data})
	if err != nil {
		t.Fatalf("Preview oversized UID: %v", err)
	}
	if len(preview.Proposals) != 1 || preview.Proposals[0].Classification != CSVProposalUnresolved ||
		!strings.Contains(preview.Proposals[0].Message, "500 characters") {
		t.Errorf("oversized UID proposal = %+v", preview.Proposals)
	}
	unicodeData := strings.Replace(data, strings.Repeat("x", maxImportReferenceLength+1),
		strings.Repeat("é", maxImportReferenceLength), 1)
	unicodePreview, err := formICalendarImportPreview(state, ICalendarImportPreviewInput{Data: unicodeData})
	if err != nil || len(unicodePreview.Proposals) != 1 ||
		unicodePreview.Proposals[0].Classification != CSVProposalAddition {
		t.Errorf("500-character Unicode UID preview = %+v, %v", unicodePreview, err)
	}
}

func TestICalendarParserRejectsMalformedNesting(t *testing.T) {
	for _, data := range []string{
		"BEGIN:VCALENDAR\nBEGIN:VEVENT\nBEGIN:VEVENT\nEND:VEVENT\nEND:VCALENDAR\n",
		"BEGIN:VCALENDAR\nBEGIN:VEVENT\nEND:VCALENDAR\n",
		"BEGIN:VCALENDAR\nVERSION:2.0\n",
		"BEGIN:VCALENDAR\nEND:VCALENDAR\nVERSION:2.0\n",
	} {
		if _, _, _, err := parseICalendar(data); err == nil {
			t.Errorf("parse malformed iCalendar %q succeeded", data)
		}
	}
}

func FuzzICalendarParser(f *testing.F) {
	f.Add("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nEND:VCALENDAR\r\n")
	f.Add("BEGIN:VCALENDAR\nBEGIN:VEVENT\nUID:a\nDTSTART:20260101T100000Z\nDTEND:20260101T110000Z\nEND:VEVENT\nEND:VCALENDAR\n")
	f.Add("BEGIN:VCALENDAR\nBEGIN:VEVENT\nSUMMARY:a\n folded\nEND:VCALENDAR\n")
	f.Fuzz(func(t *testing.T, data string) {
		_, _, _, _ = parseICalendar(data)
	})
}
