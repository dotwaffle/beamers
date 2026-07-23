package rundown

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
)

const (
	// CSVProposalAddition creates one new Draft Session when selected.
	CSVProposalAddition = "Addition"
	// CSVProposalUpdate changes one existing descriptive Draft fact when selected.
	CSVProposalUpdate = "Update"
	// CSVProposalConflict reports a change that import cannot authoritatively apply.
	CSVProposalConflict = "Conflict"
	// CSVProposalUnresolved reports a row or mapping that needs more information.
	CSVProposalUnresolved = "Unresolved"
)

var supportedCSVImportFields = []string{
	"record_type", "external_key", "title", "speaker", "type", "audience_visibility",
	"public_details", "planned_start", "planned_end", "timing_policy", "minimum_duration",
	"start_boundary", "end_boundary", "lane", "location", "track",
}

const (
	maxCSVImportRows        = 10_000
	maxCSVImportColumns     = 128
	maxCSVImportProposals   = 50_000
	maxImportReferenceRunes = 500
)

// CSVFieldMapping maps one source column to one supported Beamers concept.
type CSVFieldMapping struct {
	SourceColumn string `json:"source_column"`
	TargetField  string `json:"target_field"`
}

// CSVImportPreviewInput requests a side-effect-free mapped CSV preview.
type CSVImportPreviewInput struct {
	EventID  int               `json:"event_id"`
	CSVData  string            `json:"csv_data"`
	Mappings []CSVFieldMapping `json:"mappings"`
}

// CSVImportProposal is one selectable addition or field-level proposal.
type CSVImportProposal struct {
	ID             string `json:"id"`
	RowNumber      int    `json:"row_number"`
	RecordType     string `json:"record_type"`
	ExternalKey    string `json:"external_key,omitempty"`
	Classification string `json:"classification"`
	SessionID      int    `json:"session_id,omitempty"`
	Field          string `json:"field,omitempty"`
	CurrentValue   string `json:"current_value,omitempty"`
	ProposedValue  string `json:"proposed_value,omitempty"`
	Message        string `json:"message,omitempty"`
	draft          SessionDraftInput
}

// CSVImportPreview describes mapped work without mutating the Draft.
type CSVImportPreview struct {
	DraftRevision      int                 `json:"draft_revision"`
	Fingerprint        string              `json:"fingerprint"`
	Proposals          []CSVImportProposal `json:"proposals"`
	IgnoredFields      []string            `json:"ignored_fields,omitempty"`
	ValidationFailures []string            `json:"validation_failures,omitempty"`
}

// CSVImportInput confirms selected proposals from one exact preview.
type CSVImportInput struct {
	EventID               int               `json:"event_id"`
	CommandID             string            `json:"command_id"`
	ExpectedDraftRevision int               `json:"expected_draft_revision"`
	CSVData               string            `json:"csv_data"`
	Mappings              []CSVFieldMapping `json:"mappings"`
	Fingerprint           string            `json:"fingerprint"`
	ProposalIDs           []string          `json:"proposal_ids"`
}

// CSVImportResult is the committed Draft evidence created by an import.
type CSVImportResult struct {
	DraftRevision int           `json:"draft_revision"`
	Changes       []DraftChange `json:"changes"`
}

// PreviewCSVImport maps CSV rows into reviewable Draft proposals.
func (queries *Queries) PreviewCSVImport(
	ctx context.Context,
	actor auth.Account,
	input CSVImportPreviewInput,
) (CSVImportPreview, error) {
	if !canReadEvent(actor, input.EventID) {
		return CSVImportPreview{}, ErrEventAccessDenied
	}
	state, err := queries.storage.LoadCSVImportState(actor.Context(ctx), input.EventID)
	if err != nil {
		return CSVImportPreview{}, err
	}
	preview, previewErr := formCSVImportPreview(state, input)
	if previewErr != nil {
		return CSVImportPreview{}, &ValidationError{Field: "csv_data", Message: previewErr.Error()}
	}
	return preview, nil
}

