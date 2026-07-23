package rundown

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	maxICalendarEvents       = 10_000
	maxImportReferenceLength = 500
)

// ICalendarOccurrenceChoice resolves one repeated local time explicitly.
type ICalendarOccurrenceChoice struct {
	UID        string `json:"uid"`
	Occurrence string `json:"occurrence"`
	Property   string `json:"property"`
}

// ICalendarImportPreviewInput requests an intentionally limited iCalendar preview.
type ICalendarImportPreviewInput struct {
	EventID int                         `json:"event_id"`
	Data    string                      `json:"data"`
	Choices []ICalendarOccurrenceChoice `json:"choices,omitempty"`
}

// ICalendarImportPreview reports proposals, limitations, defaults, and time warnings.
type ICalendarImportPreview struct {
	DraftRevision      int                 `json:"draft_revision"`
	Fingerprint        string              `json:"fingerprint"`
	Proposals          []CSVImportProposal `json:"proposals"`
	Warnings           []string            `json:"warnings,omitempty"`
	UnsupportedFields  []string            `json:"unsupported_fields,omitempty"`
	AppliedDefaults    []string            `json:"applied_defaults,omitempty"`
	ValidationFailures []string            `json:"validation_failures,omitempty"`
}

// ICalendarImportInput confirms selected proposals from one exact preview.
type ICalendarImportInput struct {
	EventID               int                         `json:"event_id"`
	CommandID             string                      `json:"command_id"`
	ExpectedDraftRevision int                         `json:"expected_draft_revision"`
	Data                  string                      `json:"data"`
	Choices               []ICalendarOccurrenceChoice `json:"choices,omitempty"`
	Fingerprint           string                      `json:"fingerprint"`
	ProposalIDs           []string                    `json:"proposal_ids"`
}

// PreviewICalendarImport maps supported VEVENT facts into Draft proposals.
func (queries *Queries) PreviewICalendarImport(
	ctx context.Context,
	actor auth.Account,
	input ICalendarImportPreviewInput,
) (ICalendarImportPreview, error) {
	if !canReadEvent(actor, input.EventID) {
		return ICalendarImportPreview{}, ErrEventAccessDenied
	}
	state, err := queries.storage.LoadICalendarImportState(actor.Context(ctx), input.EventID)
	if err != nil {
		return ICalendarImportPreview{}, err
	}
	preview, previewErr := formICalendarImportPreview(state, input)
	if previewErr != nil {
		return ICalendarImportPreview{}, &ValidationError{Field: "icalendar_data", Message: previewErr.Error()}
	}
	return preview, nil
}

type icalendarImportOutcome struct {
	Result    *CSVImportResult `json:"result,omitempty"`
	Rejection *rejection       `json:"rejection,omitempty"`
}

