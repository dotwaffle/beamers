package store

import (
	"context"
	"encoding/json"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/displayoverride"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionrun"
	"github.com/dotwaffle/beamers/internal/authz"
)

// Scope loaders are the plan phase the Capability Table's scoped rows are
// judged against. Each resolves its target the same way, and in the same order,
// as the entrypoint that will apply the command, so that judging the target
// early cannot reach a different target than the application does or fail
// differently than the application would.

// SessionLaneScope resolves the Lanes one Session occupies, for the
// Lanes-of-target rows covering live Session control.
func (transaction *CommandTx) SessionLaneScope(
	ctx context.Context,
	eventID, sessionID int,
) (authz.Facts, error) {
	found, err := transaction.transaction.Session.Query().
		Where(session.IDEQ(sessionID), session.EventIDEQ(eventID)).
		Only(systemContext(ctx))
	if ent.IsNotFound(err) {
		return authz.Facts{}, ErrSessionNotFound
	}
	if err != nil {
		return authz.Facts{}, opaqueError("load Session scope", err)
	}
	laneIDs, _, err := sessionPlacement(systemContext(ctx), found)
	if err != nil {
		return authz.Facts{}, err
	}
	return authz.Lanes(eventID, laneIDs), nil
}

// LiveSessionLaneScope resolves the Lanes a running Session occupies from its
// open Run Snapshot, for the rows whose imperative guard is judged against that
// snapshot rather than against current placement.
//
// A Run Snapshot is frozen when the Session starts and is never rewritten, so
// after a republish or a forecast Lane change it names different Lanes than the
// Session's current placement does. Judging current placement would refuse an
// Operator who holds the Lanes the running Session was started in, which is
// authority they have today, so the snapshot is what the table is judged
// against too.
//
// A Session with no open Run has no snapshot to judge, and is refused rather
// than judged against something else.
func (transaction *CommandTx) LiveSessionLaneScope(
	ctx context.Context,
	eventID, sessionID int,
) (authz.Facts, error) {
	found, err := transaction.transaction.Session.Query().
		Where(session.IDEQ(sessionID), session.EventIDEQ(eventID)).
		Only(systemContext(ctx))
	if ent.IsNotFound(err) {
		return authz.Facts{}, ErrSessionNotFound
	}
	if err != nil {
		return authz.Facts{}, opaqueError("load live Session scope", err)
	}
	if found.Lifecycle != session.LifecycleLive {
		return authz.Facts{}, ErrSessionLifecycleTransition
	}
	run, err := transaction.transaction.SessionRun.Query().
		Where(sessionrun.SessionIDEQ(sessionID), sessionrun.ActualEndIsNil()).
		Order(ent.Desc(sessionrun.FieldID)).
		First(systemContext(ctx))
	if err != nil {
		return authz.Facts{}, opaqueError("load live Session Run scope", err)
	}
	var snapshot SessionRunSnapshot
	if decodeErr := json.Unmarshal([]byte(run.SnapshotJSON), &snapshot); decodeErr != nil {
		return authz.Facts{}, opaqueError("decode live Session Run Snapshot scope", decodeErr)
	}
	return authz.Lanes(eventID, snapshot.LaneIDs), nil
}

// StageMessageScope resolves the Display Group key a Stage Message will
// activate against, which the Event's presets can supply when the command does
// not name one.
func (transaction *CommandTx) StageMessageScope(
	ctx context.Context,
	params ActivateStageMessageParams,
) (authz.Facts, error) {
	if err := transaction.requireActiveEvent(ctx, params.EventID); err != nil {
		return authz.Facts{}, err
	}
	foundEvent, err := transaction.transaction.Event.Get(systemContext(ctx), params.EventID)
	if ent.IsNotFound(err) {
		return authz.Facts{}, ErrDisplayOverrideNotFound
	}
	if err != nil {
		return authz.Facts{}, opaqueError("load Stage Message Event", err)
	}
	resolved, err := resolveStageMessage(foundEvent, params)
	if err != nil {
		return authz.Facts{}, err
	}
	return authz.DisplayGroups(params.EventID, []string{resolved.TargetGroupKey}), nil
}

// TechnicalDifficultiesScope resolves the Display Group key a Technical
// Difficulties Override will activate against.
func (transaction *CommandTx) TechnicalDifficultiesScope(
	ctx context.Context,
	params ActivateTechnicalDifficultiesParams,
) (authz.Facts, error) {
	if err := transaction.requireActiveEvent(ctx, params.EventID); err != nil {
		return authz.Facts{}, err
	}
	normalized, err := normalizeTechnicalDifficulties(params)
	if err != nil {
		return authz.Facts{}, err
	}
	return authz.DisplayGroups(params.EventID, []string{normalized.TargetGroupKey}), nil
}

// PriorityOverrideScope resolves the scope facts an Urgent Notice or Emergency
// Alert is judged against.
func (transaction *CommandTx) PriorityOverrideScope(
	ctx context.Context,
	params ActivatePriorityOverrideParams,
) (authz.Facts, error) {
	if err := transaction.requireActiveEvent(ctx, params.EventID); err != nil {
		return authz.Facts{}, err
	}
	normalized, err := normalizePriorityOverride(params)
	if err != nil {
		return authz.Facts{}, err
	}
	return DisplayOverrideTargetScope(params.EventID, normalized.Target), nil
}

// ClearDisplayOverrideScope resolves the target of the Override being cleared,
// and demands the EmergencyAlert Capability when that Override is an Emergency
// Alert.
func (transaction *CommandTx) ClearDisplayOverrideScope(
	ctx context.Context,
	eventID, overrideID int,
) (authz.Facts, error) {
	found, err := transaction.transaction.DisplayOverride.Query().
		Where(
			displayoverride.IDEQ(overrideID),
			displayoverride.EventIDEQ(eventID),
		).
		Only(systemContext(ctx))
	if ent.IsNotFound(err) {
		return authz.Facts{}, ErrDisplayOverrideNotFound
	}
	if err != nil {
		return authz.Facts{}, opaqueError("load Display Override", err)
	}
	projected := displayOverride(found)
	facts := DisplayOverrideTargetScope(eventID, projected.Target)
	if projected.Kind == DisplayOverrideEmergencyAlert {
		facts = facts.Demanding(authz.EmergencyAlert)
	}
	return facts, nil
}

// DisplayOverrideTargetScope states the DisplayGroups-of-target facts one
// Override target resolves to today: a Lane target is judged by Lane grant, and
// every other target by the synthetic key displayOverrideTargetKey builds.
//
// D6: the synthetic key encodes a database identifier rather than naming the
// Display Groups of the Displays the Override will actually disturb. Resolving
// the target to those Displays both widens and narrows existing authority, so
// this mirrors today's rule until that discrepancy is decided.
func DisplayOverrideTargetScope(eventID int, target DisplayOverrideTarget) authz.Facts {
	if target.Type == DisplayOverrideTargetLane {
		return authz.DisplayGroupsOfLane(eventID, target.ID)
	}
	return authz.DisplayGroups(eventID, []string{displayOverrideTargetKey(target)})
}
