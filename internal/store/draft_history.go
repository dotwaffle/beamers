package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/draftchange"
	"github.com/dotwaffle/beamers/ent/lane"
	"github.com/dotwaffle/beamers/ent/location"
	"github.com/dotwaffle/beamers/ent/rundown"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessiondraft"
	"github.com/dotwaffle/beamers/ent/track"
)

// DraftHistoryParams identifies one revision-bound history operation.
type DraftHistoryParams struct {
	EventID, ActorAccountID, ExpectedDraftRevision int
	ChangeIDs                                      []int
	Now                                            time.Time
}

// DiscardDraftChanges restores effective facts to their recorded prior values.
func (transaction *CommandTx) DiscardDraftChanges(ctx context.Context, params DraftHistoryParams) (EditDraftResult, error) {
	return transaction.changeDraftHistory(ctx, params, false)
}

// RevertDraftChange appends effective inverse facts from one historical change.
func (transaction *CommandTx) RevertDraftChange(ctx context.Context, params DraftHistoryParams) (EditDraftResult, error) {
	return transaction.changeDraftHistory(ctx, params, true)
}

func (transaction *CommandTx) changeDraftHistory(ctx context.Context, params DraftHistoryParams, revert bool) (EditDraftResult, error) {
	current, err := transaction.transaction.Rundown.Query().Where(
		rundown.EventIDEQ(params.EventID), rundown.DraftRevisionEQ(params.ExpectedDraftRevision),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return EditDraftResult{}, ErrDraftRevisionConflict
	}
	if err != nil {
		return EditDraftResult{}, opaqueError("load Rundown history revision", err)
	}
	internalContext := systemContext(ctx)
	query := transaction.transaction.DraftChange.Query().Where(
		draftchange.EventIDEQ(params.EventID), draftchange.IDIn(params.ChangeIDs...),
	)
	if !revert {
		query = query.Where(draftchange.StatusEQ(draftchange.StatusEffective))
	}
	changes, err := query.Order(ent.Asc(draftchange.FieldID)).All(internalContext)
	if err != nil {
		return EditDraftResult{}, opaqueError("load Draft history changes", err)
	}
	if len(changes) != len(params.ChangeIDs) {
		return EditDraftResult{}, ErrDraftRevisionConflict
	}
	nextRevision := current.DraftRevision + 1
	updated, err := transaction.transaction.Rundown.UpdateOneID(current.ID).
		Where(rundown.DraftRevisionEQ(current.DraftRevision)).SetDraftRevision(nextRevision).Save(ctx)
	if ent.IsNotFound(err) {
		return EditDraftResult{}, ErrDraftRevisionConflict
	}
	if err != nil {
		return EditDraftResult{}, opaqueError("advance Draft history revision", err)
	}
	_ = updated
	edit, err := transaction.transaction.DraftEdit.Create().SetEventID(params.EventID).
		SetActorAccountID(params.ActorAccountID).SetRevision(nextRevision).SetCreatedAt(params.Now).Save(internalContext)
	if err != nil {
		return EditDraftResult{}, opaqueError("record Draft history edit", err)
	}
	result := EditDraftResult{DraftRevision: nextRevision}
	for _, change := range changes {
		var evidence struct {
			Before json.RawMessage `json:"before"`
			After  json.RawMessage `json:"after"`
		}
		if err = json.Unmarshal([]byte(change.PayloadJSON), &evidence); err != nil || len(evidence.Before) == 0 || string(evidence.Before) == "null" {
			return EditDraftResult{}, errors.New("draft change has no reversible fact evidence")
		}
		currentFact, currentErr := transaction.currentDraftFact(internalContext, change)
		if currentErr != nil {
			return EditDraftResult{}, currentErr
		}
		if applyErr := transaction.draftFacts().apply(ctx, change.TargetType, change.TargetID, change.FactKey, evidence.Before); applyErr != nil {
			return EditDraftResult{}, applyErr
		}
		if !revert {
			changed, updateErr := transaction.transaction.DraftChange.UpdateOne(change).SetStatus(draftchange.StatusDiscarded).Save(internalContext)
			if updateErr != nil {
				return EditDraftResult{}, opaqueError("mark Draft change Discarded", updateErr)
			}
			if reactivateErr := transaction.reactivateSupersededDraftFact(internalContext, change); reactivateErr != nil {
				return EditDraftResult{}, reactivateErr
			}
			result.Changes = append(result.Changes, draftChangeResult(changed))
			continue
		}
		inverse, inverseErr := transaction.recordNamedFactChange(internalContext, EditDraftParams{
			EventID: params.EventID, ActorAccountID: params.ActorAccountID, Now: params.Now,
		}, edit.ID, nextRevision, "Revert"+change.TargetType, change.TargetType, change.TargetID, change.FactKey,
			currentFact, evidence.Before)
		if inverseErr != nil {
			return EditDraftResult{}, inverseErr
		}
		if dependencyErr := transaction.restoreMembershipDependency(internalContext, inverse, evidence.Before); dependencyErr != nil {
			return EditDraftResult{}, dependencyErr
		}
		baseline, baselineErr := transaction.publishedDraftFact(internalContext, change)
		if baselineErr != nil {
			return EditDraftResult{}, baselineErr
		}
		if len(baseline) > 0 && bytes.Equal(baseline, evidence.Before) {
			inverse, inverseErr = transaction.transaction.DraftChange.UpdateOne(inverse).
				SetStatus(draftchange.StatusReverted).Save(internalContext)
			if inverseErr != nil {
				return EditDraftResult{}, opaqueError("mark baseline Draft Revert", inverseErr)
			}
		}
		result.Changes = append(result.Changes, draftChangeResult(inverse))
	}
	return result, nil
}

