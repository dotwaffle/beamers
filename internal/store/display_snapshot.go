package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	entsql "entgo.io/ent/dialect/sql"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/displayassignment"
	"github.com/dotwaffle/beamers/ent/displaycredential"
	"github.com/dotwaffle/beamers/ent/displayoverride"
	"github.com/dotwaffle/beamers/ent/displayoverridestate"
	"github.com/dotwaffle/beamers/ent/installation"
	"github.com/dotwaffle/beamers/ent/prizegiving"
	"github.com/dotwaffle/beamers/ent/publicschedulebaseline"
	"github.com/dotwaffle/beamers/ent/publicschedulebaselineentry"
	"github.com/dotwaffle/beamers/ent/rundown"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionrun"
	"github.com/dotwaffle/beamers/internal/publictime"
)

// DisplaySnapshotState is one authorized, transactionally consistent Display projection.
type DisplaySnapshotState struct {
	Display               Display
	ActiveEventID         int
	EventName             string
	EventTimezone         string
	EventDayBoundary      string
	DisplayConfiguration  string
	ActivationGeneration  int
	PublishedRevision     int
	LocationID            int
	LocationName          string
	ViewKey               string
	DisplayGroupKeys      []string
	TargetLaneIDs         []int
	Standby               bool
	StageMessage          *DisplayOverride
	TechnicalDifficulties *DisplayOverride
	UrgentNotice          *DisplayOverride
	EmergencyAlert        *DisplayOverride
	Sessions              []DisplaySessionState
	ProgramChannelID      int
	ProgramOutputRevision int
	ProgramOutput         ProgramItem
}

// DisplaySessionState contains only Display-safe Published and live Session facts.
type DisplaySessionState struct {
	ID                      int
	Title                   string
	Speaker                 string
	PublicDetails           string
	AudienceVisibility      string
	TimerTitle              string
	ForecastStart           time.Time
	ForecastEnd             time.Time
	Lifecycle               string
	LiveStateRevision       int
	ActualStart             time.Time
	ActualEnd               *time.Time
	Type                    string
	TimingPolicy            string
	RunPlannedStart         time.Time
	RunPlannedEnd           time.Time
	TargetAdjustmentSeconds int
	TargetAdjustedAt        time.Time
	LocationIDs             []int
	LaneIDs                 []int
	TrackIDs                []int
	PublicTime              publictime.Facts
}

type displayRundownCacheKey struct {
	eventID           int
	publishedRevision int
	baselineID        int
}

type displayRundownState struct {
	CrewRundownState
	baselineStarts map[int]time.Time
}

// LoadDisplaySnapshot authenticates a credential hash and captures one Active Event snapshot.
func (installationStore *SQLite) LoadDisplaySnapshot(
	ctx context.Context,
	credentialHash string,
	now time.Time,
) (DisplaySnapshotState, error) {
	internalContext := systemContext(ctx)
	transaction, err := installationStore.reader.Tx(internalContext)
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
	result.EventDayBoundary = activeEvent.EventDayBoundary
	result.DisplayConfiguration = activeEvent.DisplayConfiguration
	published, err := installationStore.loadDisplayRundown(
		internalContext,
		client,
		result.ActiveEventID,
	)
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
	result.DisplayGroupKeys = slices.Clone(assignment.DisplayGroupKeys)
	for _, lane := range published.Lanes {
		if lane.LocationID == assignment.LocationID {
			result.TargetLaneIDs = append(result.TargetLaneIDs, lane.ID)
		}
	}
	result.Standby = false
	if overrideErr := loadCurrentDisplayOverrides(
		internalContext, client, assignment, now, &result,
	); overrideErr != nil {
		return DisplaySnapshotState{}, overrideErr
	}
	sessionIDs := make([]int, 0, len(published.Sessions))
	for _, publishedSession := range published.Sessions {
		sessionIDs = append(sessionIDs, publishedSession.ID)
	}
	sessionIdentities := make(map[int]*ent.Session, len(sessionIDs))
	if len(sessionIDs) > 0 {
		identities, queryErr := client.Session.Query().
			Where(session.IDIn(sessionIDs...)).
			All(internalContext)
		if queryErr != nil {
			return DisplaySnapshotState{}, opaqueError("load Display Session identities", queryErr)
		}
		for _, identity := range identities {
			sessionIdentities[identity.ID] = identity
		}
	}
	for _, publishedSession := range published.Sessions {
		identity := sessionIdentities[publishedSession.ID]
		if identity == nil {
			return DisplaySnapshotState{}, opaqueError(
				"load Display Session identity",
				errors.New("missing Session"),
			)
		}
		sessionState, sessionErr := loadDisplaySessionWithBaseline(
			internalContext,
			client,
			publishedSession,
			published.baselineStarts[publishedSession.ID],
			identity,
		)
		if sessionErr != nil {
			return DisplaySnapshotState{}, sessionErr
		}
		result.Sessions = append(result.Sessions, sessionState)
		programSession, programErr := isProgramChannelSession(
			internalContext,
			client,
			result.ActiveEventID,
			publishedSession,
		)
		if programErr != nil {
			return DisplaySnapshotState{}, programErr
		}
		if result.ViewKey == "competition-output" &&
			programSession &&
			slices.Contains(sessionState.LocationIDs, result.LocationID) &&
			(result.ProgramChannelID == 0 || sessionState.Lifecycle == "Live") {
			channel, channelErr := loadProgramChannel(
				internalContext, client, result.ActiveEventID, publishedSession.ID, now,
			)
			if channelErr != nil {
				return DisplaySnapshotState{}, channelErr
			}
			result.ProgramChannelID = channel.SessionID
			result.ProgramOutputRevision = channel.Revision
			result.ProgramOutput = channel.Output
			if sessionState.Lifecycle == "Live" {
				break
			}
		}
	}
	return result, nil
}

