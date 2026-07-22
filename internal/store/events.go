package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"entgo.io/ent/privacy"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/account"
	"github.com/dotwaffle/beamers/ent/auditentry"
	"github.com/dotwaffle/beamers/ent/event"
	"github.com/dotwaffle/beamers/ent/eventgrant"
	"github.com/dotwaffle/beamers/internal/viewer"
)

var (
	// ErrEventNotFound means the requested Event does not exist.
	ErrEventNotFound = errors.New("Event not found")
	// ErrAccountNotFound means the requested Account does not exist or is disabled.
	ErrAccountNotFound = errors.New("account not found")
	// ErrEventGrantExists means the Account already has a role for the Event.
	ErrEventGrantExists = errors.New("Event Grant already exists")
	// ErrEventAccessDenied hides whether an unauthorized Event exists.
	ErrEventAccessDenied = errors.New("Event access denied")
)

// Event is the persistence projection of an Event's core configuration.
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

// CreateEventParams contains an Event creation command's durable values.
type CreateEventParams struct {
	ActorAccountID   int
	Name             string
	PlannedStartDate string
	PlannedEndDate   string
	Timezone         string
	EventLocale      string
	ContentLanguage  string
	EventDayBoundary string
	Now              time.Time
	CommandID        string
	PayloadHash      string
}

// EventGrant is the persistence projection of an Event role assignment.
type EventGrant struct {
	EventID   int    `json:"event_id"`
	AccountID int    `json:"account_id"`
	Role      string `json:"role"`
}

// GrantEventAccessParams contains an Event Grant command's durable values.
type GrantEventAccessParams struct {
	ActorAccountID int
	EventID        int
	AccountID      int
	Role           eventgrant.Role
	Now            time.Time
	CommandID      string
	PayloadHash    string
}

// UpdateEventParams contains a Producer's Event configuration replacement.
type UpdateEventParams struct {
	ActorAccountID   int
	EventID          int
	Name             string
	PlannedStartDate string
	PlannedEndDate   string
	Timezone         string
	EventLocale      string
	ContentLanguage  string
	EventDayBoundary string
	Now              time.Time
	CommandID        string
	PayloadHash      string
	ExpectedRevision int
}

