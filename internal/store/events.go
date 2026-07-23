package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"entgo.io/ent/privacy"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/account"
	"github.com/dotwaffle/beamers/ent/event"
	"github.com/dotwaffle/beamers/ent/eventgrant"
	"github.com/dotwaffle/beamers/ent/lane"
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
	ID                      int    `json:"id"`
	Name                    string `json:"name"`
	PlannedStartDate        string `json:"planned_start_date"`
	PlannedEndDate          string `json:"planned_end_date"`
	Timezone                string `json:"timezone"`
	EventLocale             string `json:"event_locale"`
	ContentLanguage         string `json:"content_language,omitempty"`
	EventDayBoundary        string `json:"event_day_boundary"`
	TargetAdjustmentPresets string `json:"target_adjustment_presets"`
	Revision                int    `json:"revision"`
}

// CreateEventParams contains an Event creation command's durable values.
type CreateEventParams struct {
	ActorAccountID                 int
	Name                           string
	PlannedStartDate               string
	PlannedEndDate                 string
	Timezone                       string
	EventLocale                    string
	ContentLanguage                string
	EventDayBoundary               string
	TargetAdjustmentPresetsSeconds []int
	Now                            time.Time
	CommandID                      string
	PayloadHash                    string
}

// EventGrant is the persistence projection of an Event role assignment.
type EventGrant struct {
	EventID          int      `json:"event_id"`
	AccountID        int      `json:"account_id"`
	Role             string   `json:"role"`
	LaneIDs          []int    `json:"lane_ids,omitempty"`
	DisplayGroupKeys []string `json:"display_group_keys,omitempty"`
	Capabilities     []string `json:"capabilities,omitempty"`
}

// GrantEventAccessParams contains an Event Grant command's durable values.
type GrantEventAccessParams struct {
	ActorAccountID   int
	EventID          int
	AccountID        int
	Role             string
	LaneIDs          []int
	DisplayGroupKeys []string
	Capabilities     []string
	Now              time.Time
	CommandID        string
	PayloadHash      string
}

// UpdateEventParams contains a Producer's Event configuration replacement.
type UpdateEventParams struct {
	ActorAccountID                 int
	EventID                        int
	Name                           string
	PlannedStartDate               string
	PlannedEndDate                 string
	Timezone                       string
	EventLocale                    string
	ContentLanguage                string
	EventDayBoundary               string
	TargetAdjustmentPresetsSeconds []int
	Now                            time.Time
	CommandID                      string
	PayloadHash                    string
	ExpectedRevision               int
}

// CreateEvent mutates Event state without owning command lifecycle evidence.
func (transaction *CommandTx) CreateEvent(ctx context.Context, params CreateEventParams) (Event, error) {
	presets, err := json.Marshal(params.TargetAdjustmentPresetsSeconds)
	if err != nil {
		return Event{}, opaqueError("encode Adjust Target presets", err)
	}
	create := transaction.transaction.Event.Create().
		SetName(params.Name).
		SetPlannedStartDate(params.PlannedStartDate).
		SetPlannedEndDate(params.PlannedEndDate).
		SetTimezone(params.Timezone).
		SetEventLocale(params.EventLocale).
		SetEventDayBoundary(params.EventDayBoundary).
		SetTargetAdjustmentPresets(string(presets)).
		SetCreatedAt(params.Now)
	if params.ContentLanguage != "" {
		create.SetContentLanguage(params.ContentLanguage)
	}
	created, err := create.Save(ctx)
	if err != nil {
		return Event{}, opaqueError("create Event", err)
	}
	if _, createErr := transaction.transaction.Rundown.Create().
		SetEventID(created.ID).
		Save(systemContext(ctx)); createErr != nil {
		return Event{}, opaqueError("create Event Rundown", createErr)
	}
	return eventProjection(created), nil
}

// GrantEventAccess mutates Event Grant state without owning command lifecycle evidence.
func (transaction *CommandTx) GrantEventAccess(
	ctx context.Context,
	params GrantEventAccessParams,
) (EventGrant, error) {
	eventExists, err := transaction.transaction.Event.Query().
		Where(event.IDEQ(params.EventID)).
		Exist(systemContext(ctx))
	if err != nil {
		return EventGrant{}, opaqueError("find Event for Grant", err)
	}
	if !eventExists {
		return EventGrant{}, ErrEventNotFound
	}
	accountExists, err := transaction.transaction.Account.Query().Where(
		account.IDEQ(params.AccountID), account.DisabledAtIsNil(),
	).Exist(ctx)
	if err != nil {
		return EventGrant{}, opaqueError("find Account for Event Grant", err)
	}
	if !accountExists {
		return EventGrant{}, ErrAccountNotFound
	}
	if len(params.LaneIDs) > 0 {
		laneCount, countErr := transaction.transaction.Lane.Query().Where(
			lane.IDIn(params.LaneIDs...), lane.EventIDEQ(params.EventID),
		).Count(systemContext(ctx))
		if countErr != nil {
			return EventGrant{}, opaqueError("validate Event Grant Lanes", countErr)
		}
		if laneCount != len(params.LaneIDs) {
			return EventGrant{}, ErrEventNotFound
		}
	}
	created, err := transaction.transaction.EventGrant.Create().
		SetEventID(params.EventID).
		SetAccountID(params.AccountID).
		SetRole(eventgrant.Role(params.Role)).
		SetLaneIds(params.LaneIDs).
		SetDisplayGroupKeys(params.DisplayGroupKeys).
		SetCapabilities(params.Capabilities).
		SetCreatedAt(params.Now).
		Save(ctx)
	if ent.IsConstraintError(err) {
		return EventGrant{}, ErrEventGrantExists
	}
	if err != nil {
		return EventGrant{}, opaqueError("create Event Grant", err)
	}
	return EventGrant{
		EventID: created.EventID, AccountID: created.AccountID, Role: created.Role.String(),
		LaneIDs: created.LaneIds, DisplayGroupKeys: created.DisplayGroupKeys,
		Capabilities: created.Capabilities,
	}, nil
}

// UpdateEvent mutates Event configuration without owning command lifecycle evidence.
func (transaction *CommandTx) UpdateEvent(ctx context.Context, params UpdateEventParams) (Event, error) {
	presets, err := json.Marshal(params.TargetAdjustmentPresetsSeconds)
	if err != nil {
		return Event{}, opaqueError("encode Adjust Target presets", err)
	}
	update := transaction.transaction.Event.UpdateOneID(params.EventID).
		Where(event.RevisionEQ(params.ExpectedRevision)).
		SetName(params.Name).
		SetPlannedStartDate(params.PlannedStartDate).
		SetPlannedEndDate(params.PlannedEndDate).
		SetTimezone(params.Timezone).
		SetEventLocale(params.EventLocale).
		SetEventDayBoundary(params.EventDayBoundary).
		SetTargetAdjustmentPresets(string(presets)).
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
	return eventProjection(updated), nil
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
