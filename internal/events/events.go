// Package events creates and authorizes Beamers Events.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/language"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/displayviews"
	"github.com/dotwaffle/beamers/internal/store"
)

var (
	// ErrAdministratorRequired means an Event administration action lacked
	// installation-wide Administrator authority.
	ErrAdministratorRequired = errors.New("administrator authority required")
	// ErrGrantRoleRequired means a Grant requested a role not yet supported by Event commands.
	ErrGrantRoleRequired = errors.New("role must be Producer, Operator, or Observer")
	// ErrEventNotFound means an Event Grant targeted an unknown Event.
	ErrEventNotFound = store.ErrEventNotFound
	// ErrAccountNotFound means an Event Grant targeted an unknown or disabled Account.
	ErrAccountNotFound = store.ErrAccountNotFound
	// ErrEventGrantExists means an Account already has an Event role.
	ErrEventGrantExists = store.ErrEventGrantExists
	// ErrEventAccessDenied means an Account has no role for the Event.
	ErrEventAccessDenied = store.ErrEventAccessDenied
	// ErrCommandConflict means a Command ID was reused for different work.
	ErrCommandConflict = store.ErrCommandConflict
	// ErrRevisionConflict means an Event update expected an outdated revision.
	ErrRevisionConflict = store.ErrRevisionConflict
)

// ValidationError describes one actionable invalid Event field.
type ValidationError struct {
	Field   string
	Message string
}

// Error implements error.
func (err *ValidationError) Error() string {
	return err.Field + ": " + err.Message
}

// Event is an Event's core configuration.
type Event struct {
	ID               int    `json:"id"`
	Name             string `json:"name"`
	PlannedStartDate string `json:"planned_start_date"`
	PlannedEndDate   string `json:"planned_end_date"`
	Timezone         string `json:"timezone"`
	EventLocale      string `json:"event_locale"`
	ContentLanguage  string `json:"content_language,omitempty"`
	EventDayBoundary string `json:"event_day_boundary"`
	Revision         int    `json:"revision"`
}

// CreateInput contains an Administrator's proposed Event configuration.
type CreateInput struct {
	Name                           string `json:"name"`
	PlannedStartDate               string `json:"planned_start_date"`
	PlannedEndDate                 string `json:"planned_end_date"`
	Timezone                       string `json:"timezone"`
	EventLocale                    string `json:"event_locale"`
	ContentLanguage                string `json:"content_language"`
	EventDayBoundary               string `json:"event_day_boundary"`
	TargetAdjustmentPresetsSeconds []int  `json:"target_adjustment_presets_seconds,omitempty"`
	CommandID                      string `json:"command_id"`
	ExpectedRevision               int    `json:"expected_revision,omitempty"`
}

// Grant is an Account's role for one Event.
type Grant struct {
	EventID          int      `json:"event_id"`
	AccountID        int      `json:"account_id"`
	Role             string   `json:"role"`
	LaneIDs          []int    `json:"lane_ids,omitempty"`
	DisplayGroupKeys []string `json:"display_group_keys,omitempty"`
	Capabilities     []string `json:"capabilities,omitempty"`
}

// GrantInput is one Event role and its explicit scopes.
type GrantInput struct {
	AccountID        int      `json:"account_id"`
	Role             string   `json:"role"`
	LaneIDs          []int    `json:"lane_ids,omitempty"`
	DisplayGroupKeys []string `json:"display_group_keys,omitempty"`
	Capabilities     []string `json:"capabilities,omitempty"`
	CommandID        string   `json:"command_id"`
}

// DisplayConfigurationInput replaces one Event's controlled Display presentation.
type DisplayConfigurationInput struct {
	displayviews.Configuration
	ExpectedEventRevision int    `json:"expected_event_revision"`
	CommandID             string `json:"command_id"`
}

// DisplayConfiguration is one Event's committed Display presentation.
type DisplayConfiguration struct {
	EventID       int `json:"event_id"`
	EventRevision int `json:"event_revision"`
	displayviews.Configuration
}

// Service owns Event commands and authorization.
type Service struct {
	storage *store.SQLite
	now     func() time.Time
}