// CreateEvent atomically records an Event and its Audit Entry.
func (installation *SQLite) CreateEvent(
	ctx context.Context,
	params CreateEventParams,
) (Event, error) {
	transaction, err := installation.client.Tx(ctx)
	if err != nil {
		return Event{}, opaqueError("begin Event creation", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	receipt := commandReceiptParams{
		ActorAccountID: params.ActorAccountID, CommandID: params.CommandID,
		PayloadHash: params.PayloadHash, Action: "CreateEvent", TargetType: "Event", Now: params.Now,
	}
	outcome, retry, err := findCommandReceipt(ctx, transaction, receipt)
	if errors.Is(err, ErrCommandConflict) {
		return Event{}, rejectCommandConflict(ctx, transaction, receipt)
	}
	if err != nil {
		return Event{}, err
	}
	if retry {
		var original Event
		if decodeErr := decodeCommandReceipt(outcome, &original, "decode Event Command Receipt"); decodeErr != nil {
			return Event{}, decodeErr
		}
		return original, nil
	}

	create := transaction.Event.Create().
		SetName(params.Name).
		SetPlannedStartDate(params.PlannedStartDate).
		SetPlannedEndDate(params.PlannedEndDate).
		SetTimezone(params.Timezone).
		SetEventLocale(params.EventLocale).
		SetEventDayBoundary(params.EventDayBoundary).
		SetCreatedAt(params.Now)
	if params.ContentLanguage != "" {
		create.SetContentLanguage(params.ContentLanguage)
	}
	created, err := create.Save(ctx)
	if err != nil {
		return Event{}, opaqueError("create Event", err)
	}
	projected := eventProjection(created)
	outcomeJSON, err := json.Marshal(projected)
	if err != nil {
		return Event{}, opaqueError("encode Event command outcome", err)
	}
	receipt.TargetID = strconv.Itoa(created.ID)
	receipt.OutcomeJSON = string(outcomeJSON)
	if err := createCommandReceipt(ctx, transaction, receipt); err != nil {
		return Event{}, opaqueError("record Event Command Receipt", err)
	}
	if _, err := transaction.AuditEntry.Create().
		SetActorAccountID(params.ActorAccountID).
		SetCreatedAt(params.Now).
		SetAction("CreateEvent").
		SetTargetType("Event").
		SetTargetID(strconv.Itoa(created.ID)).
		SetResult(auditentry.ResultSucceeded).
		Save(ctx); err != nil {
		return Event{}, opaqueError("audit Event creation", err)
	}
	if err := transaction.Commit(); err != nil {
		return Event{}, opaqueError("commit Event creation", err)
	}
	return projected, nil
}

// GrantEventAccess atomically records an Event Grant and its Audit Entry.
func (installation *SQLite) GrantEventAccess(
	ctx context.Context,
	params GrantEventAccessParams,
) (EventGrant, error) {
	transaction, err := installation.client.Tx(ctx)
	if err != nil {
		return EventGrant{}, opaqueError("begin Event Grant", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	receipt := commandReceiptParams{
		ActorAccountID: params.ActorAccountID, CommandID: params.CommandID,
		PayloadHash: params.PayloadHash, Action: "CreateEventGrant",
		TargetType: "EventGrant", Now: params.Now,
	}
	outcome, retry, err := findCommandReceipt(ctx, transaction, receipt)
	if errors.Is(err, ErrCommandConflict) {
		return EventGrant{}, rejectCommandConflict(ctx, transaction, receipt)
	}
	if err != nil {
		return EventGrant{}, err
	}
	if retry {
		var original EventGrant
		if decodeErr := decodeCommandReceipt(outcome, &original, "decode Event Grant Command Receipt"); decodeErr != nil {
			return EventGrant{}, decodeErr
		}
		return original, nil
	}

	eventExists, err := transaction.Event.Query().Where(event.IDEQ(params.EventID)).Exist(viewer.SystemContext(ctx))
	if err != nil {
		return EventGrant{}, opaqueError("find Event for Grant", err)
	}
	if !eventExists {
		return EventGrant{}, ErrEventNotFound
	}
	accountExists, err := transaction.Account.Query().Where(
		account.IDEQ(params.AccountID), account.DisabledAtIsNil(),
	).Exist(ctx)
	if err != nil {
		return EventGrant{}, opaqueError("find Account for Event Grant", err)
	}
	if !accountExists {
		return EventGrant{}, ErrAccountNotFound
	}
	created, err := transaction.EventGrant.Create().
		SetEventID(params.EventID).
		SetAccountID(params.AccountID).
		SetRole(params.Role).
		SetCreatedAt(params.Now).
		Save(ctx)
	if ent.IsConstraintError(err) {
		return EventGrant{}, ErrEventGrantExists
	}
	if err != nil {
		return EventGrant{}, opaqueError("create Event Grant", err)
	}
	projected := EventGrant{EventID: created.EventID, AccountID: created.AccountID, Role: created.Role.String()}
	outcomeJSON, err := json.Marshal(projected)
	if err != nil {
		return EventGrant{}, opaqueError("encode Event Grant command outcome", err)
	}
	receipt.TargetID = strconv.Itoa(created.ID)
	receipt.OutcomeJSON = string(outcomeJSON)
	if err := createCommandReceipt(ctx, transaction, receipt); err != nil {
		return EventGrant{}, opaqueError("record Event Grant Command Receipt", err)
	}
	if _, err := transaction.AuditEntry.Create().
		SetActorAccountID(params.ActorAccountID).
		SetCreatedAt(params.Now).
		SetAction("CreateEventGrant").
		SetTargetType("EventGrant").
		SetTargetID(strconv.Itoa(created.ID)).
		SetResult(auditentry.ResultSucceeded).
		Save(ctx); err != nil {
		return EventGrant{}, opaqueError("audit Event Grant", err)
	}
	if err := transaction.Commit(); err != nil {
		return EventGrant{}, opaqueError("commit Event Grant", err)
	}
	return projected, nil
}

// FindCrewEvent returns an Event only when the Account has an Event Grant.
func (installation *SQLite) FindCrewEvent(
	ctx context.Context,
	accountID int,
	eventID int,
) (Event, error) {
	found, err := installation.client.Event.Query().Where(
		event.IDEQ(eventID),
		event.HasGrantsWith(eventgrant.AccountIDEQ(accountID)),
	).Only(ctx)
	if ent.IsNotFound(err) || errors.Is(err, privacy.Deny) {
		return Event{}, ErrEventAccessDenied
	}
	if err != nil {
		return Event{}, opaqueError("read crew Event", err)
	}
	return eventProjection(found), nil
}

// UpdateCrewEvent atomically replaces Event configuration and records its Audit Entry.
func (installation *SQLite) UpdateCrewEvent(
	ctx context.Context,
	params UpdateEventParams,
) (Event, error) {
	transaction, err := installation.client.Tx(ctx)
	if err != nil {
		return Event{}, opaqueError("begin Event update", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	receipt := commandReceiptParams{
		ActorAccountID: params.ActorAccountID, CommandID: params.CommandID,
		PayloadHash: params.PayloadHash, Action: "UpdateEvent", TargetType: "Event",
		TargetID: strconv.Itoa(params.EventID), Now: params.Now,
	}
	outcome, retry, err := findCommandReceipt(ctx, transaction, receipt)
	if errors.Is(err, ErrCommandConflict) {
		return Event{}, rejectCommandConflict(ctx, transaction, receipt)
	}
	if err != nil {
		return Event{}, err
	}
	if retry {
		var original Event
		if decodeErr := decodeCommandReceipt(outcome, &original, "decode Event update Command Receipt"); decodeErr != nil {
			return Event{}, decodeErr
		}
		return original, nil
	}

	update := transaction.Event.UpdateOneID(params.EventID).
		Where(event.RevisionEQ(params.ExpectedRevision)).
		SetName(params.Name).
		SetPlannedStartDate(params.PlannedStartDate).
		SetPlannedEndDate(params.PlannedEndDate).
		SetTimezone(params.Timezone).
		SetEventLocale(params.EventLocale).
		SetEventDayBoundary(params.EventDayBoundary).
		AddRevision(1)
	if params.ContentLanguage == "" {
		update.ClearContentLanguage()
	} else {
		update.SetContentLanguage(params.ContentLanguage)
	}
	updated, err := update.Save(ctx)
	if ent.IsNotFound(err) {
		return Event{}, ErrRevisionConflict
	}
	if err != nil {
		return Event{}, opaqueError("update Event", err)
	}
	projected := eventProjection(updated)
	outcomeJSON, err := json.Marshal(projected)
	if err != nil {
		return Event{}, opaqueError("encode Event update command outcome", err)
	}
	receipt.OutcomeJSON = string(outcomeJSON)
	if err := createCommandReceipt(ctx, transaction, receipt); err != nil {
		return Event{}, opaqueError("record Event update Command Receipt", err)
	}
	if _, err := transaction.AuditEntry.Create().
		SetActorAccountID(params.ActorAccountID).
		SetCreatedAt(params.Now).
		SetAction("UpdateEvent").
		SetTargetType("Event").
		SetTargetID(strconv.Itoa(params.EventID)).
		SetResult(auditentry.ResultSucceeded).
		Save(ctx); err != nil {
		return Event{}, opaqueError("audit Event update", err)
	}
	if err := transaction.Commit(); err != nil {
		return Event{}, opaqueError("commit Event update", err)
	}
	return projected, nil
}
