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
	"github.com/dotwaffle/beamers/internal/authz"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/displayviews"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/systemactor"
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
	// ErrEventSlugUnavailable means a current slug or retained alias already owns the identifier.
	ErrEventSlugUnavailable = store.ErrEventSlugUnavailable
	// ErrEventSlugAliasNotFound means the requested retained alias does not exist.
	ErrEventSlugAliasNotFound = store.ErrEventSlugAliasNotFound
	// ErrEventSlugPruneConfirmationRequired means an Administrator omitted the destructive warning.
	ErrEventSlugPruneConfirmationRequired = errors.New("Event Slug Alias pruning confirmation required")
	// ErrEventGrantLaneMismatch means an Event Grant named a Lane that does
	// not belong to the Grant's target Event.
	ErrEventGrantLaneMismatch = store.ErrEventGrantLaneMismatch
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
	ID                             int    `json:"id"`
	Name                           string `json:"name"`
	PublicSlug                     string `json:"-"`
	Public                         bool   `json:"-"`
	PlannedStartDate               string `json:"planned_start_date"`
	PlannedEndDate                 string `json:"planned_end_date"`
	Timezone                       string `json:"timezone"`
	EventLocale                    string `json:"event_locale"`
	ContentLanguage                string `json:"content_language,omitempty"`
	EventDayBoundary               string `json:"event_day_boundary"`
	Revision                       int    `json:"revision"`
	EntryDefaultDisposition        string `json:"-"`
	SubmissionEligibility          string `json:"-"`
	VotingMethod                   string `json:"-"`
	SelfVotePolicy                 string `json:"-"`
	TargetAdjustmentPresetsSeconds []int  `json:"-"`
}

// AttachmentReleasePolicy selects when eligible Final Versions become public.
type AttachmentReleasePolicy = store.AttachmentReleasePolicy

// Attachment release policies describe the supported Event-wide release points.
const (
	AttachmentReleaseOnLive     = store.AttachmentReleaseOnLive
	AttachmentReleaseOnEnded    = store.AttachmentReleaseOnEnded
	AttachmentReleaseOnEventCue = store.AttachmentReleaseOnEventCue
)

// AttachmentReleaseState is the Event-wide release status shown to crew.
type AttachmentReleaseState struct {
	Policy AttachmentReleasePolicy
	CueAt  time.Time
}

// CrewEventOverview is one Event's Backstage landing-page projection.
type CrewEventOverview struct {
	Event             Event
	Active            bool
	AttachmentRelease AttachmentReleaseState
}

// CreateInput contains an Administrator's proposed Event configuration.
type CreateInput struct {
	Name                           string `json:"name"`
	Public                         bool   `json:"public,omitempty"`
	PublicSlug                     string `json:"public_slug,omitempty"`
	PlannedStartDate               string `json:"planned_start_date"`
	PlannedEndDate                 string `json:"planned_end_date"`
	Timezone                       string `json:"timezone"`
	EventLocale                    string `json:"event_locale"`
	ContentLanguage                string `json:"content_language"`
	EventDayBoundary               string `json:"event_day_boundary"`
	EntryDefaultDisposition        string `json:"entry_default_disposition,omitempty"`
	SubmissionEligibility          string `json:"submission_eligibility,omitempty"`
	VotingMethod                   string `json:"voting_method,omitempty"`
	SelfVotePolicy                 string `json:"self_vote_policy,omitempty"`
	TargetAdjustmentPresetsSeconds []int  `json:"target_adjustment_presets_seconds,omitempty"`
	CommandID                      string `json:"command_id"`
	ExpectedRevision               int    `json:"expected_revision,omitempty"`
}

// PublicEvent is one attendee-visible Event under its current Event Slug.
type PublicEvent struct {
	ID               int
	Name             string
	Slug             string
	PlannedStartDate string
	PlannedEndDate   string
	EventLocale      string
	Active           bool
}

// EventSlugAlias is one retained public Event URL identifier.
type EventSlugAlias struct {
	ID          int
	EventID     int
	EventName   string
	Slug        string
	CurrentSlug string
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
	storage        *store.SQLite
	now            func() time.Time
	notifyDisplays func()
	notifySchedule func()
}