// New creates an Event Service with explicit dependencies.
func New(storage *store.SQLite, now func() time.Time) (*Service, error) {
	if storage == nil {
		return nil, errors.New("Event storage is required")
	}
	if now == nil {
		return nil, errors.New("Event clock is required")
	}
	return &Service{storage: storage, now: now}, nil
}

// Create validates and commits an Event for an Administrator.
func (service *Service) Create(
	ctx context.Context,
	actor auth.Account,
	input CreateInput,
) (Event, error) {
	payloadHash := eventPayloadHash(input, input.ExpectedRevision)
	if err := command.ValidateID(input.CommandID); err != nil {
		return Event{}, invalid("command_id", err.Error())
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID, PayloadHash: payloadHash,
		Action: "CreateEvent", TargetType: "Event", TargetID: "unidentified", Now: service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[Event]{
		Storage: service.storage, Identity: identity, Replay: replayEvent,
		Apply: func(transaction *store.CommandTx) (command.Execution[Event], error) {
			if !actor.Administrator {
				return eventRejection[Event](ErrAdministratorRequired), nil
			}
			normalized, validationErr := validateCreateInput(input)
			if validationErr != nil {
				return eventRejection[Event](validationErr), nil
			}
			created, createErr := transaction.CreateEvent(actor.Context(ctx), store.CreateEventParams{
				ActorAccountID: actor.ID, Name: normalized.Name,
				PlannedStartDate: normalized.PlannedStartDate, PlannedEndDate: normalized.PlannedEndDate,
				Timezone: normalized.Timezone, EventLocale: normalized.EventLocale,
				ContentLanguage: normalized.ContentLanguage, EventDayBoundary: normalized.EventDayBoundary,
				TargetAdjustmentPresetsSeconds: normalized.TargetAdjustmentPresetsSeconds,
				Now:                            identity.Now,
				CommandID:                      input.CommandID,
				PayloadHash:                    eventPayloadHash(normalized, 0),
			})
			if createErr != nil {
				return command.Execution[Event]{}, createErr
			}
			return eventSuccess(event(created), created, strconv.Itoa(created.ID), "encode Event creation outcome")
		},
	})
}

// GrantEventAccess gives an Account unscoped authority for one Event.
func (service *Service) GrantEventAccess(
	ctx context.Context,
	actor auth.Account,
	eventID int,
	accountID int,
	role string,
	commandID string,
) (Grant, error) {
	return service.GrantScopedEventAccess(ctx, actor, eventID, GrantInput{
		AccountID: accountID, Role: role, CommandID: commandID,
	})
}

// GrantScopedEventAccess gives an Account an Event role with explicit scopes.
func (service *Service) GrantScopedEventAccess(
	ctx context.Context,
	actor auth.Account,
	eventID int,
	input GrantInput,
) (Grant, error) {
	payloadHash, err := grantPayloadHash(eventID, input)
	if err != nil {
		return Grant{}, err
	}
	targetID := strconv.Itoa(eventID) + ":" + strconv.Itoa(input.AccountID)
	if validationErr := command.ValidateID(input.CommandID); validationErr != nil {
		return Grant{}, invalid("command_id", validationErr.Error())
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID, PayloadHash: payloadHash,
		Action: "CreateEventGrant", TargetType: "EventGrant", TargetID: targetID, Now: service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[Grant]{
		Storage: service.storage, Identity: identity, Replay: replayGrant,
		Apply: func(transaction *store.CommandTx) (command.Execution[Grant], error) {
			if !actor.Administrator {
				return eventRejection[Grant](ErrAdministratorRequired), nil
			}
			normalized, validationErr := validateGrantInput(input)
			if validationErr != nil {
				return eventRejection[Grant](validationErr), nil
			}
			created, createErr := transaction.GrantEventAccess(actor.Context(ctx), store.GrantEventAccessParams{
				ActorAccountID:   actor.ID,
				EventID:          eventID,
				AccountID:        normalized.AccountID,
				Role:             normalized.Role,
				LaneIDs:          normalized.LaneIDs,
				DisplayGroupKeys: normalized.DisplayGroupKeys,
				Capabilities:     normalized.Capabilities,
				Now:              identity.Now,
				CommandID:        input.CommandID,
				PayloadHash:      payloadHash,
			})
			if createErr != nil {
				if errors.Is(createErr, ErrEventNotFound) || errors.Is(createErr, ErrAccountNotFound) || errors.Is(createErr, ErrEventGrantExists) {
					return eventRejection[Grant](createErr), nil
				}
				return command.Execution[Grant]{}, createErr
			}
			return eventSuccess(grant(created), created, "", "encode Event Grant outcome")
		},
	})
}

