package store

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/competitionentry"
	"github.com/dotwaffle/beamers/ent/installation"
	"github.com/dotwaffle/beamers/ent/lane"
	"github.com/dotwaffle/beamers/ent/lanepublishedversion"
	"github.com/dotwaffle/beamers/ent/location"
	"github.com/dotwaffle/beamers/ent/locationpublishedversion"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
	"github.com/dotwaffle/beamers/ent/track"
	"github.com/dotwaffle/beamers/ent/trackpublishedversion"
	"github.com/dotwaffle/beamers/internal/publictime"
)

// PublicScheduleState contains only attendee-safe current Published data.
type PublicScheduleState struct {
	EventID          int
	EventName        string
	Timezone         string
	EventLocale      string
	ContentLanguage  string
	EventDayBoundary string
	Locations        []PublicScheduleLocation
	Lanes            []PublicScheduleLane
	Tracks           []PublicScheduleTrack
	Sessions         []PublicScheduleSession
}

// PublicScheduleLocation identifies one attendee-visible Location.
type PublicScheduleLocation struct {
	ID   int
	Name string
}

// PublicScheduleLane identifies one attendee-visible Lane.
type PublicScheduleLane struct {
	ID   int
	Name string
}

// PublicScheduleTrack identifies one attendee-visible Track.
type PublicScheduleTrack struct {
	ID   int
	Name string
}

// PublicScheduleSession contains no crew-only fields.
type PublicScheduleSession struct {
	ID                  int
	Type                string
	Title               string
	Speaker             string
	PublicDetails       string
	CancellationMessage string
	PublicTime          publictime.Facts
	LocationIDs         []int
	LaneIDs             []int
	TrackIDs            []int
	CompetitionEntries  []PublicCompetitionEntry
}

// PublicCompetitionEntry contains attendee-safe Included Entry details.
type PublicCompetitionEntry struct {
	ID                            int
	Name                          string
	PublicDetails                 string
	ResultDisposition             string
	PublicDisqualificationMessage string
}

// LoadPublicSchedule returns the Active Event's current public projection.
func (installationStore *SQLite) LoadPublicSchedule(ctx context.Context) (PublicScheduleState, error) {
	active, err := installationStore.readClient().Installation.Query().
		Where(installation.ActiveEventIDNotNil()).
		Only(ctx)
	if ent.IsNotFound(err) {
		return PublicScheduleState{}, nil
	}
	if err != nil {
		return PublicScheduleState{}, opaqueError("load public Schedule routing", err)
	}
	activeEvent, err := installationStore.readClient().Event.Get(ctx, *active.ActiveEventID)
	if err != nil {
		return PublicScheduleState{}, opaqueError("load public Schedule Event", err)
	}
	result := PublicScheduleState{
		EventID: activeEvent.ID, EventName: activeEvent.Name,
		Timezone: activeEvent.Timezone, EventLocale: activeEvent.EventLocale,
		ContentLanguage: activeEvent.ContentLanguage, EventDayBoundary: activeEvent.EventDayBoundary,
	}
	if err := installationStore.loadPublicScheduleNames(ctx, &result); err != nil {
		return PublicScheduleState{}, err
	}
	if err := installationStore.loadPublicScheduleSessions(ctx, &result); err != nil {
		return PublicScheduleState{}, err
	}
	return result, nil
}

func (installationStore *SQLite) loadPublicScheduleNames(
	ctx context.Context,
	result *PublicScheduleState,
) error {
	client := installationStore.readClient()
	locations, err := client.Location.Query().
		Where(location.EventIDEQ(result.EventID)).All(ctx)
	if err != nil {
		return opaqueError("load public Schedule Locations", err)
	}
	locationVersions, err := client.LocationPublishedVersion.Query().
		Where(
			locationpublishedversion.HasLocationWith(location.EventIDEQ(result.EventID)),
			latestPublishedVersion(
				locationpublishedversion.Table,
				locationpublishedversion.FieldLocationID,
			),
		).
		All(ctx)
	if err != nil {
		return opaqueError("load public Schedule Location names", err)
	}
	locationNames := make(map[int]*ent.LocationPublishedVersion, len(locationVersions))
	for _, version := range locationVersions {
		locationNames[version.LocationID] = version
	}
	for _, identity := range locations {
		version := locationNames[identity.ID]
		if version == nil || version.Retired {
			continue
		}
		result.Locations = append(result.Locations, PublicScheduleLocation{ID: identity.ID, Name: version.Name})
	}
	lanes, err := client.Lane.Query().Where(lane.EventIDEQ(result.EventID)).All(ctx)
	if err != nil {
		return opaqueError("load public Schedule Lanes", err)
	}
	laneVersions, err := client.LanePublishedVersion.Query().
		Where(
			lanepublishedversion.HasLaneWith(lane.EventIDEQ(result.EventID)),
			latestPublishedVersion(
				lanepublishedversion.Table,
				lanepublishedversion.FieldLaneID,
			),
		).
		All(ctx)
	if err != nil {
		return opaqueError("load public Schedule Lane names", err)
	}
	laneNames := make(map[int]*ent.LanePublishedVersion, len(laneVersions))
	for _, version := range laneVersions {
		laneNames[version.LaneID] = version
	}
	for _, identity := range lanes {
		version := laneNames[identity.ID]
		if version == nil || version.Retired {
			continue
		}
		result.Lanes = append(result.Lanes, PublicScheduleLane{ID: identity.ID, Name: version.Name})
	}
	tracks, err := client.Track.Query().Where(track.EventIDEQ(result.EventID)).All(ctx)
	if err != nil {
		return opaqueError("load public Schedule Tracks", err)
	}
	trackVersions, err := client.TrackPublishedVersion.Query().
		Where(
			trackpublishedversion.HasTrackWith(track.EventIDEQ(result.EventID)),
			latestPublishedVersion(
				trackpublishedversion.Table,
				trackpublishedversion.FieldTrackID,
			),
		).
		All(ctx)
	if err != nil {
		return opaqueError("load public Schedule Track names", err)
	}
	trackNames := make(map[int]*ent.TrackPublishedVersion, len(trackVersions))
	for _, version := range trackVersions {
		trackNames[version.TrackID] = version
	}
	for _, identity := range tracks {
		version := trackNames[identity.ID]
		if version == nil || version.Retired {
			continue
		}
		result.Tracks = append(result.Tracks, PublicScheduleTrack{ID: identity.ID, Name: version.Name})
	}
	return nil
}

