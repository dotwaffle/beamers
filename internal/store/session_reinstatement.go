package store

import (
	"context"
	"errors"
	"slices"
	"time"

	"entgo.io/ent/privacy"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/installation"
	"github.com/dotwaffle/beamers/ent/lane"
	"github.com/dotwaffle/beamers/ent/location"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/internal/timingripple"
)

var (
	// ErrReinstatePreviewStale means placement state changed after preview.
	ErrReinstatePreviewStale = errors.New("reinstate Session preview is stale")
	// ErrReinstateConfirmation means reinstatement lacked explicit confirmation.
	ErrReinstateConfirmation = errors.New("reinstate Session requires confirmation")
	// ErrReinstatePlacement means the proposed placement is invalid.
	ErrReinstatePlacement = errors.New("invalid Reinstate Session placement")
)

// ReinstatePreview is one authoritative Placement Preview.
type ReinstatePreview struct {
	Plan                             timingripple.Plan
	Revisions                        map[int]int
	CurrentLaneIDs                   []int
	ProposedLaneIDs                  []int
	CurrentLocationIDs               []int
	ProposedLocationIDs              []int
	PreviousForecastStart            time.Time
	RequiresHardBoundaryConfirmation bool
	Fingerprint                      string
}

// ReinstateSessionParams identifies one confirmed Placement Preview.
type ReinstateSessionParams struct {
	EventID                   int
	SessionID                 int
	ExpectedLiveStateRevision int
	ForecastStart             time.Time
	LaneIDs                   []int
	LocationIDs               []int
	PreviewFingerprint        string
	Confirmed                 bool
	HardBoundaryConfirmed     bool
}

// ReinstateSessionResult is one atomically committed reinstatement.
type ReinstateSessionResult struct {
	State                 LiveSessionState `json:"state"`
	Changes               []ForecastChange `json:"changes"`
	PreviousForecastStart time.Time        `json:"previous_forecast_start"`
}

// PreviewReinstateSession computes placement effects without mutation.
func (installationStore *SQLite) PreviewReinstateSession(
	ctx context.Context,
	eventID int,
	sessionID int,
	forecastStart time.Time,
	laneIDs []int,
	locationIDs []int,
) (ReinstatePreview, error) {
	transaction, err := installationStore.client.Tx(ctx)
	if err != nil {
		return ReinstatePreview{}, opaqueError("begin Reinstate Session preview", err)
	}
	defer func() { _ = transaction.Rollback() }()
	return previewReinstateSession(
		ctx, transaction.Client(), eventID, sessionID,
		forecastStart, laneIDs, locationIDs,
	)
}