// CrewEvent returns Event crew data only through an explicit Event Grant.
func (service *Service) CrewEvent(
	ctx context.Context,
	actor auth.Account,
	eventID int,
) (Event, error) {
	found, err := service.storage.FindCrewEvent(actor.Context(ctx), actor.ID, eventID)
	if err != nil {
		return Event{}, err
	}
	return event(found), nil
}

// Update replaces Event configuration for a Producer.
func (service *Service) Update(
	ctx context.Context,
	actor auth.Account,
	eventID int,
	input CreateInput,
) (Event, error) {
	payloadHash := eventPayloadHash(input, input.ExpectedRevision)
	targetID := strconv.Itoa(eventID)
	if err := command.ValidateID(input.CommandID); err != nil {
		return Event{}, invalid("command_id", err.Error())
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID, PayloadHash: payloadHash,
		Action: "UpdateEvent", TargetType: "Event", TargetID: targetID, Now: service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[Event]{
		Storage: service.storage, Identity: identity, Replay: replayEvent,
		Apply: func(transaction *store.CommandTx) (command.Execution[Event], error) {
			if !actor.CanProduceEvent(eventID) {
				return eventRejection[Event](ErrEventAccessDenied), nil
			}
			normalized, validationErr := validateCreateInput(input)
			if validationErr != nil {
				return eventRejection[Event](validationErr), nil
			}
			if input.ExpectedRevision <= 0 {
				validation := invalid("expected_revision", "must be a positive Event revision")
				return eventRejection[Event](validation), nil
			}
			updated, updateErr := transaction.UpdateEvent(actor.Context(ctx), store.UpdateEventParams{
				ActorAccountID: actor.ID, EventID: eventID, Name: normalized.Name,
				PlannedStartDate: normalized.PlannedStartDate, PlannedEndDate: normalized.PlannedEndDate,
				Timezone: normalized.Timezone, EventLocale: normalized.EventLocale,
				ContentLanguage: normalized.ContentLanguage, EventDayBoundary: normalized.EventDayBoundary,
				TargetAdjustmentPresetsSeconds: normalized.TargetAdjustmentPresetsSeconds,
				Now:                            identity.Now,
				CommandID:                      input.CommandID, PayloadHash: eventPayloadHash(normalized, input.ExpectedRevision),
				ExpectedRevision: input.ExpectedRevision,
			})
			if errors.Is(updateErr, ErrRevisionConflict) {
				return eventRejection[Event](updateErr), nil
			}
			if updateErr != nil {
				return command.Execution[Event]{}, updateErr
			}
			return eventSuccess(event(updated), updated, "", "encode Event update outcome")
		},
	})
}

// DisplayConfiguration returns one Event's committed Display presentation.
func (service *Service) DisplayConfiguration(
	ctx context.Context,
	actor auth.Account,
	eventID int,
) (DisplayConfiguration, error) {
	found, err := service.storage.FindDisplayConfiguration(actor.Context(ctx), actor.ID, eventID)
	if err != nil {
		return DisplayConfiguration{}, err
	}
	return displayConfiguration(found)
}

