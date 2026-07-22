package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/draftchange"
	"github.com/dotwaffle/beamers/ent/draftchangedependency"
	"github.com/dotwaffle/beamers/ent/lane"
	"github.com/dotwaffle/beamers/ent/lanepublishedversion"
	"github.com/dotwaffle/beamers/ent/location"
	"github.com/dotwaffle/beamers/ent/locationpublishedversion"
	"github.com/dotwaffle/beamers/ent/rundown"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
	"github.com/dotwaffle/beamers/ent/track"
	"github.com/dotwaffle/beamers/ent/trackpublishedversion"
)

// PendingDraftChange is the persistence projection used to form a Publish Preview.
type PendingDraftChange struct {
	ID                int
	Kind              string
	TargetType        string
	TargetID          int
	PayloadJSON       string
	Status            string
	PublishedRevision int
	Dependencies      []int
}

// PublishState is the exact revisioned input to Preview and Publish.
type PublishState struct {
	DraftRevision     int
	PublishedRevision int
	Changes           []PendingDraftChange
}

// LoadPublishState returns all Draft Change evidence needed for dependency closure.
func (installation *SQLite) LoadPublishState(ctx context.Context, eventID int) (PublishState, error) {
	return loadPublishState(
		ctx,
		installation.client.Rundown,
		installation.client.DraftChange,
		installation.client.DraftChangeDependency,
		eventID,
	)
}

// LoadPublishState returns transaction-consistent Preview inputs.
func (transaction *CommandTx) LoadPublishState(ctx context.Context, eventID int) (PublishState, error) {
	return loadPublishState(
		ctx,
		transaction.transaction.Rundown,
		transaction.transaction.DraftChange,
		transaction.transaction.DraftChangeDependency,
		eventID,
	)
}

func loadPublishState(
	ctx context.Context,
	rundowns *ent.RundownClient,
	changes *ent.DraftChangeClient,
	dependencies *ent.DraftChangeDependencyClient,
	eventID int,
) (PublishState, error) {
	found, err := rundowns.Query().Where(rundown.EventIDEQ(eventID)).Only(ctx)
	if ent.IsNotFound(err) {
		return PublishState{}, ErrEventNotFound
	}
	if err != nil {
		return PublishState{}, opaqueError("load Rundown revisions", err)
	}
	internalContext := systemContext(ctx)
	storedChanges, err := changes.Query().
		Where(draftchange.EventIDEQ(eventID)).
		Order(ent.Asc(draftchange.FieldID)).
		All(internalContext)
	if err != nil {
		return PublishState{}, opaqueError("load Draft Changes", err)
	}
	ids := make([]int, 0, len(storedChanges))
	for _, change := range storedChanges {
		ids = append(ids, change.ID)
	}
	byChange := make(map[int][]int, len(ids))
	if len(ids) > 0 {
		storedDependencies, queryErr := dependencies.Query().
			Where(draftchangedependency.ChangeIDIn(ids...)).
			All(internalContext)
		if queryErr != nil {
			return PublishState{}, opaqueError("load Draft Change dependencies", queryErr)
		}
		for _, dependency := range storedDependencies {
			byChange[dependency.ChangeID] = append(byChange[dependency.ChangeID], dependency.DependsOnID)
		}
	}
	state := PublishState{
		DraftRevision: found.DraftRevision, PublishedRevision: found.PublishedRevision,
		Changes: make([]PendingDraftChange, 0, len(storedChanges)),
	}
	for _, change := range storedChanges {
		publishedRevision := 0
		if change.PublishedRevision != nil {
			publishedRevision = *change.PublishedRevision
		}
		state.Changes = append(state.Changes, PendingDraftChange{
			ID: change.ID, Kind: change.Kind, TargetType: change.TargetType,
			TargetID: change.TargetID, PayloadJSON: change.PayloadJSON,
			Status: change.Status.String(), PublishedRevision: publishedRevision,
			Dependencies: byChange[change.ID],
		})
	}
	return state, nil
}

// PublishParams contains one exact confirmed Publish transaction.
type PublishParams struct {
	EventID                   int
	ExpectedDraftRevision     int
	ExpectedPublishedRevision int
	ChangeIDs                 []int
	Now                       time.Time
}

// PublishResult is the minimal durable outcome of Publish.
type PublishResult struct {
	DraftRevision     int   `json:"draft_revision"`
	PublishedRevision int   `json:"published_revision"`
	ChangeIDs         []int `json:"change_ids"`
}