// ReinstateSession revalidates and commits one Placement Preview atomically.
func (transaction *CommandTx) ReinstateSession(
	ctx context.Context,
	params ReinstateSessionParams,
) (ReinstateSessionResult, error) {
	if !params.Confirmed {
		return ReinstateSessionResult{}, ErrReinstateConfirmation
	}
	preview, err := previewReinstateSession(
		ctx, transaction.transaction.Client(), params.EventID, params.SessionID,
		params.ForecastStart, params.LaneIDs, params.LocationIDs,
	)
	if err != nil {
		return ReinstateSessionResult{}, err
	}
	if preview.Fingerprint != params.PreviewFingerprint {
		return ReinstateSessionResult{}, ErrReinstatePreviewStale
	}
	if preview.RequiresHardBoundaryConfirmation && !params.HardBoundaryConfirmed {
		return ReinstateSessionResult{}, ErrHardBoundaryConfirmation
	}
	anchor, err := transaction.transaction.Session.Query().Where(
		session.IDEQ(params.SessionID),
		session.EventIDEQ(params.EventID),
	).Only(ctx)
	if err != nil {
		return ReinstateSessionResult{}, opaqueError("reload Session for reinstatement", err)
	}
	if anchor.LiveStateRevision != params.ExpectedLiveStateRevision {
		state, stateErr := liveSessionState(ctx, transaction.transaction.SessionRun, anchor)
		return ReinstateSessionResult{State: state}, stateErr
	}
	changes := make([]ForecastChange, 0, len(preview.Plan.Changes))
	for _, change := range preview.Plan.Changes {
		expectedRevision, ok := preview.Revisions[change.SessionID]
		if change.SessionID == params.SessionID {
			expectedRevision, ok = anchor.LiveStateRevision, true
		}
		if !ok {
			return ReinstateSessionResult{}, errors.New("reinstate Session plan omitted a revision")
		}
		update := transaction.transaction.Session.UpdateOneID(change.SessionID).
			Where(
				session.EventIDEQ(params.EventID),
				session.LiveStateRevisionEQ(expectedRevision),
			).
			SetForecastStart(change.ForecastStart).
			SetForecastEnd(change.ForecastEnd).
			AddLiveStateRevision(1)
		if change.SessionID == params.SessionID {
			update = update.
				Where(session.LifecycleEQ(session.LifecycleCanceled)).
				SetLifecycle(session.LifecycleScheduled).
				SetPreviousForecastStart(preview.PreviousForecastStart).
				SetForecastLaneIds(preview.ProposedLaneIDs).
				SetForecastLocationIds(preview.ProposedLocationIDs).
				ClearCommunicatedStart().
				ClearCommunicatedEnd().
				ClearPublicCancellationMessage().
				ClearCancellationCrewNotes()
		} else {
			update = update.Where(session.LifecycleEQ(session.LifecycleScheduled))
		}
		updated, updateErr := update.Save(ctx)
		if ent.IsNotFound(updateErr) {
			return ReinstateSessionResult{}, ErrReinstatePreviewStale
		}
		if updateErr != nil {
			return ReinstateSessionResult{}, opaqueError("apply Reinstate Session placement", updateErr)
		}
		changes = append(changes, ForecastChange{
			SessionID: updated.ID, ForecastStart: change.ForecastStart,
			ForecastEnd: change.ForecastEnd,
		})
		if change.SessionID == params.SessionID {
			anchor = updated
		}
	}
	state, err := loadLiveSessionState(ctx, transaction.transaction.SessionRun, anchor)
	if err != nil {
		return ReinstateSessionResult{}, err
	}
	return ReinstateSessionResult{
		State: state, Changes: changes,
		PreviousForecastStart: preview.PreviousForecastStart,
	}, nil
}

func previewReinstateSession(
	ctx context.Context,
	client *ent.Client,
	eventID int,
	sessionID int,
	forecastStart time.Time,
	laneIDs []int,
	locationIDs []int,
) (ReinstatePreview, error) {
	active, err := client.Installation.Query().
		Where(installation.ActiveEventIDEQ(eventID)).
		Exist(systemContext(ctx))
	if err != nil {
		return ReinstatePreview{}, opaqueError("check active Event for reinstatement", err)
	}
	if !active {
		return ReinstatePreview{}, ErrEventNotActive
	}
	identity, err := client.Session.Query().Where(
		session.IDEQ(sessionID), session.EventIDEQ(eventID),
	).Only(ctx)
	if errors.Is(err, privacy.Deny) {
		return ReinstatePreview{}, ErrSessionScopeRequired
	}
	if ent.IsNotFound(err) {
		return ReinstatePreview{}, ErrSessionNotFound
	}
	if err != nil {
		return ReinstatePreview{}, opaqueError("load Session for reinstatement", err)
	}
	if identity.Lifecycle != session.LifecycleCanceled {
		return ReinstatePreview{}, ErrSessionLifecycleTransition
	}
	version, err := identity.QueryPublishedVersions().Order(ent.Desc("published_revision")).First(ctx)
	if ent.IsNotFound(err) {
		return ReinstatePreview{}, ErrSessionNotFound
	}
	if err != nil {
		return ReinstatePreview{}, opaqueError("load Published Session for reinstatement", err)
	}
	currentLaneIDs, currentLocationIDs, err := sessionPlacementFromVersion(ctx, identity, version)
	if err != nil {
		return ReinstatePreview{}, err
	}
	proposedLaneIDs, proposedLocationIDs, err := validatePlacement(
		systemContext(ctx), client, eventID, forecastStart, laneIDs, locationIDs,
	)
	if err != nil {
		return ReinstatePreview{}, err
	}
	duration := version.PlannedEnd.Sub(version.PlannedStart)
	candidate := timingripple.Session{
		ID:           sessionID,
		PlannedStart: forecastStart, PlannedEnd: forecastStart.Add(duration),
		ForecastStart: forecastStart, ForecastEnd: forecastStart.Add(duration),
		MinimumDuration: time.Duration(version.MinimumDurationSeconds) * time.Second,
		StartBoundary:   timingripple.Boundary(version.StartBoundary.String()),
		EndBoundary:     timingripple.Boundary(version.EndBoundary.String()),
		LaneIDs:         proposedLaneIDs, LocationIDs: proposedLocationIDs,
	}
	timing, err := loadTimingState(systemContext(ctx), client, eventID, 0)
	if err != nil {
		return ReinstatePreview{}, err
	}
	action := timingripple.Place{Session: candidate}
	plan, err := timingripple.Calculate(timing.Sessions, action)
	if err != nil {
		return ReinstatePreview{}, err
	}
	previousForecastStart := version.PlannedStart
	if !identity.ForecastStart.IsZero() {
		previousForecastStart = identity.ForecastStart
	}
	return ReinstatePreview{
		Plan: plan, Revisions: timing.Revisions,
		CurrentLaneIDs: currentLaneIDs, ProposedLaneIDs: proposedLaneIDs,
		CurrentLocationIDs: currentLocationIDs, ProposedLocationIDs: proposedLocationIDs,
		PreviousForecastStart:            previousForecastStart,
		RequiresHardBoundaryConfirmation: len(plan.HardCollisions) > 0,
		Fingerprint: timingripple.Fingerprint(
			timing.Sessions, action, identity.LiveStateRevision,
		),
	}, nil
}

