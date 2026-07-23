package store

import (
	"context"
	"errors"

	"entgo.io/ent/privacy"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/installation"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/internal/pullforward"
)

var (
	// ErrPullForwardPreviewStale means timing changed after the preview.
	ErrPullForwardPreviewStale = errors.New("pull forward preview is stale")
	// ErrPullForwardConfirmation means Pull Forward lacked explicit confirmation.
	ErrPullForwardConfirmation = errors.New("pull forward requires confirmation")
)

// PullForwardPreview is one authoritative early-finish recalculation.
type PullForwardPreview struct {
	Result    pullforward.Result
	Revisions map[int]int
}

// PullForwardParams identifies one confirmed Pull Forward command.
type PullForwardParams struct {
	EventID            int
	SessionID          int
	ExpectedRevision   int
	PreviewFingerprint string
	Confirmed          bool
}

// PullForwardAdjustment is one atomically committed timing recalculation.
type PullForwardAdjustment struct {
	State   LiveSessionState `json:"state"`
	Changes []ForecastChange `json:"changes"`
}

// PreviewPullForward loads the exact ended Session and Lane timing component.
func (installationStore *SQLite) PreviewPullForward(
	ctx context.Context,
	eventID int,
	sessionID int,
) (PullForwardPreview, error) {
	transaction, err := installationStore.client.Tx(ctx)
	if err != nil {
		return PullForwardPreview{}, opaqueError("begin Pull Forward preview", err)
	}
	defer func() { _ = transaction.Rollback() }()
	return previewPullForward(ctx, transaction.Client(), eventID, sessionID)
}

// PullForward revalidates and atomically moves eligible later Forecasts.
func (transaction *CommandTx) PullForward(
	ctx context.Context,
	params PullForwardParams,
) (PullForwardAdjustment, error) {
	if !params.Confirmed {
		return PullForwardAdjustment{}, ErrPullForwardConfirmation
	}
	if err := transaction.requireActiveEvent(ctx, params.EventID); err != nil {
		return PullForwardAdjustment{}, err
	}
	preview, err := previewPullForward(
		ctx, transaction.transaction.Client(), params.EventID, params.SessionID,
	)
	if err != nil {
		return PullForwardAdjustment{}, err
	}
	if preview.Result.Fingerprint != params.PreviewFingerprint {
		return PullForwardAdjustment{}, ErrPullForwardPreviewStale
	}
	anchor, err := transaction.transaction.Session.Query().Where(
		session.IDEQ(params.SessionID), session.EventIDEQ(params.EventID),
	).Only(ctx)
	if err != nil {
		return PullForwardAdjustment{}, opaqueError("reload Pull Forward Session", err)
	}
	if anchor.LiveStateRevision != params.ExpectedRevision {
		state, stateErr := liveSessionState(ctx, transaction.transaction.SessionRun, anchor)
		if stateErr != nil {
			return PullForwardAdjustment{}, stateErr
		}
		return PullForwardAdjustment{State: state}, ErrLiveStateRevisionConflict
	}
	changes := make([]ForecastChange, 0, len(preview.Result.Changes))
	for _, change := range preview.Result.Changes {
		expectedRevision, ok := preview.Revisions[change.SessionID]
		if !ok {
			return PullForwardAdjustment{}, errors.New("pull forward plan omitted a Session revision")
		}
		_, updateErr := transaction.transaction.Session.UpdateOneID(change.SessionID).
			Where(
				session.EventIDEQ(params.EventID),
				session.LiveStateRevisionEQ(expectedRevision),
				session.LifecycleEQ(session.LifecycleScheduled),
			).
			SetForecastStart(change.ForecastStart).
			SetForecastEnd(change.ForecastEnd).
			AddLiveStateRevision(1).
			Save(ctx)
		if ent.IsNotFound(updateErr) {
			return PullForwardAdjustment{}, ErrPullForwardPreviewStale
		}
		if updateErr != nil {
			return PullForwardAdjustment{}, opaqueError("apply Pull Forward timing", updateErr)
		}
		changes = append(changes, ForecastChange{
			SessionID: change.SessionID, ForecastStart: change.ForecastStart,
			ForecastEnd: change.ForecastEnd,
		})
	}
	state, err := loadLiveSessionState(ctx, transaction.transaction.SessionRun, anchor)
	if err != nil {
		return PullForwardAdjustment{}, err
	}
	return PullForwardAdjustment{State: state, Changes: changes}, nil
}

func previewPullForward(
	ctx context.Context,
	client *ent.Client,
	eventID int,
	sessionID int,
) (PullForwardPreview, error) {
	active, err := client.Installation.Query().
		Where(installation.ActiveEventIDEQ(eventID)).
		Exist(systemContext(ctx))
	if err != nil {
		return PullForwardPreview{}, opaqueError("check active Event for Pull Forward", err)
	}
	if !active {
		return PullForwardPreview{}, ErrEventNotActive
	}
	identity, err := client.Session.Query().Where(
		session.IDEQ(sessionID), session.EventIDEQ(eventID),
	).Only(ctx)
	if errors.Is(err, privacy.Deny) {
		return PullForwardPreview{}, ErrSessionScopeRequired
	}
	if ent.IsNotFound(err) {
		return PullForwardPreview{}, ErrSessionNotFound
	}
	if err != nil {
		return PullForwardPreview{}, opaqueError("load Pull Forward Session", err)
	}
	if identity.Lifecycle != session.LifecycleEnded {
		return PullForwardPreview{}, ErrSessionLifecycleTransition
	}
	timing, err := loadTimingState(systemContext(ctx), client, eventID, sessionID)
	if err != nil {
		return PullForwardPreview{}, err
	}
	actualEnd, ok := timing.ActualEnds[sessionID]
	if !ok {
		return PullForwardPreview{}, errors.New("ended Session has no Actual End")
	}
	result, err := pullforward.Preview(pullforward.State{
		SessionID: sessionID, Revision: identity.LiveStateRevision,
		ActualEnd: actualEnd, Timing: timing.Sessions,
	})
	if err != nil {
		return PullForwardPreview{}, err
	}
	if scopeErr := requireSessionLaneScope(
		ctx, eventID, timing.affectedLaneIDs(result.Changes),
	); scopeErr != nil {
		return PullForwardPreview{}, scopeErr
	}
	return PullForwardPreview{Result: result, Revisions: timing.Revisions}, nil
}