// ImportICalendar atomically applies selected proposals to Draft state only.
func (commands *Commands) ImportICalendar(
	ctx context.Context,
	actor auth.Account,
	input ICalendarImportInput,
) (CSVImportResult, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return CSVImportResult{}, &ValidationError{Field: "command_id", Message: err.Error()}
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return CSVImportResult{}, errors.New("encode iCalendar Import command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(string(payload)), Action: "ImportICalendar",
		TargetType: "Event", TargetID: strconv.Itoa(input.EventID), Now: commands.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[CSVImportResult]{
		Storage: commands.storage, Identity: identity, Replay: decodeICalendarImportOutcome,
		Apply: func(transaction *store.CommandTx) (command.Execution[CSVImportResult], error) {
			if !actor.CanProduceEvent(input.EventID) {
				return rejectICalendarImport(rejection{Code: "event_access_denied", Message: ErrEventAccessDenied.Error()})
			}
			state, loadErr := transaction.LoadICalendarImportState(actor.Context(ctx), input.EventID)
			if loadErr != nil {
				return command.Execution[CSVImportResult]{}, loadErr
			}
			preview, previewErr := formICalendarImportPreview(state, ICalendarImportPreviewInput{
				EventID: input.EventID, Data: input.Data, Choices: input.Choices,
			})
			if previewErr != nil {
				return rejectICalendarImport(rejection{Code: "validation", Field: "icalendar_data", Message: previewErr.Error()})
			}
			if state.DraftRevision != input.ExpectedDraftRevision || preview.Fingerprint != input.Fingerprint {
				return rejectICalendarImport(rejection{Code: "draft_revision_conflict", Message: ErrDraftRevisionConflict.Error()})
			}
			if len(preview.ValidationFailures) > 0 {
				return rejectICalendarImport(rejection{Code: "validation", Field: "preview", Message: preview.ValidationFailures[0]})
			}
			draftInput, externalKeys, selectionErr := selectedCSVImportDraft(CSVImportInput{
				EventID: input.EventID, CommandID: input.CommandID,
				ExpectedDraftRevision: input.ExpectedDraftRevision, ProposalIDs: input.ProposalIDs,
			}, CSVImportPreview{Proposals: preview.Proposals})
			if selectionErr != nil {
				return rejectICalendarImport(rejection{Code: "validation", Field: "proposal_ids", Message: selectionErr.Error()})
			}
			normalized, validationErr := validateEditDraft(draftInput)
			if validationErr != nil {
				var invalid *ValidationError
				_ = errors.As(validationErr, &invalid)
				return rejectICalendarImport(rejection{Code: "validation", Field: invalid.Field, Message: invalid.Message})
			}
			stored, editErr := transaction.EditDraft(actor.Context(ctx), editDraftParams(actor.ID, normalized, identity.Now))
			if editErr != nil {
				return command.Execution[CSVImportResult]{}, editErr
			}
			if referenceErr := transaction.CreateICalendarImportReferences(
				actor.Context(ctx), input.EventID, externalKeys, stored.Changes, identity.Now,
			); referenceErr != nil {
				return command.Execution[CSVImportResult]{}, referenceErr
			}
			result := CSVImportResult{DraftRevision: stored.DraftRevision, Changes: editDraftResult(stored).Changes}
			encoded, encodeErr := json.Marshal(icalendarImportOutcome{Result: &result})
			if encodeErr != nil {
				return command.Execution[CSVImportResult]{}, errors.New("encode iCalendar Import outcome")
			}
			return command.Success(result, string(encoded)), nil
		},
	})
}

func rejectICalendarImport(rejected rejection) (command.Execution[CSVImportResult], error) {
	encoded, err := json.Marshal(icalendarImportOutcome{Rejection: &rejected})
	if err != nil {
		return command.Execution[CSVImportResult]{}, errors.New("encode rejected iCalendar Import outcome")
	}
	return command.RejectEncoded(CSVImportResult{}, string(encoded), rejectionError(rejected)), nil
}

func decodeICalendarImportOutcome(encoded string) (CSVImportResult, error) {
	var outcome icalendarImportOutcome
	if err := json.Unmarshal([]byte(encoded), &outcome); err != nil {
		return CSVImportResult{}, errors.New("decode iCalendar Import Command Receipt")
	}
	if outcome.Rejection != nil {
		return CSVImportResult{}, rejectionError(*outcome.Rejection)
	}
	if outcome.Result == nil {
		return CSVImportResult{}, errors.New("iCalendar Import Command Receipt has no outcome")
	}
	return *outcome.Result, nil
}

type icalProperty struct {
	name   string
	params map[string]string
	value  string
}

type icalEvent struct {
	properties []icalProperty
}

