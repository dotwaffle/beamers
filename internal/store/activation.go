package store

import (
	"context"
	"errors"
	"sort"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/displayassignment"
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
	DisplayAssignments   []DisplayAssignment
	UnassignedDisplays   []Display
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
	assignments, unassigned, err := loadDisplayAssignments(internalContext, client, eventID)
	if err != nil {
		return ActivationPreflightState{}, err
	}
	return ActivationPreflightState{
		EventID: eventID, EventRevision: found.Revision,
		ActivationGeneration: routing.ActivationGeneration,
		PlannedStartDate:     found.PlannedStartDate, PlannedEndDate: found.PlannedEndDate,
		Timezone: found.Timezone, EventDayBoundary: found.EventDayBoundary, PublishedRundown: published,
		DisplayAssignments: assignments,
		UnassignedDisplays: unassigned,
	}, nil
}

func loadDisplayAssignments(
	ctx context.Context,
	client *ent.Client,
	eventID int,
) ([]DisplayAssignment, []Display, error) {
	stored, err := client.DisplayAssignment.Query().Where(
		displayassignment.EventIDEQ(eventID),
	).Order(ent.Asc(displayassignment.FieldDisplayID)).All(ctx)
	if err != nil {
		return nil, nil, opaqueError("load assigned Displays for Activation Preflight", err)
	}
	assignments := make([]DisplayAssignment, 0, len(stored))
	assigned := make(map[int]struct{}, len(stored))
	for _, item := range stored {
		assignments = append(assignments, DisplayAssignment{
			DisplayID: item.DisplayID, EventID: item.EventID,
			LocationID: item.LocationID, ViewKey: item.ViewKey,
		})
		assigned[item.DisplayID] = struct{}{}
	}
	found, err := client.Display.Query().All(ctx)
	if err != nil {
		return nil, nil, opaqueError("load Displays for Activation Preflight", err)
	}
	result := make([]Display, 0)
	for _, item := range found {
		if _, ok := assigned[item.ID]; !ok {
			result = append(result, Display{ID: item.ID, Name: item.Name, EnrolledAt: item.EnrolledAt})
		}
	}
	sort.Slice(result, func(first, second int) bool { return result[first].ID < result[second].ID })
	return assignments, result, nil
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
