package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
	PublicSlug              string `json:"public_slug"`
	Public                  bool   `json:"public"`
	PlannedStartDate        string `json:"planned_start_date"`
	PlannedEndDate          string `json:"planned_end_date"`
	Timezone                string `json:"timezone"`
	EventLocale             string `json:"event_locale"`
	ContentLanguage         string `json:"content_language,omitempty"`
	EventDayBoundary        string `json:"event_day_boundary"`
	EntryDefaultDisposition string `json:"entry_default_disposition"`
	TargetAdjustmentPresets string `json:"target_adjustment_presets"`
	Revision                int    `json:"revision"`
}

// PublicEvent is the attendee-safe persistence projection of a listed Event.
type PublicEvent struct {
	ID               int
	Name             string
	PublicSlug       string
	PlannedStartDate string
	PlannedEndDate   string
	EventLocale      string
}

// EventInterchangeState is one portable Event snapshot.
type EventInterchangeState struct {
	Event   Event
	Rundown CrewRundownState
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
	EntryDefaultDisposition        string
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
	Public                         bool
	PlannedStartDate               string
	PlannedEndDate                 string
	Timezone                       string
	EventLocale                    string
	ContentLanguage                string
	EventDayBoundary               string
	EntryDefaultDisposition        string
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
	publicSlug, err := transaction.availableEventSlug(ctx, params.Name)
	if err != nil {
		return Event{}, err
	}
	create := transaction.transaction.Event.Create().
		SetName(params.Name).
		SetPublicSlug(publicSlug).
		SetPlannedStartDate(params.PlannedStartDate).
		SetPlannedEndDate(params.PlannedEndDate).
		SetTimezone(params.Timezone).
		SetEventLocale(params.EventLocale).
		SetEventDayBoundary(params.EventDayBoundary).
		SetTargetAdjustmentPresets(string(presets)).
		SetCreatedAt(params.Now)
	if params.EntryDefaultDisposition != "" {
		create.SetEntryDefaultDisposition(event.EntryDefaultDisposition(params.EntryDefaultDisposition))
	}
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

// ListEvents returns installation Events in stable creation order.
func (installation *SQLite) ListEvents(ctx context.Context) ([]Event, error) {
	found, err := installation.client.Event.Query().
		Order(ent.Asc(event.FieldID)).
		All(systemContext(ctx))
	if err != nil {
		return nil, opaqueError("list Events", err)
	}
	events := make([]Event, 0, len(found))
	for _, item := range found {
		events = append(events, eventProjection(item))
	}
	return events, nil
}

// ListPublicEvents returns the attendee-visible Event collection and Active Event marker.
func (installation *SQLite) ListPublicEvents(ctx context.Context) ([]PublicEvent, int, error) {
	found, err := installation.client.Event.Query().
		Where(event.PublicEQ(true)).
		Order(ent.Asc(event.FieldID)).
		All(systemContext(ctx))
	if err != nil {
		return nil, 0, opaqueError("list public Events", err)
	}
	active, err := installation.LoadActiveEvent(systemContext(ctx))
	if err != nil {
		return nil, 0, err
	}
	events := make([]PublicEvent, 0, len(found))
	for _, item := range found {
		events = append(events, publicEventProjection(item))
	}
	return events, active.EventID, nil
}

// FindPublicEvent returns one attendee-visible Event by its current Event Slug.
func (installation *SQLite) FindPublicEvent(ctx context.Context, slug string) (PublicEvent, error) {
	found, err := installation.client.Event.Query().Where(
		event.PublicEQ(true),
		event.PublicSlugEQ(slug),
	).Only(systemContext(ctx))
	if ent.IsNotFound(err) {
		return PublicEvent{}, ErrEventNotFound
	}
	if err != nil {
		return PublicEvent{}, opaqueError("find public Event", err)
	}
	return publicEventProjection(found), nil
}

// ListEventGrants returns installation Event Grants in stable creation order.
func (installation *SQLite) ListEventGrants(ctx context.Context) ([]EventGrant, error) {
	found, err := installation.client.EventGrant.Query().
		Order(ent.Asc(eventgrant.FieldID)).
		All(systemContext(ctx))
	if err != nil {
		return nil, opaqueError("list Event Grants", err)
	}
	grants := make([]EventGrant, 0, len(found))
	for _, item := range found {
		grants = append(grants, EventGrant{
			EventID: item.EventID, AccountID: item.AccountID, Role: item.Role.String(),
			LaneIDs: item.LaneIds, DisplayGroupKeys: item.DisplayGroupKeys,
			Capabilities: item.Capabilities,
		})
	}
	return grants, nil
}

// LoadEventInterchange returns core Event configuration and current Published structure.
func (transaction *CommandTx) LoadEventInterchange(
	ctx context.Context,
	eventID int,
) (EventInterchangeState, error) {
	client := transaction.transaction.Client()
	found, err := client.Event.Query().
		Where(event.IDEQ(eventID)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return EventInterchangeState{}, ErrEventNotFound
	}
	if err != nil {
		return EventInterchangeState{}, opaqueError("load Event interchange configuration", err)
	}
	published, err := loadCrewRundown(ctx, client, eventID)
	if err != nil {
		return EventInterchangeState{}, err
	}
	return EventInterchangeState{Event: eventProjection(found), Rundown: published}, nil
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
	publicSlug := ""
	if params.Public {
		current, currentErr := transaction.transaction.Event.Query().Where(
			event.IDEQ(params.EventID),
			event.RevisionEQ(params.ExpectedRevision),
		).Only(systemContext(ctx))
		if ent.IsNotFound(currentErr) {
			return Event{}, ErrRevisionConflict
		}
		if currentErr != nil {
			return Event{}, opaqueError("read Event Slug before publication", currentErr)
		}
		publicSlug = current.PublicSlug
		if publicSlug == "" {
			publicSlug, err = transaction.availableEventSlug(ctx, params.Name)
			if err != nil {
				return Event{}, err
			}
		}
	}
	entryDefaultDisposition := params.EntryDefaultDisposition
	if entryDefaultDisposition == "" {
		entryDefaultDisposition = "Pending"
	}
	update := transaction.transaction.Event.UpdateOneID(params.EventID).
		Where(event.RevisionEQ(params.ExpectedRevision)).
		SetName(params.Name).
		SetPublic(params.Public).
		SetPlannedStartDate(params.PlannedStartDate).
		SetPlannedEndDate(params.PlannedEndDate).
		SetTimezone(params.Timezone).
		SetEventLocale(params.EventLocale).
		SetEventDayBoundary(params.EventDayBoundary).
		SetEntryDefaultDisposition(event.EntryDefaultDisposition(entryDefaultDisposition)).
		SetTargetAdjustmentPresets(string(presets)).
		AddRevision(1)
	if publicSlug != "" {
		update.SetPublicSlug(publicSlug)
	}
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

func (transaction *CommandTx) availableEventSlug(ctx context.Context, name string) (string, error) {
	base := eventSlug(name)
	for suffix := 1; ; suffix++ {
		candidate := base
		if suffix > 1 {
			trailer := "-" + strconv.Itoa(suffix)
			runes := []rune(base)
			limit := 200 - utf8.RuneCountInString(trailer)
			candidate = strings.TrimRight(string(runes[:min(len(runes), limit)]), "-") + trailer
		}
		exists, err := transaction.transaction.Event.Query().
			Where(event.PublicSlugEQ(candidate)).
			Exist(systemContext(ctx))
		if err != nil {
			return "", opaqueError("find available Event Slug", err)
		}
		if !exists {
			return candidate, nil
		}
	}
}

func eventSlug(name string) string {
	var slug strings.Builder
	slug.Grow(min(len(name), 200))
	separator := false
	runeCount := 0
characters:
	for _, character := range strings.ToLower(name) {
		switch {
		case unicode.IsLetter(character) || unicode.IsDigit(character):
			if separator && runeCount > 0 {
				if runeCount+2 > 200 {
					break characters
				}
				slug.WriteByte('-')
				runeCount++
			}
			slug.WriteRune(character)
			runeCount++
			separator = false
		case unicode.IsMark(character) && runeCount > 0 && !separator:
			if runeCount == 200 {
				break characters
			}
			slug.WriteRune(character)
			runeCount++
		default:
			separator = true
		}
		if runeCount == 200 {
			break
		}
	}
	found := strings.Trim(slug.String(), "-")
	if found == "" {
		return "event"
	}
	return found
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