// Publish creates immutable versions for the exact confirmed effective changes.
func (transaction *CommandTx) Publish(ctx context.Context, params PublishParams) (PublishResult, error) {
	current, err := transaction.transaction.Rundown.Query().
		Where(
			rundown.EventIDEQ(params.EventID),
			rundown.DraftRevisionEQ(params.ExpectedDraftRevision),
			rundown.PublishedRevisionEQ(params.ExpectedPublishedRevision),
		).
		Only(ctx)
	if ent.IsNotFound(err) {
		return PublishResult{}, ErrDraftRevisionConflict
	}
	if err != nil {
		return PublishResult{}, opaqueError("load confirmed Rundown revisions", err)
	}
	internalContext := systemContext(ctx)
	changes, err := transaction.transaction.DraftChange.Query().
		Where(
			draftchange.EventIDEQ(params.EventID),
			draftchange.IDIn(params.ChangeIDs...),
			draftchange.StatusEQ(draftchange.StatusEffective),
		).
		All(internalContext)
	if err != nil {
		return PublishResult{}, opaqueError("load confirmed Draft Changes", err)
	}
	if len(changes) != len(params.ChangeIDs) {
		return PublishResult{}, ErrDraftRevisionConflict
	}
	nextPublishedRevision := current.PublishedRevision + 1
	sort.Slice(changes, func(first, second int) bool {
		return publishKindOrder(changes[first].Kind) < publishKindOrder(changes[second].Kind)
	})
	if publishErr := transaction.publishChanges(ctx, changes, nextPublishedRevision, params.Now); publishErr != nil {
		return PublishResult{}, publishErr
	}
	if _, updateErr := transaction.transaction.DraftChange.Update().
		Where(draftchange.IDIn(params.ChangeIDs...)).
		SetStatus(draftchange.StatusPublished).
		SetPublishedRevision(nextPublishedRevision).
		Save(internalContext); updateErr != nil {
		return PublishResult{}, opaqueError("mark Draft Changes Published", updateErr)
	}
	updated, err := transaction.transaction.Rundown.UpdateOneID(current.ID).
		Where(
			rundown.DraftRevisionEQ(params.ExpectedDraftRevision),
			rundown.PublishedRevisionEQ(params.ExpectedPublishedRevision),
		).
		AddDraftRevision(1).
		AddPublishedRevision(1).
		Save(ctx)
	if ent.IsNotFound(err) {
		return PublishResult{}, ErrDraftRevisionConflict
	}
	if err != nil {
		return PublishResult{}, opaqueError("advance Published revisions", err)
	}
	changeIDs := append([]int(nil), params.ChangeIDs...)
	sort.Ints(changeIDs)
	return PublishResult{
		DraftRevision: updated.DraftRevision, PublishedRevision: updated.PublishedRevision,
		ChangeIDs: changeIDs,
	}, nil
}

func (transaction *CommandTx) publishChanges(
	ctx context.Context,
	changes []*ent.DraftChange,
	publishedRevision int,
	now time.Time,
) error {
	updates := make(map[draftFactTarget][]*ent.DraftChange)
	for _, change := range changes {
		if strings.HasPrefix(change.Kind, "Update") || strings.HasPrefix(change.Kind, "Revert") {
			key := draftFactTarget{targetType: change.TargetType, targetID: change.TargetID}
			updates[key] = append(updates[key], change)
		}
	}
	for _, change := range changes {
		if strings.HasPrefix(change.Kind, "Update") || strings.HasPrefix(change.Kind, "Revert") {
			continue
		}
		key := draftFactTarget{targetType: change.TargetType, targetID: change.TargetID}
		if strings.HasPrefix(change.Kind, "Create") && len(updates[key]) > 0 {
			if err := transaction.draftFacts().publishCreated(ctx, change, updates[key], publishedRevision, now); err != nil {
				return err
			}
			delete(updates, key)
			continue
		}
		if err := transaction.publishChange(ctx, change, publishedRevision, now); err != nil {
			return err
		}
	}
	for target, facts := range updates {
		if err := transaction.draftFacts().publishUpdates(ctx, target, facts, publishedRevision, now); err != nil {
			return err
		}
	}
	return nil
}