// New creates an Event Service with explicit dependencies.
// Optional callbacks publish projection freshness after a successful commit or replay.
func New(
	storage *store.SQLite,
	now func() time.Time,
	notifyDisplays func(),
	notifySchedule func(),
) (*Service, error) {
	if storage == nil {
		return nil, errors.New("Event storage is required")
	}
	if now == nil {
		return nil, errors.New("Event clock is required")
	}
	return &Service{
		storage: storage, now: now,
		notifyDisplays: notifyDisplays, notifySchedule: notifySchedule,
	}, nil
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
	ctx = actor.Context(ctx)
	return command.Execute(ctx, command.Plan[Event]{
		Storage: service.storage, Identity: identity, Replay: replayEvent,
		Authorization: command.Authorization{
			Facts: authz.Installation(), Refusals: eventRejections,
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[Event], error) {
			if !actor.Administrator {
				return eventRejection[Event](ErrAdministratorRequired), nil
			}
			if input.Public {
				return eventRejection[Event](
					invalid("public", "must be changed by a Producer after Event creation"),
				), nil
			}
			normalized, validationErr := ValidateCreateInput(input)
			if validationErr != nil {
				return eventRejection[Event](validationErr), nil
			}
			created, createErr := transaction.CreateEvent(actor.Context(ctx), store.CreateEventParams{
				ActorAccountID: actor.ID, Name: normalized.Name,
				PlannedStartDate: normalized.PlannedStartDate, PlannedEndDate: normalized.PlannedEndDate,
				Timezone: normalized.Timezone, EventLocale: normalized.EventLocale,
				ContentLanguage: normalized.ContentLanguage, EventDayBoundary: normalized.EventDayBoundary,
				EntryDefaultDisposition:        normalized.EntryDefaultDisposition,
				SubmissionEligibility:          normalized.SubmissionEligibility,
				VotingMethod:                   normalized.VotingMethod,
				SelfVotePolicy:                 normalized.SelfVotePolicy,
				TargetAdjustmentPresetsSeconds: normalized.TargetAdjustmentPresetsSeconds,
				Now:                            identity.Now,
				CommandID:                      input.CommandID,
				PayloadHash:                    eventPayloadHash(normalized, 0),
			})
			if createErr != nil {
				return command.Execution[Event]{}, createErr
			}
			result, resultErr := event(created)
			if resultErr != nil {
				return command.Execution[Event]{}, resultErr
			}
			return eventSuccess(result, created, strconv.Itoa(created.ID), "encode Event creation outcome")
		},
	})
}

// List returns all Events to an Administrator.
func (service *Service) List(
	ctx context.Context,
	actor auth.Account,
) ([]Event, error) {
	if !actor.Administrator {
		return nil, ErrAdministratorRequired
	}
	found, err := service.storage.ListEvents(actor.Context(ctx))
	if err != nil {
		return nil, err
	}
	result := make([]Event, 0, len(found))
	for _, item := range found {
		converted, convertErr := event(item)
		if convertErr != nil {
			return nil, convertErr
		}
		result = append(result, converted)
	}
	return result, nil
}

// PublicListing returns only Events a Producer has placed in the Public Event Listing.
func (service *Service) PublicListing(ctx context.Context) ([]PublicEvent, error) {
	ctx = systemactor.NewContext(ctx, systemactor.PublicVisitor)
	found, activeEventID, err := service.storage.ListPublicEvents(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]PublicEvent, 0, len(found))
	for _, item := range found {
		result = append(result, publicEvent(item, item.ID == activeEventID))
	}
	return result, nil
}

// PublicEvent resolves one listed Event by a current Event Slug or retained alias.
func (service *Service) PublicEvent(
	ctx context.Context,
	slug string,
) (PublicEvent, bool, error) {
	ctx = systemactor.NewContext(ctx, systemactor.PublicVisitor)
	found, alias, err := service.storage.FindPublicEvent(ctx, slug)
	if err != nil {
		return PublicEvent{}, false, err
	}
	return publicEvent(found, false), alias, nil
}

// ListEventSlugAliases returns retained aliases to an Administrator.
func (service *Service) ListEventSlugAliases(
	ctx context.Context,
	actor auth.Account,
) ([]EventSlugAlias, error) {
	if !actor.Administrator {
		return nil, ErrAdministratorRequired
	}
	found, err := service.storage.ListEventSlugAliases(actor.Context(ctx))
	if err != nil {
		return nil, err
	}
	result := make([]EventSlugAlias, 0, len(found))
	for _, alias := range found {
		result = append(result, eventSlugAlias(alias))
	}
	return result, nil
}