// ConfigureDisplays validates and replaces one Event's Display presentation.
func (service *Service) ConfigureDisplays(
	ctx context.Context,
	actor auth.Account,
	eventID int,
	input DisplayConfigurationInput,
) (DisplayConfiguration, error) {
	input.Configuration = displayviews.NormalizeConfiguration(input.Configuration)
	if err := command.ValidateID(input.CommandID); err != nil {
		return DisplayConfiguration{}, invalid("command_id", err.Error())
	}
	if input.ExpectedEventRevision <= 0 {
		return DisplayConfiguration{}, invalid(
			"expected_event_revision",
			"must be a positive Event revision",
		)
	}
	if validationErr := displayviews.ValidateConfiguration(input.Configuration); validationErr != nil {
		var configurationValidation *displayviews.ValidationError
		if !errors.As(validationErr, &configurationValidation) {
			return DisplayConfiguration{}, validationErr
		}
		return DisplayConfiguration{}, invalid(configurationValidation.Field, configurationValidation.Message)
	}
	encodedConfiguration, err := json.Marshal(input.Configuration)
	if err != nil {
		return DisplayConfiguration{}, errors.New("encode Display configuration")
	}
	payloadHash := command.PayloadHash(
		strconv.Itoa(eventID),
		strconv.Itoa(input.ExpectedEventRevision),
		string(encodedConfiguration),
	)
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID,
		CommandID:      input.CommandID,
		PayloadHash:    payloadHash,
		Action:         "ConfigureDisplays",
		TargetType:     "Event",
		TargetID:       strconv.Itoa(eventID),
		Now:            service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[DisplayConfiguration]{
		Storage:  service.storage,
		Identity: identity,
		Replay:   replayDisplayConfiguration,
		Apply: func(transaction *store.CommandTx) (command.Execution[DisplayConfiguration], error) {
			if !actor.CanProduceEvent(eventID) {
				return eventRejection[DisplayConfiguration](ErrEventAccessDenied), nil
			}
			updated, updateErr := transaction.UpdateDisplayConfiguration(
				actor.Context(ctx),
				store.UpdateDisplayConfigurationParams{
					EventID:               eventID,
					ExpectedEventRevision: input.ExpectedEventRevision,
					Configuration:         string(encodedConfiguration),
				},
			)
			if errors.Is(updateErr, ErrRevisionConflict) {
				return eventRejection[DisplayConfiguration](updateErr), nil
			}
			if updateErr != nil {
				return command.Execution[DisplayConfiguration]{}, updateErr
			}
			result, decodeErr := displayConfiguration(updated)
			if decodeErr != nil {
				return command.Execution[DisplayConfiguration]{}, decodeErr
			}
			encodedOutcome, encodeErr := json.Marshal(updated)
			if encodeErr != nil {
				return command.Execution[DisplayConfiguration]{}, errors.New(
					"encode Display configuration outcome",
				)
			}
			return command.Success(result, string(encodedOutcome)), nil
		},
	})
}

func replayEvent(outcome string) (Event, error) {
	var original store.Event
	if err := store.DecodeCommandReceipt(outcome, &original); err != nil {
		return Event{}, restoreRejected(err)
	}
	return event(original), nil
}

func replayDisplayConfiguration(outcome string) (DisplayConfiguration, error) {
	var original store.DisplayConfigurationState
	if err := store.DecodeCommandReceipt(outcome, &original); err != nil {
		return DisplayConfiguration{}, restoreRejected(err)
	}
	return displayConfiguration(original)
}

func replayGrant(outcome string) (Grant, error) {
	var original store.EventGrant
	if err := store.DecodeCommandReceipt(outcome, &original); err != nil {
		return Grant{}, restoreRejected(err)
	}
	return grant(original), nil
}

func eventRejection[T any](reason error) command.Execution[T] {
	rejection := commandRejection(reason)
	var zero T
	return command.Reject(zero, rejection, reason)
}

func eventSuccess[T any](value T, stored any, targetID, description string) (command.Execution[T], error) {
	encoded, err := json.Marshal(stored)
	if err != nil {
		return command.Execution[T]{}, errors.New(description)
	}
	execution := command.Success(value, string(encoded))
	if targetID != "" {
		execution = execution.WithTargetID(targetID)
	}
	return execution, nil
}