type csvImportOutcome struct {
	Result    *CSVImportResult `json:"result,omitempty"`
	Rejection *rejection       `json:"rejection,omitempty"`
}

// ImportCSV atomically applies selected proposals to Draft state only.
func (commands *Commands) ImportCSV(
	ctx context.Context,
	actor auth.Account,
	input CSVImportInput,
) (CSVImportResult, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return CSVImportResult{}, &ValidationError{Field: "command_id", Message: err.Error()}
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return CSVImportResult{}, errors.New("encode CSV Import command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(string(payload)), Action: "ImportCSV",
		TargetType: "Event", TargetID: strconv.Itoa(input.EventID), Now: commands.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[CSVImportResult]{
		Storage: commands.storage, Identity: identity, Replay: decodeCSVImportOutcome,
		Apply: func(transaction *store.CommandTx) (command.Execution[CSVImportResult], error) {
			if !actor.CanProduceEvent(input.EventID) {
				return rejectCSVImport(rejection{Code: "event_access_denied", Message: ErrEventAccessDenied.Error()})
			}
			state, loadErr := transaction.LoadCSVImportState(actor.Context(ctx), input.EventID)
			if loadErr != nil {
				return command.Execution[CSVImportResult]{}, loadErr
			}
			preview, previewErr := formCSVImportPreview(state, CSVImportPreviewInput{
				EventID: input.EventID, CSVData: input.CSVData, Mappings: input.Mappings,
			})
			if previewErr != nil {
				return rejectCSVImport(rejection{Code: "validation", Field: "csv_data", Message: previewErr.Error()})
			}
			if state.DraftRevision != input.ExpectedDraftRevision || preview.Fingerprint != input.Fingerprint {
				return rejectCSVImport(rejection{Code: "draft_revision_conflict", Message: ErrDraftRevisionConflict.Error()})
			}
			if len(preview.ValidationFailures) > 0 {
				return rejectCSVImport(rejection{Code: "validation", Field: "preview", Message: preview.ValidationFailures[0]})
			}
			draftInput, externalKeys, selectionErr := selectedCSVImportDraft(input, preview)
			if selectionErr != nil {
				return rejectCSVImport(rejection{Code: "validation", Field: "proposal_ids", Message: selectionErr.Error()})
			}
			normalized, validationErr := validateEditDraft(draftInput)
			if validationErr != nil {
				var invalid *ValidationError
				_ = errors.As(validationErr, &invalid)
				return rejectCSVImport(rejection{Code: "validation", Field: invalid.Field, Message: invalid.Message})
			}
			stored, editErr := transaction.EditDraft(actor.Context(ctx), editDraftParams(actor.ID, normalized, identity.Now))
			if editErr != nil {
				return command.Execution[CSVImportResult]{}, editErr
			}
			if referenceErr := transaction.CreateCSVImportReferences(
				actor.Context(ctx), input.EventID, externalKeys, stored.Changes, identity.Now,
			); referenceErr != nil {
				return command.Execution[CSVImportResult]{}, referenceErr
			}
			result := CSVImportResult{DraftRevision: stored.DraftRevision, Changes: editDraftResult(stored).Changes}
			encoded, encodeErr := json.Marshal(csvImportOutcome{Result: &result})
			if encodeErr != nil {
				return command.Execution[CSVImportResult]{}, errors.New("encode CSV Import outcome")
			}
			return command.Success(result, string(encoded)), nil
		},
	})
}

func rejectCSVImport(rejected rejection) (command.Execution[CSVImportResult], error) {
	encoded, err := json.Marshal(csvImportOutcome{Rejection: &rejected})
	if err != nil {
		return command.Execution[CSVImportResult]{}, errors.New("encode rejected CSV Import outcome")
	}
	return command.RejectEncoded(CSVImportResult{}, string(encoded), rejectionError(rejected)), nil
}

