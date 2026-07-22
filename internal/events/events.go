// Package events creates and authorizes Beamers Events.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/language"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
)

var (
	// ErrAdministratorRequired means an Event administration action lacked
	// installation-wide Administrator authority.
	ErrAdministratorRequired = errors.New("administrator authority required")
	// ErrGrantRoleRequired means a Grant requested a role not yet supported by Event commands.
	ErrGrantRoleRequired = errors.New("role must be Producer or Operator")
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
	Name             string `json:"name"`
	PlannedStartDate string `json:"planned_start_date"`
	PlannedEndDate   string `json:"planned_end_date"`
	Timezone         string `json:"timezone"`
	EventLocale      string `json:"event_locale"`
	ContentLanguage  string `json:"content_language"`
	EventDayBoundary string `json:"event_day_boundary"`
	CommandID        string `json:"command_id"`
	ExpectedRevision int    `json:"expected_revision,omitempty"`
}

// Grant is an Account's role for one Event.
type Grant struct {
	EventID   int    `json:"event_id"`
	AccountID int    `json:"account_id"`
	Role      string `json:"role"`
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
	transaction, err := service.storage.BeginCommand(actor.Context(ctx))
	if err != nil {
		return Event{}, err
	}
	defer func() { _ = transaction.Rollback() }()
	outcome, retry, err := transaction.LookupReceipt(ctx, identity)
	if errors.Is(err, ErrCommandConflict) {
		if commitErr := transaction.CommitConflict(actor.Context(ctx), identity); commitErr != nil {
			return Event{}, commitErr
		}
		return Event{}, ErrCommandConflict
	}
	if err != nil {
		return Event{}, err
	}
	if retry {
		var original store.Event
		if decodeErr := store.DecodeCommandReceipt(outcome, &original); decodeErr != nil {
			return Event{}, restoreRejected(decodeErr)
		}
		return event(original), nil
	}
	if !actor.Administrator {
		return Event{}, service.rejectTransaction(actor.Context(ctx), transaction, identity, ErrAdministratorRequired)
	}
	normalized, err := validateCreateInput(input)
	if err != nil {
		return Event{}, service.rejectTransaction(actor.Context(ctx), transaction, identity, err)
	}
	created, err := transaction.CreateEvent(actor.Context(ctx), store.CreateEventParams{
		ActorAccountID: actor.ID, Name: normalized.Name,
		PlannedStartDate: normalized.PlannedStartDate, PlannedEndDate: normalized.PlannedEndDate,
		Timezone: normalized.Timezone, EventLocale: normalized.EventLocale,
		ContentLanguage: normalized.ContentLanguage, EventDayBoundary: normalized.EventDayBoundary,
		Now:         identity.Now,
		CommandID:   input.CommandID,
		PayloadHash: eventPayloadHash(normalized, 0),
	})
	if err != nil {
		return Event{}, err
	}
	identity.TargetID = strconv.Itoa(created.ID)
	encoded, err := json.Marshal(created)
	if err != nil {
		return Event{}, errors.New("encode Event creation outcome")
	}
	if err := transaction.RecordOutcome(actor.Context(ctx), identity, string(encoded), false); err != nil {
		return Event{}, err
	}
	if err := transaction.Commit(); err != nil {
		return Event{}, err
	}
	return event(created), nil
}