func (transaction *CommandTx) restoreMembershipDependency(ctx context.Context, inverse *ent.DraftChange, restored json.RawMessage) error {
	family, encodedID, membership := strings.Cut(inverse.FactKey, ":")
	if !membership {
		return nil
	}
	var present bool
	if err := json.Unmarshal(restored, &present); err != nil {
		return errors.New("decode restored Draft membership")
	}
	if !present {
		return nil
	}
	targetType := map[string]string{"lanes": "Lane", "locations": "Location", "tracks": "Track"}[family]
	if targetType == "" {
		return errors.New("unsupported restored Draft membership")
	}
	targetID, err := strconv.Atoi(encodedID)
	if err != nil {
		return errors.New("decode restored Draft membership target")
	}
	creation, err := transaction.transaction.DraftChange.Query().Where(
		draftchange.EventIDEQ(inverse.EventID), draftchange.TargetTypeEQ(targetType), draftchange.TargetIDEQ(targetID),
		draftchange.FactKeyEQ("entity"), draftchange.StatusEQ(draftchange.StatusEffective),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return opaqueError("load restored membership dependency", err)
	}
	return transaction.createDraftDependency(ctx, inverse.ID, creation.ID)
}

func (transaction *CommandTx) reactivateSupersededDraftFact(ctx context.Context, discarded *ent.DraftChange) error {
	prior, err := transaction.transaction.DraftChange.Query().Where(
		draftchange.EventIDEQ(discarded.EventID), draftchange.TargetTypeEQ(discarded.TargetType),
		draftchange.TargetIDEQ(discarded.TargetID), draftchange.FactKeyEQ(discarded.FactKey),
		draftchange.StatusEQ(draftchange.StatusSuperseded), draftchange.RevisionLT(discarded.Revision),
	).Order(ent.Desc(draftchange.FieldRevision), ent.Desc(draftchange.FieldID)).First(ctx)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return opaqueError("load superseded Draft fact", err)
	}
	var evidence struct {
		After json.RawMessage `json:"after"`
	}
	if err = json.Unmarshal([]byte(prior.PayloadJSON), &evidence); err != nil {
		return errors.New("decode superseded Draft fact evidence")
	}
	baseline, err := transaction.publishedDraftFact(ctx, discarded)
	if err != nil {
		return err
	}
	if len(baseline) > 0 && bytes.Equal(baseline, evidence.After) {
		return nil
	}
	if _, err = transaction.transaction.DraftChange.UpdateOne(prior).SetStatus(draftchange.StatusEffective).Save(ctx); err != nil {
		return opaqueError("reactivate superseded Draft fact", err)
	}
	return nil
}

