package store

import (
	"context"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/event"
	"github.com/dotwaffle/beamers/ent/importreference"
	"github.com/dotwaffle/beamers/ent/lane"
	"github.com/dotwaffle/beamers/ent/location"
	"github.com/dotwaffle/beamers/ent/rundown"
	"github.com/dotwaffle/beamers/ent/sessionrun"
	"github.com/dotwaffle/beamers/ent/track"
)

// CSVImportState is the current Draft/reference input to a CSV preview.
type CSVImportState struct {
	DraftRevision    int
	EventRevision    int
	Timezone         string
	EventLocale      string
	ContentLanguage  string
	EventDayBoundary string
	Sessions         map[string]CSVImportSession
	LaneIDs          map[string][]int
	LocationIDs      map[string][]int
	TrackIDs         map[string][]int
}

// CSVImportSession is one Session matched by external Import Reference.
type CSVImportSession struct {
	ID      int
	HasRuns bool
	Draft   SessionDraftCreate
}

// LoadCSVImportState returns current state for a side-effect-free preview.
func (installation *SQLite) LoadCSVImportState(ctx context.Context, eventID int) (CSVImportState, error) {
	return loadCSVImportState(
		ctx, eventID,
		importreference.SourceFormatCSV,
		installation.client.Event, installation.client.Rundown,
		installation.client.ImportReference, installation.client.Lane,
		installation.client.Location, installation.client.Track,
		installation.client.Session, installation.client.SessionRun,
	)
}

// LoadCSVImportState returns transaction-consistent state for confirmation.
func (transaction *CommandTx) LoadCSVImportState(ctx context.Context, eventID int) (CSVImportState, error) {
	return loadCSVImportState(
		ctx, eventID,
		importreference.SourceFormatCSV,
		transaction.transaction.Event, transaction.transaction.Rundown,
		transaction.transaction.ImportReference, transaction.transaction.Lane,
		transaction.transaction.Location, transaction.transaction.Track,
		transaction.transaction.Session, transaction.transaction.SessionRun,
	)
}

func loadCSVImportState(
	ctx context.Context,
	eventID int,
	sourceFormat importreference.SourceFormat,
	events *ent.EventClient,
	rundowns *ent.RundownClient,
	references *ent.ImportReferenceClient,
	lanes *ent.LaneClient,
	locations *ent.LocationClient,
	tracks *ent.TrackClient,
	sessions *ent.SessionClient,
	runs *ent.SessionRunClient,
) (CSVImportState, error) {
	internalContext := systemContext(ctx)
	foundEvent, err := events.Query().Where(event.IDEQ(eventID)).Only(internalContext)
	if ent.IsNotFound(err) {
		return CSVImportState{}, ErrEventNotFound
	}
	if err != nil {
		return CSVImportState{}, opaqueError("load Schedule Import Event", err)
	}
	state := CSVImportState{
		EventRevision: foundEvent.Revision, Timezone: foundEvent.Timezone,
		EventLocale: foundEvent.EventLocale, ContentLanguage: foundEvent.ContentLanguage,
		EventDayBoundary: foundEvent.EventDayBoundary,
		Sessions:         make(map[string]CSVImportSession),
		LaneIDs:          make(map[string][]int), LocationIDs: make(map[string][]int), TrackIDs: make(map[string][]int),
	}
	foundRundown, err := rundowns.Query().Where(rundown.EventIDEQ(eventID)).Only(internalContext)
	if err == nil {
		state.DraftRevision = foundRundown.DraftRevision
	} else if !ent.IsNotFound(err) {
		return CSVImportState{}, opaqueError("load Schedule Import Draft revision", err)
	}
	storedReferences, err := references.Query().Where(
		importreference.EventIDEQ(eventID),
		importreference.SourceFormatEQ(sourceFormat),
		importreference.RecordTypeEQ(importreference.RecordTypeSession),
	).All(internalContext)
	if err != nil {
		return CSVImportState{}, opaqueError("load Schedule Import References", err)
	}
	for _, reference := range storedReferences {
		identity, queryErr := sessions.Get(internalContext, reference.TargetID)
		if queryErr != nil {
			return CSVImportState{}, opaqueError("load Schedule Import Reference target", queryErr)
		}
		draft, queryErr := identity.QueryDraft().Only(internalContext)
		if queryErr != nil {
			return CSVImportState{}, opaqueError("load Schedule Import Session Draft", queryErr)
		}
		laneIDs, queryErr := draft.QueryLanes().IDs(internalContext)
		if queryErr != nil {
			return CSVImportState{}, opaqueError("load Schedule Import Session Lanes", queryErr)
		}
		locationIDs, queryErr := draft.QueryLocations().IDs(internalContext)
		if queryErr != nil {
			return CSVImportState{}, opaqueError("load Schedule Import Session Locations", queryErr)
		}
		trackIDs, queryErr := draft.QueryTracks().IDs(internalContext)
		if queryErr != nil {
			return CSVImportState{}, opaqueError("load Schedule Import Session Tracks", queryErr)
		}
		hasRuns, queryErr := runs.Query().Where(sessionrun.SessionIDEQ(identity.ID)).Exist(internalContext)
		if queryErr != nil {
			return CSVImportState{}, opaqueError("load Schedule Import Session Run state", queryErr)
		}
		state.Sessions[reference.ExternalKey] = CSVImportSession{
			ID: identity.ID, HasRuns: hasRuns,
			Draft: SessionDraftCreate{
				ID: identity.ID, Title: draft.Title, Speaker: draft.Speaker,
				Type: draft.Type.String(), AudienceVisibility: draft.AudienceVisibility.String(),
				PublicDetails: draft.PublicDetails, CrewNotes: draft.CrewNotes,
				PlannedStart: draft.PlannedStart, PlannedEnd: draft.PlannedEnd,
				TimingPolicy: draft.TimingPolicy.String(), MinimumDurationSeconds: draft.MinimumDurationSeconds,
				StartBoundary: draft.StartBoundary.String(), EndBoundary: draft.EndBoundary.String(),
				Lanes: draftIDs(laneIDs), Locations: draftIDs(locationIDs), Tracks: draftIDs(trackIDs),
			},
		}
	}
	storedLanes, err := lanes.Query().Where(lane.EventIDEQ(eventID)).All(internalContext)
	if err != nil {
		return CSVImportState{}, opaqueError("load Schedule Import Lanes", err)
	}
	for _, identity := range storedLanes {
		draft, queryErr := identity.QueryDraft().Only(internalContext)
		if queryErr == nil {
			state.LaneIDs[draft.Name] = append(state.LaneIDs[draft.Name], identity.ID)
		} else if !ent.IsNotFound(queryErr) {
			return CSVImportState{}, opaqueError("load Schedule Import Lane Draft", queryErr)
		}
	}
	storedLocations, err := locations.Query().Where(location.EventIDEQ(eventID)).All(internalContext)
	if err != nil {
		return CSVImportState{}, opaqueError("load Schedule Import Locations", err)
	}
	for _, identity := range storedLocations {
		draft, queryErr := identity.QueryDraft().Only(internalContext)
		if queryErr == nil {
			state.LocationIDs[draft.Name] = append(state.LocationIDs[draft.Name], identity.ID)
		} else if !ent.IsNotFound(queryErr) {
			return CSVImportState{}, opaqueError("load Schedule Import Location Draft", queryErr)
		}
	}
	storedTracks, err := tracks.Query().Where(track.EventIDEQ(eventID)).All(internalContext)
	if err != nil {
		return CSVImportState{}, opaqueError("load Schedule Import Tracks", err)
	}
	for _, identity := range storedTracks {
		draft, queryErr := identity.QueryDraft().Only(internalContext)
		if queryErr == nil {
			state.TrackIDs[draft.Name] = append(state.TrackIDs[draft.Name], identity.ID)
		} else if !ent.IsNotFound(queryErr) {
			return CSVImportState{}, opaqueError("load Schedule Import Track Draft", queryErr)
		}
	}
	return state, nil
}