func (facts rundownDraftFacts) publishCreated(ctx context.Context, creation *ent.DraftChange, changes []*ent.DraftChange, revision int, now time.Time) error {
	if err := facts.validate(creation.TargetType, draftFactEntity); err != nil {
		return err
	}
	for _, change := range changes {
		if err := facts.validate(change.TargetType, change.FactKey); err != nil {
			return err
		}
	}
	transaction := facts.transaction
	switch creation.TargetType {
	case "Location":
		return transaction.publishCreatedLocationFacts(ctx, creation, changes, revision, now)
	case "Lane":
		return transaction.publishCreatedLaneFacts(ctx, creation, changes, revision, now)
	case "Track":
		return transaction.publishCreatedTrackFacts(ctx, creation, changes, revision, now)
	case "Session":
		return transaction.publishCreatedSessionFacts(ctx, creation, changes, revision, now)
	default:
		return errors.New("unsupported Draft creation target")
	}
}

func (facts rundownDraftFacts) publishUpdates(
	ctx context.Context,
	target draftFactTarget,
	changes []*ent.DraftChange,
	revision int,
	now time.Time,
) error {
	for _, change := range changes {
		if err := facts.validate(change.TargetType, change.FactKey); err != nil {
			return err
		}
	}
	transaction := facts.transaction
	switch target.targetType {
	case "Location":
		return transaction.publishLocationFacts(ctx, target.targetID, changes, revision, now)
	case "Lane":
		return transaction.publishLaneFacts(ctx, target.targetID, changes, revision, now)
	case "Track":
		return transaction.publishTrackFacts(ctx, target.targetID, changes, revision, now)
	case "Session":
		return transaction.publishSessionFacts(ctx, target.targetID, changes, revision, now)
	default:
		return errors.New("unsupported Draft fact target")
	}
}

func changeAfter(change *ent.DraftChange, destination any) error {
	var evidence struct {
		After json.RawMessage `json:"after"`
	}
	if err := json.Unmarshal([]byte(change.PayloadJSON), &evidence); err != nil {
		return err
	}
	return json.Unmarshal(evidence.After, destination)
}

func (transaction *CommandTx) publishChange(
	ctx context.Context,
	change *ent.DraftChange,
	publishedRevision int,
	now time.Time,
) error {
	switch change.Kind {
	case "CreateLocation":
		return transaction.publishCreatedLocation(ctx, change, publishedRevision, now)
	case "CreateLane":
		return transaction.publishCreatedLane(ctx, change, publishedRevision, now)
	case "CreateTrack":
		return transaction.publishCreatedTrack(ctx, change, publishedRevision, now)
	case "CreateSession":
		return transaction.publishCreatedSession(ctx, change, publishedRevision, now)
	default:
		return errors.New("unsupported Draft Change kind")
	}
}

func (transaction *CommandTx) publishCreatedLocation(ctx context.Context, change *ent.DraftChange, revision int, now time.Time) error {
	var input LocationDraftCreate
	if err := changeAfter(change, &input); err != nil {
		return errors.New("decode Location creation evidence")
	}
	_, err := transaction.transaction.LocationPublishedVersion.Create().SetLocationID(change.TargetID).
		SetPublishedRevision(revision).SetName(input.Name).SetCreatedAt(now).Save(ctx)
	if err != nil {
		return opaqueError("publish Location creation", err)
	}
	return nil
}

func (transaction *CommandTx) publishCreatedLane(ctx context.Context, change *ent.DraftChange, revision int, now time.Time) error {
	var input LaneDraftCreate
	if err := changeAfter(change, &input); err != nil {
		return errors.New("decode Lane creation evidence")
	}
	_, err := transaction.transaction.LanePublishedVersion.Create().SetLaneID(change.TargetID).
		SetPublishedRevision(revision).SetName(input.Name).SetLocationID(input.Location.ID).SetCreatedAt(now).Save(ctx)
	if err != nil {
		return opaqueError("publish Lane creation", err)
	}
	return nil
}

func (transaction *CommandTx) publishCreatedTrack(ctx context.Context, change *ent.DraftChange, revision int, now time.Time) error {
	var input TrackDraftCreate
	if err := changeAfter(change, &input); err != nil {
		return errors.New("decode Track creation evidence")
	}
	_, err := transaction.transaction.TrackPublishedVersion.Create().SetTrackID(change.TargetID).
		SetPublishedRevision(revision).SetName(input.Name).SetCreatedAt(now).Save(ctx)
	if err != nil {
		return opaqueError("publish Track creation", err)
	}
	return nil
}