func (transaction *CommandTx) publishedDraftFact(ctx context.Context, selected *ent.DraftChange) (json.RawMessage, error) {
	if selected.TargetType == draftTargetSession {
		identity, identityErr := transaction.transaction.Session.Get(systemContext(ctx), selected.TargetID)
		if identityErr != nil && !ent.IsNotFound(identityErr) {
			return nil, opaqueError("load corrected Session Draft baseline", identityErr)
		}
		if identityErr == nil {
			var corrected *string
			switch selected.FactKey {
			case draftFactTitle:
				corrected = identity.CorrectedTitle
			case draftFactSpeaker:
				corrected = identity.CorrectedSpeaker
			case draftFactPublicDetails:
				corrected = identity.CorrectedPublicDetails
			}
			if corrected != nil {
				encoded, encodeErr := json.Marshal(*corrected)
				if encodeErr != nil {
					return nil, errors.New("encode corrected Session Draft baseline")
				}
				return encoded, nil
			}
		}
	}
	published, err := transaction.transaction.DraftChange.Query().Where(
		draftchange.EventIDEQ(selected.EventID), draftchange.TargetTypeEQ(selected.TargetType),
		draftchange.TargetIDEQ(selected.TargetID), draftchange.FactKeyEQ(selected.FactKey),
		draftchange.StatusEQ(draftchange.StatusPublished),
	).Order(ent.Desc(draftchange.FieldPublishedRevision), ent.Desc(draftchange.FieldID)).First(ctx)
	if ent.IsNotFound(err) {
		creation, creationErr := transaction.transaction.DraftChange.Query().Where(
			draftchange.EventIDEQ(selected.EventID), draftchange.TargetTypeEQ(selected.TargetType),
			draftchange.TargetIDEQ(selected.TargetID), draftchange.FactKeyEQ("entity"),
			draftchange.StatusEQ(draftchange.StatusPublished),
		).First(ctx)
		if ent.IsNotFound(creationErr) {
			return nil, nil
		}
		if creationErr != nil {
			return nil, opaqueError("load Published creation evidence", creationErr)
		}
		return transaction.draftFacts().creationValue(creation, selected.FactKey)
	}
	if err != nil {
		return nil, opaqueError("load Published Draft fact evidence", err)
	}
	var evidence struct {
		After json.RawMessage `json:"after"`
	}
	if err = json.Unmarshal([]byte(published.PayloadJSON), &evidence); err != nil {
		return nil, errors.New("decode Published Draft fact evidence")
	}
	return evidence.After, nil
}

func (facts rundownDraftFacts) creationValue(creation *ent.DraftChange, factKey string) (json.RawMessage, error) {
	if err := facts.validate(creation.TargetType, factKey); err != nil {
		return nil, err
	}
	var value any
	switch creation.TargetType {
	case "Location":
		var input LocationDraftCreate
		if err := changeAfter(creation, &input); err != nil {
			return nil, err
		}
		if factKey == "name" {
			value = input.Name
		}
	case "Lane":
		var input LaneDraftCreate
		if err := changeAfter(creation, &input); err != nil {
			return nil, err
		}
		switch factKey {
		case "name":
			value = input.Name
		case "location":
			value = input.Location.ID
		}
	case "Track":
		var input TrackDraftCreate
		if err := changeAfter(creation, &input); err != nil {
			return nil, err
		}
		if factKey == "name" {
			value = input.Name
		}
	case "Session":
		var input SessionDraftCreate
		if err := changeAfter(creation, &input); err != nil {
			return nil, err
		}
		family, encodedID, membership := strings.Cut(factKey, ":")
		if membership {
			id, parseErr := strconv.Atoi(encodedID)
			if parseErr != nil {
				return nil, errors.New("decode Published membership fact key")
			}
			switch family {
			case "lanes":
				value = slices.Contains(targetIDs(input.Lanes), id)
			case "locations":
				value = slices.Contains(targetIDs(input.Locations), id)
			case "tracks":
				value = slices.Contains(targetIDs(input.Tracks), id)
			}
		}
		if !membership {
			switch factKey {
			case "title":
				value = input.Title
			case "speaker":
				value = input.Speaker
			case "type":
				value = input.Type
			case "audience_visibility":
				value = input.AudienceVisibility
			case "public_details":
				value = input.PublicDetails
			case "crew_notes":
				value = input.CrewNotes
			case "planned_start":
				value = input.PlannedStart
			case "planned_end":
				value = input.PlannedEnd
			case "timing_policy":
				value = input.TimingPolicy
			case "minimum_duration":
				value = input.MinimumDurationSeconds
			case "start_boundary":
				value = input.StartBoundary
			case "end_boundary":
				value = input.EndBoundary
			case "lanes":
				value = targetIDs(input.Lanes)
			case "locations":
				value = targetIDs(input.Locations)
			case "tracks":
				value = targetIDs(input.Tracks)
			}
		}
	}
	if value == nil {
		return nil, errors.New("unsupported Published creation fact")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("encode Published creation fact")
	}
	return encoded, nil
}

