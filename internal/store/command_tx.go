package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/auditentry"
	"github.com/dotwaffle/beamers/ent/rundown"
	"github.com/dotwaffle/beamers/ent/sessiondraft"
	"github.com/dotwaffle/beamers/internal/viewer"
)

var (
	// ErrDraftRevisionConflict means a concurrent Draft Edit won the revision update.
	ErrDraftRevisionConflict = errors.New("draft revision conflict")
	// ErrDraftReference means a Draft entity references unknown structural identity.
	ErrDraftReference = errors.New("invalid Draft reference")
)

// CommandIdentity contains one command's durable replay identity.
type CommandIdentity struct {
	ActorAccountID int
	CommandID      string
	PayloadHash    string
	Action         string
	TargetType     string
	TargetID       string
	Now            time.Time
}

// CommandTx owns one command's persistence transaction.
type CommandTx struct {
	transaction *ent.Tx
	committed   bool
}

// BeginCommand starts one concrete command transaction.
func (installation *SQLite) BeginCommand(ctx context.Context) (*CommandTx, error) {
	transaction, err := installation.client.Tx(ctx)
	if err != nil {
		return nil, opaqueError("begin command", err)
	}
	return &CommandTx{transaction: transaction}, nil
}

// LookupReceipt returns the original outcome for an exact retry.
func (transaction *CommandTx) LookupReceipt(
	ctx context.Context,
	identity CommandIdentity,
) (string, bool, error) {
	return findCommandReceipt(ctx, transaction.transaction, commandReceiptParams{
		ActorAccountID: identity.ActorAccountID,
		CommandID:      identity.CommandID,
		PayloadHash:    identity.PayloadHash,
		Action:         identity.Action,
	})
}

// RecordOutcome appends the Command Receipt and Audit Entry without committing.
func (transaction *CommandTx) RecordOutcome(
	ctx context.Context,
	identity CommandIdentity,
	outcomeJSON string,
	rejected bool,
) error {
	result := auditentry.ResultSucceeded
	if rejected {
		result = auditentry.ResultRejected
	}
	if err := createCommandReceipt(ctx, transaction.transaction, commandReceiptParams{
		ActorAccountID: identity.ActorAccountID,
		CommandID:      identity.CommandID,
		PayloadHash:    identity.PayloadHash,
		Action:         identity.Action,
		TargetType:     identity.TargetType,
		TargetID:       identity.TargetID,
		OutcomeJSON:    outcomeJSON,
		Now:            identity.Now,
	}); err != nil {
		return opaqueError("record Command Receipt", err)
	}
	if _, err := transaction.transaction.AuditEntry.Create().
		SetActorAccountID(identity.ActorAccountID).
		SetCreatedAt(identity.Now).
		SetAction(identity.Action).
		SetTargetType(identity.TargetType).
		SetTargetID(identity.TargetID).
		SetResult(result).
		Save(ctx); err != nil {
		return opaqueError("record command Audit Entry", err)
	}
	return nil
}

// CommitConflict records one conflicting Command ID reuse without altering its receipt.
func (transaction *CommandTx) CommitConflict(ctx context.Context, identity CommandIdentity) error {
	if err := auditRejectedCommand(
		ctx,
		transaction.transaction.AuditEntry,
		identity.ActorAccountID,
		identity.Action,
		"Command",
		identity.CommandID,
		identity.Now,
	); err != nil {
		return opaqueError("audit conflicting Command ID", err)
	}
	return transaction.Commit()
}

// Commit makes the command state and evidence durable together.
func (transaction *CommandTx) Commit() error {
	if err := transaction.transaction.Commit(); err != nil {
		return opaqueError("commit command", err)
	}
	transaction.committed = true
	return nil
}

// Rollback safely releases an unfinished transaction and is harmless after commit.
func (transaction *CommandTx) Rollback() error {
	if transaction == nil || transaction.committed {
		return nil
	}
	return transaction.transaction.Rollback()
}

// DraftTarget identifies an existing identity or a batch-local creation.
type DraftTarget struct {
	ID  int
	Ref string
}

// LocationDraftCreate contains one new Location's materialized Draft state.
type LocationDraftCreate struct {
	Ref  string
	Name string
}

// LaneDraftCreate contains one new Lane's materialized Draft state.
type LaneDraftCreate struct {
	Ref      string
	Name     string
	Location DraftTarget
}

// TrackDraftCreate contains one new Track's materialized Draft state.
type TrackDraftCreate struct {
	Ref  string
	Name string
}

