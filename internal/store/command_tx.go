package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/auditentry"
	"github.com/dotwaffle/beamers/ent/draftchange"
	"github.com/dotwaffle/beamers/ent/lane"
	"github.com/dotwaffle/beamers/ent/location"
	"github.com/dotwaffle/beamers/ent/rundown"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessiondraft"
	"github.com/dotwaffle/beamers/ent/track"
)

var (
	// ErrDraftRevisionConflict means a concurrent Draft Edit won the revision update.
	ErrDraftRevisionConflict = errors.New("draft revision conflict")
	// ErrDraftReference means a Draft entity references unknown structural identity.
	ErrDraftReference = errors.New("invalid Draft reference")
)

// DraftRevisionConflictError describes the current state that overlaps a stale edit.
type DraftRevisionConflictError struct {
	CurrentDraftRevision int
	OverlappingChanges   []DraftChangeResult
}

func (conflict *DraftRevisionConflictError) Error() string { return ErrDraftRevisionConflict.Error() }

func (conflict *DraftRevisionConflictError) Unwrap() error { return ErrDraftRevisionConflict }

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

// AuditDetails contains optional domain-required evidence for one outcome.
type AuditDetails struct {
	Reason string
	Note   string
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
	return transaction.RecordOutcomeWithAudit(ctx, identity, outcomeJSON, rejected, AuditDetails{})
}

// RecordOutcomeWithAudit appends a receipt and detailed Audit Entry without committing.
func (transaction *CommandTx) RecordOutcomeWithAudit(
	ctx context.Context,
	identity CommandIdentity,
	outcomeJSON string,
	rejected bool,
	details AuditDetails,
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
		SetReason(auditReason(outcomeJSON, rejected, details.Reason)).
		SetNote(details.Note).
		Save(ctx); err != nil {
		return opaqueError("record command Audit Entry", err)
	}
	return nil
}

func auditReason(outcomeJSON string, rejected bool, reason string) string {
	if reason != "" {
		return reason
	}
	if !rejected {
		return ""
	}
	var outcome commandOutcome
	if err := json.Unmarshal([]byte(outcomeJSON), &outcome); err != nil || outcome.Rejected == nil {
		return ""
	}
	return outcome.Rejected.Code
}

// RecordRejection appends one stable rejected outcome and its Audit Entry.
func (transaction *CommandTx) RecordRejection(
	ctx context.Context,
	identity CommandIdentity,
	rejection CommandRejection,
) error {
	encoded, err := json.Marshal(commandOutcome{Rejected: &rejection})
	if err != nil {
		return opaqueError("encode rejected command outcome", err)
	}
	return transaction.RecordOutcome(ctx, identity, string(encoded), true)
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
	ID           int
	Ref          string
	Name         string
	UpdateFields []string
}

// LaneDraftCreate contains one new Lane's materialized Draft state.
type LaneDraftCreate struct {
	ID           int
	Ref          string
	Name         string
	Location     DraftTarget
	UpdateFields []string
}

// TrackDraftCreate contains one new Track's materialized Draft state.
type TrackDraftCreate struct {
	ID           int
	Ref          string
	Name         string
	UpdateFields []string
}