func (transaction *CommandTx) currentDraftFact(ctx context.Context, selected *ent.DraftChange) (json.RawMessage, error) {
	current, err := transaction.transaction.DraftChange.Query().Where(
		draftchange.EventIDEQ(selected.EventID), draftchange.TargetTypeEQ(selected.TargetType),
		draftchange.TargetIDEQ(selected.TargetID), draftchange.FactKeyEQ(selected.FactKey),
		draftchange.StatusIn(draftchange.StatusEffective, draftchange.StatusPublished),
	).Order(ent.Desc(draftchange.FieldRevision), ent.Desc(draftchange.FieldID)).First(ctx)
	if ent.IsNotFound(err) {
		return nil, errors.New("draft fact has no current evidence")
	}
	if err != nil {
		return nil, opaqueError("load current Draft fact evidence", err)
	}
	var evidence struct {
		After json.RawMessage `json:"after"`
	}
	if err = json.Unmarshal([]byte(current.PayloadJSON), &evidence); err != nil {
		return nil, errors.New("decode current Draft fact evidence")
	}
	return evidence.After, nil
}

func (facts rundownDraftFacts) apply(ctx context.Context, targetType string, targetID int, factKey string, encoded json.RawMessage) error {
	if err := facts.validate(targetType, factKey); err != nil {
		return err
	}
	transaction := facts.transaction
	switch targetType {
	case "Location":
		identity, err := transaction.transaction.Location.Query().Where(location.IDEQ(targetID)).Only(ctx)
		if err != nil {
			return ErrDraftReference
		}
		state, err := identity.QueryDraft().Only(ctx)
		if err != nil {
			return opaqueError("load Location Draft for history", err)
		}
		var name string
		if decodeErr := json.Unmarshal(encoded, &name); decodeErr != nil {
			return opaqueError("decode Location Draft history fact", decodeErr)
		}
		_, err = transaction.transaction.LocationDraft.UpdateOne(state).SetName(name).Save(ctx)
		if err != nil {
			return opaqueError("apply Location Draft history fact", err)
		}
		return nil
	case "Track":
		identity, err := transaction.transaction.Track.Query().Where(track.IDEQ(targetID)).Only(ctx)
		if err != nil {
			return ErrDraftReference
		}
		state, err := identity.QueryDraft().Only(ctx)
		if err != nil {
			return opaqueError("load Track Draft for history", err)
		}
		var name string
		if decodeErr := json.Unmarshal(encoded, &name); decodeErr != nil {
			return opaqueError("decode Track Draft history fact", decodeErr)
		}
		_, err = transaction.transaction.TrackDraft.UpdateOne(state).SetName(name).Save(ctx)
		if err != nil {
			return opaqueError("apply Track Draft history fact", err)
		}
		return nil
	case "Lane":
		return transaction.applyLaneDraftFact(ctx, targetID, factKey, encoded)
	case "Session":
		return transaction.applySessionDraftFact(ctx, targetID, factKey, encoded)
	default:
		return errors.New("unsupported reversible Draft target")
	}
}

func (transaction *CommandTx) applyLaneDraftFact(ctx context.Context, targetID int, factKey string, encoded json.RawMessage) error {
	identity, err := transaction.transaction.Lane.Query().Where(lane.IDEQ(targetID)).Only(ctx)
	if err != nil {
		return ErrDraftReference
	}
	state, err := identity.QueryDraft().Only(ctx)
	if err != nil {
		return opaqueError("load Lane Draft for history", err)
	}
	update := transaction.transaction.LaneDraft.UpdateOne(state)
	switch factKey {
	case "name":
		var value string
		err = json.Unmarshal(encoded, &value)
		update.SetName(value)
	case "location":
		var value int
		err = json.Unmarshal(encoded, &value)
		update.SetLocationID(value)
	default:
		return errors.New("unsupported reversible Lane fact")
	}
	if err != nil {
		return opaqueError("decode Lane Draft history fact", err)
	}
	_, err = update.Save(ctx)
	if err != nil {
		return opaqueError("apply Lane Draft history fact", err)
	}
	return nil
}