// LoadICalendarImportState returns current state for an iCalendar preview.
func (installation *SQLite) LoadICalendarImportState(ctx context.Context, eventID int) (CSVImportState, error) {
	return loadCSVImportState(
		ctx, eventID, importreference.SourceFormatICalendar,
		installation.client.Event, installation.client.Rundown,
		installation.client.ImportReference, installation.client.Lane,
		installation.client.Location, installation.client.Track,
		installation.client.Session, installation.client.SessionRun,
	)
}

// LoadICalendarImportState returns transaction-consistent iCalendar state.
func (transaction *CommandTx) LoadICalendarImportState(ctx context.Context, eventID int) (CSVImportState, error) {
	return loadCSVImportState(
		ctx, eventID, importreference.SourceFormatICalendar,
		transaction.transaction.Event, transaction.transaction.Rundown,
		transaction.transaction.ImportReference, transaction.transaction.Lane,
		transaction.transaction.Location, transaction.transaction.Track,
		transaction.transaction.Session, transaction.transaction.SessionRun,
	)
}

// CreateCSVImportReferences binds new external keys to created Session identities.
func (transaction *CommandTx) CreateCSVImportReferences(
	ctx context.Context,
	eventID int,
	externalKeys []string,
	changes []DraftChangeResult,
	now time.Time,
) error {
	return transaction.createImportReferences(ctx, eventID, importreference.SourceFormatCSV, externalKeys, changes, now)
}

// CreateICalendarImportReferences binds new UIDs to created Session identities.
func (transaction *CommandTx) CreateICalendarImportReferences(
	ctx context.Context,
	eventID int,
	externalKeys []string,
	changes []DraftChangeResult,
	now time.Time,
) error {
	return transaction.createImportReferences(ctx, eventID, importreference.SourceFormatICalendar, externalKeys, changes, now)
}

func (transaction *CommandTx) createImportReferences(
	ctx context.Context,
	eventID int,
	sourceFormat importreference.SourceFormat,
	externalKeys []string,
	changes []DraftChangeResult,
	now time.Time,
) error {
	createdIDs := make([]int, 0, len(externalKeys))
	for _, change := range changes {
		if change.Kind == "CreateSession" {
			createdIDs = append(createdIDs, change.TargetID)
		}
	}
	if len(createdIDs) != len(externalKeys) {
		return ErrDraftReference
	}
	internalContext := systemContext(ctx)
	for index, externalKey := range externalKeys {
		if _, err := transaction.transaction.ImportReference.Create().
			SetEventID(eventID).
			SetSourceFormat(sourceFormat).
			SetRecordType(importreference.RecordTypeSession).
			SetExternalKey(externalKey).
			SetTargetType("Session").
			SetTargetID(createdIDs[index]).
			SetCreatedAt(now).
			Save(internalContext); err != nil {
			return opaqueError("create Schedule Import Reference", err)
		}
	}
	return nil
}