func formICalendarImportPreview(
	state store.CSVImportState,
	input ICalendarImportPreviewInput,
) (ICalendarImportPreview, error) {
	calendarTimezone, events, unsupported, err := parseICalendar(input.Data)
	if err != nil {
		return ICalendarImportPreview{}, err
	}
	preview := ICalendarImportPreview{
		DraftRevision: state.DraftRevision, UnsupportedFields: unsupported,
		AppliedDefaults: []string{
			"Session type defaults to Presentation", "Audience Visibility defaults to Public",
			"Timing Policy defaults to Fixed End", "boundaries default to Soft",
		},
	}
	choices := make(map[string]string, len(input.Choices))
	for _, choice := range input.Choices {
		if choice.Occurrence != "Earlier" && choice.Occurrence != "Later" {
			preview.ValidationFailures = append(preview.ValidationFailures, "occurrence choices must be Earlier or Later")
			continue
		}
		if choice.Property != "DTSTART" && choice.Property != "DTEND" {
			preview.ValidationFailures = append(preview.ValidationFailures, "occurrence choice properties must be DTSTART or DTEND")
			continue
		}
		key := occurrenceChoiceKey(choice.UID, choice.Property)
		if choices[key] != "" {
			preview.ValidationFailures = append(preview.ValidationFailures, "occurrence choices require unique UID and property pairs")
			continue
		}
		choices[key] = choice.Occurrence
	}
	seenUIDs := make(map[string]int, len(events))
	for index, event := range events {
		if uid := icalValue(event, "UID"); uid != "" {
			seenUIDs[uid]++
		}
		proposals, warnings := previewICalendarEvent(state, calendarTimezone, event, choices, index+1)
		preview.Proposals = append(preview.Proposals, proposals...)
		preview.Warnings = append(preview.Warnings, warnings...)
	}
	for uid, count := range seenUIDs {
		if count > 1 {
			preview.ValidationFailures = append(preview.ValidationFailures, "duplicate Import Reference in iCalendar: "+uid)
		}
	}
	sort.Strings(preview.UnsupportedFields)
	preview.UnsupportedFields = slices.Compact(preview.UnsupportedFields)
	sort.Strings(preview.Warnings)
	preview.Warnings = slices.Compact(preview.Warnings)
	sort.Strings(preview.ValidationFailures)
	type fingerprintProposal struct {
		Proposal CSVImportProposal `json:"proposal"`
		Draft    SessionDraftInput `json:"draft"`
	}
	fingerprintProposals := make([]fingerprintProposal, 0, len(preview.Proposals))
	for _, proposal := range preview.Proposals {
		fingerprintProposals = append(fingerprintProposals, fingerprintProposal{Proposal: proposal, Draft: proposal.draft})
	}
	material, marshalErr := json.Marshal(struct {
		EventRevision int                         `json:"event_revision"`
		Timezone      string                      `json:"timezone"`
		Data          string                      `json:"data"`
		Choices       []ICalendarOccurrenceChoice `json:"choices"`
		Proposals     []fingerprintProposal       `json:"proposals"`
		Warnings      []string                    `json:"warnings"`
	}{state.EventRevision, state.Timezone, input.Data, input.Choices, fingerprintProposals, preview.Warnings})
	if marshalErr != nil {
		return ICalendarImportPreview{}, errors.New("encode iCalendar preview fingerprint")
	}
	preview.Fingerprint = command.PayloadHash(strconv.Itoa(state.DraftRevision), string(material))
	return preview, nil
}

func previewICalendarEvent(
	state store.CSVImportState,
	calendarTimezone string,
	event icalEvent,
	choices map[string]string,
	index int,
) ([]CSVImportProposal, []string) {
	uid := icalValue(event, "UID")
	base := CSVImportProposal{
		ID: fmt.Sprintf("event:%d:add", index), RowNumber: index,
		RecordType: "Session", ExternalKey: uid,
	}
	if uid == "" {
		base.Classification = CSVProposalUnresolved
		base.Message = "VEVENT UID is required"
		return []CSVImportProposal{base}, nil
	}
	if utf8.RuneCountInString(uid) > maxImportReferenceLength {
		base.Classification = CSVProposalUnresolved
		base.Message = fmt.Sprintf("VEVENT UID must not exceed %d characters", maxImportReferenceLength)
		return []CSVImportProposal{base}, nil
	}
	start, startWarnings, startErr := resolveICalendarTime(
		icalPropertyValue(event, "DTSTART"), icalValue(event, "TZID"), calendarTimezone,
		state.Timezone, choices[occurrenceChoiceKey(uid, "DTSTART")],
	)
	end, endWarnings, endErr := resolveICalendarTime(
		icalPropertyValue(event, "DTEND"), icalValue(event, "TZID"), calendarTimezone,
		state.Timezone, choices[occurrenceChoiceKey(uid, "DTEND")],
	)
	warnings := slices.Concat(startWarnings, endWarnings)
	if startErr != nil || endErr != nil {
		base.Classification = CSVProposalUnresolved
		base.Message = errors.Join(startErr, endErr).Error()
		return []CSVImportProposal{base}, warnings
	}
	values := map[string]string{
		"external_key": uid, "title": unescapeICalText(icalValue(event, "SUMMARY")),
		"speaker": attendeeNames(event), "public_details": unescapeICalText(icalValue(event, "DESCRIPTION")),
		"planned_start": start.Format(time.RFC3339), "planned_end": end.Format(time.RFC3339),
		"lane":  unescapeICalText(icalValue(event, "LOCATION")),
		"track": unescapeICalText(icalValue(event, "CATEGORIES")),
	}
	proposals := previewCSVRow(state, values, index, time.UTC)
	if len(proposals) == 0 {
		base.Classification = CSVProposalUpdate
		base.Message = "Import Reference matches with no changed supported fields"
		return []CSVImportProposal{base}, warnings
	}
	return proposals, warnings
}

