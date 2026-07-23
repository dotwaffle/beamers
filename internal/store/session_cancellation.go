package store

import (
	"context"
	"errors"
	"time"

	"entgo.io/ent/privacy"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionrun"
)

// CancelSessionParams identifies one confirmed cancellation.
type CancelSessionParams struct {
	EventID                   int
	SessionID                 int
	ExpectedLiveStateRevision int
	PublicMessage             string
	CrewNotes                 string
	Now                       time.Time
}

// CancelSession ends any active Run and preserves the Session as Canceled.
func (transaction *CommandTx) CancelSession(
	ctx context.Context,
	params CancelSessionParams,
) (LiveSessionState, error) {
	if err := transaction.requireActiveEvent(ctx, params.EventID); err != nil {
		return LiveSessionState{}, err
	}
	identity, err := transaction.transaction.Session.Query().Where(
		session.IDEQ(params.SessionID),
		session.EventIDEQ(params.EventID),
	).Only(ctx)
	if errors.Is(err, privacy.Deny) {
		return LiveSessionState{}, ErrSessionScopeRequired
	}
	if ent.IsNotFound(err) {
		return LiveSessionState{}, ErrSessionNotFound
	}
	if err != nil {
		return LiveSessionState{}, opaqueError("load Session for cancellation", err)
	}
	if scopeErr := requireSessionControlScope(ctx, identity); scopeErr != nil {
		return LiveSessionState{}, scopeErr
	}
	if identity.LiveStateRevision != params.ExpectedLiveStateRevision {
		return liveSessionState(ctx, transaction.transaction.SessionRun, identity)
	}
	if identity.Lifecycle != session.LifecycleScheduled &&
		identity.Lifecycle != session.LifecycleLive {
		return LiveSessionState{}, ErrSessionLifecycleTransition
	}
	var sessionRunID *int
	if identity.Lifecycle == session.LifecycleLive {
		run, runErr := transaction.transaction.SessionRun.Query().Where(
			sessionrun.SessionIDEQ(params.SessionID),
			sessionrun.ActualEndIsNil(),
		).Order(ent.Desc(sessionrun.FieldID)).First(ctx)
		if ent.IsNotFound(runErr) {
			return LiveSessionState{}, ErrSessionLifecycleTransition
		}
		if runErr != nil {
			return LiveSessionState{}, opaqueError("load Session Run for cancellation", runErr)
		}
		if _, runErr = transaction.transaction.SessionRun.UpdateOneID(run.ID).
			Where(sessionrun.ActualEndIsNil()).
			SetActualEnd(params.Now).
			SetOutcome(sessionrun.OutcomeCanceled).
			Save(ctx); runErr != nil {
			return LiveSessionState{}, opaqueError("end Session Run for cancellation", runErr)
		}
		sessionRunID = &run.ID
	}
	version, err := identity.QueryPublishedVersions().
		Order(ent.Desc("published_revision")).
		First(ctx)
	if ent.IsNotFound(err) {
		return LiveSessionState{}, ErrSessionNotFound
	}
	if err != nil {
		return LiveSessionState{}, opaqueError("load cancellation Forecast Time", err)
	}
	forecastStart := version.PlannedStart
	if !identity.ForecastStart.IsZero() {
		forecastStart = identity.ForecastStart
	}
	if _, createErr := transaction.transaction.SessionCancellation.Create().
		SetSessionID(params.SessionID).
		SetNillableSessionRunID(sessionRunID).
		SetPublicMessage(params.PublicMessage).
		SetCrewNotes(params.CrewNotes).
		SetForecastStart(forecastStart).
		SetCreatedAt(params.Now).
		Save(systemContext(ctx)); createErr != nil {
		return LiveSessionState{}, opaqueError("record Session cancellation", createErr)
	}
	updated, err := transaction.transaction.Session.UpdateOneID(params.SessionID).
		Where(
			session.EventIDEQ(params.EventID),
			session.LiveStateRevisionEQ(params.ExpectedLiveStateRevision),
		).
		SetLifecycle(session.LifecycleCanceled).
		SetPublicCancellationMessage(params.PublicMessage).
		SetCancellationCrewNotes(params.CrewNotes).
		AddLiveStateRevision(1).
		Save(ctx)
	if ent.IsNotFound(err) {
		return transaction.currentLiveSessionState(ctx, params.EventID, params.SessionID)
	}
	if err != nil {
		return LiveSessionState{}, opaqueError("cancel Session", err)
	}
	return loadLiveSessionState(ctx, transaction.transaction.SessionRun, updated)
}