func decodeCSVImportOutcome(encoded string) (CSVImportResult, error) {
	var outcome csvImportOutcome
	if err := json.Unmarshal([]byte(encoded), &outcome); err != nil {
		return CSVImportResult{}, errors.New("decode CSV Import Command Receipt")
	}
	if outcome.Rejection != nil {
		return CSVImportResult{}, rejectionError(*outcome.Rejection)
	}
	if outcome.Result == nil {
		return CSVImportResult{}, errors.New("CSV Import Command Receipt has no outcome")
	}
	return *outcome.Result, nil
}

func formCSVImportPreview(state store.CSVImportState, input CSVImportPreviewInput) (CSVImportPreview, error) {
	preview := CSVImportPreview{DraftRevision: state.DraftRevision}
	mapping, mappingFailures := validateCSVFieldMappings(input.Mappings)
	preview.ValidationFailures = append(preview.ValidationFailures, mappingFailures...)
	reader := csv.NewReader(strings.NewReader(input.CSVData))
	headers, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return CSVImportPreview{}, errors.New("CSV must include a header row")
	}
	if err != nil {
		return CSVImportPreview{}, fmt.Errorf("parse CSV: %w", err)
	}
	if len(headers) > maxCSVImportColumns {
		return CSVImportPreview{}, fmt.Errorf("CSV must not exceed %d columns", maxCSVImportColumns)
	}
	columns := make(map[string]int, len(headers))
	for index, header := range headers {
		name := strings.TrimSpace(header)
		if name == "" || columns[name] > 0 {
			preview.ValidationFailures = append(preview.ValidationFailures, "CSV headers must be non-empty and unique")
			continue
		}
		columns[name] = index + 1
		if _, mapped := mapping[name]; !mapped {
			preview.IgnoredFields = append(preview.IgnoredFields, name)
		}
	}
	for source := range mapping {
		if columns[source] == 0 {
			preview.ValidationFailures = append(preview.ValidationFailures, "mapped source column is missing: "+source)
		}
	}
	location, locationErr := time.LoadLocation(state.Timezone)
	if locationErr != nil {
		return CSVImportPreview{}, errors.New("load Event timezone")
	}
	seenKeys := make(map[string]int)
	for index := 0; ; index++ {
		record, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return CSVImportPreview{}, fmt.Errorf("parse CSV: %w", readErr)
		}
		if index >= maxCSVImportRows {
			return CSVImportPreview{}, fmt.Errorf("CSV must not exceed %d data rows", maxCSVImportRows)
		}
		rowNumber := index + 2
		values := mappedCSVValues(record, columns, mapping)
		externalKey := values["external_key"]
		if externalKey != "" {
			seenKeys[externalKey]++
			if utf8.RuneCountInString(externalKey) > maxImportReferenceRunes {
				preview.ValidationFailures = append(
					preview.ValidationFailures,
					fmt.Sprintf("row %d external_key must not exceed %d characters", rowNumber, maxImportReferenceRunes),
				)
			}
		}
		preview.Proposals = append(preview.Proposals, previewCSVRow(state, values, rowNumber, location)...)
		if len(preview.Proposals) > maxCSVImportProposals {
			return CSVImportPreview{}, fmt.Errorf("CSV preview must not exceed %d proposals", maxCSVImportProposals)
		}
	}
	for key, count := range seenKeys {
		if count > 1 {
			preview.ValidationFailures = append(preview.ValidationFailures, "duplicate Import Reference in CSV: "+key)
		}
	}
	sort.Strings(preview.IgnoredFields)
	sort.Strings(preview.ValidationFailures)
	normalizedMappings := append([]CSVFieldMapping(nil), input.Mappings...)
	sort.Slice(normalizedMappings, func(left, right int) bool {
		return normalizedMappings[left].SourceColumn < normalizedMappings[right].SourceColumn
	})
	encodedMappings, err := json.Marshal(normalizedMappings)
	if err != nil {
		return CSVImportPreview{}, errors.New("encode normalized CSV mappings")
	}
	type fingerprintProposal struct {
		Proposal CSVImportProposal `json:"proposal"`
		Draft    SessionDraftInput `json:"draft"`
	}
	material := make([]fingerprintProposal, 0, len(preview.Proposals))
	for _, proposal := range preview.Proposals {
		material = append(material, fingerprintProposal{Proposal: proposal, Draft: proposal.draft})
	}
	encodedMaterial, err := json.Marshal(material)
	if err != nil {
		return CSVImportPreview{}, errors.New("encode CSV preview fingerprint material")
	}
	preview.Fingerprint = command.PayloadHash(
		strconv.Itoa(state.DraftRevision), strconv.Itoa(state.EventRevision), state.Timezone,
		input.CSVData, string(encodedMappings), string(encodedMaterial),
		strings.Join(preview.ValidationFailures, "\x00"),
	)
	return preview, nil
}