// SessionDraftCreate contains one new Session's materialized Draft state.
type SessionDraftCreate struct {
	ID                     int
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
	AddLanes               []DraftTarget
	RemoveLanes            []DraftTarget
	AddLocations           []DraftTarget
	RemoveLocations        []DraftTarget
	AddTracks              []DraftTarget
	RemoveTracks           []DraftTarget
	UpdateFields           []string
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
	ID               int    `json:"id"`
	Kind             string `json:"kind"`
	TargetType       string `json:"target_type"`
	TargetID         int    `json:"target_id"`
	FactKey          string `json:"fact_key,omitempty"`
	Status           string `json:"status,omitempty"`
	CurrentValueJSON string `json:"current_value_json,omitempty"`
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
	if referenceErr := transaction.validateDraftReferences(ctx, params); referenceErr != nil {
		return EditDraftResult{}, referenceErr
	}
	if params.ExpectedDraftRevision > current.DraftRevision {
		return EditDraftResult{}, ErrDraftRevisionConflict
	}
	if params.ExpectedDraftRevision < current.DraftRevision {
		overlaps, overlapErr := transaction.draftOverlaps(ctx, params, params.ExpectedDraftRevision)
		if overlapErr != nil {
			return EditDraftResult{}, overlapErr
		}
		if len(overlaps) > 0 {
			return EditDraftResult{}, &DraftRevisionConflictError{
				CurrentDraftRevision: current.DraftRevision, OverlappingChanges: overlaps,
			}
		}
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
	internalContext := systemContext(ctx)
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
		if input.ID > 0 {
			changes, updateErr := transaction.updateLocationDraft(ctx, internalContext, params, edit.ID, nextRevision, input)
			if updateErr != nil {
				return EditDraftResult{}, updateErr
			}
			result.Changes = append(result.Changes, changes...)
			continue
		}
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
		if input.ID > 0 {
			changes, updateErr := transaction.updateLaneDraft(ctx, internalContext, params, edit.ID, nextRevision, input, state)
			if updateErr != nil {
				return EditDraftResult{}, updateErr
			}
			result.Changes = append(result.Changes, changes...)
			continue
		}
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
			"CreateLane", "Lane", created.ID, LaneDraftCreate{Name: input.Name, Location: DraftTarget{ID: locationID}},
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
		if input.ID > 0 {
			changes, updateErr := transaction.updateTrackDraft(ctx, internalContext, params, edit.ID, nextRevision, input)
			if updateErr != nil {
				return EditDraftResult{}, updateErr
			}
			result.Changes = append(result.Changes, changes...)
			continue
		}
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
		if input.ID > 0 {
			changes, updateErr := transaction.updateSessionDraft(ctx, internalContext, params, edit.ID, nextRevision, input, state)
			if updateErr != nil {
				return EditDraftResult{}, updateErr
			}
			result.Changes = append(result.Changes, changes...)
			continue
		}
		created, createErr := transaction.createSessionDraft(ctx, params, input, state)
		if createErr != nil {
			return EditDraftResult{}, createErr
		}
		change, createErr := transaction.createDraftChange(
			internalContext, params, edit.ID, nextRevision,
			"CreateSession", "Session", created.sessionID, created.payload,
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

func (transaction *CommandTx) validateDraftReferences(ctx context.Context, params EditDraftParams) error {
	locationIDs := make(map[int]struct{})
	laneIDs := make(map[int]struct{})
	trackIDs := make(map[int]struct{})
	for _, item := range params.Lanes {
		if item.Location.ID > 0 {
			locationIDs[item.Location.ID] = struct{}{}
		}
	}
	for _, item := range params.Sessions {
		collectDraftTargetIDs(laneIDs, item.Lanes)
		collectDraftTargetIDs(laneIDs, item.AddLanes)
		collectDraftTargetIDs(laneIDs, item.RemoveLanes)
		collectDraftTargetIDs(locationIDs, item.Locations)
		collectDraftTargetIDs(locationIDs, item.AddLocations)
		collectDraftTargetIDs(locationIDs, item.RemoveLocations)
		collectDraftTargetIDs(trackIDs, item.Tracks)
		collectDraftTargetIDs(trackIDs, item.AddTracks)
		collectDraftTargetIDs(trackIDs, item.RemoveTracks)
	}
	for id := range locationIDs {
		exists, err := transaction.transaction.Location.Query().
			Where(location.IDEQ(id), location.EventIDEQ(params.EventID)).
			Exist(ctx)
		if err != nil {
			return opaqueError("validate Draft Location reference", err)
		}
		if !exists {
			return ErrDraftReference
		}
	}
	for id := range laneIDs {
		exists, err := transaction.transaction.Lane.Query().
			Where(lane.IDEQ(id), lane.EventIDEQ(params.EventID)).
			Exist(ctx)
		if err != nil {
			return opaqueError("validate Draft Lane reference", err)
		}
		if !exists {
			return ErrDraftReference
		}
	}
	for id := range trackIDs {
		exists, err := transaction.transaction.Track.Query().
			Where(track.IDEQ(id), track.EventIDEQ(params.EventID)).
			Exist(ctx)
		if err != nil {
			return opaqueError("validate Draft Track reference", err)
		}
		if !exists {
			return ErrDraftReference
		}
	}
	return nil
}

type draftFactTarget struct {
	targetType string
	targetID   int
	factKey    string
}

func (transaction *CommandTx) draftOverlaps(ctx context.Context, params EditDraftParams, afterRevision int) ([]DraftChangeResult, error) {
	wanted := make(map[draftFactTarget]struct{})
	add := func(targetType string, targetID int, fields []string) {
		for _, field := range fields {
			wanted[draftFactTarget{targetType: targetType, targetID: targetID, factKey: field}] = struct{}{}
		}
	}
	for _, item := range params.Locations {
		add("Location", item.ID, item.UpdateFields)
	}
	for _, item := range params.Lanes {
		add("Lane", item.ID, item.UpdateFields)
	}
	for _, item := range params.Tracks {
		add("Track", item.ID, item.UpdateFields)
	}
	for _, item := range params.Sessions {
		for _, field := range item.UpdateFields {
			if !strings.HasPrefix(field, "add_") && !strings.HasPrefix(field, "remove_") {
				add("Session", item.ID, []string{field})
			}
		}
		addMembership := func(family string, targets ...[]DraftTarget) {
			for _, group := range targets {
				for _, target := range group {
					key := target.Ref
					if target.ID > 0 {
						key = strconv.Itoa(target.ID)
					}
					add("Session", item.ID, []string{family + ":" + key})
				}
			}
		}
		addMembership("lanes", item.AddLanes, item.RemoveLanes)
		addMembership("locations", item.AddLocations, item.RemoveLocations)
		addMembership("tracks", item.AddTracks, item.RemoveTracks)
	}
	if len(wanted) == 0 {
		return []DraftChangeResult{{Kind: "ConcurrentDraftEdit"}}, nil
	}
	changes, err := transaction.transaction.DraftChange.Query().
		Where(draftchange.EventIDEQ(params.EventID), draftchange.RevisionGT(afterRevision),
			draftchange.StatusIn(draftchange.StatusEffective, draftchange.StatusPublished)).
		Order(ent.Desc(draftchange.FieldRevision), ent.Desc(draftchange.FieldID)).
		All(systemContext(ctx))
	if err != nil {
		return nil, opaqueError("check overlapping Draft changes", err)
	}
	result := make([]DraftChangeResult, 0)
	seen := make(map[draftFactTarget]struct{})
	for _, change := range changes {
		for target := range wanted {
			if change.TargetType != target.targetType || change.TargetID != target.targetID ||
				(change.FactKey != target.factKey && change.FactKey != "entity") {
				continue
			}
			key := draftFactTarget{targetType: change.TargetType, targetID: change.TargetID, factKey: target.factKey}
			if _, exists := seen[key]; exists {
				break
			}
			found := draftChangeResult(change)
			var evidence struct {
				After json.RawMessage `json:"after"`
			}
			if json.Unmarshal([]byte(change.PayloadJSON), &evidence) == nil {
				found.CurrentValueJSON = string(evidence.After)
			}
			result = append(result, found)
			seen[key] = struct{}{}
			break
		}
	}
	return result, nil
}

func (transaction *CommandTx) recordFactChange(
	ctx context.Context,
	params EditDraftParams,
	editID, revision int,
	targetType string,
	targetID int,
	factKey string,
	before, after any,
) (*ent.DraftChange, error) {
	return transaction.recordNamedFactChange(ctx, params, editID, revision, "Update"+targetType, targetType, targetID, factKey, before, after)
}

func (transaction *CommandTx) recordNamedFactChange(
	ctx context.Context,
	params EditDraftParams,
	editID, revision int,
	kind, targetType string,
	targetID int,
	factKey string,
	before, after any,
) (*ent.DraftChange, error) {
	if err := transaction.draftFacts().validate(targetType, factKey); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(struct {
		Version int `json:"version"`
		Before  any `json:"before"`
		After   any `json:"after"`
	}{Version: 1, Before: before, After: after})
	if err != nil {
		return nil, opaqueError("encode Draft fact change", err)
	}
	if _, err = transaction.transaction.DraftChange.Update().
		Where(
			draftchange.EventIDEQ(params.EventID), draftchange.TargetTypeEQ(targetType),
			draftchange.TargetIDEQ(targetID), draftchange.FactKeyEQ(factKey),
			draftchange.StatusEQ(draftchange.StatusEffective),
		).
		SetStatus(draftchange.StatusSuperseded).
		Save(ctx); err != nil {
		return nil, opaqueError("supersede Draft fact change", err)
	}
	change, err := transaction.transaction.DraftChange.Create().
		SetEventID(params.EventID).SetDraftEditID(editID).SetRevision(revision).
		SetKind(kind).SetTargetType(targetType).SetTargetID(targetID).
		SetFactKey(factKey).SetPayloadJSON(string(encoded)).SetCreatedAt(params.Now).Save(ctx)
	if err != nil {
		return nil, opaqueError("record Draft fact change", err)
	}
	creation, queryErr := transaction.transaction.DraftChange.Query().Where(
		draftchange.EventIDEQ(params.EventID), draftchange.TargetTypeEQ(targetType), draftchange.TargetIDEQ(targetID),
		draftchange.FactKeyEQ("entity"), draftchange.StatusEQ(draftchange.StatusEffective),
	).Only(ctx)
	if queryErr == nil {
		if dependencyErr := transaction.createDraftDependency(ctx, change.ID, creation.ID); dependencyErr != nil {
			return nil, dependencyErr
		}
	} else if !ent.IsNotFound(queryErr) {
		return nil, opaqueError("load Draft entity creation dependency", queryErr)
	}
	return change, nil
}

func (transaction *CommandTx) updateLocationDraft(
	ctx, internalContext context.Context,
	params EditDraftParams,
	editID, revision int,
	input LocationDraftCreate,
) ([]DraftChangeResult, error) {
	identity, err := transaction.transaction.Location.Query().Where(location.IDEQ(input.ID), location.EventIDEQ(params.EventID)).Only(ctx)
	if err != nil {
		return nil, ErrDraftReference
	}
	state, err := identity.QueryDraft().Only(ctx)
	if err != nil {
		return nil, opaqueError("load Location Draft state", err)
	}
	if state.Name == input.Name {
		return nil, nil
	}
	change, err := transaction.recordFactChange(internalContext, params, editID, revision, "Location", input.ID, "name", state.Name, input.Name)
	if err != nil {
		return nil, err
	}
	if _, err = transaction.transaction.LocationDraft.UpdateOne(state).SetName(input.Name).Save(ctx); err != nil {
		return nil, opaqueError("update Location Draft state", err)
	}
	return []DraftChangeResult{draftChangeResult(change)}, nil
}

func (transaction *CommandTx) updateLaneDraft(
	ctx, internalContext context.Context,
	params EditDraftParams,
	editID, revision int,
	input LaneDraftCreate,
	creations draftCreationState,
) ([]DraftChangeResult, error) {
	identity, err := transaction.transaction.Lane.Query().Where(lane.IDEQ(input.ID), lane.EventIDEQ(params.EventID)).Only(ctx)
	if err != nil {
		return nil, ErrDraftReference
	}
	state, err := identity.QueryDraft().Only(ctx)
	if err != nil {
		return nil, opaqueError("load Lane Draft state", err)
	}
	results := make([]DraftChangeResult, 0, len(input.UpdateFields))
	update := transaction.transaction.LaneDraft.UpdateOne(state)
	for _, field := range input.UpdateFields {
		var before, after any
		dependencyID := 0
		switch field {
		case "name":
			before, after = state.Name, input.Name
			update.SetName(input.Name)
		case "location":
			locationID, resolvedDependencyID, resolveErr := resolveDraftTarget(input.Location, creations.locationIDs, creations.locationChanges)
			if resolveErr != nil {
				return nil, resolveErr
			}
			before, after, dependencyID = state.LocationID, locationID, resolvedDependencyID
			update.SetLocationID(locationID)
		}
		if before == after {
			continue
		}
		change, changeErr := transaction.recordFactChange(internalContext, params, editID, revision, "Lane", input.ID, field, before, after)
		if changeErr != nil {
			return nil, changeErr
		}
		if dependencyID > 0 {
			if dependencyErr := transaction.createDraftDependency(internalContext, change.ID, dependencyID); dependencyErr != nil {
				return nil, dependencyErr
			}
		}
		results = append(results, draftChangeResult(change))
	}
	if _, err = update.Save(ctx); err != nil {
		return nil, opaqueError("update Lane Draft state", err)
	}
	return results, nil
}

func (transaction *CommandTx) updateTrackDraft(
	ctx, internalContext context.Context,
	params EditDraftParams,
	editID, revision int,
	input TrackDraftCreate,
) ([]DraftChangeResult, error) {
	identity, err := transaction.transaction.Track.Query().Where(track.IDEQ(input.ID), track.EventIDEQ(params.EventID)).Only(ctx)
	if err != nil {
		return nil, ErrDraftReference
	}
	state, err := identity.QueryDraft().Only(ctx)
	if err != nil {
		return nil, opaqueError("load Track Draft state", err)
	}
	if state.Name == input.Name {
		return nil, nil
	}
	change, err := transaction.recordFactChange(internalContext, params, editID, revision, "Track", input.ID, "name", state.Name, input.Name)
	if err != nil {
		return nil, err
	}
	if _, err = transaction.transaction.TrackDraft.UpdateOne(state).SetName(input.Name).Save(ctx); err != nil {
		return nil, opaqueError("update Track Draft state", err)
	}
	return []DraftChangeResult{draftChangeResult(change)}, nil
}

func (transaction *CommandTx) updateSessionDraft(
	ctx, internalContext context.Context,
	params EditDraftParams,
	editID, revision int,
	input SessionDraftCreate,
	creations draftCreationState,
) ([]DraftChangeResult, error) {
	identity, err := transaction.transaction.Session.Query().Where(session.IDEQ(input.ID), session.EventIDEQ(params.EventID)).Only(ctx)
	if err != nil {
		return nil, ErrDraftReference
	}
	state, err := identity.QueryDraft().Only(ctx)
	if err != nil {
		return nil, opaqueError("load Session Draft state", err)
	}
	currentLaneIDs, err := state.QueryLanes().IDs(ctx)
	if err != nil {
		return nil, opaqueError("load Session Draft Lanes", err)
	}
	currentLocationIDs, err := state.QueryLocations().IDs(ctx)
	if err != nil {
		return nil, opaqueError("load Session Draft Locations", err)
	}
	currentTrackIDs, err := state.QueryTracks().IDs(ctx)
	if err != nil {
		return nil, opaqueError("load Session Draft Tracks", err)
	}
	results := make([]DraftChangeResult, 0, len(input.UpdateFields))
	update := transaction.transaction.SessionDraft.UpdateOne(state)
	for _, field := range input.UpdateFields {
		if strings.HasPrefix(field, "add_") || strings.HasPrefix(field, "remove_") {
			continue
		}
		var before, after any
		switch field {
		case "title":
			before, after = state.Title, input.Title
			update.SetTitle(input.Title)
		case "type":
			before, after = string(state.Type), input.Type
			update.SetType(sessiondraft.Type(input.Type))
		case "audience_visibility":
			before, after = string(state.AudienceVisibility), input.AudienceVisibility
			update.SetAudienceVisibility(sessiondraft.AudienceVisibility(input.AudienceVisibility))
		case "public_details":
			before, after = state.PublicDetails, input.PublicDetails
			update.SetPublicDetails(input.PublicDetails)
		case "crew_notes":
			before, after = state.CrewNotes, input.CrewNotes
			update.SetCrewNotes(input.CrewNotes)
		case "planned_start":
			before, after = state.PlannedStart, input.PlannedStart
			update.SetPlannedStart(input.PlannedStart)
		case "planned_end":
			before, after = state.PlannedEnd, input.PlannedEnd
			update.SetPlannedEnd(input.PlannedEnd)
		case "timing_policy":
			before, after = string(state.TimingPolicy), input.TimingPolicy
			update.SetTimingPolicy(sessiondraft.TimingPolicy(input.TimingPolicy))
		case "minimum_duration":
			before, after = state.MinimumDurationSeconds, input.MinimumDurationSeconds
			update.SetMinimumDurationSeconds(input.MinimumDurationSeconds)
		case "start_boundary":
			before, after = string(state.StartBoundary), input.StartBoundary
			update.SetStartBoundary(sessiondraft.StartBoundary(input.StartBoundary))
		case "end_boundary":
			before, after = string(state.EndBoundary), input.EndBoundary
			update.SetEndBoundary(sessiondraft.EndBoundary(input.EndBoundary))
		default:
			return nil, ErrDraftReference
		}
		if reflect.DeepEqual(before, after) {
			continue
		}
		change, changeErr := transaction.recordFactChange(internalContext, params, editID, revision, "Session", input.ID, field, before, after)
		if changeErr != nil {
			return nil, changeErr
		}
		results = append(results, draftChangeResult(change))
	}
	membershipResults, membershipErr := transaction.updateSessionMemberships(
		internalContext, params, editID, revision, input, creations, update,
		currentLaneIDs, currentLocationIDs, currentTrackIDs,
	)
	if membershipErr != nil {
		return nil, membershipErr
	}
	results = append(results, membershipResults...)
	if _, err = update.Save(ctx); err != nil {
		return nil, opaqueError("update Session Draft state", err)
	}
	return results, nil
}

func containsString(values []string, wanted string) bool {
	return slices.Contains(values, wanted)
}

func (transaction *CommandTx) updateSessionMemberships(
	ctx context.Context,
	params EditDraftParams,
	editID, revision int,
	input SessionDraftCreate,
	creations draftCreationState,
	update *ent.SessionDraftUpdateOne,
	currentLaneIDs, currentLocationIDs, currentTrackIDs []int,
) ([]DraftChangeResult, error) {
	type operation struct {
		field, family          string
		targets                []DraftTarget
		current                []int
		localIDs, localChanges map[string]int
		adding                 bool
	}
	operations := []operation{
		{"add_lanes", "lanes", input.AddLanes, currentLaneIDs, creations.laneIDs, creations.laneChanges, true},
		{"remove_lanes", "lanes", input.RemoveLanes, currentLaneIDs, creations.laneIDs, creations.laneChanges, false},
		{"add_locations", "locations", input.AddLocations, currentLocationIDs, creations.locationIDs, creations.locationChanges, true},
		{"remove_locations", "locations", input.RemoveLocations, currentLocationIDs, creations.locationIDs, creations.locationChanges, false},
		{"add_tracks", "tracks", input.AddTracks, currentTrackIDs, creations.trackIDs, creations.trackChanges, true},
		{"remove_tracks", "tracks", input.RemoveTracks, currentTrackIDs, creations.trackIDs, creations.trackChanges, false},
	}
	results := make([]DraftChangeResult, 0)
	laneCount := len(currentLaneIDs)
	for _, operation := range operations {
		if !containsString(input.UpdateFields, operation.field) {
			continue
		}
		if len(operation.targets) == 0 {
			return nil, ErrDraftReference
		}
		for _, target := range operation.targets {
			id, dependencyID, err := resolveDraftTarget(target, operation.localIDs, operation.localChanges)
			if err != nil {
				return nil, err
			}
			present := slices.Contains(operation.current, id)
			if present == operation.adding {
				continue
			}
			switch operation.family {
			case "lanes":
				if operation.adding {
					update.AddLaneIDs(id)
					laneCount++
				} else {
					update.RemoveLaneIDs(id)
					laneCount--
				}
			case "locations":
				if operation.adding {
					update.AddLocationIDs(id)
				} else {
					update.RemoveLocationIDs(id)
				}
			default:
				if operation.adding {
					update.AddTrackIDs(id)
				} else {
					update.RemoveTrackIDs(id)
				}
			}
			change, err := transaction.recordFactChange(
				ctx, params, editID, revision, "Session", input.ID,
				operation.family+":"+strconv.Itoa(id), present, operation.adding,
			)
			if err != nil {
				return nil, err
			}
			if dependencyID > 0 {
				if dependencyErr := transaction.createDraftDependency(ctx, change.ID, dependencyID); dependencyErr != nil {
					return nil, dependencyErr
				}
			}
			results = append(results, draftChangeResult(change))
		}
	}
	if laneCount == 0 {
		return nil, ErrDraftReference
	}
	return results, nil
}

func collectDraftTargetIDs(ids map[int]struct{}, targets []DraftTarget) {
	for _, target := range targets {
		if target.ID > 0 {
			ids[target.ID] = struct{}{}
		}
	}
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
	payload       SessionDraftCreate
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
	payload := input
	payload.Lanes = draftIDs(laneIDs)
	payload.Locations = draftIDs(locationIDs)
	payload.Tracks = draftIDs(trackIDs)
	return createdSessionDraft{sessionID: created.ID, dependencyIDs: uniqueInts(dependencies), payload: payload}, nil
}

func draftIDs(ids []int) []DraftTarget {
	result := make([]DraftTarget, 0, len(ids))
	for _, id := range ids {
		result = append(result, DraftTarget{ID: id})
	}
	return result
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
	if err := transaction.draftFacts().validate(targetType, draftFactEntity); err != nil {
		return nil, err
	}
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
		SetFactKey(draftFactEntity).
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
		FactKey: change.FactKey, Status: change.Status.String(),
	}
}