func (transaction *CommandTx) publishCreatedSession(ctx context.Context, change *ent.DraftChange, revision int, now time.Time) error {
	var input SessionDraftCreate
	if err := changeAfter(change, &input); err != nil {
		return errors.New("decode Session creation evidence")
	}
	laneIDs, locationIDs, trackIDs := targetIDs(input.Lanes), targetIDs(input.Locations), targetIDs(input.Tracks)
	create := transaction.transaction.SessionPublishedVersion.Create().SetSessionID(change.TargetID).SetPublishedRevision(revision).
		SetTitle(input.Title).SetType(sessionpublishedversion.Type(input.Type)).
		SetAudienceVisibility(sessionpublishedversion.AudienceVisibility(input.AudienceVisibility)).
		SetPublicDetails(input.PublicDetails).SetCrewNotes(input.CrewNotes).
		SetPlannedStart(input.PlannedStart).SetPlannedEnd(input.PlannedEnd).
		SetTimingPolicy(sessionpublishedversion.TimingPolicy(input.TimingPolicy)).SetMinimumDurationSeconds(input.MinimumDurationSeconds).
		SetStartBoundary(sessionpublishedversion.StartBoundary(input.StartBoundary)).SetEndBoundary(sessionpublishedversion.EndBoundary(input.EndBoundary)).
		SetCreatedAt(now).AddLaneIDs(laneIDs...).AddLocationIDs(locationIDs...).AddTrackIDs(trackIDs...)
	if _, err := create.Save(ctx); err != nil {
		return opaqueError("publish Session creation", err)
	}
	return nil
}

func targetIDs(targets []DraftTarget) []int {
	result := make([]int, 0, len(targets))
	for _, target := range targets {
		result = append(result, target.ID)
	}
	return result
}

func (transaction *CommandTx) publishCreatedLocationFacts(ctx context.Context, creation *ent.DraftChange, facts []*ent.DraftChange, revision int, now time.Time) error {
	var input LocationDraftCreate
	if err := changeAfter(creation, &input); err != nil {
		return errors.New("decode Location creation evidence")
	}
	for _, fact := range facts {
		if fact.FactKey == "name" {
			if err := changeAfter(fact, &input.Name); err != nil {
				return err
			}
		}
	}
	_, err := transaction.transaction.LocationPublishedVersion.Create().SetLocationID(creation.TargetID).
		SetPublishedRevision(revision).SetName(input.Name).SetCreatedAt(now).Save(ctx)
	return err
}

func (transaction *CommandTx) publishCreatedLaneFacts(ctx context.Context, creation *ent.DraftChange, facts []*ent.DraftChange, revision int, now time.Time) error {
	var input LaneDraftCreate
	if err := changeAfter(creation, &input); err != nil {
		return errors.New("decode Lane creation evidence")
	}
	for _, fact := range facts {
		switch fact.FactKey {
		case "name":
			if err := changeAfter(fact, &input.Name); err != nil {
				return err
			}
		case "location":
			if err := changeAfter(fact, &input.Location.ID); err != nil {
				return err
			}
		}
	}
	_, err := transaction.transaction.LanePublishedVersion.Create().SetLaneID(creation.TargetID).
		SetPublishedRevision(revision).SetName(input.Name).SetLocationID(input.Location.ID).SetCreatedAt(now).Save(ctx)
	return err
}

func (transaction *CommandTx) publishCreatedTrackFacts(ctx context.Context, creation *ent.DraftChange, facts []*ent.DraftChange, revision int, now time.Time) error {
	var input TrackDraftCreate
	if err := changeAfter(creation, &input); err != nil {
		return errors.New("decode Track creation evidence")
	}
	for _, fact := range facts {
		if fact.FactKey == "name" {
			if err := changeAfter(fact, &input.Name); err != nil {
				return err
			}
		}
	}
	_, err := transaction.transaction.TrackPublishedVersion.Create().SetTrackID(creation.TargetID).
		SetPublishedRevision(revision).SetName(input.Name).SetCreatedAt(now).Save(ctx)
	return err
}