func validateCSVFieldMappings(mappings []CSVFieldMapping) (map[string]string, []string) {
	result := make(map[string]string, len(mappings))
	targets := make(map[string]struct{}, len(mappings))
	failures := make([]string, 0)
	for _, item := range mappings {
		source := strings.TrimSpace(item.SourceColumn)
		target := strings.TrimSpace(item.TargetField)
		if source == "" || target == "" || result[source] != "" {
			failures = append(failures, "CSV mappings require unique non-empty source columns")
			continue
		}
		if slices.Contains([]string{"crew_notes", "attachments", "lifecycle", "session_runs"}, target) {
			failures = append(failures, "CSV import cannot target "+target)
			continue
		}
		if !slices.Contains(supportedCSVImportFields, target) {
			failures = append(failures, "unsupported CSV target field: "+target)
			continue
		}
		if _, duplicate := targets[target]; duplicate {
			failures = append(failures, "CSV target field is mapped more than once: "+target)
			continue
		}
		result[source] = target
		targets[target] = struct{}{}
	}
	if _, exists := targets["external_key"]; !exists {
		failures = append(failures, "CSV mapping must include external_key")
	}
	return result, failures
}

func mappedCSVValues(record []string, columns map[string]int, mapping map[string]string) map[string]string {
	values := make(map[string]string, len(mapping))
	for source, target := range mapping {
		index := columns[source] - 1
		if index >= 0 && index < len(record) {
			values[target] = strings.TrimSpace(record[index])
		}
	}
	return values
}

func previewCSVRow(
	state store.CSVImportState,
	values map[string]string,
	rowNumber int,
	location *time.Location,
) []CSVImportProposal {
	recordType := values["record_type"]
	if recordType == "" {
		recordType = "Session"
	}
	externalKey := values["external_key"]
	base := CSVImportProposal{RowNumber: rowNumber, RecordType: recordType, ExternalKey: externalKey}
	if recordType == "CompetitionEntry" {
		base.ID = fmt.Sprintf("row:%d:entry", rowNumber)
		base.Classification = CSVProposalUnresolved
		base.Message = "Competition Entry mapping awaits the canonical Entry model"
		return []CSVImportProposal{base}
	}
	if recordType != "Session" {
		base.ID = fmt.Sprintf("row:%d:type", rowNumber)
		base.Classification = CSVProposalUnresolved
		base.Message = "record_type must be Session or CompetitionEntry"
		return []CSVImportProposal{base}
	}
	if externalKey == "" {
		base.ID = fmt.Sprintf("row:%d:key", rowNumber)
		base.Classification = CSVProposalUnresolved
		base.Message = "external key is required"
		return []CSVImportProposal{base}
	}
	matched, exists := state.Sessions[externalKey]
	if !exists {
		draft, err := csvSessionAddition(values, rowNumber, state, location)
		base.ID = fmt.Sprintf("row:%d:add", rowNumber)
		base.Classification = CSVProposalAddition
		base.Field = "session"
		base.ProposedValue = values["title"]
		base.draft = draft
		if err != nil {
			base.Classification = CSVProposalUnresolved
			base.Message = err.Error()
		}
		return []CSVImportProposal{base}
	}
	return csvSessionUpdates(base, matched, values, state, location)
}