func validateCreateInput(input CreateInput) (CreateInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 200 || containsControl(input.Name) {
		return CreateInput{}, invalid("name", "must be 1 to 200 characters without control characters")
	}
	start, err := parseDate("planned_start_date", input.PlannedStartDate)
	if err != nil {
		return CreateInput{}, err
	}
	end, err := parseDate("planned_end_date", input.PlannedEndDate)
	if err != nil {
		return CreateInput{}, err
	}
	if end.Before(start) {
		return CreateInput{}, invalid("planned_end_date", "must be on or after planned_start_date")
	}
	if input.Timezone == "" {
		return CreateInput{}, invalid("timezone", "must be an IANA timezone such as Europe/Berlin")
	}
	if input.Timezone == "Local" || strings.HasPrefix(input.Timezone, "/") || strings.Contains(input.Timezone, "\\") {
		return CreateInput{}, invalid("timezone", "must be a recognized IANA timezone such as Europe/Berlin")
	}
	if _, locationErr := time.LoadLocation(input.Timezone); locationErr != nil {
		return CreateInput{}, invalid("timezone", "must be a recognized IANA timezone such as Europe/Berlin")
	}
	input.EventLocale, err = parseLanguageTag("event_locale", input.EventLocale, false)
	if err != nil {
		return CreateInput{}, err
	}
	input.ContentLanguage, err = parseLanguageTag("content_language", input.ContentLanguage, true)
	if err != nil {
		return CreateInput{}, err
	}
	if input.EventDayBoundary == "" {
		input.EventDayBoundary = "00:00"
	}
	boundary, err := time.Parse("15:04", input.EventDayBoundary)
	if err != nil || boundary.Format("15:04") != input.EventDayBoundary {
		return CreateInput{}, invalid("event_day_boundary", "must be a 24-hour local time in HH:MM form")
	}
	if input.TargetAdjustmentPresetsSeconds == nil {
		input.TargetAdjustmentPresetsSeconds = []int{-300, 300, 600}
	}
	if len(input.TargetAdjustmentPresetsSeconds) > 12 {
		return CreateInput{}, invalid("target_adjustment_presets_seconds", "must contain no more than 12 presets")
	}
	seenPresets := make(map[int]struct{}, len(input.TargetAdjustmentPresetsSeconds))
	for _, seconds := range input.TargetAdjustmentPresetsSeconds {
		if seconds == 0 || seconds < -86400 || seconds > 86400 {
			return CreateInput{}, invalid("target_adjustment_presets_seconds", "values must be non-zero and no more than 86400 seconds")
		}
		if _, exists := seenPresets[seconds]; exists {
			return CreateInput{}, invalid("target_adjustment_presets_seconds", "values must be unique")
		}
		seenPresets[seconds] = struct{}{}
	}
	return input, nil
}

// ResolveDayBoundary resolves one Event day's configured wall time. A gap uses
// the first valid minute after the jump; a repetition uses the later occurrence.
func ResolveDayBoundary(date time.Time, location *time.Location, boundary string) (time.Time, error) {
	if location == nil {
		return time.Time{}, errors.New("Event timezone is required")
	}
	parsed, err := time.Parse("15:04", boundary)
	if err != nil || parsed.Format("15:04") != boundary {
		return time.Time{}, invalid("event_day_boundary", "must be a 24-hour local time in HH:MM form")
	}
	year, month, day := date.Date()
	targetMinute := parsed.Hour()*60 + parsed.Minute()
	start := time.Date(year, month, day, 12, 0, 0, 0, location).Add(-18 * time.Hour)
	end := start.Add(36 * time.Hour)
	var laterOccurrence time.Time
	var firstAfterGap time.Time
	for instant := start; !instant.After(end); instant = instant.Add(time.Minute) {
		local := instant.In(location)
		localYear, localMonth, localDay := local.Date()
		if localYear != year || localMonth != month || localDay != day || local.Second() != 0 {
			continue
		}
		localMinute := local.Hour()*60 + local.Minute()
		if localMinute == targetMinute {
			laterOccurrence = instant
		}
		if localMinute > targetMinute && firstAfterGap.IsZero() {
			firstAfterGap = instant
		}
	}
	if !laterOccurrence.IsZero() {
		return laterOccurrence, nil
	}
	if !firstAfterGap.IsZero() {
		return firstAfterGap, nil
	}
	return time.Time{}, errors.New("Event Day Boundary cannot be resolved on the requested date")
}