func occurrenceChoiceKey(uid, property string) string {
	return uid + "\x00" + property
}

func parseICalendar(data string) (string, []icalEvent, []string, error) {
	lines := unfoldICalendarLines(data)
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "BEGIN:VCALENDAR" {
		return "", nil, nil, errors.New("iCalendar must begin with VCALENDAR")
	}
	calendarTimezone := ""
	events := make([]icalEvent, 0)
	unsupported := make([]string, 0)
	var current *icalEvent
	calendarEnded := false
	for index, line := range lines {
		property, err := parseICalendarProperty(line)
		if err != nil {
			return "", nil, nil, err
		}
		if calendarEnded {
			return "", nil, nil, errors.New("iCalendar contains data after VCALENDAR end")
		}
		switch {
		case property.name == "BEGIN" && property.value == "VCALENDAR" && index != 0:
			return "", nil, nil, errors.New("iCalendar contains nested VCALENDAR")
		case property.name == "BEGIN" && property.value == "VEVENT":
			if current != nil {
				return "", nil, nil, errors.New("iCalendar contains nested VEVENT")
			}
			if len(events) >= maxICalendarEvents {
				return "", nil, nil, fmt.Errorf("iCalendar must not exceed %d events", maxICalendarEvents)
			}
			current = &icalEvent{}
		case property.name == "END" && property.value == "VEVENT":
			if current == nil {
				return "", nil, nil, errors.New("iCalendar VEVENT end has no beginning")
			}
			events = append(events, *current)
			current = nil
		case property.name == "END" && property.value == "VCALENDAR":
			if current != nil {
				return "", nil, nil, errors.New("iCalendar VCALENDAR ends before VEVENT")
			}
			calendarEnded = true
		case current != nil:
			current.properties = append(current.properties, property)
			if !slices.Contains([]string{
				"UID", "SUMMARY", "DESCRIPTION", "DTSTART", "DTEND", "LOCATION", "CATEGORIES", "ATTENDEE", "TZID",
			}, property.name) {
				unsupported = append(unsupported, property.name)
			}
		case property.name == "X-WR-TIMEZONE":
			calendarTimezone = property.value
		}
	}
	if current != nil {
		return "", nil, nil, errors.New("iCalendar VEVENT is not closed")
	}
	if !calendarEnded {
		return "", nil, nil, errors.New("iCalendar VCALENDAR is not closed")
	}
	return calendarTimezone, events, unsupported, nil
}