func csvSessionAddition(
	values map[string]string,
	rowNumber int,
	state store.CSVImportState,
	location *time.Location,
) (SessionDraftInput, error) {
	if values["title"] == "" || values["planned_start"] == "" || values["planned_end"] == "" || values["lane"] == "" {
		return SessionDraftInput{}, errors.New("new Session requires title, planned_start, planned_end, and lane")
	}
	plannedStart, err := parseCSVTime(values["planned_start"], location)
	if err != nil {
		return SessionDraftInput{}, fmt.Errorf("planned_start: %w", err)
	}
	plannedEnd, err := parseCSVTime(values["planned_end"], location)
	if err != nil {
		return SessionDraftInput{}, fmt.Errorf("planned_end: %w", err)
	}
	lanes, err := resolveCSVTargets(values["lane"], state.LaneIDs)
	if err != nil {
		return SessionDraftInput{}, fmt.Errorf("lane: %w", err)
	}
	locations, err := resolveOptionalCSVTargets(values["location"], state.LocationIDs)
	if err != nil {
		return SessionDraftInput{}, fmt.Errorf("location: %w", err)
	}
	tracks, err := resolveOptionalCSVTargets(values["track"], state.TrackIDs)
	if err != nil {
		return SessionDraftInput{}, fmt.Errorf("track: %w", err)
	}
	typeValue := defaultString(values["type"], string(SessionPresentation))
	audience := defaultString(values["audience_visibility"], string(AudiencePublic))
	timing := defaultString(values["timing_policy"], string(TimingFixedEnd))
	startBoundary := defaultString(values["start_boundary"], string(BoundarySoft))
	endBoundary := defaultString(values["end_boundary"], string(BoundarySoft))
	minimumDuration := plannedEnd.Sub(plannedStart)
	if value := values["minimum_duration"]; value != "" {
		minimumDuration, err = time.ParseDuration(value)
		if err != nil {
			return SessionDraftInput{}, errors.New("minimum_duration must be a duration such as 30m")
		}
	}
	result := SessionDraftInput{
		Ref: fmt.Sprintf("csv-row-%d", rowNumber), Title: values["title"], Speaker: values["speaker"],
		Type: SessionType(typeValue), AudienceVisibility: AudienceVisibility(audience),
		PublicDetails: values["public_details"], PlannedStart: plannedStart, PlannedEnd: plannedEnd,
		TimingPolicy: TimingPolicy(timing), MinimumDuration: minimumDuration,
		StartBoundary: Boundary(startBoundary), EndBoundary: Boundary(endBoundary),
		Lanes: lanes, Locations: locations, Tracks: tracks,
	}
	normalized, validationErr := validateEditDraft(EditDraftInput{EventID: 1, Sessions: []SessionDraftInput{result}})
	if validationErr != nil {
		return SessionDraftInput{}, validationErr
	}
	return normalized.Sessions[0], nil
}

