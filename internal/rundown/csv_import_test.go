package rundown

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/store"
)

func TestCSVImportPreviewClassifiesAdditionAndIgnoredFields(t *testing.T) {
	state := store.CSVImportState{
		DraftRevision: 3, Timezone: "Europe/Brussels", Sessions: map[string]store.CSVImportSession{},
		LaneIDs: map[string][]int{"Janson": {11}}, LocationIDs: map[string][]int{}, TrackIDs: map[string][]int{},
	}
	input := CSVImportPreviewInput{
		EventID: 1,
		CSVData: "key,title,start,end,lane,vendor_only\nfosdem-1,Opening,2026-01-31 10:00,2026-01-31 11:00,Janson,x\n",
		Mappings: []CSVFieldMapping{
			{SourceColumn: "key", TargetField: "external_key"},
			{SourceColumn: "title", TargetField: "title"},
			{SourceColumn: "start", TargetField: "planned_start"},
			{SourceColumn: "end", TargetField: "planned_end"},
			{SourceColumn: "lane", TargetField: "lane"},
		},
	}
	preview, err := formCSVImportPreview(state, input)
	if err != nil {
		t.Fatalf("Preview CSV addition: %v", err)
	}
	if len(preview.ValidationFailures) != 0 || len(preview.Proposals) != 1 {
		t.Fatalf("CSV addition preview = %+v", preview)
	}
	proposal := preview.Proposals[0]
	if proposal.Classification != CSVProposalAddition || proposal.ExternalKey != "fosdem-1" ||
		proposal.draft.Title != "Opening" || len(proposal.draft.Lanes) != 1 ||
		proposal.draft.PlannedStart.Location().String() != "Europe/Brussels" {
		t.Errorf("CSV addition proposal = %+v", proposal)
	}
	if len(preview.IgnoredFields) != 1 || preview.IgnoredFields[0] != "vendor_only" {
		t.Errorf("ignored CSV fields = %v", preview.IgnoredFields)
	}
}

func TestCSVImportPreviewKeepsAbsentFieldsAndClassifiesRepeatChanges(t *testing.T) {
	plannedStart := time.Date(2026, 1, 31, 9, 0, 0, 0, time.UTC)
	state := store.CSVImportState{
		DraftRevision: 4, Timezone: "UTC",
		Sessions: map[string]store.CSVImportSession{
			"session-1": {
				ID: 7, HasRuns: false,
				Draft: store.SessionDraftCreate{
					ID: 7, Title: "Old title", Speaker: "Retained speaker", PublicDetails: "Old details",
					AudienceVisibility: "CrewOnly", PlannedStart: plannedStart,
				},
			},
		},
		LaneIDs: map[string][]int{}, LocationIDs: map[string][]int{}, TrackIDs: map[string][]int{},
	}
	preview, err := formCSVImportPreview(state, CSVImportPreviewInput{
		EventID: 1,
		CSVData: "key,title,details,start,audience\nsession-1,New title,New details,2026-01-31T10:00:00Z,Public\n",
		Mappings: []CSVFieldMapping{
			{SourceColumn: "key", TargetField: "external_key"},
			{SourceColumn: "title", TargetField: "title"},
			{SourceColumn: "details", TargetField: "public_details"},
			{SourceColumn: "start", TargetField: "planned_start"},
			{SourceColumn: "audience", TargetField: "audience_visibility"},
		},
	})
	if err != nil {
		t.Fatalf("Preview repeat CSV: %v", err)
	}
	classifications := map[string]string{}
	for _, proposal := range preview.Proposals {
		classifications[proposal.Field] = proposal.Classification
		if proposal.Field == "speaker" {
			t.Error("absent speaker produced a proposal")
		}
	}
	if classifications["title"] != CSVProposalUpdate ||
		classifications["public_details"] != CSVProposalUpdate ||
		classifications["audience_visibility"] != CSVProposalUpdate ||
		classifications["planned_start"] != CSVProposalConflict {
		t.Errorf("repeat CSV classifications = %v", classifications)
	}

	matched := state.Sessions["session-1"]
	matched.HasRuns = true
	state.Sessions["session-1"] = matched
	runPreview, err := formCSVImportPreview(state, CSVImportPreviewInput{
		EventID: 1, CSVData: "key,title,details\nsession-1,New title,New details\n",
		Mappings: []CSVFieldMapping{
			{SourceColumn: "key", TargetField: "external_key"},
			{SourceColumn: "title", TargetField: "title"},
			{SourceColumn: "details", TargetField: "public_details"},
		},
	})
	if err != nil {
		t.Fatalf("Preview Run-protected CSV: %v", err)
	}
	if runPreview.Proposals[0].Classification != CSVProposalConflict ||
		runPreview.Proposals[1].Classification != CSVProposalUpdate {
		t.Errorf("Run-protected proposals = %+v", runPreview.Proposals)
	}
}