func sessionPlacement(
	ctx context.Context,
	identity *ent.Session,
) ([]int, []int, error) {
	version, err := identity.QueryPublishedVersions().Order(ent.Desc("published_revision")).First(ctx)
	if err != nil {
		return nil, nil, opaqueError("load current Session placement", err)
	}
	return sessionPlacementFromVersion(ctx, identity, version)
}

func sessionPlacementFromVersion(
	ctx context.Context,
	identity *ent.Session,
	version *ent.SessionPublishedVersion,
) ([]int, []int, error) {
	laneIDs, err := version.QueryLanes().IDs(ctx)
	if err != nil {
		return nil, nil, opaqueError("load current Session Lanes", err)
	}
	locationIDs, err := version.QueryLocations().IDs(ctx)
	if err != nil {
		return nil, nil, opaqueError("load current Session Locations", err)
	}
	if len(identity.ForecastLaneIds) > 0 {
		laneIDs = slices.Clone(identity.ForecastLaneIds)
	}
	if len(identity.ForecastLocationIds) > 0 {
		locationIDs = slices.Clone(identity.ForecastLocationIds)
	}
	slices.Sort(laneIDs)
	slices.Sort(locationIDs)
	return laneIDs, locationIDs, nil
}

func validatePlacement(
	ctx context.Context,
	client *ent.Client,
	eventID int,
	forecastStart time.Time,
	laneIDs []int,
	locationIDs []int,
) ([]int, []int, error) {
	if forecastStart.IsZero() || len(laneIDs) == 0 || len(locationIDs) == 0 {
		return nil, nil, ErrReinstatePlacement
	}
	laneIDs = slices.Clone(laneIDs)
	locationIDs = slices.Clone(locationIDs)
	slices.Sort(laneIDs)
	slices.Sort(locationIDs)
	if hasDuplicateOrInvalidID(laneIDs) || hasDuplicateOrInvalidID(locationIDs) {
		return nil, nil, ErrReinstatePlacement
	}
	laneCount, err := client.Lane.Query().Where(
		lane.EventIDEQ(eventID), lane.IDIn(laneIDs...),
	).Count(ctx)
	if err != nil {
		return nil, nil, opaqueError("validate Reinstate Session Lanes", err)
	}
	locationCount, err := client.Location.Query().Where(
		location.EventIDEQ(eventID), location.IDIn(locationIDs...),
	).Count(ctx)
	if err != nil {
		return nil, nil, opaqueError("validate Reinstate Session Locations", err)
	}
	if laneCount != len(laneIDs) || locationCount != len(locationIDs) {
		return nil, nil, ErrReinstatePlacement
	}
	return laneIDs, locationIDs, nil
}

func hasDuplicateOrInvalidID(values []int) bool {
	for index, value := range values {
		if value <= 0 || index > 0 && values[index-1] == value {
			return true
		}
	}
	return false
}
