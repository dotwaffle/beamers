package store

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/draftchange"
	"github.com/dotwaffle/beamers/ent/draftchangedependency"
	"github.com/dotwaffle/beamers/ent/lane"
	"github.com/dotwaffle/beamers/ent/location"
	"github.com/dotwaffle/beamers/ent/rundown"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
	"github.com/dotwaffle/beamers/ent/track"
	"github.com/dotwaffle/beamers/internal/viewer"
)

// PendingDraftChange is the persistence projection used to form a Publish Preview.
type PendingDraftChange struct {
	ID                int
	Kind              string
	TargetType        string
	TargetID          int
	PayloadJSON       string
	Status            string
	PublishedRevision int
	Dependencies      []int
}

// PublishState is the exact revisioned input to Preview and Publish.
type PublishState struct {
	DraftRevision     int
	PublishedRevision int
	Changes           []PendingDraftChange
}

// LoadPublishState returns all Draft Change evidence needed for dependency closure.
func (installation *SQLite) LoadPublishState(ctx context.Context, eventID int) (PublishState, error) {
	return loadPublishState(
		ctx,
		installation.client.Rundown,
		installation.client.DraftChange,
		installation.client.DraftChangeDependency,
		eventID,
	)
}

// LoadPublishState returns transaction-consistent Preview inputs.
func (transaction *CommandTx) LoadPublishState(ctx context.Context, eventID int) (PublishState, error) {
	return loadPublishState(
		ctx,
		transaction.transaction.Rundown,
		transaction.transaction.DraftChange,
		transaction.transaction.DraftChangeDependency,
		eventID,
	)
}

func loadPublishState(
	ctx context.Context,
	rundowns *ent.RundownClient,
	changes *ent.DraftChangeClient,
	dependencies *ent.DraftChangeDependencyClient,
	eventID int,
) (PublishState, error) {
	found, err := rundowns.Query().Where(rundown.EventIDEQ(eventID)).Only(ctx)
	if ent.IsNotFound(err) {
		return PublishState{}, ErrEventNotFound
	}
	if err != nil {
		return PublishState{}, opaqueError("load Rundown revisions", err)
	}
	internalContext := viewer.SystemContext(ctx)
	storedChanges, err := changes.Query().
		Where(draftchange.EventIDEQ(eventID)).
		Order(ent.Asc(draftchange.FieldID)).
		All(internalContext)
	if err != nil {
		return PublishState{}, opaqueError("load Draft Changes", err)
	}
	ids := make([]int, 0, len(storedChanges))
	for _, change := range storedChanges {
		ids = append(ids, change.ID)
	}
	byChange := make(map[int][]int, len(ids))
	if len(ids) > 0 {
		storedDependencies, queryErr := dependencies.Query().
			Where(draftchangedependency.ChangeIDIn(ids...)).
			All(internalContext)
		if queryErr != nil {
			return PublishState{}, opaqueError("load Draft Change dependencies", queryErr)
		}
		for _, dependency := range storedDependencies {
			byChange[dependency.ChangeID] = append(byChange[dependency.ChangeID], dependency.DependsOnID)
		}
	}
	state := PublishState{
		DraftRevision: found.DraftRevision, PublishedRevision: found.PublishedRevision,
		Changes: make([]PendingDraftChange, 0, len(storedChanges)),
	}
	for _, change := range storedChanges {
		publishedRevision := 0
		if change.PublishedRevision != nil {
			publishedRevision = *change.PublishedRevision
		}
		state.Changes = append(state.Changes, PendingDraftChange{
			ID: change.ID, Kind: change.Kind, TargetType: change.TargetType,
			TargetID: change.TargetID, PayloadJSON: change.PayloadJSON,
			Status: change.Status.String(), PublishedRevision: publishedRevision,
			Dependencies: byChange[change.ID],
		})
	}
	return state, nil
}

// PublishParams contains one exact confirmed Publish transaction.
type PublishParams struct {
	EventID                   int
	ExpectedDraftRevision     int
	ExpectedPublishedRevision int
	ChangeIDs                 []int
	Now                       time.Time
}

// PublishResult is the minimal durable outcome of Publish.
type PublishResult struct {
	DraftRevision     int   `json:"draft_revision"`
	PublishedRevision int   `json:"published_revision"`
	ChangeIDs         []int `json:"change_ids"`
}