func parseDate(field, value string) (time.Time, error) {
	parsed, err := time.Parse(time.DateOnly, value)
	if err != nil || parsed.Format(time.DateOnly) != value {
		return time.Time{}, invalid(field, "must be a calendar date in YYYY-MM-DD form")
	}
	return parsed, nil
}

func parseLanguageTag(field, value string, optional bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" && optional {
		return "", nil
	}
	if strings.ContainsAny(value, "_ \t\r\n") {
		return "", invalid(field, "must be a recognized BCP 47 language tag such as en-GB")
	}
	tag, err := language.Parse(value)
	if err != nil || tag == language.Und {
		return "", invalid(field, "must be a recognized BCP 47 language tag such as en-GB")
	}
	return tag.String(), nil
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func invalid(field, message string) error {
	return &ValidationError{Field: field, Message: message}
}

func event(found store.Event) Event {
	return Event{
		ID: found.ID, Name: found.Name,
		PlannedStartDate: found.PlannedStartDate, PlannedEndDate: found.PlannedEndDate,
		Timezone: found.Timezone, EventLocale: found.EventLocale,
		ContentLanguage: found.ContentLanguage, EventDayBoundary: found.EventDayBoundary,
		Revision: found.Revision,
	}
}

func grant(found store.EventGrant) Grant {
	return Grant{
		EventID: found.EventID, AccountID: found.AccountID, Role: found.Role,
		LaneIDs: found.LaneIDs, DisplayGroupKeys: found.DisplayGroupKeys,
		Capabilities: found.Capabilities,
	}
}

func displayConfiguration(found store.DisplayConfigurationState) (DisplayConfiguration, error) {
	var configuration displayviews.Configuration
	if err := json.Unmarshal([]byte(found.Configuration), &configuration); err != nil {
		return DisplayConfiguration{}, errors.New("decode Display configuration")
	}
	return DisplayConfiguration{
		EventID: found.EventID, EventRevision: found.EventRevision,
		Configuration: configuration,
	}, nil
}

func grantPayloadHash(eventID int, input GrantInput) (string, error) {
	parts := []string{strconv.Itoa(eventID), strconv.Itoa(input.AccountID), input.Role}
	if len(input.LaneIDs) == 0 && len(input.DisplayGroupKeys) == 0 && len(input.Capabilities) == 0 {
		return command.PayloadHash(parts...), nil
	}
	laneIDs := append([]int(nil), input.LaneIDs...)
	displayGroupKeys := append([]string(nil), input.DisplayGroupKeys...)
	capabilities := append([]string(nil), input.Capabilities...)
	sort.Ints(laneIDs)
	sort.Strings(displayGroupKeys)
	sort.Strings(capabilities)
	scopes, err := json.Marshal(struct {
		LaneIDs          []int    `json:"lane_ids,omitempty"`
		DisplayGroupKeys []string `json:"display_group_keys,omitempty"`
		Capabilities     []string `json:"capabilities,omitempty"`
	}{
		LaneIDs: laneIDs, DisplayGroupKeys: displayGroupKeys,
		Capabilities: capabilities,
	})
	if err != nil {
		return "", errors.New("encode Event Grant scopes")
	}
	return command.PayloadHash(append(parts, string(scopes))...), nil
}