// SessionDraftCreate contains one new Session's materialized Draft state.
type SessionDraftCreate struct {
	Ref                    string
	Title                  string
	Type                   string
	AudienceVisibility     string
	PublicDetails          string
	CrewNotes              string
	PlannedStart           time.Time
	PlannedEnd             time.Time
	TimingPolicy           string
	MinimumDurationSeconds int
	StartBoundary          string
	EndBoundary            string
	Lanes                  []DraftTarget
	Locations              []DraftTarget
	Tracks                 []DraftTarget
}

// EditDraftParams contains one validated atomic Draft Edit.
type EditDraftParams struct {
	EventID               int
	ActorAccountID        int
	ExpectedDraftRevision int
	Now                   time.Time
	Locations             []LocationDraftCreate
	Lanes                 []LaneDraftCreate
	Tracks                []TrackDraftCreate
	Sessions              []SessionDraftCreate
}

// DraftChangeResult identifies one durable effective Draft Change.
type DraftChangeResult struct {
	ID         int    `json:"id"`
	Kind       string `json:"kind"`
	TargetType string `json:"target_type"`
	TargetID   int    `json:"target_id"`
}

// EditDraftResult is the minimal durable outcome of one Draft Edit.
type EditDraftResult struct {
	DraftRevision int                 `json:"draft_revision"`
	Changes       []DraftChangeResult `json:"changes"`
}

// EditDraft persists materialized Draft state and immutable change evidence.
func (transaction *CommandTx) EditDraft(
	ctx context.Context,
	params EditDraftParams,
) (EditDraftResult, error) {
	current, err := transaction.transaction.Rundown.Query().
		Where(rundown.EventIDEQ(params.EventID)).
		Only(ctx)
	if ent.IsNotFound(err) {
		current, err = transaction.transaction.Rundown.Create().
			SetEventID(params.EventID).
			Save(ctx)
	}
	if err != nil {
		return EditDraftResult{}, opaqueError("load Rundown Draft", err)
	}
	if params.ExpectedDraftRevision != current.DraftRevision {
		return EditDraftResult{}, ErrDraftRevisionConflict
	}
	nextRevision := current.DraftRevision + 1
	updated, err := transaction.transaction.Rundown.UpdateOneID(current.ID).
		Where(rundown.DraftRevisionEQ(current.DraftRevision)).
		SetDraftRevision(nextRevision).
		Save(ctx)
	if ent.IsNotFound(err) {
		return EditDraftResult{}, ErrDraftRevisionConflict
	}
	if err != nil {
		return EditDraftResult{}, opaqueError("advance Draft revision", err)
	}
	_ = updated
	internalContext := viewer.SystemContext(ctx)
	edit, err := transaction.transaction.DraftEdit.Create().
		SetEventID(params.EventID).
		SetActorAccountID(params.ActorAccountID).
		SetRevision(nextRevision).
		SetCreatedAt(params.Now).
		Save(internalContext)
	if err != nil {
		return EditDraftResult{}, opaqueError("record Draft Edit", err)
	}
	state := draftCreationState{
		locationIDs: make(map[string]int), locationChanges: make(map[string]int),
		laneIDs: make(map[string]int), laneChanges: make(map[string]int),
		trackIDs: make(map[string]int), trackChanges: make(map[string]int),
	}
	result := EditDraftResult{DraftRevision: nextRevision}
	for _, input := range params.Locations {
		created, createErr := transaction.transaction.Location.Create().
			SetEventID(params.EventID).
			SetCreatedAt(params.Now).
			Save(ctx)
		if createErr != nil {
			return EditDraftResult{}, opaqueError("create Draft Location identity", createErr)
		}
		if _, createErr = transaction.transaction.LocationDraft.Create().
			SetLocationID(created.ID).
			SetName(input.Name).
			Save(ctx); createErr != nil {
			return EditDraftResult{}, opaqueError("create Location Draft state", createErr)
		}
		change, createErr := transaction.createDraftChange(
			internalContext, params, edit.ID, nextRevision,
			"CreateLocation", "Location", created.ID, input,
		)
		if createErr != nil {
			return EditDraftResult{}, createErr
		}
		state.locationIDs[input.Ref] = created.ID
		state.locationChanges[input.Ref] = change.ID
		result.Changes = append(result.Changes, draftChangeResult(change))
	}
	for _, input := range params.Lanes {
		locationID, dependencyID, resolveErr := resolveDraftTarget(
			input.Location, state.locationIDs, state.locationChanges,
		)
		if resolveErr != nil {
			return EditDraftResult{}, resolveErr
		}
		created, createErr := transaction.transaction.Lane.Create().
			SetEventID(params.EventID).
			SetCreatedAt(params.Now).
			Save(ctx)
		if createErr != nil {
			return EditDraftResult{}, opaqueError("create Draft Lane identity", createErr)
		}
		if _, createErr = transaction.transaction.LaneDraft.Create().
			SetLaneID(created.ID).
			SetLocationID(locationID).
			SetName(input.Name).
			Save(ctx); createErr != nil {
			return EditDraftResult{}, opaqueError("create Lane Draft state", createErr)
		}
		change, createErr := transaction.createDraftChange(
			internalContext, params, edit.ID, nextRevision,
			"CreateLane", "Lane", created.ID, input,
		)
		if createErr != nil {
			return EditDraftResult{}, createErr
		}
		if dependencyID > 0 {
			if dependencyErr := transaction.createDraftDependency(internalContext, change.ID, dependencyID); dependencyErr != nil {
				return EditDraftResult{}, dependencyErr
			}
		}
		state.laneIDs[input.Ref] = created.ID
		state.laneChanges[input.Ref] = change.ID
		result.Changes = append(result.Changes, draftChangeResult(change))
	}
	for _, input := range params.Tracks {
		created, createErr := transaction.transaction.Track.Create().
			SetEventID(params.EventID).
			SetCreatedAt(params.Now).
			Save(ctx)
		if createErr != nil {
			return EditDraftResult{}, opaqueError("create Draft Track identity", createErr)
		}
		if _, createErr = transaction.transaction.TrackDraft.Create().
			SetTrackID(created.ID).
			SetName(input.Name).
			Save(ctx); createErr != nil {
			return EditDraftResult{}, opaqueError("create Track Draft state", createErr)
		}
		change, createErr := transaction.createDraftChange(
			internalContext, params, edit.ID, nextRevision,
			"CreateTrack", "Track", created.ID, input,
		)
		if createErr != nil {
			return EditDraftResult{}, createErr
		}
		state.trackIDs[input.Ref] = created.ID
		state.trackChanges[input.Ref] = change.ID
		result.Changes = append(result.Changes, draftChangeResult(change))
	}
	for _, input := range params.Sessions {
		created, createErr := transaction.createSessionDraft(ctx, params, input, state)
		if createErr != nil {
			return EditDraftResult{}, createErr
		}
		change, createErr := transaction.createDraftChange(
			internalContext, params, edit.ID, nextRevision,
			"CreateSession", "Session", created.sessionID, input,
		)
		if createErr != nil {
			return EditDraftResult{}, createErr
		}
		for _, dependencyID := range created.dependencyIDs {
			if dependencyErr := transaction.createDraftDependency(internalContext, change.ID, dependencyID); dependencyErr != nil {
				return EditDraftResult{}, dependencyErr
			}
		}
		result.Changes = append(result.Changes, draftChangeResult(change))
	}
	return result, nil
}

