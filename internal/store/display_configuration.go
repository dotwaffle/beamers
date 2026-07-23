package store

import (
	"context"
	"errors"

	"entgo.io/ent/privacy"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/event"
	"github.com/dotwaffle/beamers/ent/eventgrant"
)

// DisplayConfigurationState is one Event's durable Display presentation.
type DisplayConfigurationState struct {
	EventID       int    `json:"event_id"`
	EventRevision int    `json:"event_revision"`
	Configuration string `json:"configuration"`
}

// UpdateDisplayConfigurationParams contains a Producer's validated replacement.
type UpdateDisplayConfigurationParams struct {
	EventID               int
	ExpectedEventRevision int
	Configuration         string
}

// UpdateDisplayConfiguration replaces one Event's presentation atomically.
func (transaction *CommandTx) UpdateDisplayConfiguration(
	ctx context.Context,
	params UpdateDisplayConfigurationParams,
) (DisplayConfigurationState, error) {
	updated, err := transaction.transaction.Event.UpdateOneID(params.EventID).
		Where(event.RevisionEQ(params.ExpectedEventRevision)).
		SetDisplayConfiguration(params.Configuration).
		AddRevision(1).
		Save(ctx)
	if ent.IsNotFound(err) {
		return DisplayConfigurationState{}, ErrRevisionConflict
	}
	if err != nil {
		return DisplayConfigurationState{}, opaqueError("update Display configuration", err)
	}
	return DisplayConfigurationState{
		EventID:       updated.ID,
		EventRevision: updated.Revision,
		Configuration: updated.DisplayConfiguration,
	}, nil
}

// FindDisplayConfiguration returns presentation only to Event crew.
func (installation *SQLite) FindDisplayConfiguration(
	ctx context.Context,
	accountID int,
	eventID int,
) (DisplayConfigurationState, error) {
	found, err := installation.client.Event.Query().Where(
		event.IDEQ(eventID),
		event.HasGrantsWith(eventgrant.AccountIDEQ(accountID)),
	).Only(ctx)
	if ent.IsNotFound(err) || errors.Is(err, privacy.Deny) {
		return DisplayConfigurationState{}, ErrEventAccessDenied
	}
	if err != nil {
		return DisplayConfigurationState{}, opaqueError("read Display configuration", err)
	}
	return DisplayConfigurationState{
		EventID:       found.ID,
		EventRevision: found.Revision,
		Configuration: found.DisplayConfiguration,
	}, nil
}