func (transaction *CommandTx) publishCreatedSessionFacts(ctx context.Context, creation *ent.DraftChange, facts []*ent.DraftChange, revision int, now time.Time) error {
	var input SessionDraftCreate
	if err := changeAfter(creation, &input); err != nil {
		return errors.New("decode Session creation evidence")
	}
	laneIDs, locationIDs, trackIDs := targetIDs(input.Lanes), targetIDs(input.Locations), targetIDs(input.Tracks)
	for _, fact := range facts {
		handled, membershipErr := applyMembershipAfter(fact, &laneIDs, &locationIDs, &trackIDs)
		if membershipErr != nil {
			return membershipErr
		}
		if handled {
			continue
		}
		switch fact.FactKey {
		case "title":
			if err := changeAfter(fact, &input.Title); err != nil {
				return err
			}
		case "type":
			if err := changeAfter(fact, &input.Type); err != nil {
				return err
			}
		case "audience_visibility":
			if err := changeAfter(fact, &input.AudienceVisibility); err != nil {
				return err
			}
		case "public_details":
			if err := changeAfter(fact, &input.PublicDetails); err != nil {
				return err
			}
		case "crew_notes":
			if err := changeAfter(fact, &input.CrewNotes); err != nil {
				return err
			}
		case "planned_start":
			if err := changeAfter(fact, &input.PlannedStart); err != nil {
				return err
			}
		case "planned_end":
			if err := changeAfter(fact, &input.PlannedEnd); err != nil {
				return err
			}
		case "timing_policy":
			if err := changeAfter(fact, &input.TimingPolicy); err != nil {
				return err
			}
		case "minimum_duration":
			if err := changeAfter(fact, &input.MinimumDurationSeconds); err != nil {
				return err
			}
		case "start_boundary":
			if err := changeAfter(fact, &input.StartBoundary); err != nil {
				return err
			}
		case "end_boundary":
			if err := changeAfter(fact, &input.EndBoundary); err != nil {
				return err
			}
		case "lanes":
			if err := changeAfter(fact, &laneIDs); err != nil {
				return err
			}
		case "locations":
			if err := changeAfter(fact, &locationIDs); err != nil {
				return err
			}
		case "tracks":
			if err := changeAfter(fact, &trackIDs); err != nil {
				return err
			}
		default:
			return errors.New("unsupported Session creation fact")
		}
	}
	create := transaction.transaction.SessionPublishedVersion.Create().SetSessionID(creation.TargetID).SetPublishedRevision(revision).
		SetTitle(input.Title).SetType(sessionpublishedversion.Type(input.Type)).SetAudienceVisibility(sessionpublishedversion.AudienceVisibility(input.AudienceVisibility)).
		SetPublicDetails(input.PublicDetails).SetCrewNotes(input.CrewNotes).SetPlannedStart(input.PlannedStart).SetPlannedEnd(input.PlannedEnd).
		SetTimingPolicy(sessionpublishedversion.TimingPolicy(input.TimingPolicy)).SetMinimumDurationSeconds(input.MinimumDurationSeconds).
		SetStartBoundary(sessionpublishedversion.StartBoundary(input.StartBoundary)).SetEndBoundary(sessionpublishedversion.EndBoundary(input.EndBoundary)).
		SetCreatedAt(now).AddLaneIDs(laneIDs...).AddLocationIDs(locationIDs...).AddTrackIDs(trackIDs...)
	_, err := create.Save(ctx)
	return err
}

func publishKindOrder(kind string) int {
	switch kind {
	case "CreateLocation":
		return 1
	case "CreateLane":
		return 2
	case "CreateTrack":
		return 3
	case "CreateSession":
		return 4
	default:
		return 100
	}
}

func (transaction *CommandTx) publishLocationFacts(ctx context.Context, id int, changes []*ent.DraftChange, revision int, now time.Time) error {
	identity, err := transaction.transaction.Location.Get(ctx, id)
	if err != nil {
		return opaqueError("load Location identity for fact Publish", err)
	}
	baseline, err := identity.QueryPublishedVersions().Order(ent.Desc(locationpublishedversion.FieldPublishedRevision)).First(ctx)
	if err != nil {
		return opaqueError("load Published Location baseline", err)
	}
	name := baseline.Name
	for _, change := range changes {
		if change.FactKey == "name" && changeAfter(change, &name) != nil {
			return errors.New("decode Location fact change")
		}
	}
	_, err = transaction.transaction.LocationPublishedVersion.Create().SetLocationID(id).SetPublishedRevision(revision).
		SetName(name).SetRetired(baseline.Retired).SetCreatedAt(now).Save(ctx)
	if err != nil {
		return opaqueError("publish Location fact version", err)
	}
	return nil
}