// PruneEventSlugAlias releases a retained alias after an Administrator confirms the warning.
func (service *Service) PruneEventSlugAlias(
	ctx context.Context,
	actor auth.Account,
	aliasID int,
	confirmed bool,
	commandID string,
) (EventSlugAlias, error) {
	if err := command.ValidateID(commandID); err != nil {
		return EventSlugAlias{}, invalid("command_id", err.Error())
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: commandID,
		PayloadHash: command.PayloadHash(strconv.Itoa(aliasID), strconv.FormatBool(confirmed)),
		Action:      "PruneEventSlugAlias", TargetType: "EventSlugAlias",
		TargetID: strconv.Itoa(aliasID), Now: service.now().UTC(),
	}
	ctx = actor.Context(ctx)
	return command.Execute(ctx, command.Plan[EventSlugAlias]{
		Storage: service.storage, Identity: identity, Replay: replayEventSlugAlias,
		Authorization: command.Authorization{
			Facts: authz.Installation(), Refusals: eventRejections,
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[EventSlugAlias], error) {
			if !actor.Administrator {
				return eventRejection[EventSlugAlias](ErrAdministratorRequired), nil
			}
			if !confirmed {
				return eventRejection[EventSlugAlias](
					ErrEventSlugPruneConfirmationRequired,
				), nil
			}
			pruned, pruneErr := transaction.PruneEventSlugAlias(actor.Context(ctx), aliasID)
			if errors.Is(pruneErr, ErrEventSlugAliasNotFound) {
				return eventRejection[EventSlugAlias](pruneErr), nil
			}
			if pruneErr != nil {
				return command.Execution[EventSlugAlias]{}, pruneErr
			}
			result := eventSlugAlias(pruned)
			return eventSuccess(
				result,
				pruned,
				"",
				"encode Event Slug Alias pruning outcome",
			)
		},
	})
}