func unfoldICalendarLines(data string) []string {
	normalized := strings.ReplaceAll(strings.ReplaceAll(data, "\r\n", "\n"), "\r", "\n")
	physical := strings.Split(normalized, "\n")
	lines := make([]string, 0, len(physical))
	for _, line := range physical {
		if line == "" {
			continue
		}
		if (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) && len(lines) > 0 {
			lines[len(lines)-1] += line[1:]
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func parseICalendarProperty(line string) (icalProperty, error) {
	left, value, found := strings.Cut(line, ":")
	if !found {
		return icalProperty{}, errors.New("iCalendar property is missing ':'")
	}
	parts := strings.Split(left, ";")
	property := icalProperty{name: strings.ToUpper(parts[0]), params: make(map[string]string), value: value}
	for _, raw := range parts[1:] {
		key, parameter, exists := strings.Cut(raw, "=")
		if exists {
			property.params[strings.ToUpper(key)] = strings.Trim(parameter, `"`)
		}
	}
	return property, nil
}

func icalValue(event icalEvent, name string) string {
	return icalPropertyValue(event, name).value
}

func icalPropertyValue(event icalEvent, name string) icalProperty {
	for _, property := range event.properties {
		if property.name == name {
			return property
		}
	}
	return icalProperty{}
}

func attendeeNames(event icalEvent) string {
	names := make([]string, 0)
	for _, property := range event.properties {
		if property.name == "ATTENDEE" && property.params["CN"] != "" {
			names = append(names, unescapeICalText(property.params["CN"]))
		}
	}
	return strings.Join(names, ", ")
}

func unescapeICalText(value string) string {
	replacer := strings.NewReplacer(`\n`, "\n", `\N`, "\n", `\,`, ",", `\;`, ";", `\\`, `\`)
	return replacer.Replace(value)
}

func resolveICalendarTime(
	property icalProperty,
	eventTimezone, calendarTimezone, fallbackTimezone, occurrence string,
) (time.Time, []string, error) {
	if property.value == "" {
		return time.Time{}, nil, errors.New("VEVENT requires DTSTART and DTEND")
	}
	if strings.HasSuffix(property.value, "Z") {
		parsed, err := time.Parse("20060102T150405Z", property.value)
		if err != nil {
			return time.Time{}, nil, errors.New("UTC iCalendar time must use YYYYMMDDTHHMMSSZ")
		}
		return parsed, nil, nil
	}
	zoneName := property.params["TZID"]
	warnings := make([]string, 0)
	if zoneName == "" {
		zoneName = eventTimezone
	}
	if zoneName == "" {
		zoneName = calendarTimezone
	}
	if zoneName == "" {
		zoneName = fallbackTimezone
		warnings = append(warnings, "floating iCalendar time uses Event timezone "+fallbackTimezone)
	}
	location, err := time.LoadLocation(zoneName)
	if err != nil && calendarTimezone != "" && zoneName != calendarTimezone {
		warnings = append(warnings, "unsupported TZID "+zoneName+" uses calendar timezone "+calendarTimezone)
		zoneName = calendarTimezone
		location, err = time.LoadLocation(zoneName)
	}
	if err != nil {
		return time.Time{}, warnings, fmt.Errorf("TZID %q cannot be resolved", zoneName)
	}
	local, err := time.Parse("20060102T150405", property.value)
	if err != nil {
		return time.Time{}, warnings, errors.New("local iCalendar time must use YYYYMMDDTHHMMSS")
	}
	candidates := localTimeCandidates(local, location)
	switch len(candidates) {
	case 0:
		return time.Time{}, warnings, fmt.Errorf("local time %s does not exist in %s", property.value, zoneName)
	case 1:
		return candidates[0], warnings, nil
	default:
		if occurrence == "Earlier" {
			return candidates[0], warnings, nil
		}
		if occurrence == "Later" {
			return candidates[len(candidates)-1], warnings, nil
		}
		return time.Time{}, warnings, fmt.Errorf("local time %s is ambiguous in %s; choose Earlier or Later", property.value, zoneName)
	}
}

func localTimeCandidates(local time.Time, location *time.Location) []time.Time {
	year, month, day := local.Date()
	hour, minute, second := local.Clock()
	center := time.Date(year, month, day, hour, minute, second, 0, location)
	result := make([]time.Time, 0, 2)
	offsets := make(map[int]struct{})
	for hourOffset := -24; hourOffset <= 24; hourOffset++ {
		_, offset := center.Add(time.Duration(hourOffset) * time.Hour).Zone()
		offsets[offset] = struct{}{}
	}
	wallUTC := time.Date(year, month, day, hour, minute, second, 0, time.UTC)
	for offset := range offsets {
		candidate := wallUTC.Add(-time.Duration(offset) * time.Second)
		localized := candidate.In(location)
		if localized.Year() == year && localized.Month() == month && localized.Day() == day &&
			localized.Hour() == hour && localized.Minute() == minute && localized.Second() == second {
			result = append(result, candidate)
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Before(result[right]) })
	result = slices.CompactFunc(result, func(left, right time.Time) bool { return left.Equal(right) })
	return result
}