// Publish creates immutable versions for the exact confirmed effective changes.
func (transaction *CommandTx) Publish(ctx context.Context, params PublishParams) (PublishResult, error) {
	current, err := transaction.transaction.Rundown.Query().
		Where(
			rundown.EventIDEQ(params.EventID),
			rundown.DraftRevisionEQ(params.ExpectedDraftRevision),
			rundown.PublishedRevisionEQ(params.ExpectedPublishedRevision),
		).
		Only(ctx)
	if ent.IsNotFound(err) {
		return PublishResult{}, ErrDraftRevisionConflict
	}
	if err != nil {
		return PublishResult{}, opaqueError("load confirmed Rundown revisions", err)
	}
	internalContext := viewer.SystemContext(ctx)
	changes, err := transaction.transaction.DraftChange.Query().
		Where(
			draftchange.EventIDEQ(params.EventID),
			draftchange.IDIn(params.ChangeIDs...),
			draftchange.StatusEQ(draftchange.StatusEffective),
		).
		All(internalContext)
	if err != nil {
		return PublishResult{}, opaqueError("load confirmed Draft Changes", err)
	}
	if len(changes) != len(params.ChangeIDs) {
		return PublishResult{}, ErrDraftRevisionConflict
	}
	nextPublishedRevision := current.PublishedRevision + 1
	sort.Slice(changes, func(first, second int) bool {
		return publishKindOrder(changes[first].Kind) < publishKindOrder(changes[second].Kind)
	})
	for _, change := range changes {
		if publishErr := transaction.publishChange(ctx, change, nextPublishedRevision, params.Now); publishErr != nil {
			return PublishResult{}, publishErr
		}
	}
	if _, updateErr := transaction.transaction.DraftChange.Update().
		Where(draftchange.IDIn(params.ChangeIDs...)).
		SetStatus(draftchange.StatusPublished).
		SetPublishedRevision(nextPublishedRevision).
		Save(internalContext); updateErr != nil {
		return PublishResult{}, opaqueError("mark Draft Changes Published", updateErr)
	}
	updated, err := transaction.transaction.Rundown.UpdateOneID(current.ID).
		Where(
			rundown.DraftRevisionEQ(params.ExpectedDraftRevision),
			rundown.PublishedRevisionEQ(params.ExpectedPublishedRevision),
		).
		AddDraftRevision(1).
		AddPublishedRevision(1).
		Save(ctx)
	if ent.IsNotFound(err) {
		return PublishResult{}, ErrDraftRevisionConflict
	}
	if err != nil {
		return PublishResult{}, opaqueError("advance Published revisions", err)
	}
	changeIDs := append([]int(nil), params.ChangeIDs...)
	sort.Ints(changeIDs)
	return PublishResult{
		DraftRevision: updated.DraftRevision, PublishedRevision: updated.PublishedRevision,
		ChangeIDs: changeIDs,
	}, nil
}

func (transaction *CommandTx) publishChange(
	ctx context.Context,
	change *ent.DraftChange,
	publishedRevision int,
	now time.Time,
) error {
	switch change.Kind {
	case "CreateLocation":
		return transaction.publishLocation(ctx, change.TargetID, publishedRevision, now)
	case "CreateLane":
		return transaction.publishLane(ctx, change.TargetID, publishedRevision, now)
	case "CreateTrack":
		return transaction.publishTrack(ctx, change.TargetID, publishedRevision, now)
	case "CreateSession":
		return transaction.publishSession(ctx, change.TargetID, publishedRevision, now)
	default:
		return errors.New("unsupported Draft Change kind")
	}
}

func publishKindOrder(kind string) int {
	switch kind {
	case "CreateLocation":
		return 1
	case "CreateLane":
		return 2
	case "CreateTrack":
		return 3
	case "CreateSession":
		return 4
	default:
		return 100
	}
}

func (transaction *CommandTx) publishLocation(ctx context.Context, id, revision int, now time.Time) error {
	identity, err := transaction.transaction.Location.Get(ctx, id)
	if err != nil {
		return opaqueError("load Draft Location identity", err)
	}
	state, err := identity.QueryDraft().Only(ctx)
	if err != nil {
		return opaqueError("load Location Draft state", err)
	}
	_, err = transaction.transaction.LocationPublishedVersion.Create().
		SetLocationID(id).
		SetPublishedRevision(revision).
		SetName(state.Name).
		SetRetired(state.Retired).
		SetCreatedAt(now).
		Save(ctx)
	if err != nil {
		return opaqueError("publish Location version", err)
	}
	return nil
}

func (transaction *CommandTx) publishLane(ctx context.Context, id, revision int, now time.Time) error {
	identity, err := transaction.transaction.Lane.Get(ctx, id)
	if err != nil {
		return opaqueError("load Draft Lane identity", err)
	}
	state, err := identity.QueryDraft().Only(ctx)
	if err != nil {
		return opaqueError("load Lane Draft state", err)
	}
	_, err = transaction.transaction.LanePublishedVersion.Create().
		SetLaneID(id).
		SetLocationID(state.LocationID).
		SetPublishedRevision(revision).
		SetName(state.Name).
		SetRetired(state.Retired).
		SetCreatedAt(now).
		Save(ctx)
	if err != nil {
		return opaqueError("publish Lane version", err)
	}
	return nil
}

