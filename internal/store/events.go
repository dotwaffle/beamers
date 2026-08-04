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
	"github.com/dotwaffle/beamers/ent/eventslug"
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
	// ErrEventSlugUnavailable means a current slug or retained alias already owns the identifier.
	ErrEventSlugUnavailable = errors.New("Event Slug is already in use")
	// ErrEventSlugAliasNotFound means the requested retained alias does not exist.
	ErrEventSlugAliasNotFound = errors.New("Event Slug Alias not found")
	// ErrEventGrantLaneMismatch means an Event Grant named a Lane that does
	// not belong to the Grant's target Event.
	ErrEventGrantLaneMismatch = errors.New("lane does not belong to the granted Event")
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
	SubmissionEligibility   string `json:"submission_eligibility"`
	VotingMethod            string `json:"voting_method"`
	SelfVotePolicy          string `json:"self_vote_policy"`
	TargetAdjustmentPresets string `json:"target_adjustment_presets"`
	Revision                int    `json:"revision"`
}

// CrewEventOverview is the Event status needed by its Backstage landing page.
type CrewEventOverview struct {
	Event             Event
	Active            bool
	AttachmentRelease AttachmentReleaseConfiguration
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

// EventSlugAlias is one retained public Event URL identifier.
type EventSlugAlias struct {
	ID          int    `json:"id"`
	EventID     int    `json:"event_id"`
	EventName   string `json:"event_name"`
	Slug        string `json:"slug"`
	CurrentSlug string `json:"current_slug"`
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
	SubmissionEligibility          string
	VotingMethod                   string
	SelfVotePolicy                 string
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
	PublicSlug                     string
	PlannedStartDate               string
	PlannedEndDate                 string
	Timezone                       string
	EventLocale                    string
	ContentLanguage                string
	EventDayBoundary               string
	EntryDefaultDisposition        string
	SubmissionEligibility          string
	VotingMethod                   string
	SelfVotePolicy                 string
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
	if params.SubmissionEligibility != "" {
		create.SetSubmissionEligibility(event.SubmissionEligibility(params.SubmissionEligibility))
	}
	if params.VotingMethod != "" {
		create.SetVotingMethod(event.VotingMethod(params.VotingMethod))
	}
	if params.SelfVotePolicy != "" {
		create.SetSelfVotePolicy(event.SelfVotePolicy(params.SelfVotePolicy))
	}
	if params.ContentLanguage != "" {
		create.SetContentLanguage(params.ContentLanguage)
	}
	created, err := create.Save(ctx)
	if err != nil {
		return Event{}, opaqueError("create Event", err)
	}
	if _, err = transaction.transaction.EventSlug.Create().
		SetEventID(created.ID).
		SetSlug(publicSlug).
		SetExposed(false).
		SetCreatedAt(params.Now).
		Save(ctx); err != nil {
		return Event{}, opaqueError("reserve Event Slug", err)
	}
	if _, createErr := transaction.transaction.Rundown.Create().
		SetEventID(created.ID).
		Save(ctx); createErr != nil {
		return Event{}, opaqueError("create Event Rundown", createErr)
	}
	return eventProjection(created), nil
}

// ListEvents returns installation Events in stable creation order.
func (installation *SQLite) ListEvents(ctx context.Context) ([]Event, error) {
	found, err := installation.client.Event.Query().
		Order(ent.Asc(event.FieldID)).
		All(ctx)
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
		All(ctx)
	if err != nil {
		return nil, 0, opaqueError("list public Events", err)
	}
	active, err := installation.LoadActiveEvent(ctx)
	if err != nil {
		return nil, 0, err
	}
	events := make([]PublicEvent, 0, len(found))
	for _, item := range found {
		events = append(events, publicEventProjection(item))
	}
	return events, active.EventID, nil
}

// FindPublicEvent resolves one attendee-visible current Event Slug or retained alias.
func (installation *SQLite) FindPublicEvent(
	ctx context.Context,
	slug string,
) (PublicEvent, bool, error) {
	reserved, err := installation.client.EventSlug.Query().
		Where(eventslug.SlugEQ(slug)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return PublicEvent{}, false, ErrEventNotFound
	}
	if err != nil {
		return PublicEvent{}, false, opaqueError("find public Event Slug", err)
	}
	found, err := reserved.QueryEvent().
		Where(event.PublicEQ(true)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return PublicEvent{}, false, ErrEventNotFound
	}
	if err != nil {
		return PublicEvent{}, false, opaqueError("find public Event", err)
	}
	return publicEventProjection(found), reserved.Slug != found.PublicSlug, nil
}

// ListEventSlugAliases returns retained aliases in stable creation order.
func (installation *SQLite) ListEventSlugAliases(
	ctx context.Context,
) ([]EventSlugAlias, error) {
	found, err := installation.client.EventSlug.Query().
		WithEvent().
		Order(ent.Asc(eventslug.FieldID)).
		All(ctx)
	if err != nil {
		return nil, opaqueError("list Event Slug Aliases", err)
	}
	aliases := make([]EventSlugAlias, 0, len(found))
	for _, item := range found {
		linked, edgeErr := item.Edges.EventOrErr()
		if edgeErr != nil {
			return nil, opaqueError("read Event for Slug Alias", edgeErr)
		}
		if item.Slug == linked.PublicSlug {
			continue
		}
		aliases = append(aliases, EventSlugAlias{
			ID: item.ID, EventID: item.EventID, EventName: linked.Name,
			Slug: item.Slug, CurrentSlug: linked.PublicSlug,
		})
	}
	return aliases, nil
}

// PruneEventSlugAlias releases one retained alias for reuse.
func (transaction *CommandTx) PruneEventSlugAlias(
	ctx context.Context,
	aliasID int,
) (EventSlugAlias, error) {
	found, err := transaction.transaction.EventSlug.Query().
		Where(eventslug.IDEQ(aliasID)).
		WithEvent().
		Only(ctx)
	if ent.IsNotFound(err) {
		return EventSlugAlias{}, ErrEventSlugAliasNotFound
	}
	if err != nil {
		return EventSlugAlias{}, opaqueError("find Event Slug Alias", err)
	}
	linked, err := found.Edges.EventOrErr()
	if err != nil {
		return EventSlugAlias{}, opaqueError("read Event for Slug Alias", err)
	}
	if found.Slug == linked.PublicSlug {
		return EventSlugAlias{}, ErrEventSlugAliasNotFound
	}
	if err = transaction.transaction.EventSlug.DeleteOne(found).Exec(ctx); err != nil {
		return EventSlugAlias{}, opaqueError("prune Event Slug Alias", err)
	}
	return EventSlugAlias{
		ID: found.ID, EventID: found.EventID, EventName: linked.Name,
		Slug: found.Slug, CurrentSlug: linked.PublicSlug,
	}, nil
}

// ListEventGrants returns installation Event Grants in stable creation order.
func (installation *SQLite) ListEventGrants(ctx context.Context) ([]EventGrant, error) {
	found, err := installation.client.EventGrant.Query().
		Order(ent.Asc(eventgrant.FieldID)).
		All(ctx)
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
		Exist(ctx)
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
		).Count(ctx)
		if countErr != nil {
			return EventGrant{}, opaqueError("validate Event Grant Lanes", countErr)
		}
		if laneCount != len(params.LaneIDs) {
			return EventGrant{}, ErrEventGrantLaneMismatch
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
	current, currentErr := transaction.transaction.Event.Query().Where(
		event.IDEQ(params.EventID),
		event.RevisionEQ(params.ExpectedRevision),
	).Only(ctx)
	if ent.IsNotFound(currentErr) {
		return Event{}, ErrRevisionConflict
	}
	if currentErr != nil {
		return Event{}, opaqueError("read Event Slug before update", currentErr)
	}
	publicSlug := current.PublicSlug
	if params.PublicSlug != "" {
		requestedSlug := eventSlug(params.PublicSlug)
		if requestedSlug != publicSlug {
			if replaceErr := transaction.replaceEventSlug(
				ctx,
				current,
				requestedSlug,
				params.Public,
				params.Now,
			); replaceErr != nil {
				return Event{}, replaceErr
			}
			publicSlug = requestedSlug
		}
	}
	if params.Public && publicSlug == "" {
		publicSlug, err = transaction.availableEventSlug(ctx, params.Name)
		if err != nil {
			return Event{}, err
		}
		if reserveErr := transaction.reserveCurrentEventSlug(
			ctx,
			params.EventID,
			publicSlug,
			true,
			params.Now,
		); reserveErr != nil {
			return Event{}, reserveErr
		}
	} else if params.Public && !current.Public && publicSlug != "" {
		if _, err = transaction.transaction.EventSlug.Update().
			Where(
				eventslug.EventIDEQ(params.EventID),
				eventslug.SlugEQ(publicSlug),
			).
			SetExposed(true).
			Save(ctx); err != nil {
			return Event{}, opaqueError("mark Event Slug public", err)
		}
	}
	entryDefaultDisposition := params.EntryDefaultDisposition
	if entryDefaultDisposition == "" {
		entryDefaultDisposition = "Pending"
	}
	submissionEligibility := params.SubmissionEligibility
	if submissionEligibility == "" {
		submissionEligibility = "AllAccounts"
	}
	votingMethod := params.VotingMethod
	if votingMethod == "" {
		votingMethod = "Range1To5"
	}
	selfVotePolicy := params.SelfVotePolicy
	if selfVotePolicy == "" {
		selfVotePolicy = "Allowed"
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
		SetSubmissionEligibility(event.SubmissionEligibility(submissionEligibility)).
		SetVotingMethod(event.VotingMethod(votingMethod)).
		SetSelfVotePolicy(event.SelfVotePolicy(selfVotePolicy)).
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
		exists, err := transaction.transaction.EventSlug.Query().
			Where(eventslug.SlugEQ(candidate)).
			Exist(ctx)
		if err != nil {
			return "", opaqueError("find available Event Slug", err)
		}
		if !exists {
			return candidate, nil
		}
	}
}

func (transaction *CommandTx) replaceEventSlug(
	ctx context.Context,
	found *ent.Event,
	slug string,
	exposed bool,
	now time.Time,
) error {
	exists, err := transaction.transaction.EventSlug.Query().
		Where(eventslug.SlugEQ(slug)).
		Exist(ctx)
	if err != nil {
		return opaqueError("check Event Slug availability", err)
	}
	if exists {
		return ErrEventSlugUnavailable
	}
	if found.PublicSlug != "" {
		current, currentErr := transaction.transaction.EventSlug.Query().
			Where(
				eventslug.EventIDEQ(found.ID),
				eventslug.SlugEQ(found.PublicSlug),
			).
			Only(ctx)
		switch {
		case currentErr != nil && !ent.IsNotFound(currentErr):
			return opaqueError("read previous Event Slug", currentErr)
		case ent.IsNotFound(currentErr) && found.Public:
			if _, currentErr = transaction.transaction.EventSlug.Create().
				SetEventID(found.ID).
				SetSlug(found.PublicSlug).
				SetExposed(true).
				SetCreatedAt(now).
				Save(ctx); currentErr != nil {
				return opaqueError("retain previous Event Slug", currentErr)
			}
		case currentErr == nil && !current.Exposed:
			if currentErr = transaction.transaction.EventSlug.DeleteOne(current).
				Exec(ctx); currentErr != nil {
				return opaqueError("release private Event Slug", currentErr)
			}
		}
	}
	return transaction.reserveCurrentEventSlug(ctx, found.ID, slug, exposed, now)
}

func (transaction *CommandTx) reserveCurrentEventSlug(
	ctx context.Context,
	eventID int,
	slug string,
	exposed bool,
	now time.Time,
) error {
	_, err := transaction.transaction.EventSlug.Create().
		SetEventID(eventID).
		SetSlug(slug).
		SetExposed(exposed).
		SetCreatedAt(now).
		Save(ctx)
	if ent.IsConstraintError(err) {
		return ErrEventSlugUnavailable
	}
	if err != nil {
		return opaqueError("reserve Event Slug", err)
	}
	return nil
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
	found, err := installation.findCrewEvent(ctx, accountID, eventID)
	if err != nil {
		return Event{}, err
	}
	return eventProjection(found), nil
}

// FindCrewEventOverview returns status only when the Account has an Event Grant.
func (installation *SQLite) FindCrewEventOverview(
	ctx context.Context,
	accountID int,
	eventID int,
) (CrewEventOverview, error) {
	found, err := installation.findCrewEvent(ctx, accountID, eventID)
	if err != nil {
		return CrewEventOverview{}, err
	}
	active, err := installation.LoadActiveEvent(ctx)
	if err != nil {
		return CrewEventOverview{}, err
	}
	return CrewEventOverview{
		Event:             eventProjection(found),
		Active:            active.EventID == eventID,
		AttachmentRelease: eventAttachmentRelease(found),
	}, nil
}

func (installation *SQLite) findCrewEvent(
	ctx context.Context,
	accountID int,
	eventID int,
) (*ent.Event, error) {
	found, err := installation.client.Event.Query().Where(
		event.IDEQ(eventID),
		event.HasGrantsWith(eventgrant.AccountIDEQ(accountID)),
	).Only(ctx)
	if ent.IsNotFound(err) || errors.Is(err, privacy.Deny) {
		return nil, ErrEventAccessDenied
	}
	if err != nil {
		return nil, opaqueError("read crew Event", err)
	}
	return found, nil
}