// GrantEventAccess gives an Account Producer or Operator authority for one Event.
func (service *Service) GrantEventAccess(
	ctx context.Context,
	actor auth.Account,
	eventID int,
	accountID int,
	role string,
	commandID string,
) (Grant, error) {
	payloadHash := command.PayloadHash(strconv.Itoa(eventID), strconv.Itoa(accountID), role)
	targetID := strconv.Itoa(eventID) + ":" + strconv.Itoa(accountID)
	if err := command.ValidateID(commandID); err != nil {
		return Grant{}, invalid("command_id", err.Error())
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: commandID, PayloadHash: payloadHash,
		Action: "CreateEventGrant", TargetType: "EventGrant", TargetID: targetID, Now: service.now().UTC(),
	}
	transaction, err := service.storage.BeginCommand(actor.Context(ctx))
	if err != nil {
		return Grant{}, err
	}
	defer func() { _ = transaction.Rollback() }()
	outcome, retry, err := transaction.LookupReceipt(ctx, identity)
	if errors.Is(err, ErrCommandConflict) {
		if commitErr := transaction.CommitConflict(actor.Context(ctx), identity); commitErr != nil {
			return Grant{}, commitErr
		}
		return Grant{}, ErrCommandConflict
	}
	if err != nil {
		return Grant{}, err
	}
	if retry {
		var original store.EventGrant
		if decodeErr := store.DecodeCommandReceipt(outcome, &original); decodeErr != nil {
			return Grant{}, restoreRejected(decodeErr)
		}
		return Grant{EventID: original.EventID, AccountID: original.AccountID, Role: original.Role}, nil
	}
	if !actor.Administrator {
		return Grant{}, service.rejectTransaction(actor.Context(ctx), transaction, identity, ErrAdministratorRequired)
	}
	if role != "Producer" && role != "Operator" {
		return Grant{}, service.rejectTransaction(actor.Context(ctx), transaction, identity, ErrGrantRoleRequired)
	}
	created, err := transaction.GrantEventAccess(actor.Context(ctx), store.GrantEventAccessParams{
		ActorAccountID: actor.ID,
		EventID:        eventID,
		AccountID:      accountID,
		Role:           role,
		Now:            identity.Now,
		CommandID:      commandID,
		PayloadHash:    payloadHash,
	})
	if err != nil {
		if errors.Is(err, ErrEventNotFound) || errors.Is(err, ErrAccountNotFound) || errors.Is(err, ErrEventGrantExists) {
			return Grant{}, service.rejectTransaction(actor.Context(ctx), transaction, identity, err)
		}
		return Grant{}, err
	}
	encoded, err := json.Marshal(created)
	if err != nil {
		return Grant{}, errors.New("encode Event Grant outcome")
	}
	if err := transaction.RecordOutcome(actor.Context(ctx), identity, string(encoded), false); err != nil {
		return Grant{}, err
	}
	if err := transaction.Commit(); err != nil {
		return Grant{}, err
	}
	return Grant{EventID: created.EventID, AccountID: created.AccountID, Role: created.Role}, nil
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
	transaction, err := service.storage.BeginCommand(actor.Context(ctx))
	if err != nil {
		return Event{}, err
	}
	defer func() { _ = transaction.Rollback() }()
	outcome, retry, err := transaction.LookupReceipt(ctx, identity)
	if errors.Is(err, ErrCommandConflict) {
		if commitErr := transaction.CommitConflict(actor.Context(ctx), identity); commitErr != nil {
			return Event{}, commitErr
		}
		return Event{}, ErrCommandConflict
	}
	if err != nil {
		return Event{}, err
	}
	if retry {
		var original store.Event
		if decodeErr := store.DecodeCommandReceipt(outcome, &original); decodeErr != nil {
			return Event{}, restoreRejected(decodeErr)
		}
		return event(original), nil
	}
	if !actor.CanProduceEvent(eventID) {
		return Event{}, service.rejectTransaction(actor.Context(ctx), transaction, identity, ErrEventAccessDenied)
	}
	normalized, err := validateCreateInput(input)
	if err != nil {
		return Event{}, service.rejectTransaction(actor.Context(ctx), transaction, identity, err)
	}
	if input.ExpectedRevision <= 0 {
		validation := invalid("expected_revision", "must be a positive Event revision")
		return Event{}, service.rejectTransaction(actor.Context(ctx), transaction, identity, validation)
	}
	updated, err := transaction.UpdateEvent(actor.Context(ctx), store.UpdateEventParams{
		ActorAccountID: actor.ID, EventID: eventID, Name: normalized.Name,
		PlannedStartDate: normalized.PlannedStartDate, PlannedEndDate: normalized.PlannedEndDate,
		Timezone: normalized.Timezone, EventLocale: normalized.EventLocale,
		ContentLanguage: normalized.ContentLanguage, EventDayBoundary: normalized.EventDayBoundary,
		Now:       identity.Now,
		CommandID: input.CommandID, PayloadHash: eventPayloadHash(normalized, input.ExpectedRevision),
		ExpectedRevision: input.ExpectedRevision,
	})
	if err != nil {
		if errors.Is(err, ErrRevisionConflict) {
			return Event{}, service.rejectTransaction(actor.Context(ctx), transaction, identity, err)
		}
		return Event{}, err
	}
	encoded, err := json.Marshal(updated)
	if err != nil {
		return Event{}, errors.New("encode Event update outcome")
	}
	if err := transaction.RecordOutcome(actor.Context(ctx), identity, string(encoded), false); err != nil {
		return Event{}, err
	}
	if err := transaction.Commit(); err != nil {
		return Event{}, err
	}
	return event(updated), nil
}

func (service *Service) rejectTransaction(
	ctx context.Context,
	transaction *store.CommandTx,
	identity store.CommandIdentity,
	reason error,
) error {
	if err := transaction.RecordRejection(ctx, identity, commandRejection(reason)); err != nil {
		return errors.Join(reason, err)
	}
	if err := transaction.Commit(); err != nil {
		return errors.Join(reason, err)
	}
	return reason
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

func eventPayloadHash(input CreateInput, expectedRevision int) string {
	return command.PayloadHash(
		input.Name, input.PlannedStartDate, input.PlannedEndDate, input.Timezone,
		input.EventLocale, input.ContentLanguage, input.EventDayBoundary,
		strconv.Itoa(expectedRevision),
	)
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
