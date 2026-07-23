package store

import (
	"context"
	"errors"
	"slices"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/draftchange"
	"github.com/dotwaffle/beamers/ent/draftchangedependency"
	"github.com/dotwaffle/beamers/ent/rundown"
	"github.com/dotwaffle/beamers/ent/session"
)

var (
	// ErrDraftSessionDeletion means a Session has Published, Run, or dependent history.
	ErrDraftSessionDeletion = errors.New("only a never-Published, unreferenced Draft Session can be deleted")
)

// DeleteDraftSessionParams identifies one permanent Draft-only deletion.
type DeleteDraftSessionParams struct {
	EventID               int
	SessionID             int
	ExpectedDraftRevision int
}

// DeleteDraftSessionResult is the committed Draft revision and deleted identity.
type DeleteDraftSessionResult struct {
	DraftRevision int `json:"draft_revision"`
	SessionID     int `json:"session_id"`
}

// DeleteDraftSession permanently removes one never-Published, unreferenced Draft Session.
func (transaction *CommandTx) DeleteDraftSession(
	ctx context.Context,
	params DeleteDraftSessionParams,
) (DeleteDraftSessionResult, error) {
	current, err := transaction.transaction.Rundown.Query().Where(
		rundown.EventIDEQ(params.EventID),
		rundown.DraftRevisionEQ(params.ExpectedDraftRevision),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return DeleteDraftSessionResult{}, ErrDraftRevisionConflict
	}
	if err != nil {
		return DeleteDraftSessionResult{}, opaqueError("load Rundown for Draft Session deletion", err)
	}
	identity, err := transaction.transaction.Session.Query().Where(
		session.IDEQ(params.SessionID), session.EventIDEQ(params.EventID),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return DeleteDraftSessionResult{}, ErrSessionNotFound
	}
	if err != nil {
		return DeleteDraftSessionResult{}, opaqueError("load Draft Session for deletion", err)
	}
	published, err := identity.QueryPublishedVersions().Exist(ctx)
	if err != nil {
		return DeleteDraftSessionResult{}, opaqueError("check Draft Session Published history", err)
	}
	runs, err := identity.QueryRuns().Exist(ctx)
	if err != nil {
		return DeleteDraftSessionResult{}, opaqueError("check Draft Session Run history", err)
	}
	referenced, err := transaction.draftSessionHasDependents(ctx, params.EventID, params.SessionID)
	if err != nil {
		return DeleteDraftSessionResult{}, err
	}
	if published || runs || referenced {
		return DeleteDraftSessionResult{}, ErrDraftSessionDeletion
	}
	draft, err := identity.QueryDraft().Only(ctx)
	if ent.IsNotFound(err) {
		return DeleteDraftSessionResult{}, ErrSessionNotFound
	}
	if err != nil {
		return DeleteDraftSessionResult{}, opaqueError("load Session Draft state for deletion", err)
	}
	if deleteErr := transaction.transaction.SessionDraft.DeleteOne(draft).Exec(ctx); deleteErr != nil {
		return DeleteDraftSessionResult{}, opaqueError("delete Session Draft state", deleteErr)
	}
	if _, updateErr := transaction.transaction.DraftChange.Update().Where(
		draftchange.EventIDEQ(params.EventID),
		draftchange.TargetTypeEQ(draftTargetSession),
		draftchange.TargetIDEQ(params.SessionID),
		draftchange.StatusIn(draftchange.StatusEffective, draftchange.StatusConflicted),
	).SetStatus(draftchange.StatusDiscarded).Save(systemContext(ctx)); updateErr != nil {
		return DeleteDraftSessionResult{}, opaqueError("discard deleted Session Draft history", updateErr)
	}
	if deleteErr := transaction.transaction.Session.DeleteOne(identity).Exec(ctx); deleteErr != nil {
		return DeleteDraftSessionResult{}, opaqueError("delete Draft Session identity", deleteErr)
	}
	updated, err := transaction.transaction.Rundown.UpdateOneID(current.ID).
		Where(rundown.DraftRevisionEQ(params.ExpectedDraftRevision)).
		AddDraftRevision(1).Save(ctx)
	if ent.IsNotFound(err) {
		return DeleteDraftSessionResult{}, ErrDraftRevisionConflict
	}
	if err != nil {
		return DeleteDraftSessionResult{}, opaqueError("advance Draft after Session deletion", err)
	}
	return DeleteDraftSessionResult{DraftRevision: updated.DraftRevision, SessionID: params.SessionID}, nil
}

func (transaction *CommandTx) draftSessionHasDependents(
	ctx context.Context,
	eventID int,
	sessionID int,
) (bool, error) {
	internalContext := systemContext(ctx)
	owned, err := transaction.transaction.DraftChange.Query().Where(
		draftchange.EventIDEQ(eventID),
		draftchange.TargetTypeEQ(draftTargetSession),
		draftchange.TargetIDEQ(sessionID),
	).IDs(internalContext)
	if err != nil {
		return false, opaqueError("load Draft Session change history", err)
	}
	if len(owned) == 0 {
		return false, nil
	}
	dependencies, err := transaction.transaction.DraftChangeDependency.Query().Where(
		draftchangedependency.DependsOnIDIn(owned...),
	).All(internalContext)
	if err != nil {
		return false, opaqueError("load Draft Session dependents", err)
	}
	for _, dependency := range dependencies {
		if !slices.Contains(owned, dependency.ChangeID) {
			return true, nil
		}
	}
	return false, nil
}
