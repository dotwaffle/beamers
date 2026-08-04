package store

import (
	"context"
	"errors"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/event"
	"github.com/dotwaffle/beamers/ent/eventthemerevision"
	"github.com/dotwaffle/beamers/ent/predicate"
	"github.com/dotwaffle/beamers/internal/themevalue"
)

var (
	// ErrEventThemeRevisionNotFound means an Event Theme Revision does not exist.
	ErrEventThemeRevisionNotFound = errors.New("Event Theme Revision not found")
	// ErrActiveEventThemeRevisionConflict means active Event Theme state changed.
	ErrActiveEventThemeRevisionConflict = errors.New("active Event Theme Revision conflict")
)

// EventThemeRevision is one immutable controlled Event presentation.
type EventThemeRevision struct {
	ID                 int
	EventID            int
	Config             themevalue.EventConfig
	CreatedByAccountID int
	CreatedAt          time.Time
}

// CreateEventThemeRevision appends one immutable Draft inside a command.
func (transaction *CommandTx) CreateEventThemeRevision(
	ctx context.Context,
	eventID int,
	config themevalue.EventConfig,
	createdByAccountID int,
	now time.Time,
) (EventThemeRevision, error) {
	created, err := transaction.transaction.EventThemeRevision.Create().
		SetEventID(eventID).
		SetConfig(config).
		SetCreatedByAccountID(createdByAccountID).
		SetCreatedAt(now).
		Save(ctx)
	if err != nil {
		return EventThemeRevision{}, opaqueError("create Event Theme Revision", err)
	}
	return eventThemeRevision(created), nil
}

// ActivateEventThemeRevision selects one exact Revision or full inheritance.
func (transaction *CommandTx) ActivateEventThemeRevision(
	ctx context.Context,
	eventID int,
	revisionID int,
	expectedActiveRevisionID int,
) (EventThemeRevision, error) {
	var selected EventThemeRevision
	if revisionID > 0 {
		found, err := transaction.transaction.EventThemeRevision.Get(
			ctx,
			revisionID,
		)
		if ent.IsNotFound(err) || err == nil && found.EventID != eventID {
			return EventThemeRevision{}, ErrEventThemeRevisionNotFound
		}
		if err != nil {
			return EventThemeRevision{}, opaqueError("load Event Theme Revision", err)
		}
		selected = eventThemeRevision(found)
	}
	predicates := []predicate.Event{event.IDEQ(eventID)}
	if expectedActiveRevisionID == 0 {
		predicates = append(predicates, event.ActiveThemeRevisionIDIsNil())
	} else {
		predicates = append(
			predicates,
			event.ActiveThemeRevisionIDEQ(expectedActiveRevisionID),
		)
	}
	update := transaction.transaction.Event.UpdateOneID(eventID).Where(predicates...)
	if revisionID == 0 {
		update.ClearActiveThemeRevisionID()
	} else {
		update.SetActiveThemeRevisionID(revisionID)
	}
	if _, err := update.Save(ctx); ent.IsNotFound(err) {
		return EventThemeRevision{}, ErrActiveEventThemeRevisionConflict
	} else if err != nil {
		return EventThemeRevision{}, opaqueError("activate Event Theme Revision", err)
	}
	return selected, nil
}

// LoadEventThemeRevision returns one immutable Draft.
func (installationStore *SQLite) LoadEventThemeRevision(
	ctx context.Context,
	eventID int,
	revisionID int,
) (EventThemeRevision, error) {
	return loadEventThemeRevision(ctx, installationStore.client, eventID, revisionID)
}

// LoadEventThemeRevision returns one immutable Draft inside a command.
func (transaction *CommandTx) LoadEventThemeRevision(
	ctx context.Context,
	eventID int,
	revisionID int,
) (EventThemeRevision, error) {
	return loadEventThemeRevision(
		ctx,
		transaction.transaction.Client(),
		eventID,
		revisionID,
	)
}

func loadEventThemeRevision(
	ctx context.Context,
	client *ent.Client,
	eventID int,
	revisionID int,
) (EventThemeRevision, error) {
	found, err := client.EventThemeRevision.Get(
		ctx,
		revisionID,
	)
	if ent.IsNotFound(err) || err == nil && found.EventID != eventID {
		return EventThemeRevision{}, ErrEventThemeRevisionNotFound
	}
	if err != nil {
		return EventThemeRevision{}, opaqueError("load Event Theme Revision", err)
	}
	return eventThemeRevision(found), nil
}

// ListEventThemeRevisions returns newest immutable Drafts first.
func (installationStore *SQLite) ListEventThemeRevisions(
	ctx context.Context,
	eventID int,
) ([]EventThemeRevision, error) {
	found, err := installationStore.client.EventThemeRevision.Query().
		Where(eventthemerevision.EventIDEQ(eventID)).
		Order(ent.Desc(eventthemerevision.FieldID)).
		All(ctx)
	if err != nil {
		return nil, opaqueError("list Event Theme Revisions", err)
	}
	revisions := make([]EventThemeRevision, len(found))
	for index, item := range found {
		revisions[index] = eventThemeRevision(item)
	}
	return revisions, nil
}

// ListActiveEventThemeConfigs returns every currently selected Event override.
func (transaction *CommandTx) ListActiveEventThemeConfigs(
	ctx context.Context,
) ([]themevalue.EventConfig, error) {
	found, err := transaction.transaction.Event.Query().
		Where(event.ActiveThemeRevisionIDNotNil()).
		All(ctx)
	if err != nil {
		return nil, opaqueError("list active Event Themes", err)
	}
	configs := make([]themevalue.EventConfig, 0, len(found))
	for _, item := range found {
		revision, loadErr := transaction.LoadEventThemeRevision(
			ctx,
			item.ID,
			*item.ActiveThemeRevisionID,
		)
		if loadErr != nil {
			return nil, loadErr
		}
		configs = append(configs, revision.Config)
	}
	return configs, nil
}

// LoadActiveEventThemeRevision returns the selected Revision or ID zero.
func (installationStore *SQLite) LoadActiveEventThemeRevision(
	ctx context.Context,
	eventID int,
) (EventThemeRevision, error) {
	found, err := installationStore.client.Event.Get(ctx, eventID)
	if ent.IsNotFound(err) {
		return EventThemeRevision{}, ErrEventNotFound
	}
	if err != nil {
		return EventThemeRevision{}, opaqueError("load active Event Theme Revision", err)
	}
	if found.ActiveThemeRevisionID == nil {
		return EventThemeRevision{EventID: eventID}, nil
	}
	return installationStore.LoadEventThemeRevision(ctx, eventID, *found.ActiveThemeRevisionID)
}

func eventThemeRevision(found *ent.EventThemeRevision) EventThemeRevision {
	return EventThemeRevision{
		ID: found.ID, EventID: found.EventID, Config: found.Config,
		CreatedByAccountID: found.CreatedByAccountID, CreatedAt: found.CreatedAt,
	}
}
