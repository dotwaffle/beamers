package store

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"entgo.io/ent/privacy"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/event"
	"github.com/dotwaffle/beamers/ent/installation"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionrun"
	"github.com/dotwaffle/beamers/internal/sessiontarget"
)

var (
	// ErrTargetPreviewStale means live or downstream timing changed after preview.
	ErrTargetPreviewStale = errors.New("adjust target preview is stale")
	// ErrTargetConfirmation means Adjust Target lacked an explicit required confirmation.
	ErrTargetConfirmation = errors.New("adjust target requires confirmation")
	// ErrHardBoundaryConfirmation means a Hard Boundary override lacked explicit confirmation.
	ErrHardBoundaryConfirmation = errors.New("hard boundary override requires explicit confirmation")
)

// SessionTargetPreview is one authoritative target-adjustment preview.
type SessionTargetPreview struct {
	Result  sessiontarget.Result
	Presets []time.Duration
}

// AdjustSessionTargetParams identifies one confirmed target adjustment.
type AdjustSessionTargetParams struct {
	EventID               int
	SessionID             int
	ExpectedRevision      int
	Adjustment            sessiontarget.Adjustment
	PreviewFingerprint    string
	Confirmed             bool
	HardBoundaryConfirmed bool
	Now                   time.Time
}

// SessionTargetAdjustment is one atomically committed Forecast target.
type SessionTargetAdjustment struct {
	State       LiveSessionState `json:"state"`
	ForecastEnd time.Time        `json:"forecast_end"`
	Adjustment  time.Duration    `json:"adjustment"`
	AdjustedAt  time.Time        `json:"adjusted_at"`
}

// PreviewSessionTarget loads current live and shared-resource downstream timing.
func (installationStore *SQLite) PreviewSessionTarget(
	ctx context.Context,
	eventID int,
	sessionID int,
	adjustment sessiontarget.Adjustment,
	now time.Time,
) (SessionTargetPreview, error) {
	transaction, err := installationStore.client.Tx(ctx)
	if err != nil {
		return SessionTargetPreview{}, opaqueError("begin Adjust Target preview", err)
	}
	defer func() { _ = transaction.Rollback() }()
	return previewSessionTarget(ctx, transaction.Client(), eventID, sessionID, adjustment, now)
}

// AdjustSessionTarget validates a fresh preview and commits Forecast and timer state atomically.
func (transaction *CommandTx) AdjustSessionTarget(
	ctx context.Context,
	params AdjustSessionTargetParams,
) (SessionTargetAdjustment, error) {
	if !params.Confirmed {
		return SessionTargetAdjustment{}, ErrTargetConfirmation
	}
	if err := transaction.requireActiveEvent(ctx, params.EventID); err != nil {
		return SessionTargetAdjustment{}, err
	}
	preview, err := previewSessionTarget(
		ctx, transaction.transaction.Client(), params.EventID, params.SessionID,
		params.Adjustment, params.Now,
	)
	if err != nil {
		return SessionTargetAdjustment{}, err
	}
	if preview.Result.Fingerprint != params.PreviewFingerprint {
		return SessionTargetAdjustment{}, ErrTargetPreviewStale
	}
	if preview.Result.RequiresHardBoundaryConfirmation && !params.HardBoundaryConfirmed {
		return SessionTargetAdjustment{}, ErrHardBoundaryConfirmation
	}
	identity, err := transaction.transaction.Session.Query().Where(
		session.IDEQ(params.SessionID), session.EventIDEQ(params.EventID),
	).Only(ctx)
	if err != nil {
		return SessionTargetAdjustment{}, opaqueError("reload Adjust Target Session", err)
	}
	if identity.LiveStateRevision != params.ExpectedRevision {
		state, stateErr := liveSessionState(ctx, transaction.transaction.SessionRun, identity)
		if stateErr != nil {
			return SessionTargetAdjustment{}, stateErr
		}
		return SessionTargetAdjustment{State: state}, ErrLiveStateRevisionConflict
	}
	updated, err := transaction.transaction.Session.UpdateOneID(params.SessionID).
		Where(
			session.EventIDEQ(params.EventID),
			session.LiveStateRevisionEQ(params.ExpectedRevision),
			session.LifecycleEQ(session.LifecycleLive),
		).
		SetForecastEnd(preview.Result.ProposedTarget).
		AddLiveStateRevision(1).
		Save(ctx)
	if ent.IsNotFound(err) {
		return SessionTargetAdjustment{}, ErrLiveStateRevisionConflict
	}
	if err != nil {
		return SessionTargetAdjustment{}, opaqueError("adjust Session target", err)
	}
	run, err := transaction.transaction.SessionRun.Query().Where(
		sessionrun.SessionIDEQ(params.SessionID), sessionrun.ActualEndIsNil(),
	).Order(ent.Desc(sessionrun.FieldID)).First(ctx)
	if err != nil {
		return SessionTargetAdjustment{}, opaqueError("load Adjust Target Session Run", err)
	}
	run, err = transaction.transaction.SessionRun.UpdateOneID(run.ID).
		SetTargetAdjustmentSeconds(int(params.Adjustment.Duration / time.Second)).
		SetTargetAdjustedAt(params.Now).
		Save(ctx)
	if err != nil {
		return SessionTargetAdjustment{}, opaqueError("record Stage Timer adjustment", err)
	}
	return SessionTargetAdjustment{
		State: LiveSessionState{
			SessionID: updated.ID, SessionRunID: run.ID, Lifecycle: updated.Lifecycle.String(),
			LiveStateRevision: updated.LiveStateRevision, ActualStart: run.ActualStart,
		},
		ForecastEnd: preview.Result.ProposedTarget,
		Adjustment:  params.Adjustment.Duration,
		AdjustedAt:  params.Now,
	}, nil
}