// ListGrants returns all Event Grants to an Administrator.
func (service *Service) ListGrants(
	ctx context.Context,
	actor auth.Account,
) ([]Grant, error) {
	if !actor.Administrator {
		return nil, ErrAdministratorRequired
	}
	found, err := service.storage.ListEventGrants(actor.Context(ctx))
	if err != nil {
		return nil, err
	}
	result := make([]Grant, 0, len(found))
	for _, item := range found {
		result = append(result, grant(item))
	}
	return result, nil
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
	ctx = actor.Context(ctx)
	return command.Execute(ctx, command.Plan[Grant]{
		Storage: service.storage, Identity: identity, Replay: replayGrant,
		Authorization: command.Authorization{
			Facts: authz.Installation(), Refusals: eventRejections,
		},
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
				if errors.Is(createErr, ErrEventNotFound) || errors.Is(createErr, ErrAccountNotFound) ||
					errors.Is(createErr, ErrEventGrantExists) || errors.Is(createErr, ErrEventGrantLaneMismatch) {
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
	return event(found)
}

// CrewEventOverview returns the Event status visible to any granted crew role.
func (service *Service) CrewEventOverview(
	ctx context.Context,
	actor auth.Account,
	eventID int,
) (CrewEventOverview, error) {
	found, err := service.storage.FindCrewEventOverview(
		actor.Context(ctx),
		actor.ID,
		eventID,
	)
	if err != nil {
		return CrewEventOverview{}, err
	}
	event, err := event(found.Event)
	if err != nil {
		return CrewEventOverview{}, err
	}
	return CrewEventOverview{
		Event:  event,
		Active: found.Active,
		AttachmentRelease: AttachmentReleaseState{
			Policy: found.AttachmentRelease.Policy,
			CueAt:  found.AttachmentRelease.CueAt,
		},
	}, nil
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
	ctx = actor.Context(ctx)
	return command.Execute(ctx, command.Plan[Event]{
		Storage: service.storage, Identity: identity, Replay: replayEvent,
		Notify: service.notifyEventChange,
		Authorization: command.Authorization{
			Facts: authz.Event(eventID), Refusals: eventRejections,
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[Event], error) {
			normalized, validationErr := ValidateCreateInput(input)
			if validationErr != nil {
				return eventRejection[Event](validationErr), nil
			}
			if input.ExpectedRevision <= 0 {
				validation := invalid("expected_revision", "must be a positive Event revision")
				return eventRejection[Event](validation), nil
			}
			updated, updateErr := transaction.UpdateEvent(actor.Context(ctx), store.UpdateEventParams{
				ActorAccountID: actor.ID, EventID: eventID, Name: normalized.Name,
				Public:           normalized.Public,
				PublicSlug:       normalized.PublicSlug,
				PlannedStartDate: normalized.PlannedStartDate, PlannedEndDate: normalized.PlannedEndDate,
				Timezone: normalized.Timezone, EventLocale: normalized.EventLocale,
				ContentLanguage: normalized.ContentLanguage, EventDayBoundary: normalized.EventDayBoundary,
				EntryDefaultDisposition:        normalized.EntryDefaultDisposition,
				SubmissionEligibility:          normalized.SubmissionEligibility,
				VotingMethod:                   normalized.VotingMethod,
				SelfVotePolicy:                 normalized.SelfVotePolicy,
				TargetAdjustmentPresetsSeconds: normalized.TargetAdjustmentPresetsSeconds,
				Now:                            identity.Now,
				CommandID:                      input.CommandID, PayloadHash: eventPayloadHash(normalized, input.ExpectedRevision),
				ExpectedRevision: input.ExpectedRevision,
			})
			if errors.Is(updateErr, ErrRevisionConflict) ||
				errors.Is(updateErr, ErrEventSlugUnavailable) {
				return eventRejection[Event](updateErr), nil
			}
			if updateErr != nil {
				return command.Execution[Event]{}, updateErr
			}
			result, resultErr := event(updated)
			if resultErr != nil {
				return command.Execution[Event]{}, resultErr
			}
			return eventSuccess(result, updated, "", "encode Event update outcome")
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
	ctx = actor.Context(ctx)
	return command.Execute(ctx, command.Plan[DisplayConfiguration]{
		Storage:  service.storage,
		Identity: identity,
		Replay:   replayDisplayConfiguration,
		Notify:   service.notifyDisplays,
		Authorization: command.Authorization{
			Facts: authz.Event(eventID), Refusals: eventRejections,
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[DisplayConfiguration], error) {
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

func (service *Service) notifyEventChange() {
	if service.notifyDisplays != nil {
		service.notifyDisplays()
	}
	if service.notifySchedule != nil {
		service.notifySchedule()
	}
}

func replayEvent(outcome string) (Event, error) {
	var original store.Event
	if err := store.DecodeCommandReceipt(outcome, &original); err != nil {
		return Event{}, restoreRejected(err)
	}
	return event(original)
}

func replayDisplayConfiguration(outcome string) (DisplayConfiguration, error) {
	var original store.DisplayConfigurationState
	if err := store.DecodeCommandReceipt(outcome, &original); err != nil {
		return DisplayConfiguration{}, restoreRejected(err)
	}
	return displayConfiguration(original)
}

func replayEventSlugAlias(outcome string) (EventSlugAlias, error) {
	var original store.EventSlugAlias
	if err := store.DecodeCommandReceipt(outcome, &original); err != nil {
		return EventSlugAlias{}, restoreRejected(err)
	}
	return eventSlugAlias(original), nil
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

// ValidateCreateInput normalizes and validates complete Event creation configuration.
func ValidateCreateInput(input CreateInput) (CreateInput, error) {
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
	if input.EntryDefaultDisposition == "" {
		input.EntryDefaultDisposition = "Pending"
	}
	if input.EntryDefaultDisposition != "Pending" && input.EntryDefaultDisposition != "Included" {
		return CreateInput{}, invalid("entry_default_disposition", "must be Pending or Included")
	}
	if input.SubmissionEligibility == "" {
		input.SubmissionEligibility = "AllAccounts"
	}
	if input.SubmissionEligibility != "AllAccounts" &&
		input.SubmissionEligibility != "VotingEligibleAccounts" {
		return CreateInput{}, invalid(
			"submission_eligibility",
			"must be AllAccounts or VotingEligibleAccounts",
		)
	}
	if input.VotingMethod == "" {
		input.VotingMethod = "Range1To5"
	}
	if input.VotingMethod != "Range1To5" {
		return CreateInput{}, invalid("voting_method", "must be Range1To5")
	}
	if input.SelfVotePolicy == "" {
		input.SelfVotePolicy = "Allowed"
	}
	if input.SelfVotePolicy != "Allowed" && input.SelfVotePolicy != "Neutral" {
		return CreateInput{}, invalid("self_vote_policy", "must be Allowed or Neutral")
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

func event(found store.Event) (Event, error) {
	var targetAdjustmentPresets []int
	if err := json.Unmarshal([]byte(found.TargetAdjustmentPresets), &targetAdjustmentPresets); err != nil {
		return Event{}, errors.New("decode Event target adjustment presets")
	}
	return Event{
		ID: found.ID, Name: found.Name,
		PublicSlug: found.PublicSlug, Public: found.Public,
		PlannedStartDate: found.PlannedStartDate, PlannedEndDate: found.PlannedEndDate,
		Timezone: found.Timezone, EventLocale: found.EventLocale,
		ContentLanguage: found.ContentLanguage, EventDayBoundary: found.EventDayBoundary,
		Revision: found.Revision, EntryDefaultDisposition: found.EntryDefaultDisposition,
		SubmissionEligibility:          found.SubmissionEligibility,
		VotingMethod:                   found.VotingMethod,
		SelfVotePolicy:                 found.SelfVotePolicy,
		TargetAdjustmentPresetsSeconds: targetAdjustmentPresets,
	}, nil
}

func publicEvent(found store.PublicEvent, active bool) PublicEvent {
	return PublicEvent{
		ID: found.ID, Name: found.Name, Slug: found.PublicSlug,
		PlannedStartDate: found.PlannedStartDate,
		PlannedEndDate:   found.PlannedEndDate,
		EventLocale:      found.EventLocale,
		Active:           active,
	}
}

func eventSlugAlias(found store.EventSlugAlias) EventSlugAlias {
	return EventSlugAlias{
		ID: found.ID, EventID: found.EventID, EventName: found.EventName,
		Slug: found.Slug, CurrentSlug: found.CurrentSlug,
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
	parts := []string{
		input.Name,
		input.PlannedStartDate, input.PlannedEndDate, input.Timezone,
		input.EventLocale, input.ContentLanguage, input.EventDayBoundary,
		intsPayload(input.TargetAdjustmentPresetsSeconds),
		strconv.Itoa(expectedRevision),
	}
	if input.Public {
		parts = append(parts, "public=true")
	}
	if input.PublicSlug != "" {
		parts = append(parts, "public_slug="+input.PublicSlug)
	}
	if input.SubmissionEligibility != "" &&
		input.SubmissionEligibility != "AllAccounts" {
		parts = append(parts, "submission_eligibility="+input.SubmissionEligibility)
	}
	if input.VotingMethod != "" && input.VotingMethod != "Range1To5" {
		parts = append(parts, "voting_method="+input.VotingMethod)
	}
	if input.SelfVotePolicy != "" && input.SelfVotePolicy != "Allowed" {
		parts = append(parts, "self_vote_policy="+input.SelfVotePolicy)
	}
	return command.PayloadHash(parts...)
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

// eventRejections is the single source for Event command rejection codes in
// both directions. The list is ordered rather than keyed, so a failure matching
// two sentinels always classifies the same way.
var eventRejections = command.RejectionTable{
	Rejections: []command.Rejection{
		{Err: ErrAdministratorRequired, Code: "administrator_required"},
		{Err: ErrGrantRoleRequired, Code: "grant_role_required"},
		{Err: ErrEventNotFound, Code: "event_not_found"},
		{Err: ErrAccountNotFound, Code: "account_not_found"},
		{Err: ErrEventGrantExists, Code: "event_grant_exists"},
		{Err: ErrEventGrantLaneMismatch, Code: "event_grant_lane_mismatch"},
		{Err: ErrEventAccessDenied, Code: "event_access_denied"},
		{Err: ErrRevisionConflict, Code: "revision_conflict"},
		{Err: ErrEventSlugUnavailable, Code: "event_slug_unavailable"},
		{Err: ErrEventSlugAliasNotFound, Code: "event_slug_alias_not_found"},
		{
			Err:  ErrEventSlugPruneConfirmationRequired,
			Code: "event_slug_prune_confirmation_required",
		},
	},
}

func commandRejection(reason error) store.CommandRejection {
	var validation *ValidationError
	if errors.As(reason, &validation) {
		return store.CommandRejection{
			Code: "validation", Field: validation.Field, Message: validation.Message,
		}
	}
	rejection, known := eventRejections.Rejection(reason)
	if !known {
		return store.CommandRejection{Code: "unavailable"}
	}
	return rejection
}

func restoreRejected(err error) error {
	var rejected *store.RejectedCommandError
	if !errors.As(err, &rejected) {
		return err
	}
	if rejected.Rejection.Code == "validation" {
		return invalid(rejected.Rejection.Field, rejected.Rejection.Message)
	}
	if sentinel := eventRejections.Sentinel(rejected.Rejection.Code); sentinel != nil {
		return sentinel
	}
	return errors.New("Event command unavailable")
}
