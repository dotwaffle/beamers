package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/installation"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionrun"
	"github.com/dotwaffle/beamers/internal/viewer"
)

var (
	// ErrSessionNotFound means the Event has no Published Session with that identity.
	ErrSessionNotFound = errors.New("session not found")
	// ErrLiveStateRevisionConflict means a Session changed after the caller observed it.
	ErrLiveStateRevisionConflict = errors.New("live state revision conflict")
	// ErrSessionLifecycleTransition means the requested progression is not valid now.
	ErrSessionLifecycleTransition = errors.New("invalid Session lifecycle transition")
	// ErrEventNotActive means the command targeted an Event outside live authority.
	ErrEventNotActive = errors.New("event is not active")
)

// SessionRunSnapshot is the immutable Published context captured by Start.
type SessionRunSnapshot struct {
	PublishedRevision      int       `json:"published_revision"`
	Title                  string    `json:"title"`
	Type                   string    `json:"type"`
	PublicDetails          string    `json:"public_details,omitempty"`
	PlannedStart           time.Time `json:"planned_start"`
	PlannedEnd             time.Time `json:"planned_end"`
	TimingPolicy           string    `json:"timing_policy"`
	MinimumDurationSeconds int       `json:"minimum_duration_seconds"`
	StartBoundary          string    `json:"start_boundary"`
	EndBoundary            string    `json:"end_boundary"`
	LaneIDs                []int     `json:"lane_ids"`
	LocationIDs            []int     `json:"location_ids"`
	TrackIDs               []int     `json:"track_ids,omitempty"`
}

// LiveSessionState is the durable outcome of one Session progression command.
type LiveSessionState struct {
	SessionID         int        `json:"session_id"`
	SessionRunID      int        `json:"session_run_id"`
	Lifecycle         string     `json:"lifecycle"`
	LiveStateRevision int        `json:"live_state_revision"`
	ActualStart       time.Time  `json:"actual_start"`
	ActualEnd         *time.Time `json:"actual_end,omitempty"`
}

// StartSession creates one Run and advances one Scheduled Session to Live atomically.
func (transaction *CommandTx) StartSession(
	ctx context.Context,
	eventID int,
	sessionID int,
	expectedRevision int,
	now time.Time,
) (LiveSessionState, error) {
	internalContext := viewer.SystemContext(ctx)
	if err := transaction.requireActiveEvent(internalContext, eventID); err != nil {
		return LiveSessionState{}, err
	}
	identity, err := transaction.transaction.Session.Query().Where(
		session.IDEQ(sessionID), session.EventIDEQ(eventID),
	).Only(internalContext)
	if ent.IsNotFound(err) {
		return LiveSessionState{}, ErrSessionNotFound
	}
	if err != nil {
		return LiveSessionState{}, opaqueError("load Session live state", err)
	}
	if identity.LiveStateRevision != expectedRevision {
		return liveSessionState(ctx, transaction.transaction.SessionRun, identity)
	}
	if identity.Lifecycle != session.LifecycleScheduled {
		return LiveSessionState{}, ErrSessionLifecycleTransition
	}
	snapshot, err := sessionRunSnapshot(internalContext, identity)
	if err != nil {
		return LiveSessionState{}, err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return LiveSessionState{}, opaqueError("encode Session Run Snapshot", err)
	}
	updated, err := transaction.transaction.Session.UpdateOneID(sessionID).
		Where(
			session.EventIDEQ(eventID),
			session.LiveStateRevisionEQ(expectedRevision),
			session.LifecycleEQ(session.LifecycleScheduled),
		).
		SetLifecycle(session.LifecycleLive).
		AddLiveStateRevision(1).
		Save(internalContext)
	if ent.IsNotFound(err) {
		return transaction.currentLiveSessionState(ctx, eventID, sessionID)
	}
	if err != nil {
		return LiveSessionState{}, opaqueError("start Session", err)
	}
	run, err := transaction.transaction.SessionRun.Create().
		SetSessionID(sessionID).
		SetActualStart(now).
		SetSnapshotJSON(string(encoded)).
		SetCreatedAt(now).
		Save(internalContext)
	if err != nil {
		return LiveSessionState{}, opaqueError("create Session Run", err)
	}
	return LiveSessionState{
		SessionID: updated.ID, SessionRunID: run.ID,
		Lifecycle: updated.Lifecycle.String(), LiveStateRevision: updated.LiveStateRevision,
		ActualStart: run.ActualStart,
	}, nil
}