func (installationStore *SQLite) loadDisplayRundown(
	ctx context.Context,
	client *ent.Client,
	eventID int,
) (displayRundownState, error) {
	var rows []struct {
		PublishedRevision int  `sql:"published_revision"`
		BaselineID        *int `sql:"baseline_id"`
	}
	err := client.Rundown.Query().
		Where(rundown.EventIDEQ(eventID)).
		Select(rundown.FieldPublishedRevision).
		Aggregate(func(selector *entsql.Selector) string {
			baseline := entsql.Table(publicschedulebaseline.Table)
			selector.LeftJoin(baseline).On(
				selector.C(rundown.FieldEventID),
				baseline.C(publicschedulebaseline.FieldEventID),
			)
			return entsql.As(
				baseline.C(publicschedulebaseline.FieldID),
				"baseline_id",
			)
		}).
		Scan(ctx, &rows)
	if err != nil || len(rows) != 1 {
		if err == nil {
			err = errors.New("missing Display Rundown revision")
		}
		return displayRundownState{}, opaqueError("load Display Rundown revision", err)
	}
	key := displayRundownCacheKey{
		eventID:           eventID,
		publishedRevision: rows[0].PublishedRevision,
	}
	if rows[0].BaselineID != nil {
		key.baselineID = *rows[0].BaselineID
	}
	installationStore.displayRundownMu.Lock()
	defer installationStore.displayRundownMu.Unlock()
	if installationStore.displayRundownCached && installationStore.displayRundownKey == key {
		return installationStore.displayRundown, nil
	}
	found, err := loadCrewRundown(ctx, client, eventID)
	if err != nil {
		return displayRundownState{}, err
	}
	sessionIDs := make([]int, 0, len(found.Sessions))
	for _, published := range found.Sessions {
		sessionIDs = append(sessionIDs, published.ID)
	}
	baselineStarts := make(map[int]time.Time)
	if len(sessionIDs) > 0 {
		entries, queryErr := client.PublicScheduleBaselineEntry.Query().
			Where(publicschedulebaselineentry.SessionIDIn(sessionIDs...)).
			All(ctx)
		if queryErr != nil {
			return displayRundownState{}, opaqueError(
				"load Display Public Schedule Baselines",
				queryErr,
			)
		}
		for _, entry := range entries {
			baselineStarts[entry.SessionID] = entry.ForecastStart
		}
	}
	result := displayRundownState{
		CrewRundownState: found,
		baselineStarts:   baselineStarts,
	}
	installationStore.displayRundownKey = key
	installationStore.displayRundown = result
	installationStore.displayRundownCached = true
	return result, nil
}