func previewSessionTarget(
	ctx context.Context,
	client *ent.Client,
	eventID int,
	sessionID int,
	adjustment sessiontarget.Adjustment,
	now time.Time,
) (SessionTargetPreview, error) {
	active, err := client.Installation.Query().Where(installation.ActiveEventIDEQ(eventID)).Exist(systemContext(ctx))
	if err != nil {
		return SessionTargetPreview{}, opaqueError("check active Event for Adjust Target", err)
	}
	if !active {
		return SessionTargetPreview{}, ErrEventNotActive
	}
	identity, err := client.Session.Query().Where(
		session.IDEQ(sessionID), session.EventIDEQ(eventID),
	).Only(ctx)
	if errors.Is(err, privacy.Deny) {
		return SessionTargetPreview{}, ErrSessionScopeRequired
	}
	if ent.IsNotFound(err) {
		return SessionTargetPreview{}, ErrSessionNotFound
	}
	if err != nil {
		return SessionTargetPreview{}, opaqueError("load Adjust Target Session", err)
	}
	if identity.Lifecycle != session.LifecycleLive {
		return SessionTargetPreview{}, ErrSessionLifecycleTransition
	}
	run, err := client.SessionRun.Query().Where(
		sessionrun.SessionIDEQ(sessionID), sessionrun.ActualEndIsNil(),
	).Order(ent.Desc(sessionrun.FieldID)).First(ctx)
	if err != nil {
		return SessionTargetPreview{}, opaqueError("load Adjust Target Run", err)
	}
	var snapshot SessionRunSnapshot
	if decodeErr := json.Unmarshal([]byte(run.SnapshotJSON), &snapshot); decodeErr != nil {
		return SessionTargetPreview{}, opaqueError("decode Adjust Target Run Snapshot", decodeErr)
	}
	if scopeErr := requireSessionLaneScope(ctx, eventID, snapshot.LaneIDs); scopeErr != nil {
		return SessionTargetPreview{}, scopeErr
	}
	currentTarget := identity.ForecastEnd
	if currentTarget.IsZero() {
		currentTarget = initialForecastEnd(snapshot, run.ActualStart)
	}
	currentStart := identity.ForecastStart
	if currentStart.IsZero() {
		currentStart = run.ActualStart
	}
	foundEvent, err := client.Event.Query().Where(event.IDEQ(eventID)).Only(ctx)
	if err != nil {
		return SessionTargetPreview{}, opaqueError("load Adjust Target presets", err)
	}
	var presetSeconds []int
	if decodeErr := json.Unmarshal([]byte(foundEvent.TargetAdjustmentPresets), &presetSeconds); decodeErr != nil {
		return SessionTargetPreview{}, opaqueError("decode Adjust Target presets", decodeErr)
	}
	presets := make([]time.Duration, len(presetSeconds))
	for index, seconds := range presetSeconds {
		presets[index] = time.Duration(seconds) * time.Second
	}
	internalContext := systemContext(ctx)
	published, err := loadCrewRundown(internalContext, client, eventID)
	if err != nil {
		return SessionTargetPreview{}, err
	}
	downstream := make([]sessiontarget.DownstreamSession, 0)
	for _, item := range published.Sessions {
		if item.ID == sessionID || !sharesResource(
			item.LaneIDs, item.LocationIDs, snapshot.LaneIDs, snapshot.LocationIDs,
		) {
			continue
		}
		found, queryErr := client.Session.Get(internalContext, item.ID)
		if queryErr != nil {
			return SessionTargetPreview{}, opaqueError("load downstream Session timing", queryErr)
		}
		if found.Lifecycle == session.LifecycleEnded ||
			found.Lifecycle == session.LifecycleCanceled {
			continue
		}
		start, end := item.PlannedStart, item.PlannedEnd
		if !found.ForecastStart.IsZero() {
			start = found.ForecastStart
		}
		if !found.ForecastEnd.IsZero() {
			end = found.ForecastEnd
		}
		if start.Before(currentStart) {
			continue
		}
		downstream = append(downstream, sessiontarget.DownstreamSession{
			SessionID: item.ID, ForecastStart: start, ForecastEnd: end,
			StartBoundary: item.StartBoundary,
		})
	}
	slices.SortFunc(downstream, func(first, second sessiontarget.DownstreamSession) int {
		if order := first.ForecastStart.Compare(second.ForecastStart); order != 0 {
			return order
		}
		return cmp.Compare(first.SessionID, second.SessionID)
	})
	result, err := sessiontarget.Preview(sessiontarget.State{
		SessionID: sessionID, Revision: identity.LiveStateRevision,
		CurrentTarget: currentTarget, EndBoundary: snapshot.EndBoundary,
		TimingPolicy: snapshot.TimingPolicy,
		Presets:      presets, Downstream: downstream,
	}, adjustment, now)
	return SessionTargetPreview{Result: result, Presets: presets}, err
}

func initialForecastEnd(snapshot SessionRunSnapshot, actualStart time.Time) time.Time {
	if snapshot.TimingPolicy == "FixedEnd" {
		return snapshot.PlannedEnd
	}
	return actualStart.Add(snapshot.PlannedEnd.Sub(snapshot.PlannedStart))
}

func sharesID(first, second []int) bool {
	for _, value := range first {
		if slices.Contains(second, value) {
			return true
		}
	}
	return false
}

func sharesResource(lanes, locations, currentLanes, currentLocations []int) bool {
	return sharesID(lanes, currentLanes) || sharesID(locations, currentLocations)
}