func TestCSVImportPreviewValidatesAdditionsAndBindsFingerprintToEventTime(t *testing.T) {
	state := store.CSVImportState{
		DraftRevision: 2, EventRevision: 4, Timezone: "UTC",
		Sessions: map[string]store.CSVImportSession{}, LaneIDs: map[string][]int{"Main": {1}},
		LocationIDs: map[string][]int{}, TrackIDs: map[string][]int{},
	}
	input := CSVImportPreviewInput{
		EventID: 1,
		CSVData: "key,title,type,start,end,lane\nkey-1,Talk,Bogus,2026-01-31 10:00,2026-01-31 11:00,Main\n",
		Mappings: []CSVFieldMapping{
			{SourceColumn: "key", TargetField: "external_key"},
			{SourceColumn: "title", TargetField: "title"},
			{SourceColumn: "type", TargetField: "type"},
			{SourceColumn: "start", TargetField: "planned_start"},
			{SourceColumn: "end", TargetField: "planned_end"},
			{SourceColumn: "lane", TargetField: "lane"},
		},
	}
	preview, err := formCSVImportPreview(state, input)
	if err != nil {
		t.Fatalf("Preview invalid addition: %v", err)
	}
	if len(preview.Proposals) != 1 || preview.Proposals[0].Classification != CSVProposalUnresolved ||
		!strings.Contains(preview.Proposals[0].Message, "sessions.type") {
		t.Errorf("invalid addition preview = %+v", preview.Proposals)
	}

	validInput := input
	validInput.CSVData = "key,title,type,start,end,lane\nkey-1,Talk,Presentation,2026-01-31 10:00,2026-01-31 11:00,Main\n"
	utcPreview, err := formCSVImportPreview(state, validInput)
	if err != nil {
		t.Fatalf("Preview UTC addition: %v", err)
	}
	state.Timezone = "Europe/Brussels"
	brusselsPreview, err := formCSVImportPreview(state, validInput)
	if err != nil {
		t.Fatalf("Preview Brussels addition: %v", err)
	}
	if utcPreview.Fingerprint == brusselsPreview.Fingerprint {
		t.Error("CSV preview fingerprint did not bind Event timezone")
	}
}

func TestCSVImportPreviewLimitsExternalKeysAndColumns(t *testing.T) {
	state := store.CSVImportState{
		Timezone: "UTC", Sessions: map[string]store.CSVImportSession{},
		LaneIDs: map[string][]int{}, LocationIDs: map[string][]int{}, TrackIDs: map[string][]int{},
	}
	longKey := strings.Repeat("x", maxImportReferenceRunes+1)
	preview, err := formCSVImportPreview(state, CSVImportPreviewInput{
		EventID: 1, CSVData: "key\n" + longKey + "\n",
		Mappings: []CSVFieldMapping{{SourceColumn: "key", TargetField: "external_key"}},
	})
	if err != nil {
		t.Fatalf("Preview long Import Reference: %v", err)
	}
	if !strings.Contains(strings.Join(preview.ValidationFailures, " "), "external_key must not exceed") {
		t.Errorf("long Import Reference failures = %v", preview.ValidationFailures)
	}

	headers := make([]string, maxCSVImportColumns+1)
	for index := range headers {
		headers[index] = "column-" + strconv.Itoa(index)
	}
	_, err = formCSVImportPreview(state, CSVImportPreviewInput{CSVData: strings.Join(headers, ",") + "\n"})
	if err == nil || !strings.Contains(err.Error(), "must not exceed") {
		t.Errorf("wide CSV error = %v", err)
	}
}

func TestCSVImportPreviewBlocksDuplicateReferencesAndUnsafeTargets(t *testing.T) {
	state := store.CSVImportState{
		Timezone: "UTC", Sessions: map[string]store.CSVImportSession{},
		LaneIDs: map[string][]int{}, LocationIDs: map[string][]int{}, TrackIDs: map[string][]int{},
	}
	preview, err := formCSVImportPreview(state, CSVImportPreviewInput{
		EventID: 1, CSVData: "key,notes\nduplicate,one\nduplicate,two\n",
		Mappings: []CSVFieldMapping{
			{SourceColumn: "key", TargetField: "external_key"},
			{SourceColumn: "notes", TargetField: "crew_notes"},
		},
	})
	if err != nil {
		t.Fatalf("Preview invalid CSV: %v", err)
	}
	joined := strings.Join(preview.ValidationFailures, " ")
	if !strings.Contains(joined, "duplicate Import Reference") || !strings.Contains(joined, "cannot target crew_notes") {
		t.Errorf("CSV validation failures = %v", preview.ValidationFailures)
	}
}

func TestCSVImportPreviewKeepsCompetitionEntryUnresolvedUntilEntryModel(t *testing.T) {
	state := store.CSVImportState{
		Timezone: "UTC", Sessions: map[string]store.CSVImportSession{},
		LaneIDs: map[string][]int{}, LocationIDs: map[string][]int{}, TrackIDs: map[string][]int{},
	}
	preview, err := formCSVImportPreview(state, CSVImportPreviewInput{
		EventID: 1, CSVData: "kind,key,title\nCompetitionEntry,entry-1,Demo\n",
		Mappings: []CSVFieldMapping{
			{SourceColumn: "kind", TargetField: "record_type"},
			{SourceColumn: "key", TargetField: "external_key"},
			{SourceColumn: "title", TargetField: "title"},
		},
	})
	if err != nil {
		t.Fatalf("Preview Competition Entry CSV: %v", err)
	}
	if len(preview.Proposals) != 1 || preview.Proposals[0].Classification != CSVProposalUnresolved {
		t.Errorf("Competition Entry preview = %+v", preview.Proposals)
	}
}