func loadCurrentDisplayOverrides(
	ctx context.Context,
	client *ent.Client,
	assignment *ent.DisplayAssignment,
	now time.Time,
	result *DisplaySnapshotState,
) error {
	hasOverrides, err := client.DisplayOverride.Query().Exist(ctx)
	if err != nil {
		return opaqueError("inspect current Display Overrides", err)
	}
	if !hasOverrides {
		return nil
	}
	states, err := client.DisplayOverrideState.Query().
		Where(
			displayoverridestate.EventIDEQ(assignment.EventID),
			displayoverridestate.DisplayIDEQ(assignment.DisplayID),
			displayoverridestate.KindEQ(displayoverridestate.KindStageMessage),
		).
		WithOverride().
		All(ctx)
	if err != nil {
		return opaqueError("load current Display Overrides", err)
	}
	for _, state := range states {
		found, edgeErr := state.Edges.OverrideOrErr()
		if edgeErr != nil {
			return opaqueError("load selected Display Override", edgeErr)
		}
		if found.ClearedAt != nil ||
			(!found.UntilCleared && (found.ExpiresAt == nil || !found.ExpiresAt.After(now))) ||
			!assignmentInDisplayGroup(assignment, found.TargetGroupKey) {
			continue
		}
		projected := displayOverride(found)
		if assignment.ViewKey == "stage-timer" {
			result.StageMessage = &projected
		}
	}
	technical, err := client.DisplayOverride.Query().
		Where(
			displayoverride.EventIDEQ(assignment.EventID),
			displayoverride.KindEQ(displayoverride.KindTechnicalDifficulties),
			displayoverride.ClearedAtIsNil(),
			displayoverride.Or(
				displayoverride.UntilClearedEQ(true),
				displayoverride.ExpiresAtGT(now),
			),
		).
		Order(ent.Desc(displayoverride.FieldCreatedAt), ent.Desc(displayoverride.FieldID)).
		All(ctx)
	if err != nil {
		return opaqueError("load current Technical Difficulties Overrides", err)
	}
	for _, candidate := range technical {
		if assignmentInDisplayGroup(assignment, candidate.TargetGroupKey) {
			projected := displayOverride(candidate)
			result.TechnicalDifficulties = &projected
			break
		}
	}
	if err := loadPriorityDisplayOverride(
		ctx, client, assignment, now, DisplayOverrideUrgentNotice, &result.UrgentNotice,
	); err != nil {
		return err
	}
	if err := loadPriorityDisplayOverride(
		ctx, client, assignment, now, DisplayOverrideEmergencyAlert, &result.EmergencyAlert,
	); err != nil {
		return err
	}
	return nil
}

func loadPriorityDisplayOverride(
	ctx context.Context,
	client *ent.Client,
	assignment *ent.DisplayAssignment,
	now time.Time,
	kind DisplayOverrideKind,
	result **DisplayOverride,
) error {
	found, err := client.DisplayOverride.Query().
		Where(
			displayoverride.EventIDEQ(assignment.EventID),
			displayoverride.KindEQ(displayoverride.Kind(kind)),
			displayoverride.ClearedAtIsNil(),
			displayoverride.Or(
				displayoverride.UntilClearedEQ(true),
				displayoverride.ExpiresAtGT(now),
			),
		).
		Order(ent.Desc(displayoverride.FieldCreatedAt), ent.Desc(displayoverride.FieldID)).
		All(ctx)
	if err != nil {
		return opaqueError("load priority Display Overrides", err)
	}
	for _, candidate := range found {
		target := displayOverride(candidate).Target
		laneLocationID := 0
		if target.Type == DisplayOverrideTargetLane {
			published, rundownErr := loadCrewRundown(ctx, client, assignment.EventID)
			if rundownErr != nil {
				return rundownErr
			}
			for _, lane := range published.Lanes {
				if lane.ID == target.ID {
					laneLocationID = lane.LocationID
					break
				}
			}
		}
		matches, matchErr := overrideTargetMatchesAssignment(
			ctx, client, assignment.EventID, assignment, target, laneLocationID,
		)
		if matchErr != nil {
			return matchErr
		}
		if matches {
			projected := displayOverride(candidate)
			*result = &projected
			return nil
		}
	}
	return nil
}