func (transaction *CommandTx) publishLaneFacts(ctx context.Context, id int, changes []*ent.DraftChange, revision int, now time.Time) error {
	identity, err := transaction.transaction.Lane.Get(ctx, id)
	if err != nil {
		return opaqueError("load Lane identity for fact Publish", err)
	}
	baseline, err := identity.QueryPublishedVersions().Order(ent.Desc(lanepublishedversion.FieldPublishedRevision)).First(ctx)
	if err != nil {
		return opaqueError("load Published Lane baseline", err)
	}
	name, locationID := baseline.Name, baseline.LocationID
	for _, change := range changes {
		switch change.FactKey {
		case "name":
			if err = changeAfter(change, &name); err != nil {
				return errors.New("decode Lane name fact")
			}
		case "location":
			if err = changeAfter(change, &locationID); err != nil {
				return errors.New("decode Lane location fact")
			}
		}
	}
	_, err = transaction.transaction.LanePublishedVersion.Create().SetLaneID(id).SetPublishedRevision(revision).
		SetName(name).SetLocationID(locationID).SetRetired(baseline.Retired).SetCreatedAt(now).Save(ctx)
	if err != nil {
		return opaqueError("publish Lane fact version", err)
	}
	return nil
}

func applyMembershipAfter(change *ent.DraftChange, laneIDs, locationIDs, trackIDs *[]int) (bool, error) {
	family, encodedID, found := strings.Cut(change.FactKey, ":")
	if !found {
		return false, nil
	}
	id, err := strconv.Atoi(encodedID)
	if err != nil {
		return true, errors.New("decode Draft membership fact key")
	}
	var present bool
	if err = changeAfter(change, &present); err != nil {
		return true, errors.New("decode Draft membership fact")
	}
	var ids *[]int
	switch family {
	case "lanes":
		ids = laneIDs
	case "locations":
		ids = locationIDs
	case "tracks":
		ids = trackIDs
	default:
		return false, nil
	}
	if present && !slices.Contains(*ids, id) {
		*ids = append(*ids, id)
	}
	if !present {
		filtered := (*ids)[:0]
		for _, candidate := range *ids {
			if candidate != id {
				filtered = append(filtered, candidate)
			}
		}
		*ids = filtered
	}
	return true, nil
}

func (transaction *CommandTx) publishTrackFacts(ctx context.Context, id int, changes []*ent.DraftChange, revision int, now time.Time) error {
	identity, err := transaction.transaction.Track.Get(ctx, id)
	if err != nil {
		return opaqueError("load Track identity for fact Publish", err)
	}
	baseline, err := identity.QueryPublishedVersions().Order(ent.Desc(trackpublishedversion.FieldPublishedRevision)).First(ctx)
	if err != nil {
		return opaqueError("load Published Track baseline", err)
	}
	name := baseline.Name
	for _, change := range changes {
		if change.FactKey == "name" && changeAfter(change, &name) != nil {
			return errors.New("decode Track fact change")
		}
	}
	_, err = transaction.transaction.TrackPublishedVersion.Create().SetTrackID(id).SetPublishedRevision(revision).
		SetName(name).SetRetired(baseline.Retired).SetCreatedAt(now).Save(ctx)
	if err != nil {
		return opaqueError("publish Track fact version", err)
	}
	return nil
}