func (transaction *CommandTx) publishTrack(ctx context.Context, id, revision int, now time.Time) error {
	identity, err := transaction.transaction.Track.Get(ctx, id)
	if err != nil {
		return opaqueError("load Draft Track identity", err)
	}
	state, err := identity.QueryDraft().Only(ctx)
	if err != nil {
		return opaqueError("load Track Draft state", err)
	}
	_, err = transaction.transaction.TrackPublishedVersion.Create().
		SetTrackID(id).
		SetPublishedRevision(revision).
		SetName(state.Name).
		SetRetired(state.Retired).
		SetCreatedAt(now).
		Save(ctx)
	if err != nil {
		return opaqueError("publish Track version", err)
	}
	return nil
}

func (transaction *CommandTx) publishSession(ctx context.Context, id, revision int, now time.Time) error {
	identity, err := transaction.transaction.Session.Get(ctx, id)
	if err != nil {
		return opaqueError("load Draft Session identity", err)
	}
	state, err := identity.QueryDraft().Only(ctx)
	if err != nil {
		return opaqueError("load Session Draft state", err)
	}
	lanes, err := state.QueryLanes().All(ctx)
	if err != nil {
		return opaqueError("load Session Draft Lanes", err)
	}
	laneIDs := make([]int, 0, len(lanes))
	for _, item := range lanes {
		laneIDs = append(laneIDs, item.ID)
	}
	locations, err := state.QueryLocations().All(ctx)
	if err != nil {
		return opaqueError("load Session Draft Locations", err)
	}
	locationIDs := make([]int, 0, len(locations))
	for _, item := range locations {
		locationIDs = append(locationIDs, item.ID)
	}
	tracks, err := state.QueryTracks().All(ctx)
	if err != nil {
		return opaqueError("load Session Draft Tracks", err)
	}
	trackIDs := make([]int, 0, len(tracks))
	for _, item := range tracks {
		trackIDs = append(trackIDs, item.ID)
	}
	create := transaction.transaction.SessionPublishedVersion.Create().
		SetSessionID(id).
		SetPublishedRevision(revision).
		SetTitle(state.Title).
		SetType(sessionpublishedversion.Type(state.Type)).
		SetAudienceVisibility(sessionpublishedversion.AudienceVisibility(state.AudienceVisibility)).
		SetPlannedStart(state.PlannedStart).
		SetPlannedEnd(state.PlannedEnd).
		SetTimingPolicy(sessionpublishedversion.TimingPolicy(state.TimingPolicy)).
		SetMinimumDurationSeconds(state.MinimumDurationSeconds).
		SetStartBoundary(sessionpublishedversion.StartBoundary(state.StartBoundary)).
		SetEndBoundary(sessionpublishedversion.EndBoundary(state.EndBoundary)).
		SetCreatedAt(now).
		AddLaneIDs(laneIDs...).
		AddLocationIDs(locationIDs...).
		AddTrackIDs(trackIDs...)
	if state.PublicDetails != "" {
		create.SetPublicDetails(state.PublicDetails)
	}
	if state.CrewNotes != "" {
		create.SetCrewNotes(state.CrewNotes)
	}
	if _, err = create.Save(ctx); err != nil {
		return opaqueError("publish Session version", err)
	}
	return nil
}

// CrewRundownState is the store-owned Published projection input.
type CrewRundownState struct {
	DraftRevision     int
	PublishedRevision int
	Locations         []PublishedLocation
	Lanes             []PublishedLane
	Tracks            []PublishedTrack
	Sessions          []PublishedSession
}

// PublishedLocation is the store projection of one current Location version.
type PublishedLocation struct {
	ID      int
	Name    string
	Retired bool
}

// PublishedLane is the store projection of one current Lane version.
type PublishedLane struct {
	ID         int
	Name       string
	LocationID int
	Retired    bool
}

// PublishedTrack is the store projection of one current Track version.
type PublishedTrack struct {
	ID      int
	Name    string
	Retired bool
}

// PublishedSession is the store projection of one current Session version.
type PublishedSession struct {
	ID                     int
	Title                  string
	Type                   string
	AudienceVisibility     string
	PublicDetails          string
	CrewNotes              string
	PlannedStart           time.Time
	PlannedEnd             time.Time
	TimingPolicy           string
	MinimumDurationSeconds int
	StartBoundary          string
	EndBoundary            string
	LaneIDs                []int
	LocationIDs            []int
	TrackIDs               []int
}