// publicScheduleInputs are the whole-Event reads the attendee projection needs.
// Every map is filled by one query, so building the projection costs a fixed
// number of round trips no matter how many Sessions the Event has.
type publicScheduleInputs struct {
	Versions  map[int]*ent.SessionPublishedVersion
	Runs      map[int]*ent.SessionRun
	Baselines map[int]time.Time
	Entries   map[int][]*ent.CompetitionEntry
}

func (installationStore *SQLite) loadPublicScheduleSessions(
	ctx context.Context,
	result *PublicScheduleState,
) error {
	client := installationStore.readClient()
	sessions, err := client.Session.Query().Where(session.EventIDEQ(result.EventID)).All(ctx)
	if err != nil {
		return opaqueError("load public Schedule Sessions", err)
	}
	inputs, err := loadPublicScheduleInputs(ctx, client, result.EventID, sessions)
	if err != nil {
		return err
	}
	for _, identity := range sessions {
		version := inputs.Versions[identity.ID]
		if version == nil ||
			version.AudienceVisibility != sessionpublishedversion.AudienceVisibilityPublic {
			continue
		}
		projected, buildErr := publicScheduleSession(identity, version, inputs)
		if buildErr != nil {
			return buildErr
		}
		result.Sessions = append(result.Sessions, projected)
	}
	return nil
}

func loadPublicScheduleInputs(
	ctx context.Context,
	client *ent.Client,
	eventID int,
	sessions []*ent.Session,
) (publicScheduleInputs, error) {
	versions, err := client.SessionPublishedVersion.Query().
		Where(
			sessionpublishedversion.HasSessionWith(session.EventIDEQ(eventID)),
			latestPublishedVersion(
				sessionpublishedversion.Table,
				sessionpublishedversion.FieldSessionID,
			),
		).
		WithLanes().
		WithLocations().
		WithTracks().
		All(ctx)
	if err != nil {
		return publicScheduleInputs{}, opaqueError("load public Schedule Sessions", err)
	}
	inputs := publicScheduleInputs{
		Versions: make(map[int]*ent.SessionPublishedVersion, len(versions)),
	}
	for _, version := range versions {
		inputs.Versions[version.SessionID] = version
	}
	visible := make([]int, 0, len(sessions))
	running := make([]int, 0, len(sessions))
	competitions := make([]int, 0, len(sessions))
	for _, identity := range sessions {
		version := inputs.Versions[identity.ID]
		if version == nil ||
			version.AudienceVisibility != sessionpublishedversion.AudienceVisibilityPublic {
			continue
		}
		visible = append(visible, identity.ID)
		// A Run only contributes to the projection once a Session has actually
		// run, so Scheduled and Canceled Sessions never pay for the lookup.
		if identity.Lifecycle == session.LifecycleLive ||
			identity.Lifecycle == session.LifecycleEnded {
			running = append(running, identity.ID)
		}
		if version.Type == sessionpublishedversion.TypeCompetition {
			competitions = append(competitions, identity.ID)
		}
	}
	inputs.Runs, err = latestSessionRuns(ctx, client, running)
	if err != nil {
		return publicScheduleInputs{}, err
	}
	inputs.Baselines, err = sessionBaselineStarts(ctx, client, visible)
	if err != nil {
		return publicScheduleInputs{}, err
	}
	inputs.Entries, err = includedCompetitionEntries(ctx, client, competitions)
	if err != nil {
		return publicScheduleInputs{}, err
	}
	return inputs, nil
}