func loadDisplaySessionWithBaseline(
	ctx context.Context,
	client *ent.Client,
	published PublishedSession,
	baselineStart time.Time,
	identity *ent.Session,
) (DisplaySessionState, error) {
	result := DisplaySessionState{
		ID: published.ID, AudienceVisibility: published.AudienceVisibility,
		TimerTitle:    published.Title,
		ForecastStart: published.PlannedStart, ForecastEnd: published.PlannedEnd,
		Type: published.Type, TimingPolicy: published.TimingPolicy,
		RunPlannedStart: published.PlannedStart, RunPlannedEnd: published.PlannedEnd,
		Lifecycle: identity.Lifecycle.String(), LiveStateRevision: identity.LiveStateRevision,
		LocationIDs: slices.Clone(published.LocationIDs),
		LaneIDs:     slices.Clone(published.LaneIDs),
		TrackIDs:    slices.Clone(published.TrackIDs),
	}
	if !identity.ForecastStart.IsZero() {
		result.ForecastStart = identity.ForecastStart
	}
	if !identity.ForecastEnd.IsZero() {
		result.ForecastEnd = identity.ForecastEnd
	}
	if len(identity.ForecastLocationIds) > 0 {
		result.LocationIDs = slices.Clone(identity.ForecastLocationIds)
	}
	if len(identity.ForecastLaneIds) > 0 {
		result.LaneIDs = slices.Clone(identity.ForecastLaneIds)
	}
	if published.AudienceVisibility == "Public" {
		result.Title = published.Title
		result.Speaker = published.Speaker
		result.PublicDetails = published.PublicDetails
	}
	if identity.Lifecycle == session.LifecycleLive ||
		identity.Lifecycle == session.LifecycleEnded {
		run, runErr := client.SessionRun.Query().Where(
			sessionrun.SessionIDEQ(published.ID),
		).Order(ent.Desc(sessionrun.FieldID)).First(ctx)
		if runErr != nil && !ent.IsNotFound(runErr) {
			return DisplaySessionState{}, opaqueError("load Display Session Run", runErr)
		}
		if runErr == nil {
			var snapshot SessionRunSnapshot
			if decodeErr := json.Unmarshal([]byte(run.SnapshotJSON), &snapshot); decodeErr != nil {
				return DisplaySessionState{}, opaqueError("decode Display Session Run Snapshot", decodeErr)
			}
			result.ActualStart = run.ActualStart
			result.TargetAdjustmentSeconds = run.TargetAdjustmentSeconds
			result.TargetAdjustedAt = run.TargetAdjustedAt
			result.Type = snapshot.Type
			result.TimingPolicy = snapshot.TimingPolicy
			result.RunPlannedStart = snapshot.PlannedStart
			result.RunPlannedEnd = snapshot.PlannedEnd
			result.LocationIDs = slices.Clone(snapshot.LocationIDs)
			result.LaneIDs = slices.Clone(snapshot.LaneIDs)
			if !run.ActualEnd.IsZero() {
				actualEnd := run.ActualEnd
				result.ActualEnd = &actualEnd
			}
		}
	}
	result.PublicTime = publicTimeFacts(publicTimeFactsParams{
		Session:     identity,
		Lifecycle:   publictime.Lifecycle(result.Lifecycle),
		Forecast:    publictime.Range{Start: result.ForecastStart, End: result.ForecastEnd},
		ActualStart: result.ActualStart,
		ActualEnd:   result.ActualEnd,
		RunDuration: result.RunPlannedEnd.Sub(result.RunPlannedStart),
	}, instantPointer(baselineStart))
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
	baseline, err := client.PublicScheduleBaselineEntry.Query().
		Where(publicschedulebaselineentry.SessionIDEQ(published.ID)).
		Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return DisplaySessionState{}, opaqueError("load Public Schedule Baseline entry", err)
	}
	var baselineStart time.Time
	if err == nil {
		baselineStart = baseline.ForecastStart
	}
	return loadDisplaySessionWithBaseline(ctx, client, published, baselineStart, identity)
}

func competitionOutputProgramChannelID(
	ctx context.Context,
	client *ent.Client,
	eventID, locationID int,
) (int, error) {
	published, err := loadCrewRundown(ctx, client, eventID)
	if err != nil {
		return 0, err
	}
	selected := 0
	for _, publishedSession := range published.Sessions {
		programSession, programErr := isProgramChannelSession(
			ctx,
			client,
			eventID,
			publishedSession,
		)
		if programErr != nil {
			return 0, programErr
		}
		if !programSession {
			continue
		}
		sessionState, sessionErr := loadDisplaySession(ctx, client, publishedSession)
		if sessionErr != nil {
			return 0, sessionErr
		}
		if !slices.Contains(sessionState.LocationIDs, locationID) {
			continue
		}
		if selected == 0 {
			selected = publishedSession.ID
		}
		if sessionState.Lifecycle == "Live" {
			return publishedSession.ID, nil
		}
	}
	return selected, nil
}

func isProgramChannelSession(
	ctx context.Context,
	client *ent.Client,
	eventID int,
	published PublishedSession,
) (bool, error) {
	switch published.Type {
	case "Competition":
		return true, nil
	case "Ceremony":
		locked, err := client.Prizegiving.Query().
			Where(
				prizegiving.EventIDEQ(eventID),
				prizegiving.CeremonySessionIDEQ(published.ID),
				prizegiving.LockedEQ(true),
			).
			Exist(ctx)
		if err != nil {
			return false, opaqueError(
				"check locked Prizegiving Program Channel",
				err,
			)
		}
		return locked, nil
	default:
		return false, nil
	}
}

func publishedLocationName(locations []PublishedLocation, locationID int) string {
	for _, location := range locations {
		if location.ID == locationID {
			return location.Name
		}
	}
	return ""
}
