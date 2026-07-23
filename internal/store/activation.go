package store

import (
	"context"
	"errors"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/event"
	"github.com/dotwaffle/beamers/ent/installation"
	"github.com/dotwaffle/beamers/ent/rundown"
)

var (
	// ErrActivationRevisionConflict means Activation Preflight no longer matches durable state.
	ErrActivationRevisionConflict = errors.New("activation preflight is stale")
)

// ActivationPreflightState is the store-owned input to Activation Preflight.
type ActivationPreflightState struct {
	EventID              int
	EventRevision        int
	ActivationGeneration int
	PlannedStartDate     string
	PlannedEndDate       string
	Timezone             string
	EventDayBoundary     string
	PublishedRundown     CrewRundownState
}

// ActiveEventState is the installation-wide live routing designation.
type ActiveEventState struct {
	EventID    int
	Generation int
}

// LoadActivationPreflight returns all current durable inputs to Activation Preflight.
func (installationStore *SQLite) LoadActivationPreflight(
	ctx context.Context,
	eventID int,
) (ActivationPreflightState, error) {
	return loadActivationPreflight(ctx, installationStore.client, eventID)
}

// LoadActivationPreflight returns current durable inputs inside this command transaction.
func (transaction *CommandTx) LoadActivationPreflight(
	ctx context.Context,
	eventID int,
) (ActivationPreflightState, error) {
	return loadActivationPreflight(ctx, transaction.transaction.Client(), eventID)
}

func loadActivationPreflight(
	ctx context.Context,
	client *ent.Client,
	eventID int,
) (ActivationPreflightState, error) {
	internalContext := systemContext(ctx)
	routing, err := client.Installation.Query().Only(ctx)
	if err != nil {
		return ActivationPreflightState{}, opaqueError("load installation activation generation", err)
	}
	found, err := client.Event.Get(internalContext, eventID)
	if ent.IsNotFound(err) {
		return ActivationPreflightState{}, ErrEventNotFound
	}
	if err != nil {
		return ActivationPreflightState{}, opaqueError("load Event for Activation Preflight", err)
	}
	published, err := loadCrewRundown(internalContext, client, eventID)
	if err != nil {
		return ActivationPreflightState{}, err
	}
	return ActivationPreflightState{
		EventID: eventID, EventRevision: found.Revision,
		ActivationGeneration: routing.ActivationGeneration,
		PlannedStartDate:     found.PlannedStartDate, PlannedEndDate: found.PlannedEndDate,
		Timezone: found.Timezone, EventDayBoundary: found.EventDayBoundary, PublishedRundown: published,
	}, nil
}

// ActivateEvent designates one Event and advances the installation generation.
func (transaction *CommandTx) ActivateEvent(
	ctx context.Context,
	eventID int,
	expectedEventRevision int,
	expectedPublishedRevision int,
	expectedActivationGeneration int,
) (ActiveEventState, error) {
	internalContext := systemContext(ctx)
	exists, err := transaction.transaction.Event.Query().
		Where(event.IDEQ(eventID), event.RevisionEQ(expectedEventRevision)).
		Exist(internalContext)
	if err != nil {
		return ActiveEventState{}, opaqueError("verify Event activation revision", err)
	}
	if !exists {
		return ActiveEventState{}, ErrActivationRevisionConflict
	}
	exists, err = transaction.transaction.Rundown.Query().
		Where(rundown.EventIDEQ(eventID), rundown.PublishedRevisionEQ(expectedPublishedRevision)).
		Exist(internalContext)
	if err != nil {
		return ActiveEventState{}, opaqueError("verify Published Rundown activation revision", err)
	}
	if !exists {
		return ActiveEventState{}, ErrActivationRevisionConflict
	}
	found, err := transaction.transaction.Installation.Query().Only(ctx)
	if err != nil {
		return ActiveEventState{}, opaqueError("load installation activation state", err)
	}
	updated, err := transaction.transaction.Installation.UpdateOneID(found.ID).
		Where(installation.ActivationGenerationEQ(expectedActivationGeneration)).
		SetActiveEventID(eventID).
		AddActivationGeneration(1).
		Save(ctx)
	if ent.IsNotFound(err) {
		return ActiveEventState{}, ErrActivationRevisionConflict
	}
	if err != nil {
		return ActiveEventState{}, opaqueError("activate Event", err)
	}
	return ActiveEventState{EventID: eventID, Generation: updated.ActivationGeneration}, nil
}

// LoadActiveEvent returns the current installation-wide live routing designation.
func (installationStore *SQLite) LoadActiveEvent(ctx context.Context) (ActiveEventState, error) {
	found, err := installationStore.client.Installation.Query().
		Where(installation.ActiveEventIDNotNil()).
		Only(ctx)
	if ent.IsNotFound(err) {
		return ActiveEventState{}, nil
	}
	if err != nil {
		return ActiveEventState{}, opaqueError("load Active Event", err)
	}
	return ActiveEventState{
		EventID:    *found.ActiveEventID,
		Generation: found.ActivationGeneration,
	}, nil
}