func csvSessionUpdates(
	base CSVImportProposal,
	matched store.CSVImportSession,
	values map[string]string,
	state store.CSVImportState,
	location *time.Location,
) []CSVImportProposal {
	base.SessionID = matched.ID
	proposals := make([]CSVImportProposal, 0)
	for _, field := range supportedCSVImportFields {
		value, present := values[field]
		if !present || value == "" || slices.Contains([]string{"record_type", "external_key"}, field) {
			continue
		}
		current, proposed, draft, err := csvUpdateValue(field, value, matched, state, location)
		if err == nil && !slices.Contains([]string{"lane", "location", "track"}, field) {
			normalized, validationErr := validateEditDraft(EditDraftInput{
				EventID: 1, Sessions: []SessionDraftInput{draft},
			})
			if validationErr != nil {
				err = validationErr
			} else {
				draft = normalized.Sessions[0]
			}
		}
		if err != nil {
			proposal := base
			proposal.ID = fmt.Sprintf("row:%d:%s", base.RowNumber, field)
			proposal.Field = field
			proposal.Classification = CSVProposalUnresolved
			proposal.Message = err.Error()
			proposals = append(proposals, proposal)
			continue
		}
		if current == proposed {
			continue
		}
		proposal := base
		proposal.ID = fmt.Sprintf("row:%d:%s", base.RowNumber, field)
		proposal.Field = field
		proposal.CurrentValue = current
		proposal.ProposedValue = proposed
		proposal.draft = draft
		proposal.Classification = CSVProposalUpdate
		if !slices.Contains([]string{"title", "speaker", "public_details", "audience_visibility"}, field) ||
			(matched.HasRuns && field != "public_details") {
			proposal.Classification = CSVProposalConflict
			proposal.Message = "imported structural or Run-protected changes require deliberate editing outside import"
		}
		proposals = append(proposals, proposal)
	}
	return proposals
}

func csvUpdateValue(
	field string,
	value string,
	matched store.CSVImportSession,
	state store.CSVImportState,
	location *time.Location,
) (string, string, SessionDraftInput, error) {
	draft := SessionDraftInput{ID: matched.ID, UpdateFields: []string{field}}
	current := matched.Draft
	switch field {
	case "title":
		draft.Title = value
		return current.Title, value, draft, nil
	case "speaker":
		draft.Speaker = value
		return current.Speaker, value, draft, nil
	case "public_details":
		draft.PublicDetails = value
		return current.PublicDetails, value, draft, nil
	case "type":
		draft.Type = SessionType(value)
		return current.Type, value, draft, nil
	case "audience_visibility":
		draft.AudienceVisibility = AudienceVisibility(value)
		return current.AudienceVisibility, value, draft, nil
	case "planned_start", "planned_end":
		parsed, err := parseCSVTime(value, location)
		if err != nil {
			return "", "", draft, err
		}
		if field == "planned_start" {
			draft.PlannedStart = parsed
			return current.PlannedStart.Format(time.RFC3339), parsed.Format(time.RFC3339), draft, nil
		}
		draft.PlannedEnd = parsed
		return current.PlannedEnd.Format(time.RFC3339), parsed.Format(time.RFC3339), draft, nil
	case "timing_policy":
		draft.TimingPolicy = TimingPolicy(value)
		return current.TimingPolicy, value, draft, nil
	case "minimum_duration":
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return "", "", draft, errors.New("minimum_duration must be a duration such as 30m")
		}
		draft.MinimumDuration = parsed
		return (time.Duration(current.MinimumDurationSeconds) * time.Second).String(), parsed.String(), draft, nil
	case "start_boundary":
		draft.StartBoundary = Boundary(value)
		return current.StartBoundary, value, draft, nil
	case "end_boundary":
		draft.EndBoundary = Boundary(value)
		return current.EndBoundary, value, draft, nil
	case "lane", "location", "track":
		return csvMembershipUpdate(field, value, matched, state, draft)
	default:
		return "", "", draft, errors.New("unsupported Session import field")
	}
}