// LoadCrewRundown returns the current Published versions without exposing Ent entities.
func (installation *SQLite) LoadCrewRundown(ctx context.Context, eventID int) (CrewRundownState, error) {
	revisions, err := installation.client.Rundown.Query().Where(rundown.EventIDEQ(eventID)).Only(ctx)
	if ent.IsNotFound(err) {
		return CrewRundownState{}, ErrEventNotFound
	}
	if err != nil {
		return CrewRundownState{}, opaqueError("load Crew Rundown revisions", err)
	}
	result := CrewRundownState{
		DraftRevision: revisions.DraftRevision, PublishedRevision: revisions.PublishedRevision,
	}
	locations, err := installation.client.Location.Query().Where(location.EventIDEQ(eventID)).All(ctx)
	if err != nil {
		return CrewRundownState{}, opaqueError("load Crew Locations", err)
	}
	for _, identity := range locations {
		version, queryErr := identity.QueryPublishedVersions().
			Order(ent.Desc("published_revision")).
			First(ctx)
		if ent.IsNotFound(queryErr) {
			continue
		}
		if queryErr != nil {
			return CrewRundownState{}, opaqueError("load current Published Location", queryErr)
		}
		if !version.Retired {
			result.Locations = append(result.Locations, PublishedLocation{ID: identity.ID, Name: version.Name})
		}
	}
	lanes, err := installation.client.Lane.Query().Where(lane.EventIDEQ(eventID)).All(ctx)
	if err != nil {
		return CrewRundownState{}, opaqueError("load Crew Lanes", err)
	}
	for _, identity := range lanes {
		version, queryErr := identity.QueryPublishedVersions().Order(ent.Desc("published_revision")).First(ctx)
		if ent.IsNotFound(queryErr) {
			continue
		}
		if queryErr != nil {
			return CrewRundownState{}, opaqueError("load current Published Lane", queryErr)
		}
		if !version.Retired {
			result.Lanes = append(result.Lanes, PublishedLane{
				ID: identity.ID, Name: version.Name, LocationID: version.LocationID,
			})
		}
	}
	tracks, err := installation.client.Track.Query().Where(track.EventIDEQ(eventID)).All(ctx)
	if err != nil {
		return CrewRundownState{}, opaqueError("load Crew Tracks", err)
	}
	for _, identity := range tracks {
		version, queryErr := identity.QueryPublishedVersions().Order(ent.Desc("published_revision")).First(ctx)
		if ent.IsNotFound(queryErr) {
			continue
		}
		if queryErr != nil {
			return CrewRundownState{}, opaqueError("load current Published Track", queryErr)
		}
		if !version.Retired {
			result.Tracks = append(result.Tracks, PublishedTrack{ID: identity.ID, Name: version.Name})
		}
	}
	sessions, err := installation.client.Session.Query().Where(session.EventIDEQ(eventID)).All(ctx)
	if err != nil {
		return CrewRundownState{}, opaqueError("load Crew Sessions", err)
	}
	for _, identity := range sessions {
		version, queryErr := identity.QueryPublishedVersions().Order(ent.Desc("published_revision")).First(ctx)
		if ent.IsNotFound(queryErr) {
			continue
		}
		if queryErr != nil {
			return CrewRundownState{}, opaqueError("load current Published Session", queryErr)
		}
		lanes, queryErr := version.QueryLanes().All(ctx)
		if queryErr != nil {
			return CrewRundownState{}, opaqueError("load Published Session Lanes", queryErr)
		}
		laneIDs := make([]int, 0, len(lanes))
		for _, item := range lanes {
			laneIDs = append(laneIDs, item.ID)
		}
		locations, queryErr := version.QueryLocations().All(ctx)
		if queryErr != nil {
			return CrewRundownState{}, opaqueError("load Published Session Locations", queryErr)
		}
		locationIDs := make([]int, 0, len(locations))
		for _, item := range locations {
			locationIDs = append(locationIDs, item.ID)
		}
		tracks, queryErr := version.QueryTracks().All(ctx)
		if queryErr != nil {
			return CrewRundownState{}, opaqueError("load Published Session Tracks", queryErr)
		}
		trackIDs := make([]int, 0, len(tracks))
		for _, item := range tracks {
			trackIDs = append(trackIDs, item.ID)
		}
		result.Sessions = append(result.Sessions, PublishedSession{
			ID: identity.ID, Title: version.Title, Type: version.Type.String(),
			AudienceVisibility: version.AudienceVisibility.String(),
			PublicDetails:      version.PublicDetails, CrewNotes: version.CrewNotes,
			PlannedStart: version.PlannedStart, PlannedEnd: version.PlannedEnd,
			TimingPolicy:           version.TimingPolicy.String(),
			MinimumDurationSeconds: version.MinimumDurationSeconds,
			StartBoundary:          version.StartBoundary.String(), EndBoundary: version.EndBoundary.String(),
			LaneIDs: laneIDs, LocationIDs: locationIDs, TrackIDs: trackIDs,
		})
	}
	return result, nil
}