func (transaction *CommandTx) publishSessionFacts(ctx context.Context, id int, changes []*ent.DraftChange, revision int, now time.Time) error {
	identity, err := transaction.transaction.Session.Get(ctx, id)
	if err != nil {
		return opaqueError("load Session identity for fact Publish", err)
	}
	baseline, err := identity.QueryPublishedVersions().Order(ent.Desc(sessionpublishedversion.FieldPublishedRevision)).First(ctx)
	if err != nil {
		return opaqueError("load Published Session baseline", err)
	}
	laneIDs, err := baseline.QueryLanes().IDs(ctx)
	if err != nil {
		return opaqueError("load Published Session Lanes", err)
	}
	locationIDs, err := baseline.QueryLocations().IDs(ctx)
	if err != nil {
		return opaqueError("load Published Session Locations", err)
	}
	trackIDs, err := baseline.QueryTracks().IDs(ctx)
	if err != nil {
		return opaqueError("load Published Session Tracks", err)
	}
	title, sessionType := baseline.Title, string(baseline.Type)
	audience, publicDetails, crewNotes := string(baseline.AudienceVisibility), baseline.PublicDetails, baseline.CrewNotes
	plannedStart, plannedEnd := baseline.PlannedStart, baseline.PlannedEnd
	timingPolicy, minimumDuration := string(baseline.TimingPolicy), baseline.MinimumDurationSeconds
	startBoundary, endBoundary := string(baseline.StartBoundary), string(baseline.EndBoundary)
	for _, change := range changes {
		handled, membershipErr := applyMembershipAfter(change, &laneIDs, &locationIDs, &trackIDs)
		if membershipErr != nil {
			return membershipErr
		}
		if handled {
			continue
		}
		switch change.FactKey {
		case "title":
			err = changeAfter(change, &title)
		case "type":
			err = changeAfter(change, &sessionType)
		case "audience_visibility":
			err = changeAfter(change, &audience)
		case "public_details":
			err = changeAfter(change, &publicDetails)
		case "crew_notes":
			err = changeAfter(change, &crewNotes)
		case "planned_start":
			err = changeAfter(change, &plannedStart)
		case "planned_end":
			err = changeAfter(change, &plannedEnd)
		case "timing_policy":
			err = changeAfter(change, &timingPolicy)
		case "minimum_duration":
			err = changeAfter(change, &minimumDuration)
		case "start_boundary":
			err = changeAfter(change, &startBoundary)
		case "end_boundary":
			err = changeAfter(change, &endBoundary)
		case "lanes":
			err = changeAfter(change, &laneIDs)
		case "locations":
			err = changeAfter(change, &locationIDs)
		case "tracks":
			err = changeAfter(change, &trackIDs)
		default:
			return errors.New("unsupported Session fact change")
		}
		if err != nil {
			return errors.New("decode Session fact change")
		}
	}
	create := transaction.transaction.SessionPublishedVersion.Create().SetSessionID(id).SetPublishedRevision(revision).
		SetTitle(title).SetType(sessionpublishedversion.Type(sessionType)).
		SetAudienceVisibility(sessionpublishedversion.AudienceVisibility(audience)).
		SetPublicDetails(publicDetails).SetCrewNotes(crewNotes).
		SetPlannedStart(plannedStart).SetPlannedEnd(plannedEnd).
		SetTimingPolicy(sessionpublishedversion.TimingPolicy(timingPolicy)).
		SetMinimumDurationSeconds(minimumDuration).
		SetStartBoundary(sessionpublishedversion.StartBoundary(startBoundary)).
		SetEndBoundary(sessionpublishedversion.EndBoundary(endBoundary)).SetCreatedAt(now).
		AddLaneIDs(laneIDs...).AddLocationIDs(locationIDs...).AddTrackIDs(trackIDs...)
	if _, err = create.Save(ctx); err != nil {
		return opaqueError("publish Session fact version", err)
	}
	return nil
}

// CrewRundownState is the store-owned Published projection input.
type CrewRundownState struct {
	DraftRevision     int
	PublishedRevision int
	Locations         []PublishedLocation
	Lanes             []PublishedLane
	Tracks            []PublishedTrack
	Sessions          []PublishedSession
}

// PublishedLocation is the store projection of one current Location version.
type PublishedLocation struct {
	ID      int
	Name    string
	Retired bool
}

// PublishedLane is the store projection of one current Lane version.
type PublishedLane struct {
	ID         int
	Name       string
	LocationID int
	Retired    bool
}

// PublishedTrack is the store projection of one current Track version.
type PublishedTrack struct {
	ID      int
	Name    string
	Retired bool
}

// PublishedSession is the store projection of one current Session version.
type PublishedSession struct {
	ID                     int
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
	LaneIDs                []int
	LocationIDs            []int
	TrackIDs               []int
}

// LoadCrewRundown returns the current Published versions without exposing Ent entities.
func (installation *SQLite) LoadCrewRundown(ctx context.Context, eventID int) (CrewRundownState, error) {
	return loadCrewRundown(ctx, installation.client, eventID)
}