func validateGrantInput(input GrantInput) (GrantInput, error) {
	if input.Role != "Producer" && input.Role != "Operator" && input.Role != "Observer" {
		return GrantInput{}, ErrGrantRoleRequired
	}
	if input.AccountID <= 0 {
		return GrantInput{}, invalid("account_id", "must identify an Account")
	}
	lanes := make(map[int]struct{}, len(input.LaneIDs))
	for _, laneID := range input.LaneIDs {
		if laneID <= 0 {
			return GrantInput{}, invalid("lane_ids", "must contain positive Lane IDs")
		}
		if _, duplicate := lanes[laneID]; duplicate {
			return GrantInput{}, invalid("lane_ids", "must not contain duplicates")
		}
		lanes[laneID] = struct{}{}
	}
	sort.Ints(input.LaneIDs)
	groups := make(map[string]struct{}, len(input.DisplayGroupKeys))
	for _, key := range input.DisplayGroupKeys {
		if !validScopeKey(key) {
			return GrantInput{}, invalid("display_group_keys", "must contain stable opaque keys")
		}
		if _, duplicate := groups[key]; duplicate {
			return GrantInput{}, invalid("display_group_keys", "must not contain duplicates")
		}
		groups[key] = struct{}{}
	}
	sort.Strings(input.DisplayGroupKeys)
	capabilities := make(map[string]struct{}, len(input.Capabilities))
	for _, capability := range input.Capabilities {
		switch capability {
		case "EmergencyAlert", "ViewResults", "ManageResults":
		default:
			return GrantInput{}, invalid("capabilities", "contains an unsupported capability")
		}
		if _, duplicate := capabilities[capability]; duplicate {
			return GrantInput{}, invalid("capabilities", "must not contain duplicates")
		}
		capabilities[capability] = struct{}{}
	}
	sort.Strings(input.Capabilities)
	if input.Role == "Producer" &&
		(len(input.LaneIDs) > 0 || len(input.DisplayGroupKeys) > 0 || len(input.Capabilities) > 0) {
		return GrantInput{}, invalid("role", "Producer authority is Event-wide")
	}
	if input.Role == "Observer" && (len(input.LaneIDs) > 0 || len(input.DisplayGroupKeys) > 0) {
		return GrantInput{}, invalid("role", "Observer authority is read-only")
	}
	if input.Role == "Observer" {
		for _, capability := range input.Capabilities {
			if capability != "ViewResults" {
				return GrantInput{}, invalid("capabilities", "Observer may receive only ViewResults")
			}
		}
	}
	return input, nil
}

func validScopeKey(value string) bool {
	if value == "" || len(value) > 100 {
		return false
	}
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z':
		case character >= 'A' && character <= 'Z':
		case character >= '0' && character <= '9':
		case character == '-', character == '_', character == ':':
		default:
			return false
		}
	}
	return true
}

func eventPayloadHash(input CreateInput, expectedRevision int) string {
	return command.PayloadHash(
		input.Name, input.PlannedStartDate, input.PlannedEndDate, input.Timezone,
		input.EventLocale, input.ContentLanguage, input.EventDayBoundary,
		intsPayload(input.TargetAdjustmentPresetsSeconds),
		strconv.Itoa(expectedRevision),
	)
}

func intsPayload(values []int) string {
	var result strings.Builder
	result.WriteByte('[')
	for index, value := range values {
		if index > 0 {
			result.WriteByte(',')
		}
		result.WriteString(strconv.Itoa(value))
	}
	result.WriteByte(']')
	return result.String()
}

func commandRejection(reason error) store.CommandRejection {
	var validation *ValidationError
	if errors.As(reason, &validation) {
		return store.CommandRejection{Code: "validation", Field: validation.Field, Message: validation.Message}
	}
	for candidate, code := range map[error]string{
		ErrAdministratorRequired: "administrator_required",
		ErrGrantRoleRequired:     "grant_role_required",
		ErrEventNotFound:         "event_not_found",
		ErrAccountNotFound:       "account_not_found",
		ErrEventGrantExists:      "event_grant_exists",
		ErrEventAccessDenied:     "event_access_denied",
		ErrRevisionConflict:      "revision_conflict",
	} {
		if errors.Is(reason, candidate) {
			return store.CommandRejection{Code: code}
		}
	}
	return store.CommandRejection{Code: "unavailable"}
}

func restoreRejected(err error) error {
	var rejected *store.RejectedCommandError
	if !errors.As(err, &rejected) {
		return err
	}
	switch rejected.Rejection.Code {
	case "validation":
		return invalid(rejected.Rejection.Field, rejected.Rejection.Message)
	case "administrator_required":
		return ErrAdministratorRequired
	case "grant_role_required":
		return ErrGrantRoleRequired
	case "event_not_found":
		return ErrEventNotFound
	case "account_not_found":
		return ErrAccountNotFound
	case "event_grant_exists":
		return ErrEventGrantExists
	case "event_access_denied":
		return ErrEventAccessDenied
	case "revision_conflict":
		return ErrRevisionConflict
	default:
		return errors.New("Event command unavailable")
	}
}