func publicScheduleSession(
	identity *ent.Session,
	version *ent.SessionPublishedVersion,
	inputs publicScheduleInputs,
) (PublicScheduleSession, error) {
	lanes, locations := sessionPlacementFromEdges(identity, version)
	tracks := make([]int, 0, len(version.Edges.Tracks))
	for _, item := range version.Edges.Tracks {
		tracks = append(tracks, item.ID)
	}
	slices.Sort(tracks)
	var actualStart time.Time
	var actualEnd *time.Time
	runDuration := version.PlannedEnd.Sub(version.PlannedStart)
	if run := inputs.Runs[identity.ID]; run != nil {
		var snapshot SessionRunSnapshot
		if decodeErr := json.Unmarshal([]byte(run.SnapshotJSON), &snapshot); decodeErr != nil {
			return PublicScheduleSession{}, opaqueError("decode public Schedule Run Snapshot", decodeErr)
		}
		runDuration = snapshot.PlannedEnd.Sub(snapshot.PlannedStart)
		actualStart = run.ActualStart
		if !run.ActualEnd.IsZero() {
			ended := run.ActualEnd
			actualEnd = &ended
		}
	}
	details := correctedSessionDetails(identity, SessionDetails{
		Title: version.Title, Speaker: version.Speaker, PublicDetails: version.PublicDetails,
	})
	competitionEntries, err := publicCompetitionEntries(identity, version, inputs.Entries[identity.ID])
	if err != nil {
		return PublicScheduleSession{}, err
	}
	forecastStart, forecastEnd := version.PlannedStart, version.PlannedEnd
	if !identity.ForecastStart.IsZero() {
		forecastStart = identity.ForecastStart
	}
	if !identity.ForecastEnd.IsZero() {
		forecastEnd = identity.ForecastEnd
	}
	var baselineStart *time.Time
	if start, found := inputs.Baselines[identity.ID]; found {
		baselineStart = instantPointer(start)
	}
	publicTime := publicTimeFacts(publicTimeFactsParams{
		Session:     identity,
		Lifecycle:   publictime.Lifecycle(identity.Lifecycle.String()),
		Forecast:    publictime.Range{Start: forecastStart, End: forecastEnd},
		ActualStart: actualStart,
		ActualEnd:   actualEnd,
		RunDuration: runDuration,
	}, baselineStart)
	return PublicScheduleSession{
		ID: identity.ID, Type: version.Type.String(),
		Title: details.Title, Speaker: details.Speaker, PublicDetails: details.PublicDetails,
		CancellationMessage: identity.PublicCancellationMessage,
		PublicTime:          publicTime,
		LocationIDs:         locations, LaneIDs: lanes, TrackIDs: tracks,
		CompetitionEntries: competitionEntries,
	}, nil
}

func publicCompetitionEntries(
	identity *ent.Session,
	version *ent.SessionPublishedVersion,
	entries []*ent.CompetitionEntry,
) ([]PublicCompetitionEntry, error) {
	if version.Type != sessionpublishedversion.TypeCompetition {
		return nil, nil
	}
	order, _, err := competitionEntryOrder(identity, entries)
	if err != nil {
		return nil, err
	}
	byID := make(map[int]*ent.CompetitionEntry, len(entries))
	for _, entry := range entries {
		byID[entry.ID] = entry
	}
	result := make([]PublicCompetitionEntry, 0, len(entries))
	for _, entryID := range order.EntryIDs {
		entry := byID[entryID]
		if entry == nil ||
			entry.ResultDisposition == competitionentry.ResultDispositionWithheld {
			continue
		}
		result = append(result, PublicCompetitionEntry{
			ID: entry.ID, Name: entry.Name, PublicDetails: entry.PublicDetails,
			ResultDisposition:             string(entry.ResultDisposition),
			PublicDisqualificationMessage: entry.PublicDisqualificationMessage,
		})
	}
	return result, nil
}

func includedCompetitionEntries(
	ctx context.Context,
	client *ent.Client,
	sessionIDs []int,
) (map[int][]*ent.CompetitionEntry, error) {
	if len(sessionIDs) == 0 {
		return map[int][]*ent.CompetitionEntry{}, nil
	}
	entries, err := client.CompetitionEntry.Query().
		Where(
			competitionentry.CompetitionSessionIDIn(sessionIDs...),
			competitionentry.DispositionEQ(competitionentry.DispositionIncluded),
		).
		Order(ent.Asc(competitionentry.FieldCreatedAt), ent.Asc(competitionentry.FieldID)).
		All(ctx)
	if err != nil {
		return nil, opaqueError("load public Competition Entries", err)
	}
	result := make(map[int][]*ent.CompetitionEntry, len(sessionIDs))
	for _, entry := range entries {
		result[entry.CompetitionSessionID] = append(result[entry.CompetitionSessionID], entry)
	}
	return result, nil
}
