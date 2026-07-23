package store

import (
	"context"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/installation"
	"github.com/dotwaffle/beamers/ent/lane"
	"github.com/dotwaffle/beamers/ent/location"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
	"github.com/dotwaffle/beamers/ent/sessionrun"
	"github.com/dotwaffle/beamers/ent/track"
)

// PublicScheduleState contains only attendee-safe current Published data.
type PublicScheduleState struct {
	EventID         int
	EventName       string
	Timezone        string
	ContentLanguage string
	Locations       []PublicScheduleLocation
	Lanes           []PublicScheduleLane
	Tracks          []PublicScheduleTrack
	Sessions        []PublicScheduleSession
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
	ID            int
	Title         string
	Speaker       string
	PublicDetails string
	ForecastStart time.Time
	ForecastEnd   time.Time
	Lifecycle     string
	ActualStart   time.Time
	ActualEnd     *time.Time
	LocationIDs   []int
	LaneIDs       []int
	TrackIDs      []int
}

// LoadPublicSchedule returns the Active Event's current public projection.
func (installationStore *SQLite) LoadPublicSchedule(ctx context.Context) (PublicScheduleState, error) {
	internalContext := systemContext(ctx)
	active, err := installationStore.client.Installation.Query().
		Where(installation.ActiveEventIDNotNil()).
		Only(internalContext)
	if ent.IsNotFound(err) {
		return PublicScheduleState{}, nil
	}
	if err != nil {
		return PublicScheduleState{}, opaqueError("load public Schedule routing", err)
	}
	activeEvent, err := installationStore.client.Event.Get(internalContext, *active.ActiveEventID)
	if err != nil {
		return PublicScheduleState{}, opaqueError("load public Schedule Event", err)
	}
	result := PublicScheduleState{
		EventID: activeEvent.ID, EventName: activeEvent.Name,
		Timezone: activeEvent.Timezone, ContentLanguage: activeEvent.ContentLanguage,
	}
	if err := installationStore.loadPublicScheduleNames(internalContext, &result); err != nil {
		return PublicScheduleState{}, err
	}
	if err := installationStore.loadPublicScheduleSessions(internalContext, &result); err != nil {
		return PublicScheduleState{}, err
	}
	return result, nil
}

func (installationStore *SQLite) loadPublicScheduleNames(
	ctx context.Context,
	result *PublicScheduleState,
) error {
	locations, err := installationStore.client.Location.Query().
		Where(location.EventIDEQ(result.EventID)).All(ctx)
	if err != nil {
		return opaqueError("load public Schedule Locations", err)
	}
	for _, identity := range locations {
		version, queryErr := identity.QueryPublishedVersions().Order(ent.Desc("published_revision")).First(ctx)
		if ent.IsNotFound(queryErr) {
			continue
		}
		if queryErr != nil {
			return opaqueError("load public Schedule Location", queryErr)
		}
		if version.Retired {
			continue
		}
		result.Locations = append(result.Locations, PublicScheduleLocation{ID: identity.ID, Name: version.Name})
	}
	lanes, err := installationStore.client.Lane.Query().Where(lane.EventIDEQ(result.EventID)).All(ctx)
	if err != nil {
		return opaqueError("load public Schedule Lanes", err)
	}
	for _, identity := range lanes {
		version, queryErr := identity.QueryPublishedVersions().Order(ent.Desc("published_revision")).First(ctx)
		if ent.IsNotFound(queryErr) {
			continue
		}
		if queryErr != nil {
			return opaqueError("load public Schedule Lane", queryErr)
		}
		if version.Retired {
			continue
		}
		result.Lanes = append(result.Lanes, PublicScheduleLane{ID: identity.ID, Name: version.Name})
	}
	tracks, err := installationStore.client.Track.Query().Where(track.EventIDEQ(result.EventID)).All(ctx)
	if err != nil {
		return opaqueError("load public Schedule Tracks", err)
	}
	for _, identity := range tracks {
		version, queryErr := identity.QueryPublishedVersions().Order(ent.Desc("published_revision")).First(ctx)
		if ent.IsNotFound(queryErr) {
			continue
		}
		if queryErr != nil {
			return opaqueError("load public Schedule Track", queryErr)
		}
		if version.Retired {
			continue
		}
		result.Tracks = append(result.Tracks, PublicScheduleTrack{ID: identity.ID, Name: version.Name})
	}
	return nil
}

func (installationStore *SQLite) loadPublicScheduleSessions(
	ctx context.Context,
	result *PublicScheduleState,
) error {
	sessions, err := installationStore.client.Session.Query().Where(session.EventIDEQ(result.EventID)).All(ctx)
	if err != nil {
		return opaqueError("load public Schedule Sessions", err)
	}
	for _, identity := range sessions {
		version, queryErr := identity.QueryPublishedVersions().
			Order(ent.Desc("published_revision")).First(ctx)
		if ent.IsNotFound(queryErr) {
			continue
		}
		if queryErr != nil {
			return opaqueError("load public Schedule Session", queryErr)
		}
		if version.AudienceVisibility != sessionpublishedversion.AudienceVisibilityPublic {
			continue
		}
		locations, queryErr := version.QueryLocations().IDs(ctx)
		if queryErr != nil {
			return opaqueError("load public Schedule Session Locations", queryErr)
		}
		lanes, queryErr := version.QueryLanes().IDs(ctx)
		if queryErr != nil {
			return opaqueError("load public Schedule Session Lanes", queryErr)
		}
		tracks, queryErr := version.QueryTracks().IDs(ctx)
		if queryErr != nil {
			return opaqueError("load public Schedule Session Tracks", queryErr)
		}
		var actualStart time.Time
		var actualEnd *time.Time
		run, queryErr := installationStore.client.SessionRun.Query().
			Where(sessionrun.SessionIDEQ(identity.ID)).Order(ent.Desc(sessionrun.FieldID)).First(ctx)
		if queryErr != nil && !ent.IsNotFound(queryErr) {
			return opaqueError("load public Schedule Session Run", queryErr)
		}
		if queryErr == nil {
			actualStart = run.ActualStart
			if !run.ActualEnd.IsZero() {
				ended := run.ActualEnd
				actualEnd = &ended
			}
		}
		details := correctedSessionDetails(identity, SessionDetails{
			Title: version.Title, Speaker: version.Speaker, PublicDetails: version.PublicDetails,
		})
		result.Sessions = append(result.Sessions, PublicScheduleSession{
			ID: identity.ID, Title: details.Title, Speaker: details.Speaker, PublicDetails: details.PublicDetails,
			ForecastStart: version.PlannedStart, ForecastEnd: version.PlannedEnd,
			Lifecycle: identity.Lifecycle.String(), ActualStart: actualStart, ActualEnd: actualEnd,
			LocationIDs: locations, LaneIDs: lanes, TrackIDs: tracks,
		})
	}
	return nil
}