type draftCreationState struct {
	locationIDs     map[string]int
	locationChanges map[string]int
	laneIDs         map[string]int
	laneChanges     map[string]int
	trackIDs        map[string]int
	trackChanges    map[string]int
}

type createdSessionDraft struct {
	sessionID     int
	dependencyIDs []int
}

func (transaction *CommandTx) createSessionDraft(
	ctx context.Context,
	params EditDraftParams,
	input SessionDraftCreate,
	state draftCreationState,
) (createdSessionDraft, error) {
	laneIDs, laneDependencies, err := resolveDraftTargets(input.Lanes, state.laneIDs, state.laneChanges)
	if err != nil {
		return createdSessionDraft{}, err
	}
	locationIDs, locationDependencies, err := resolveDraftTargets(
		input.Locations, state.locationIDs, state.locationChanges,
	)
	if err != nil {
		return createdSessionDraft{}, err
	}
	if len(locationIDs) == 0 {
		locationIDs, err = transaction.laneLocationIDs(ctx, laneIDs)
		if err != nil {
			return createdSessionDraft{}, err
		}
	}
	trackIDs, trackDependencies, err := resolveDraftTargets(input.Tracks, state.trackIDs, state.trackChanges)
	if err != nil {
		return createdSessionDraft{}, err
	}
	created, err := transaction.transaction.Session.Create().
		SetEventID(params.EventID).
		SetCreatedAt(params.Now).
		Save(ctx)
	if err != nil {
		return createdSessionDraft{}, opaqueError("create Draft Session identity", err)
	}
	create := transaction.transaction.SessionDraft.Create().
		SetSessionID(created.ID).
		SetTitle(input.Title).
		SetType(sessiondraft.Type(input.Type)).
		SetAudienceVisibility(sessiondraft.AudienceVisibility(input.AudienceVisibility)).
		SetPlannedStart(input.PlannedStart).
		SetPlannedEnd(input.PlannedEnd).
		SetTimingPolicy(sessiondraft.TimingPolicy(input.TimingPolicy)).
		SetMinimumDurationSeconds(input.MinimumDurationSeconds).
		SetStartBoundary(sessiondraft.StartBoundary(input.StartBoundary)).
		SetEndBoundary(sessiondraft.EndBoundary(input.EndBoundary)).
		AddLaneIDs(laneIDs...).
		AddLocationIDs(locationIDs...).
		AddTrackIDs(trackIDs...)
	if input.PublicDetails != "" {
		create.SetPublicDetails(input.PublicDetails)
	}
	if input.CrewNotes != "" {
		create.SetCrewNotes(input.CrewNotes)
	}
	if _, err = create.Save(ctx); err != nil {
		return createdSessionDraft{}, opaqueError("create Session Draft state", err)
	}
	dependencies := append([]int(nil), laneDependencies...)
	dependencies = append(dependencies, locationDependencies...)
	dependencies = append(dependencies, trackDependencies...)
	return createdSessionDraft{sessionID: created.ID, dependencyIDs: uniqueInts(dependencies)}, nil
}