// EndSession records Actual End and advances one Live Session to Ended atomically.
func (transaction *CommandTx) EndSession(
	ctx context.Context,
	eventID int,
	sessionID int,
	expectedRevision int,
	now time.Time,
) (LiveSessionState, error) {
	internalContext := viewer.SystemContext(ctx)
	if err := transaction.requireActiveEvent(internalContext, eventID); err != nil {
		return LiveSessionState{}, err
	}
	identity, err := transaction.transaction.Session.Query().Where(
		session.IDEQ(sessionID), session.EventIDEQ(eventID),
	).Only(internalContext)
	if ent.IsNotFound(err) {
		return LiveSessionState{}, ErrSessionNotFound
	}
	if err != nil {
		return LiveSessionState{}, opaqueError("load Session live state", err)
	}
	if identity.LiveStateRevision != expectedRevision {
		return liveSessionState(ctx, transaction.transaction.SessionRun, identity)
	}
	if identity.Lifecycle != session.LifecycleLive {
		return LiveSessionState{}, ErrSessionLifecycleTransition
	}
	run, err := transaction.transaction.SessionRun.Query().Where(
		sessionrun.SessionIDEQ(sessionID), sessionrun.ActualEndIsNil(),
	).Order(ent.Desc(sessionrun.FieldID)).First(internalContext)
	if ent.IsNotFound(err) {
		return LiveSessionState{}, ErrSessionLifecycleTransition
	}
	if err != nil {
		return LiveSessionState{}, opaqueError("load Live Session Run", err)
	}
	updated, err := transaction.transaction.Session.UpdateOneID(sessionID).
		Where(
			session.EventIDEQ(eventID),
			session.LiveStateRevisionEQ(expectedRevision),
			session.LifecycleEQ(session.LifecycleLive),
		).
		SetLifecycle(session.LifecycleEnded).
		AddLiveStateRevision(1).
		Save(internalContext)
	if ent.IsNotFound(err) {
		return transaction.currentLiveSessionState(ctx, eventID, sessionID)
	}
	if err != nil {
		return LiveSessionState{}, opaqueError("end Session", err)
	}
	endedRun, err := transaction.transaction.SessionRun.UpdateOneID(run.ID).
		Where(sessionrun.ActualEndIsNil()).SetActualEnd(now).Save(internalContext)
	if ent.IsNotFound(err) {
		return LiveSessionState{}, opaqueError("end Session Run", errors.New("open session run disappeared"))
	}
	if err != nil {
		return LiveSessionState{}, opaqueError("end Session Run", err)
	}
	actualEnd := endedRun.ActualEnd
	return LiveSessionState{
		SessionID: updated.ID, SessionRunID: endedRun.ID,
		Lifecycle: updated.Lifecycle.String(), LiveStateRevision: updated.LiveStateRevision,
		ActualStart: endedRun.ActualStart, ActualEnd: &actualEnd,
	}, nil
}

func (transaction *CommandTx) requireActiveEvent(ctx context.Context, eventID int) error {
	active, err := transaction.transaction.Installation.Query().
		Where(installation.ActiveEventIDEQ(eventID)).Exist(ctx)
	if err != nil {
		return opaqueError("load Active Event for Session command", err)
	}
	if !active {
		return ErrEventNotActive
	}
	return nil
}

func (transaction *CommandTx) currentLiveSessionState(
	ctx context.Context,
	eventID int,
	sessionID int,
) (LiveSessionState, error) {
	identity, err := transaction.transaction.Session.Query().Where(
		session.IDEQ(sessionID), session.EventIDEQ(eventID),
	).Only(viewer.SystemContext(ctx))
	if ent.IsNotFound(err) {
		return LiveSessionState{}, ErrSessionNotFound
	}
	if err != nil {
		return LiveSessionState{}, opaqueError("reload Session after revision conflict", err)
	}
	return liveSessionState(ctx, transaction.transaction.SessionRun, identity)
}

func sessionRunSnapshot(ctx context.Context, identity *ent.Session) (SessionRunSnapshot, error) {
	version, err := identity.QueryPublishedVersions().Order(ent.Desc("published_revision")).First(ctx)
	if ent.IsNotFound(err) {
		return SessionRunSnapshot{}, ErrSessionNotFound
	}
	if err != nil {
		return SessionRunSnapshot{}, opaqueError("load Published Session for Start", err)
	}
	lanes, err := version.QueryLanes().IDs(ctx)
	if err != nil {
		return SessionRunSnapshot{}, opaqueError("load Session Run Snapshot Lanes", err)
	}
	locations, err := version.QueryLocations().IDs(ctx)
	if err != nil {
		return SessionRunSnapshot{}, opaqueError("load Session Run Snapshot Locations", err)
	}
	tracks, err := version.QueryTracks().IDs(ctx)
	if err != nil {
		return SessionRunSnapshot{}, opaqueError("load Session Run Snapshot Tracks", err)
	}
	return SessionRunSnapshot{
		PublishedRevision: version.PublishedRevision,
		Title:             version.Title, Type: version.Type.String(), PublicDetails: version.PublicDetails,
		PlannedStart: version.PlannedStart, PlannedEnd: version.PlannedEnd,
		TimingPolicy: version.TimingPolicy.String(), MinimumDurationSeconds: version.MinimumDurationSeconds,
		StartBoundary: version.StartBoundary.String(), EndBoundary: version.EndBoundary.String(),
		LaneIDs: lanes, LocationIDs: locations, TrackIDs: tracks,
	}, nil
}

func liveSessionState(
	ctx context.Context,
	runs *ent.SessionRunClient,
	identity *ent.Session,
) (LiveSessionState, error) {
	state := LiveSessionState{
		SessionID: identity.ID, Lifecycle: identity.Lifecycle.String(),
		LiveStateRevision: identity.LiveStateRevision,
	}
	run, err := runs.Query().Where(sessionrun.SessionIDEQ(identity.ID)).
		Order(ent.Desc(sessionrun.FieldID)).First(viewer.SystemContext(ctx))
	if ent.IsNotFound(err) {
		return state, ErrLiveStateRevisionConflict
	}
	if err != nil {
		return LiveSessionState{}, opaqueError("load current Session Run", err)
	}
	state.SessionRunID = run.ID
	state.ActualStart = run.ActualStart
	if !run.ActualEnd.IsZero() {
		actualEnd := run.ActualEnd
		state.ActualEnd = &actualEnd
	}
	return state, ErrLiveStateRevisionConflict
}
