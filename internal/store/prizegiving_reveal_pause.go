package store

import (
	"context"
	"slices"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/displayassignment"
	"github.com/dotwaffle/beamers/ent/displayoverride"
	"github.com/dotwaffle/beamers/ent/prizegiving"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/internal/prizegivingvalue"
)

func (transaction *CommandTx) reconcilePrizegivingRevealPauses(
	ctx context.Context,
	eventID int,
	now time.Time,
) error {
	ctx = systemContext(ctx)
	plans, err := transaction.transaction.Prizegiving.Query().
		Where(
			prizegiving.EventIDEQ(eventID),
			prizegiving.LockedEQ(true),
		).
		All(ctx)
	if err != nil {
		return opaqueError("load Prizegiving Reveals for Override coverage", err)
	}
	for _, plan := range plans {
		if err := transaction.reconcilePrizegivingRevealPause(
			ctx,
			plan,
			now,
		); err != nil {
			return err
		}
	}
	return nil
}

func (transaction *CommandTx) reconcilePrizegivingRevealPause(
	ctx context.Context,
	plan *ent.Prizegiving,
	now time.Time,
) error {
	foundSession, err := transaction.transaction.Session.Query().
		Where(
			session.IDEQ(plan.CeremonySessionID),
			session.EventIDEQ(plan.EventID),
			session.ProgramOutputKindEQ(session.ProgramOutputKindResult),
		).
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return opaqueError("load Prizegiving Reveal for pause update", err)
	}
	index := -1
	for candidate, state := range plan.ItemStates {
		if state.ItemRef == foundSession.ProgramOutputResult.ItemRef &&
			state.EffectiveAt(now).Status == prizegivingvalue.StageRevealing {
			index = candidate
			break
		}
	}
	if index < 0 {
		return nil
	}
	current := plan.ItemStates[index]
	transitions, err := transaction.prizegivingRevealCoverageTransitions(
		ctx,
		plan.EventID,
		plan.CeremonySessionID,
		current.RevealPausedAt,
		now,
	)
	if err != nil {
		return err
	}
	next := prizegivingvalue.ReconcileRevealCoverageState(
		current,
		transitions,
		now,
	)
	if next == current {
		return nil
	}
	if foundSession.ProgramOutputRevision != plan.OperationRevision {
		return ErrProgramRevision
	}
	states := slices.Clone(plan.ItemStates)
	states[index] = next
	if _, err = transaction.transaction.Prizegiving.UpdateOne(plan).
		Where(prizegiving.OperationRevisionEQ(plan.OperationRevision)).
		AddOperationRevision(1).
		SetItemStates(states).
		Save(ctx); err != nil {
		if ent.IsNotFound(err) {
			return ErrProgramRevision
		}
		return opaqueError("save Prizegiving Reveal pause", err)
	}
	if _, err = transaction.transaction.Session.UpdateOne(foundSession).
		Where(session.ProgramOutputRevisionEQ(plan.OperationRevision)).
		SetProgramOutputRevision(plan.OperationRevision + 1).
		Save(ctx); err != nil {
		if ent.IsNotFound(err) {
			return ErrProgramRevision
		}
		return opaqueError("advance Program Output after Reveal pause", err)
	}
	return nil
}

func (transaction *CommandTx) prizegivingRevealCoverageTransitions(
	ctx context.Context,
	eventID, sessionID int,
	pausedAt, now time.Time,
) ([]prizegivingvalue.RevealCoverageTransition, error) {
	return prizegivingRevealCoverageTransitions(
		ctx,
		transaction.transaction.Client(),
		eventID,
		sessionID,
		pausedAt,
		now,
	)
}

func prizegivingRevealCoverageTransitions(
	ctx context.Context,
	client *ent.Client,
	eventID, sessionID int,
	pausedAt, now time.Time,
) ([]prizegivingvalue.RevealCoverageTransition, error) {
	assignments, err := client.DisplayAssignment.Query().
		Where(
			displayassignment.EventIDEQ(eventID),
			displayassignment.ViewKeyEQ("competition-output"),
		).
		All(ctx)
	if err != nil {
		return nil, opaqueError(
			"load Program Channel Display consumers",
			err,
		)
	}
	consumers := make([]*ent.DisplayAssignment, 0, len(assignments))
	consumerIDs := make([]int, 0, len(assignments))
	for _, assignment := range assignments {
		channelID, channelErr := competitionOutputProgramChannelID(
			ctx,
			client,
			eventID,
			assignment.LocationID,
		)
		if channelErr != nil {
			return nil, channelErr
		}
		if channelID == sessionID {
			consumers = append(consumers, assignment)
			consumerIDs = append(consumerIDs, assignment.DisplayID)
		}
	}
	overrides, err := client.DisplayOverride.Query().
		Where(
			displayoverride.EventIDEQ(eventID),
			displayoverride.PresentationEQ(displayoverride.PresentationReplace),
			displayoverride.CreatedAtLTE(now),
		).
		All(ctx)
	if err != nil {
		return nil, opaqueError(
			"load Replace Overrides for Reveal coverage",
			err,
		)
	}
	intervals := make(
		[]prizegivingvalue.RevealCoverageInterval,
		0,
		len(overrides),
	)
	for _, candidate := range overrides {
		displayIDs, resolveErr := replaceOverrideDisplayIDs(
			ctx,
			client,
			eventID,
			consumers,
			candidate,
		)
		if resolveErr != nil {
			return nil, resolveErr
		}
		intervals = append(intervals, prizegivingvalue.RevealCoverageInterval{
			DisplayIDs: displayIDs,
			StartedAt:  candidate.CreatedAt,
			EndedAt:    displayOverrideEndedAt(candidate),
		})
	}
	return prizegivingvalue.ReconcileRevealCoverage(
		consumerIDs,
		intervals,
		pausedAt,
		now,
	), nil
}

func displayOverrideEndedAt(found *ent.DisplayOverride) time.Time {
	if found.ClearedAt != nil &&
		(found.ExpiresAt == nil || found.ClearedAt.Before(*found.ExpiresAt)) {
		return *found.ClearedAt
	}
	if found.ExpiresAt != nil {
		return *found.ExpiresAt
	}
	return time.Time{}
}

func replaceOverrideDisplayIDs(
	ctx context.Context,
	client *ent.Client,
	eventID int,
	consumers []*ent.DisplayAssignment,
	found *ent.DisplayOverride,
) ([]int, error) {
	if found.Kind == displayoverride.KindTechnicalDifficulties {
		displayIDs := make([]int, 0, len(consumers))
		for _, consumer := range consumers {
			if assignmentInDisplayGroup(consumer, found.TargetGroupKey) {
				displayIDs = append(displayIDs, consumer.DisplayID)
			}
		}
		return displayIDs, nil
	}
	targets, err := resolveOverrideTargets(
		ctx,
		client,
		eventID,
		DisplayOverrideTarget{
			Type: DisplayOverrideTargetType(found.TargetType.String()),
			ID:   found.TargetID,
			Key:  found.TargetGroupKey,
		},
	)
	if err != nil {
		return nil, err
	}
	displayIDs := make([]int, 0, len(targets))
	for _, target := range targets {
		displayIDs = append(displayIDs, target.ID)
	}
	return displayIDs, nil
}
