package store

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/displayassignment"
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
	if err := requireActor(ctx, "CommandTx.SessionLaneScope"); err != nil {
		return authz.Facts{}, err
	}

	found, err := transaction.transaction.Session.Query().
		Where(session.IDEQ(sessionID), session.EventIDEQ(eventID)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return authz.Facts{}, ErrSessionNotFound
	}
	if err != nil {
		return authz.Facts{}, opaqueError("load Session scope", err)
	}
	laneIDs, err := sessionLanes(ctx, found)
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
	if err := requireActor(ctx, "CommandTx.LiveSessionLaneScope"); err != nil {
		return authz.Facts{}, err
	}

	found, err := transaction.transaction.Session.Query().
		Where(session.IDEQ(sessionID), session.EventIDEQ(eventID)).
		Only(ctx)
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
		First(ctx)
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
	if err := requireActor(ctx, "CommandTx.StageMessageScope"); err != nil {
		return authz.Facts{}, err
	}

	if err := transaction.requireActiveEvent(ctx, params.EventID); err != nil {
		return authz.Facts{}, err
	}
	foundEvent, err := transaction.transaction.Event.Get(ctx, params.EventID)
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
	if err := requireActor(ctx, "CommandTx.TechnicalDifficultiesScope"); err != nil {
		return authz.Facts{}, err
	}

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
	if err := requireActor(ctx, "CommandTx.PriorityOverrideScope"); err != nil {
		return authz.Facts{}, err
	}

	if err := transaction.requireActiveEvent(ctx, params.EventID); err != nil {
		return authz.Facts{}, err
	}
	normalized, err := normalizePriorityOverride(params)
	if err != nil {
		return authz.Facts{}, err
	}
	return DisplayOverrideTargetScope(
		ctx, transaction.transaction.Client(), params.EventID, normalized.Target,
	)
}

// ClearDisplayOverrideScope resolves the target of the Override being cleared,
// and demands the EmergencyAlert Capability when that Override is an Emergency
// Alert.
func (transaction *CommandTx) ClearDisplayOverrideScope(
	ctx context.Context,
	eventID, overrideID int,
) (authz.Facts, error) {
	if err := requireActor(ctx, "CommandTx.ClearDisplayOverrideScope"); err != nil {
		return authz.Facts{}, err
	}

	found, err := transaction.transaction.DisplayOverride.Query().
		Where(
			displayoverride.IDEQ(overrideID),
			displayoverride.EventIDEQ(eventID),
		).
		Only(ctx)
	if ent.IsNotFound(err) {
		return authz.Facts{}, ErrDisplayOverrideNotFound
	}
	if err != nil {
		return authz.Facts{}, opaqueError("load Display Override", err)
	}
	projected := displayOverride(found)
	facts, err := DisplayOverrideTargetScope(
		ctx, transaction.transaction.Client(), eventID, projected.Target,
	)
	if err != nil {
		return authz.Facts{}, err
	}
	if projected.Kind == DisplayOverrideEmergencyAlert {
		facts = facts.Demanding(authz.EmergencyAlert)
	}
	return facts, nil
}

// DisplayOverrideTargetScope states the DisplayGroups-of-target facts one
// Override target resolves to: a Lane target is judged by Lane grant, a
// Program Channel target by the Display Group keys of the Displays currently
// consuming it, and every other target by the literal or synthetic key
// displayOverrideTargetKey builds.
//
// D6 resolved a Program Channel target to the real Display Groups of the
// Displays it feeds, rather than the synthetic key displayOverrideTargetKey
// built from its database identifier. Repointing a Program Channel to
// different consuming Displays therefore changes who may override it: no
// grant is grandfathered by the identifier alone.
func DisplayOverrideTargetScope(
	ctx context.Context,
	client *ent.Client,
	eventID int,
	target DisplayOverrideTarget,
) (authz.Facts, error) {
	switch target.Type {
	case DisplayOverrideTargetLane:
		return authz.DisplayGroupsOfLane(eventID, target.ID), nil
	case DisplayOverrideTargetProgramChannel:
		groupKeys, err := programChannelConsumingDisplayGroupKeys(ctx, client, eventID, target.ID)
		if err != nil {
			return authz.Facts{}, err
		}
		return authz.DisplayGroups(eventID, groupKeys), nil
	default:
		return authz.DisplayGroups(eventID, []string{displayOverrideTargetKey(target)}), nil
	}
}

// programChannelConsumingDisplayGroupKeys returns the Display Group keys
// carried by every Display Assignment currently routed to channelID's
// Program Channel, deduplicated and sorted for a stable Facts comparison.
//
// A channel feeding a Display with no Display Group key at all contributes
// none, so an Operator can never be granted authority over it by key; only a
// Producer, whose shortcut this dimension already carries, can override it.
// That is a deliberate fail-closed default, not an oversight: it is no
// stricter than today's rule, under which the synthetic key it replaces was
// never a key any grant could actually name.
func programChannelConsumingDisplayGroupKeys(
	ctx context.Context,
	client *ent.Client,
	eventID, channelID int,
) ([]string, error) {
	assignments, err := client.DisplayAssignment.Query().
		Where(
			displayassignment.EventIDEQ(eventID),
			displayassignment.ViewKeyEQ("competition-output"),
		).
		All(ctx)
	if err != nil {
		return nil, opaqueError("resolve Program Channel Override scope", err)
	}
	routing, err := loadProgramChannelRouting(ctx, client, eventID)
	if err != nil {
		return nil, err
	}
	keys := make(map[string]struct{})
	for _, assignment := range assignments {
		if routing.channelAt(assignment.LocationID) != channelID {
			continue
		}
		for _, key := range assignment.DisplayGroupKeys {
			keys[key] = struct{}{}
		}
	}
	result := make([]string, 0, len(keys))
	for key := range keys {
		result = append(result, key)
	}
	sort.Strings(result)
	return result, nil
}
