package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/displayassignment"
	"github.com/dotwaffle/beamers/ent/displaycredential"
	"github.com/dotwaffle/beamers/ent/installation"
	"github.com/dotwaffle/beamers/ent/sessionrun"
)

// DisplaySnapshotState is one authorized, transactionally consistent Display projection.
type DisplaySnapshotState struct {
	Display              Display
	ActiveEventID        int
	EventName            string
	EventTimezone        string
	DisplayConfiguration string
	ActivationGeneration int
	PublishedRevision    int
	LocationID           int
	LocationName         string
	ViewKey              string
	Standby              bool
	Sessions             []DisplaySessionState
}

// DisplaySessionState contains only Display-safe Published and live Session facts.
type DisplaySessionState struct {
	ID                 int
	Title              string
	Speaker            string
	PublicDetails      string
	AudienceVisibility string
	TimerTitle         string
	ForecastStart      time.Time
	ForecastEnd        time.Time
	Lifecycle          string
	LiveStateRevision  int
	ActualStart        time.Time
	ActualEnd          *time.Time
	Type               string
	TimingPolicy       string
	RunPlannedStart    time.Time
	RunPlannedEnd      time.Time
	LocationIDs        []int
	LaneIDs            []int
	TrackIDs           []int
}

// LoadDisplaySnapshot authenticates a credential hash and captures one Active Event snapshot.
func (installationStore *SQLite) LoadDisplaySnapshot(
	ctx context.Context,
	credentialHash string,
) (DisplaySnapshotState, error) {
	internalContext := systemContext(ctx)
	transaction, err := installationStore.client.Tx(internalContext)
	if err != nil {
		return DisplaySnapshotState{}, opaqueError("begin Display Snapshot", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	client := transaction.Client()
	credential, err := client.DisplayCredential.Query().Where(
		displaycredential.TokenHashEQ(credentialHash),
		displaycredential.RevokedAtIsNil(),
	).WithDisplay().Only(internalContext)
	if ent.IsNotFound(err) {
		return DisplaySnapshotState{}, ErrDisplayCredential
	}
	if err != nil {
		return DisplaySnapshotState{}, opaqueError("authenticate Display Snapshot", err)
	}
	found := credential.Edges.Display
	if found == nil {
		return DisplaySnapshotState{}, opaqueError("load Display Snapshot owner", errors.New("missing Display"))
	}
	result := DisplaySnapshotState{
		Display: Display{ID: found.ID, Name: found.Name, EnrolledAt: found.EnrolledAt},
		Standby: true,
	}
	routing, err := client.Installation.Query().
		Where(installation.ActiveEventIDNotNil()).
		Only(internalContext)
	if ent.IsNotFound(err) {
		return result, nil
	}
	if err != nil {
		return DisplaySnapshotState{}, opaqueError("load Display Snapshot routing", err)
	}
	result.ActiveEventID = *routing.ActiveEventID
	result.ActivationGeneration = routing.ActivationGeneration
	activeEvent, err := client.Event.Get(internalContext, result.ActiveEventID)
	if err != nil {
		return DisplaySnapshotState{}, opaqueError("load Display Snapshot Event", err)
	}
	result.EventName = activeEvent.Name
	result.EventTimezone = activeEvent.Timezone
	result.DisplayConfiguration = activeEvent.DisplayConfiguration
	published, err := loadCrewRundown(internalContext, client, result.ActiveEventID)
	if err != nil {
		return DisplaySnapshotState{}, err
	}
	result.PublishedRevision = published.PublishedRevision
	assignment, err := client.DisplayAssignment.Query().Where(
		displayassignment.DisplayIDEQ(found.ID),
		displayassignment.EventIDEQ(result.ActiveEventID),
	).Only(internalContext)
	if ent.IsNotFound(err) {
		return result, nil
	}
	if err != nil {
		return DisplaySnapshotState{}, opaqueError("load Display Snapshot Assignment", err)
	}
	result.LocationID = assignment.LocationID
	result.LocationName = publishedLocationName(published.Locations, assignment.LocationID)
	if result.LocationName == "" {
		return result, nil
	}
	result.ViewKey = assignment.ViewKey
	result.Standby = false
	for _, publishedSession := range published.Sessions {
		sessionState, sessionErr := loadDisplaySession(
			internalContext,
			client,
			publishedSession,
		)
		if sessionErr != nil {
			return DisplaySnapshotState{}, sessionErr
		}
		result.Sessions = append(result.Sessions, sessionState)
	}
	return result, nil
}

func loadDisplaySession(
	ctx context.Context,
	client *ent.Client,
	published PublishedSession,
) (DisplaySessionState, error) {
	identity, err := client.Session.Get(ctx, published.ID)
	if err != nil {
		return DisplaySessionState{}, opaqueError("load Display Session identity", err)
	}
	result := DisplaySessionState{
		ID: published.ID, AudienceVisibility: published.AudienceVisibility,
		TimerTitle:    published.Title,
		ForecastStart: published.PlannedStart, ForecastEnd: published.PlannedEnd,
		Type: published.Type, TimingPolicy: published.TimingPolicy,
		RunPlannedStart: published.PlannedStart, RunPlannedEnd: published.PlannedEnd,
		Lifecycle: identity.Lifecycle.String(), LiveStateRevision: identity.LiveStateRevision,
		LocationIDs: published.LocationIDs, LaneIDs: published.LaneIDs, TrackIDs: published.TrackIDs,
	}
	if published.AudienceVisibility == "Public" {
		result.Title = published.Title
		result.Speaker = published.Speaker
		result.PublicDetails = published.PublicDetails
	}
	run, err := client.SessionRun.Query().Where(
		sessionrun.SessionIDEQ(published.ID),
	).Order(ent.Desc(sessionrun.FieldID)).First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return DisplaySessionState{}, opaqueError("load Display Session Run", err)
	}
	if err == nil {
		var snapshot SessionRunSnapshot
		if decodeErr := json.Unmarshal([]byte(run.SnapshotJSON), &snapshot); decodeErr != nil {
			return DisplaySessionState{}, opaqueError("decode Display Session Run Snapshot", decodeErr)
		}
		result.ActualStart = run.ActualStart
		result.Type = snapshot.Type
		result.TimingPolicy = snapshot.TimingPolicy
		result.RunPlannedStart = snapshot.PlannedStart
		result.RunPlannedEnd = snapshot.PlannedEnd
		if !run.ActualEnd.IsZero() {
			actualEnd := run.ActualEnd
			result.ActualEnd = &actualEnd
		}
	}
	return result, nil
}

func publishedLocationName(locations []PublishedLocation, locationID int) string {
	for _, location := range locations {
		if location.ID == locationID {
			return location.Name
		}
	}
	return ""
}