func loadCrewRundown(ctx context.Context, client *ent.Client, eventID int) (CrewRundownState, error) {
	revisions, err := client.Rundown.Query().Where(rundown.EventIDEQ(eventID)).Only(ctx)
	if ent.IsNotFound(err) {
		return CrewRundownState{}, ErrEventNotFound
	}
	if err != nil {
		return CrewRundownState{}, opaqueError("load Crew Rundown revisions", err)
	}
	result := CrewRundownState{
		DraftRevision: revisions.DraftRevision, PublishedRevision: revisions.PublishedRevision,
	}
	locations, err := client.Location.Query().Where(location.EventIDEQ(eventID)).All(ctx)
	if err != nil {
		return CrewRundownState{}, opaqueError("load Crew Locations", err)
	}
	for _, identity := range locations {
		version, queryErr := identity.QueryPublishedVersions().
			Order(ent.Desc("published_revision")).
			First(ctx)
		if ent.IsNotFound(queryErr) {
			continue
		}
		if queryErr != nil {
			return CrewRundownState{}, opaqueError("load current Published Location", queryErr)
		}
		if !version.Retired {
			result.Locations = append(result.Locations, PublishedLocation{ID: identity.ID, Name: version.Name})
		}
	}
	lanes, err := client.Lane.Query().Where(lane.EventIDEQ(eventID)).All(ctx)
	if err != nil {
		return CrewRundownState{}, opaqueError("load Crew Lanes", err)
	}
	for _, identity := range lanes {
		version, queryErr := identity.QueryPublishedVersions().Order(ent.Desc("published_revision")).First(ctx)
		if ent.IsNotFound(queryErr) {
			continue
		}
		if queryErr != nil {
			return CrewRundownState{}, opaqueError("load current Published Lane", queryErr)
		}
		if !version.Retired {
			result.Lanes = append(result.Lanes, PublishedLane{
				ID: identity.ID, Name: version.Name, LocationID: version.LocationID,
			})
		}
	}
	tracks, err := client.Track.Query().Where(track.EventIDEQ(eventID)).All(ctx)
	if err != nil {
		return CrewRundownState{}, opaqueError("load Crew Tracks", err)
	}
	for _, identity := range tracks {
		version, queryErr := identity.QueryPublishedVersions().Order(ent.Desc("published_revision")).First(ctx)
		if ent.IsNotFound(queryErr) {
			continue
		}
		if queryErr != nil {
			return CrewRundownState{}, opaqueError("load current Published Track", queryErr)
		}
		if !version.Retired {
			result.Tracks = append(result.Tracks, PublishedTrack{ID: identity.ID, Name: version.Name})
		}
	}
	sessions, err := client.Session.Query().Where(session.EventIDEQ(eventID)).All(ctx)
	if err != nil {
		return CrewRundownState{}, opaqueError("load Crew Sessions", err)
	}
	for _, identity := range sessions {
		version, queryErr := identity.QueryPublishedVersions().Order(ent.Desc("published_revision")).First(ctx)
		if ent.IsNotFound(queryErr) {
			continue
		}
		if queryErr != nil {
			return CrewRundownState{}, opaqueError("load current Published Session", queryErr)
		}
		lanes, queryErr := version.QueryLanes().All(ctx)
		if queryErr != nil {
			return CrewRundownState{}, opaqueError("load Published Session Lanes", queryErr)
		}
		laneIDs := make([]int, 0, len(lanes))
		for _, item := range lanes {
			laneIDs = append(laneIDs, item.ID)
		}
		locations, queryErr := version.QueryLocations().All(ctx)
		if queryErr != nil {
			return CrewRundownState{}, opaqueError("load Published Session Locations", queryErr)
		}
		locationIDs := make([]int, 0, len(locations))
		for _, item := range locations {
			locationIDs = append(locationIDs, item.ID)
		}
		tracks, queryErr := version.QueryTracks().All(ctx)
		if queryErr != nil {
			return CrewRundownState{}, opaqueError("load Published Session Tracks", queryErr)
		}
		trackIDs := make([]int, 0, len(tracks))
		for _, item := range tracks {
			trackIDs = append(trackIDs, item.ID)
		}
		result.Sessions = append(result.Sessions, PublishedSession{
			ID: identity.ID, Title: version.Title, Type: version.Type.String(),
			AudienceVisibility: version.AudienceVisibility.String(),
			PublicDetails:      version.PublicDetails, CrewNotes: version.CrewNotes,
			PlannedStart: version.PlannedStart, PlannedEnd: version.PlannedEnd,
			TimingPolicy:           version.TimingPolicy.String(),
			MinimumDurationSeconds: version.MinimumDurationSeconds,
			StartBoundary:          version.StartBoundary.String(), EndBoundary: version.EndBoundary.String(),
			LaneIDs: laneIDs, LocationIDs: locationIDs, TrackIDs: trackIDs,
		})
	}
	return result, nil
}