func (transaction *CommandTx) laneLocationIDs(ctx context.Context, laneIDs []int) ([]int, error) {
	locationIDs := make([]int, 0, len(laneIDs))
	for _, laneID := range laneIDs {
		identity, err := transaction.transaction.Lane.Get(ctx, laneID)
		if err != nil {
			return nil, opaqueError("load Session Lane identity", err)
		}
		state, err := identity.QueryDraft().Only(ctx)
		if err != nil {
			return nil, opaqueError("load Session Lane Draft state", err)
		}
		locationIDs = append(locationIDs, state.LocationID)
	}
	return uniqueInts(locationIDs), nil
}

func (transaction *CommandTx) createDraftChange(
	ctx context.Context,
	params EditDraftParams,
	editID int,
	revision int,
	kind string,
	targetType string,
	targetID int,
	payload any,
) (*ent.DraftChange, error) {
	encoded, err := json.Marshal(struct {
		Version int `json:"version"`
		After   any `json:"after"`
	}{Version: 1, After: payload})
	if err != nil {
		return nil, opaqueError("encode Draft Change evidence", err)
	}
	change, err := transaction.transaction.DraftChange.Create().
		SetEventID(params.EventID).
		SetDraftEditID(editID).
		SetRevision(revision).
		SetKind(kind).
		SetTargetType(targetType).
		SetTargetID(targetID).
		SetFactKey("entity").
		SetPayloadJSON(string(encoded)).
		SetCreatedAt(params.Now).
		Save(ctx)
	if err != nil {
		return nil, opaqueError("record Draft Change", err)
	}
	return change, nil
}

func (transaction *CommandTx) createDraftDependency(ctx context.Context, changeID, dependencyID int) error {
	_, err := transaction.transaction.DraftChangeDependency.Create().
		SetChangeID(changeID).
		SetDependsOnID(dependencyID).
		Save(ctx)
	if err != nil {
		return opaqueError("record Draft Change dependency", err)
	}
	return nil
}

func resolveDraftTargets(
	targets []DraftTarget,
	localIDs map[string]int,
	localChanges map[string]int,
) ([]int, []int, error) {
	ids := make([]int, 0, len(targets))
	dependencies := make([]int, 0, len(targets))
	for _, target := range targets {
		id, dependencyID, err := resolveDraftTarget(target, localIDs, localChanges)
		if err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		if dependencyID > 0 {
			dependencies = append(dependencies, dependencyID)
		}
	}
	return ids, dependencies, nil
}

func resolveDraftTarget(target DraftTarget, localIDs, localChanges map[string]int) (int, int, error) {
	if target.ID > 0 {
		return target.ID, 0, nil
	}
	id := localIDs[target.Ref]
	if id == 0 {
		return 0, 0, ErrDraftReference
	}
	return id, localChanges[target.Ref], nil
}

func uniqueInts(values []int) []int {
	seen := make(map[int]struct{}, len(values))
	result := make([]int, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists || value == 0 {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func draftChangeResult(change *ent.DraftChange) DraftChangeResult {
	return DraftChangeResult{
		ID: change.ID, Kind: change.Kind, TargetType: change.TargetType, TargetID: change.TargetID,
	}
}