func (transaction *CommandTx) applySessionDraftFact(ctx context.Context, targetID int, factKey string, encoded json.RawMessage) error {
	identity, err := transaction.transaction.Session.Query().Where(session.IDEQ(targetID)).Only(ctx)
	if err != nil {
		return ErrDraftReference
	}
	state, err := identity.QueryDraft().Only(ctx)
	if err != nil {
		return opaqueError("load Session Draft for history", err)
	}
	update := transaction.transaction.SessionDraft.UpdateOne(state)
	family, encodedID, membership := strings.Cut(factKey, ":")
	if membership {
		id, parseErr := strconv.Atoi(encodedID)
		if parseErr != nil {
			return errors.New("decode Draft membership fact key")
		}
		var present bool
		if decodeErr := json.Unmarshal(encoded, &present); decodeErr != nil {
			return opaqueError("decode Session membership history fact", decodeErr)
		}
		switch family {
		case "lanes":
			if present {
				update.AddLaneIDs(id)
			} else {
				update.RemoveLaneIDs(id)
			}
		case "locations":
			if present {
				update.AddLocationIDs(id)
			} else {
				update.RemoveLocationIDs(id)
			}
		case "tracks":
			if present {
				update.AddTrackIDs(id)
			} else {
				update.RemoveTrackIDs(id)
			}
		default:
			return errors.New("unsupported reversible Session membership fact")
		}
		_, err = update.Save(ctx)
		if err != nil {
			return opaqueError("apply Session membership history fact", err)
		}
		return nil
	}
	switch factKey {
	case "title":
		var value string
		err = json.Unmarshal(encoded, &value)
		update.SetTitle(value)
	case "speaker":
		var value string
		err = json.Unmarshal(encoded, &value)
		update.SetSpeaker(value)
	case "type":
		var value string
		err = json.Unmarshal(encoded, &value)
		update.SetType(sessiondraft.Type(value))
	case "audience_visibility":
		var value string
		err = json.Unmarshal(encoded, &value)
		update.SetAudienceVisibility(sessiondraft.AudienceVisibility(value))
	case "public_details":
		var value string
		err = json.Unmarshal(encoded, &value)
		update.SetPublicDetails(value)
	case "crew_notes":
		var value string
		err = json.Unmarshal(encoded, &value)
		update.SetCrewNotes(value)
	case "planned_start":
		var value time.Time
		err = json.Unmarshal(encoded, &value)
		update.SetPlannedStart(value)
	case "planned_end":
		var value time.Time
		err = json.Unmarshal(encoded, &value)
		update.SetPlannedEnd(value)
	case "timing_policy":
		var value string
		err = json.Unmarshal(encoded, &value)
		update.SetTimingPolicy(sessiondraft.TimingPolicy(value))
	case "minimum_duration":
		var value int
		err = json.Unmarshal(encoded, &value)
		update.SetMinimumDurationSeconds(value)
	case "start_boundary":
		var value string
		err = json.Unmarshal(encoded, &value)
		update.SetStartBoundary(sessiondraft.StartBoundary(value))
	case "end_boundary":
		var value string
		err = json.Unmarshal(encoded, &value)
		update.SetEndBoundary(sessiondraft.EndBoundary(value))
	case "submission_deadline":
		var value time.Time
		err = json.Unmarshal(encoded, &value)
		update.SetSubmissionDeadline(value)
	case "entry_default_disposition":
		var value string
		err = json.Unmarshal(encoded, &value)
		update.SetEntryDefaultDisposition(sessiondraft.EntryDefaultDisposition(value))
	case "lanes":
		var value []int
		err = json.Unmarshal(encoded, &value)
		update.ClearLanes().AddLaneIDs(value...)
	case "locations":
		var value []int
		err = json.Unmarshal(encoded, &value)
		update.ClearLocations().AddLocationIDs(value...)
	case "tracks":
		var value []int
		err = json.Unmarshal(encoded, &value)
		update.ClearTracks().AddTrackIDs(value...)
	default:
		return errors.New("unsupported reversible Session fact")
	}
	if err != nil {
		return opaqueError("decode Session Draft history fact", err)
	}
	_, err = update.Save(ctx)
	if err != nil {
		return opaqueError("apply Session Draft history fact", err)
	}
	return nil
}