func csvMembershipUpdate(
	field string,
	value string,
	matched store.CSVImportSession,
	state store.CSVImportState,
	draft SessionDraftInput,
) (string, string, SessionDraftInput, error) {
	var names map[string][]int
	var current []store.DraftTarget
	switch field {
	case "lane":
		names, current = state.LaneIDs, matched.Draft.Lanes
	case "location":
		names, current = state.LocationIDs, matched.Draft.Locations
	default:
		names, current = state.TrackIDs, matched.Draft.Tracks
	}
	resolved, err := resolveCSVTargets(value, names)
	if err != nil {
		return "", "", draft, err
	}
	currentIDs := targetRefIDs(current)
	resolvedIDs := targetRefIDsFromRundown(resolved)
	sort.Ints(currentIDs)
	sort.Ints(resolvedIDs)
	return fmt.Sprint(currentIDs), fmt.Sprint(resolvedIDs), draft, nil
}

func parseCSVTime(value string, location *time.Location) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, nil
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04", value, location)
	if err != nil {
		return time.Time{}, errors.New("must be RFC3339 or YYYY-MM-DD HH:MM in the Event timezone")
	}
	return parsed, nil
}

func resolveOptionalCSVTargets(value string, names map[string][]int) ([]TargetRef, error) {
	if value == "" {
		return nil, nil
	}
	return resolveCSVTargets(value, names)
}

func resolveCSVTargets(value string, names map[string][]int) ([]TargetRef, error) {
	parts := strings.Split(value, "|")
	result := make([]TargetRef, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		ids := names[name]
		if len(ids) != 1 {
			return nil, fmt.Errorf("%q must resolve to exactly one Draft target", name)
		}
		result = append(result, TargetRef{ID: ids[0]})
	}
	return result, nil
}

func selectedCSVImportDraft(input CSVImportInput, preview CSVImportPreview) (EditDraftInput, []string, error) {
	selected := make(map[string]struct{}, len(input.ProposalIDs))
	for _, id := range input.ProposalIDs {
		if id == "" {
			return EditDraftInput{}, nil, errors.New("proposal IDs must be non-empty")
		}
		selected[id] = struct{}{}
	}
	if len(selected) == 0 {
		return EditDraftInput{}, nil, errors.New("select at least one proposal")
	}
	result := EditDraftInput{
		EventID: input.EventID, CommandID: input.CommandID, ExpectedDraftRevision: input.ExpectedDraftRevision,
	}
	externalKeys := make([]string, 0)
	updates := make(map[int]SessionDraftInput)
	for _, proposal := range preview.Proposals {
		if _, wanted := selected[proposal.ID]; !wanted {
			continue
		}
		delete(selected, proposal.ID)
		switch proposal.Classification {
		case CSVProposalAddition:
			result.Sessions = append(result.Sessions, proposal.draft)
			externalKeys = append(externalKeys, proposal.ExternalKey)
		case CSVProposalUpdate:
			merged := updates[proposal.SessionID]
			merged.ID = proposal.SessionID
			mergeCSVSessionField(&merged, proposal.draft, proposal.Field)
			updates[proposal.SessionID] = merged
		default:
			return EditDraftInput{}, nil, errors.New("only additions and updates can be applied")
		}
	}
	if len(selected) > 0 {
		return EditDraftInput{}, nil, errors.New("proposal selection does not match preview")
	}
	ids := make([]int, 0, len(updates))
	for id := range updates {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		result.Sessions = append(result.Sessions, updates[id])
	}
	return result, externalKeys, nil
}

func mergeCSVSessionField(target *SessionDraftInput, source SessionDraftInput, field string) {
	target.UpdateFields = append(target.UpdateFields, field)
	switch field {
	case "title":
		target.Title = source.Title
	case "speaker":
		target.Speaker = source.Speaker
	case "public_details":
		target.PublicDetails = source.PublicDetails
	case "audience_visibility":
		target.AudienceVisibility = source.AudienceVisibility
	}
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func targetRefIDs(targets []store.DraftTarget) []int {
	result := make([]int, 0, len(targets))
	for _, target := range targets {
		result = append(result, target.ID)
	}
	return result
}

func targetRefIDsFromRundown(targets []TargetRef) []int {
	result := make([]int, 0, len(targets))
	for _, target := range targets {
		result = append(result, target.ID)
	}
	return result
}
