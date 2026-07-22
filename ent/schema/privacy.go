package schema

import (
	"context"

	"entgo.io/ent"
	"entgo.io/ent/dialect/sql"
	"entgo.io/ent/privacy"

	beamersent "github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/lane"
	"github.com/dotwaffle/beamers/ent/lanedraft"
	"github.com/dotwaffle/beamers/ent/lanepublishedversion"
	"github.com/dotwaffle/beamers/ent/location"
	"github.com/dotwaffle/beamers/ent/locationdraft"
	"github.com/dotwaffle/beamers/ent/locationpublishedversion"
	"github.com/dotwaffle/beamers/ent/predicate"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessiondraft"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
	"github.com/dotwaffle/beamers/ent/sessionrun"
	"github.com/dotwaffle/beamers/ent/track"
	"github.com/dotwaffle/beamers/ent/trackdraft"
	"github.com/dotwaffle/beamers/ent/trackpublishedversion"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func denyMissingViewer() privacy.QueryMutationRule {
	return privacy.ContextQueryMutationRule(func(ctx context.Context) error {
		if _, ok := viewer.FromContext(ctx); !ok {
			return privacy.Denyf("viewer context is missing")
		}
		return privacy.Skip
	})
}

func allowAdministrator() privacy.QueryMutationRule {
	return privacy.ContextQueryMutationRule(func(ctx context.Context) error {
		identity, _ := viewer.FromContext(ctx)
		if identity.Administrator {
			return privacy.Allow
		}
		return privacy.Skip
	})
}

func filterGrantedEvents() privacy.QueryRule {
	type selectorFilter interface {
		Where(...predicate.Event) *beamersent.EventQuery
	}
	return eventQueryRule(func(ctx context.Context, query ent.Query) error {
		identity, _ := viewer.FromContext(ctx)
		ids := make([]any, 0, len(identity.EventRoles))
		for eventID := range identity.EventRoles {
			ids = append(ids, eventID)
		}
		if len(ids) == 0 {
			return privacy.Denyf("Event Grant is required")
		}
		filter, ok := query.(selectorFilter)
		if !ok {
			return privacy.Denyf("unexpected Event query %T", query)
		}
		filter.Where(func(selector *sql.Selector) {
			selector.Where(sql.In(selector.C("id"), ids...))
		})
		return privacy.Skip
	})
}

func filterGrantedRundowns() privacy.QueryRule {
	type selectorFilter interface {
		Where(...predicate.Rundown) *beamersent.RundownQuery
	}
	return eventQueryRule(func(ctx context.Context, query ent.Query) error {
		ids, err := grantedEventIDs(ctx)
		if err != nil {
			return err
		}
		filter, ok := query.(selectorFilter)
		if !ok {
			return privacy.Denyf("unexpected Rundown query %T", query)
		}
		filter.Where(func(selector *sql.Selector) {
			selector.Where(sql.InInts(selector.C("event_id"), ids...))
		})
		return privacy.Skip
	})
}

func filterGrantedLocations() privacy.QueryRule {
	type selectorFilter interface {
		Where(...predicate.Location) *beamersent.LocationQuery
	}
	return eventQueryRule(func(ctx context.Context, query ent.Query) error {
		ids, err := grantedEventIDs(ctx)
		if err != nil {
			return err
		}
		filter, ok := query.(selectorFilter)
		if !ok {
			return privacy.Denyf("unexpected Location query %T", query)
		}
		filter.Where(func(selector *sql.Selector) {
			selector.Where(sql.InInts(selector.C("event_id"), ids...))
		})
		return privacy.Skip
	})
}

func filterGrantedLocationDrafts() privacy.QueryRule {
	type selectorFilter interface {
		Where(...predicate.LocationDraft) *beamersent.LocationDraftQuery
	}
	return eventQueryRule(func(ctx context.Context, query ent.Query) error {
		ids, err := grantedEventIDs(ctx)
		if err != nil {
			return err
		}
		filter, ok := query.(selectorFilter)
		if !ok {
			return privacy.Denyf("unexpected Location Draft query %T", query)
		}
		filter.Where(locationdraft.HasLocationWith(location.EventIDIn(ids...)))
		return privacy.Skip
	})
}

func filterGrantedLocationPublishedVersions() privacy.QueryRule {
	type selectorFilter interface {
		Where(...predicate.LocationPublishedVersion) *beamersent.LocationPublishedVersionQuery
	}
	return eventQueryRule(func(ctx context.Context, query ent.Query) error {
		ids, err := grantedEventIDs(ctx)
		if err != nil {
			return err
		}
		filter, ok := query.(selectorFilter)
		if !ok {
			return privacy.Denyf("unexpected Published Location version query %T", query)
		}
		filter.Where(locationpublishedversion.HasLocationWith(location.EventIDIn(ids...)))
		return privacy.Skip
	})
}

func filterGrantedLanes() privacy.QueryRule {
	type selectorFilter interface {
		Where(...predicate.Lane) *beamersent.LaneQuery
	}
	return eventQueryRule(func(ctx context.Context, query ent.Query) error {
		allowed, err := laneReadPredicate(ctx)
		if err != nil {
			return err
		}
		filter, ok := query.(selectorFilter)
		if !ok {
			return privacy.Denyf("unexpected Lane query %T", query)
		}
		filter.Where(allowed)
		return privacy.Skip
	})
}

func filterGrantedLaneDrafts() privacy.QueryRule {
	type selectorFilter interface {
		Where(...predicate.LaneDraft) *beamersent.LaneDraftQuery
	}
	return eventQueryRule(func(ctx context.Context, query ent.Query) error {
		allowed, err := laneReadPredicate(ctx)
		if err != nil {
			return err
		}
		filter, ok := query.(selectorFilter)
		if !ok {
			return privacy.Denyf("unexpected Lane Draft query %T", query)
		}
		filter.Where(lanedraft.HasLaneWith(allowed))
		return privacy.Skip
	})
}

func filterGrantedLanePublishedVersions() privacy.QueryRule {
	type selectorFilter interface {
		Where(...predicate.LanePublishedVersion) *beamersent.LanePublishedVersionQuery
	}
	return eventQueryRule(func(ctx context.Context, query ent.Query) error {
		allowed, err := laneReadPredicate(ctx)
		if err != nil {
			return err
		}
		filter, ok := query.(selectorFilter)
		if !ok {
			return privacy.Denyf("unexpected Published Lane version query %T", query)
		}
		filter.Where(lanepublishedversion.HasLaneWith(allowed))
		return privacy.Skip
	})
}

func filterGrantedTracks() privacy.QueryRule {
	type selectorFilter interface {
		Where(...predicate.Track) *beamersent.TrackQuery
	}
	return eventQueryRule(func(ctx context.Context, query ent.Query) error {
		ids, err := grantedEventIDs(ctx)
		if err != nil {
			return err
		}
		filter, ok := query.(selectorFilter)
		if !ok {
			return privacy.Denyf("unexpected Track query %T", query)
		}
		filter.Where(func(selector *sql.Selector) {
			selector.Where(sql.InInts(selector.C("event_id"), ids...))
		})
		return privacy.Skip
	})
}

func filterGrantedTrackDrafts() privacy.QueryRule {
	type selectorFilter interface {
		Where(...predicate.TrackDraft) *beamersent.TrackDraftQuery
	}
	return eventQueryRule(func(ctx context.Context, query ent.Query) error {
		ids, err := grantedEventIDs(ctx)
		if err != nil {
			return err
		}
		filter, ok := query.(selectorFilter)
		if !ok {
			return privacy.Denyf("unexpected Track Draft query %T", query)
		}
		filter.Where(trackdraft.HasTrackWith(track.EventIDIn(ids...)))
		return privacy.Skip
	})
}

func filterGrantedTrackPublishedVersions() privacy.QueryRule {
	type selectorFilter interface {
		Where(...predicate.TrackPublishedVersion) *beamersent.TrackPublishedVersionQuery
	}
	return eventQueryRule(func(ctx context.Context, query ent.Query) error {
		ids, err := grantedEventIDs(ctx)
		if err != nil {
			return err
		}
		filter, ok := query.(selectorFilter)
		if !ok {
			return privacy.Denyf("unexpected Published Track version query %T", query)
		}
		filter.Where(trackpublishedversion.HasTrackWith(track.EventIDIn(ids...)))
		return privacy.Skip
	})
}

func filterGrantedSessions() privacy.QueryRule {
	type selectorFilter interface {
		Where(...predicate.Session) *beamersent.SessionQuery
	}
	return eventQueryRule(func(ctx context.Context, query ent.Query) error {
		allowed, err := sessionReadPredicate(ctx)
		if err != nil {
			return err
		}
		filter, ok := query.(selectorFilter)
		if !ok {
			return privacy.Denyf("unexpected Session query %T", query)
		}
		filter.Where(allowed)
		return privacy.Skip
	})
}

func filterGrantedSessionDrafts() privacy.QueryRule {
	type selectorFilter interface {
		Where(...predicate.SessionDraft) *beamersent.SessionDraftQuery
	}
	return eventQueryRule(func(ctx context.Context, query ent.Query) error {
		allowed, err := sessionDraftReadPredicate(ctx)
		if err != nil {
			return err
		}
		filter, ok := query.(selectorFilter)
		if !ok {
			return privacy.Denyf("unexpected Session Draft query %T", query)
		}
		filter.Where(allowed)
		return privacy.Skip
	})
}

func filterGrantedSessionPublishedVersions() privacy.QueryRule {
	type selectorFilter interface {
		Where(...predicate.SessionPublishedVersion) *beamersent.SessionPublishedVersionQuery
	}
	return eventQueryRule(func(ctx context.Context, query ent.Query) error {
		allowed, err := sessionPublishedVersionReadPredicate(ctx)
		if err != nil {
			return err
		}
		filter, ok := query.(selectorFilter)
		if !ok {
			return privacy.Denyf("unexpected Published Session version query %T", query)
		}
		filter.Where(allowed)
		return privacy.Skip
	})
}

func filterGrantedSessionRuns() privacy.QueryRule {
	type selectorFilter interface {
		Where(...predicate.SessionRun) *beamersent.SessionRunQuery
	}
	return eventQueryRule(func(ctx context.Context, query ent.Query) error {
		allowed, err := sessionRunReadPredicate(ctx)
		if err != nil {
			return err
		}
		filter, ok := query.(selectorFilter)
		if !ok {
			return privacy.Denyf("unexpected Session Run query %T", query)
		}
		filter.Where(allowed)
		return privacy.Skip
	})
}

func grantedEventIDs(ctx context.Context) ([]int, error) {
	identity, _ := viewer.FromContext(ctx)
	ids := make([]int, 0, len(identity.EventRoles))
	for eventID := range identity.EventRoles {
		ids = append(ids, eventID)
	}
	if len(ids) == 0 {
		return nil, privacy.Denyf("Event Grant is required")
	}
	return ids, nil
}

func laneReadPredicate(ctx context.Context) (predicate.Lane, error) {
	fullEventIDs, operatorEventIDs, laneIDs, err := grantedLaneReadScopes(ctx)
	if err != nil {
		return nil, err
	}
	allowed := make([]predicate.Lane, 0, 2)
	if len(fullEventIDs) > 0 {
		allowed = append(allowed, lane.EventIDIn(fullEventIDs...))
	}
	if len(operatorEventIDs) > 0 && len(laneIDs) > 0 {
		allowed = append(allowed, lane.And(
			lane.EventIDIn(operatorEventIDs...), lane.IDIn(laneIDs...),
		))
	}
	if len(allowed) == 0 {
		return nil, privacy.Denyf("Lane scope is required")
	}
	return lane.Or(allowed...), nil
}

func sessionReadPredicate(ctx context.Context) (predicate.Session, error) {
	fullEventIDs, operatorEventIDs, laneIDs, err := grantedLaneReadScopes(ctx)
	if err != nil {
		return nil, err
	}
	allowed := make([]predicate.Session, 0, 2)
	if len(fullEventIDs) > 0 {
		allowed = append(allowed, session.EventIDIn(fullEventIDs...))
	}
	if len(operatorEventIDs) > 0 && len(laneIDs) > 0 {
		allowed = append(allowed, session.And(
			session.EventIDIn(operatorEventIDs...), latestPublishedLanesWithin(laneIDs),
		))
	}
	if len(allowed) == 0 {
		return nil, privacy.Denyf("Session Lane scope is required")
	}
	return session.Or(allowed...), nil
}

func sessionDraftReadPredicate(ctx context.Context) (predicate.SessionDraft, error) {
	fullEventIDs, operatorEventIDs, laneIDs, err := grantedLaneReadScopes(ctx)
	if err != nil {
		return nil, err
	}
	allowed := make([]predicate.SessionDraft, 0, 2)
	if len(fullEventIDs) > 0 {
		allowed = append(allowed, sessiondraft.HasSessionWith(session.EventIDIn(fullEventIDs...)))
	}
	if len(operatorEventIDs) > 0 && len(laneIDs) > 0 {
		allowed = append(allowed, sessiondraft.And(
			sessiondraft.HasSessionWith(session.EventIDIn(operatorEventIDs...)),
			sessiondraft.HasLanesWith(lane.IDIn(laneIDs...)),
			sessiondraft.Not(sessiondraft.HasLanesWith(lane.IDNotIn(laneIDs...))),
		))
	}
	if len(allowed) == 0 {
		return nil, privacy.Denyf("Session Draft Lane scope is required")
	}
	return sessiondraft.Or(allowed...), nil
}

func sessionPublishedVersionReadPredicate(ctx context.Context) (predicate.SessionPublishedVersion, error) {
	fullEventIDs, operatorEventIDs, laneIDs, err := grantedLaneReadScopes(ctx)
	if err != nil {
		return nil, err
	}
	allowed := make([]predicate.SessionPublishedVersion, 0, 2)
	if len(fullEventIDs) > 0 {
		allowed = append(allowed,
			sessionpublishedversion.HasSessionWith(session.EventIDIn(fullEventIDs...)),
		)
	}
	if len(operatorEventIDs) > 0 && len(laneIDs) > 0 {
		allowed = append(allowed, sessionpublishedversion.And(
			sessionpublishedversion.HasSessionWith(session.EventIDIn(operatorEventIDs...)),
			sessionpublishedversion.HasLanesWith(lane.IDIn(laneIDs...)),
			sessionpublishedversion.Not(sessionpublishedversion.HasLanesWith(lane.IDNotIn(laneIDs...))),
		))
	}
	if len(allowed) == 0 {
		return nil, privacy.Denyf("Published Session Lane scope is required")
	}
	return sessionpublishedversion.Or(allowed...), nil
}

func sessionRunReadPredicate(ctx context.Context) (predicate.SessionRun, error) {
	fullEventIDs, operatorEventIDs, laneIDs, err := grantedLaneReadScopes(ctx)
	if err != nil {
		return nil, err
	}
	allowed := make([]predicate.SessionRun, 0, 2)
	if len(fullEventIDs) > 0 {
		allowed = append(allowed, sessionrun.HasSessionWith(session.EventIDIn(fullEventIDs...)))
	}
	if len(operatorEventIDs) > 0 && len(laneIDs) > 0 {
		allowed = append(allowed, sessionrun.And(
			sessionrun.HasSessionWith(session.EventIDIn(operatorEventIDs...)),
			sessionRunSnapshotLanesWithin(laneIDs),
		))
	}
	if len(allowed) == 0 {
		return nil, privacy.Denyf("Session Run Lane scope is required")
	}
	return sessionrun.Or(allowed...), nil
}

func latestPublishedLanesWithin(laneIDs []int) predicate.Session {
	return func(selector *sql.Selector) {
		selector.Where(sql.P(func(builder *sql.Builder) {
			builder.WriteString("EXISTS (SELECT 1 FROM session_published_versions AS spv_current ").
				WriteString("WHERE spv_current.session_id = ").WriteString(selector.C("id")).
				WriteString(" AND spv_current.published_revision = ").
				WriteString("(SELECT MAX(spv_latest.published_revision) FROM session_published_versions AS spv_latest ").
				WriteString("WHERE spv_latest.session_id = ").WriteString(selector.C("id")).WriteString(") ").
				WriteString("AND EXISTS (SELECT 1 FROM session_published_version_lanes AS scoped_lanes ").
				WriteString("WHERE scoped_lanes.session_published_version_id = spv_current.id ").
				WriteString("AND scoped_lanes.lane_id IN (").Args(intArgs(laneIDs)...).WriteString(")) ").
				WriteString("AND NOT EXISTS (SELECT 1 FROM session_published_version_lanes AS other_lanes ").
				WriteString("WHERE other_lanes.session_published_version_id = spv_current.id ").
				WriteString("AND other_lanes.lane_id NOT IN (").Args(intArgs(laneIDs)...).WriteString(")))")
		}))
	}
}

func sessionRunSnapshotLanesWithin(laneIDs []int) predicate.SessionRun {
	return func(selector *sql.Selector) {
		selector.Where(sql.P(func(builder *sql.Builder) {
			builder.WriteString("EXISTS (SELECT 1 FROM json_each(").WriteString(selector.C("snapshot_json")).
				WriteString(", '$.lane_ids')) AND NOT EXISTS (SELECT 1 FROM json_each(").
				WriteString(selector.C("snapshot_json")).WriteString(", '$.lane_ids') AS snapshot_lanes ").
				WriteString("WHERE snapshot_lanes.value NOT IN (").Args(intArgs(laneIDs)...).WriteString("))")
		}))
	}
}

func intArgs(values []int) []any {
	args := make([]any, len(values))
	for index, value := range values {
		args[index] = value
	}
	return args
}

func grantedLaneReadScopes(ctx context.Context) ([]int, []int, []int, error) {
	identity, _ := viewer.FromContext(ctx)
	fullEventIDs := make([]int, 0, len(identity.EventRoles))
	operatorEventIDs := make([]int, 0, len(identity.EventRoles))
	laneIDs := make([]int, 0)
	for eventID, role := range identity.EventRoles {
		switch role {
		case viewer.Producer, viewer.Observer:
			fullEventIDs = append(fullEventIDs, eventID)
		case viewer.Operator:
			operatorEventIDs = append(operatorEventIDs, eventID)
			for laneID := range identity.EventScopes[eventID].LaneIDs {
				laneIDs = append(laneIDs, laneID)
			}
		}
	}
	if len(fullEventIDs) == 0 && len(operatorEventIDs) == 0 {
		return nil, nil, nil, privacy.Denyf("Event Grant is required")
	}
	return fullEventIDs, operatorEventIDs, laneIDs, nil
}

type eventQueryRule func(context.Context, ent.Query) error

func (rule eventQueryRule) EvalQuery(ctx context.Context, query ent.Query) error {
	return rule(ctx, query)
}

func allowEventMutation() privacy.MutationRule {
	type identifiedMutation interface {
		ID() (int, bool)
	}
	return privacy.MutationRuleFunc(func(ctx context.Context, mutation ent.Mutation) error {
		identity, _ := viewer.FromContext(ctx)
		if mutation.Op().Is(ent.OpCreate) && identity.Administrator {
			return privacy.Allow
		}
		identified, ok := mutation.(identifiedMutation)
		if !ok {
			return privacy.Skip
		}
		eventID, ok := identified.ID()
		if ok && identity.CanProduceEvent(eventID) {
			return privacy.Allow
		}
		return privacy.Skip
	})
}

func allowEventOwnedMutation() privacy.MutationRule {
	type eventOwnedMutation interface {
		EventID() (int, bool)
		OldEventID(context.Context) (int, error)
	}
	return privacy.MutationRuleFunc(func(ctx context.Context, mutation ent.Mutation) error {
		identity, _ := viewer.FromContext(ctx)
		owned, ok := mutation.(eventOwnedMutation)
		if !ok {
			return privacy.Skip
		}
		eventID, exists := owned.EventID()
		if !exists {
			var err error
			eventID, err = owned.OldEventID(ctx)
			if err != nil {
				return privacy.Denyf("read mutation Event ownership: %v", err)
			}
		}
		if identity.CanProduceEvent(eventID) {
			return privacy.Allow
		}
		return privacy.Skip
	})
}

func allowScopedSessionLiveMutation() privacy.MutationRule {
	type sessionMutation interface {
		ID() (int, bool)
		Client() *beamersent.Client
	}
	return privacy.MutationRuleFunc(func(ctx context.Context, mutation ent.Mutation) error {
		if !mutation.Op().Is(ent.OpUpdateOne) || !onlyFields(mutation, "lifecycle", "live_state_revision") {
			return privacy.Skip
		}
		identified, ok := mutation.(sessionMutation)
		if !ok {
			return privacy.Skip
		}
		sessionID, ok := identified.ID()
		if !ok {
			return privacy.Skip
		}
		allowed, err := canOperateSession(ctx, identified.Client(), sessionID)
		if err != nil {
			return privacy.Denyf("authorize Session live mutation: %v", err)
		}
		if allowed {
			return privacy.Allow
		}
		return privacy.Skip
	})
}

func allowScopedSessionRunMutation() privacy.MutationRule {
	type sessionRunMutation interface {
		SessionID() (int, bool)
		OldSessionID(context.Context) (int, error)
		Client() *beamersent.Client
	}
	return privacy.MutationRuleFunc(func(ctx context.Context, mutation ent.Mutation) error {
		owned, ok := mutation.(sessionRunMutation)
		if !ok || mutation.Op().Is(ent.OpDelete|ent.OpDeleteOne) {
			return privacy.Skip
		}
		if mutation.Op().Is(ent.OpUpdateOne) && !onlyFields(mutation, "actual_end") {
			return privacy.Skip
		}
		sessionID, exists := owned.SessionID()
		if !exists {
			var err error
			sessionID, err = owned.OldSessionID(ctx)
			if err != nil {
				return privacy.Denyf("read Session Run ownership: %v", err)
			}
		}
		allowed, err := canOperateSession(ctx, owned.Client(), sessionID)
		if err != nil {
			return privacy.Denyf("authorize Session Run mutation: %v", err)
		}
		if allowed {
			return privacy.Allow
		}
		return privacy.Skip
	})
}

func canOperateSession(ctx context.Context, client *beamersent.Client, sessionID int) (bool, error) {
	found, err := client.Session.Get(ctx, sessionID)
	if err != nil {
		return false, err
	}
	identity, _ := viewer.FromContext(ctx)
	if identity.CanProduceEvent(found.EventID) {
		return true, nil
	}
	if identity.EventRoles[found.EventID] != viewer.Operator {
		return false, nil
	}
	version, err := client.SessionPublishedVersion.Query().Where(
		sessionpublishedversion.SessionIDEQ(sessionID),
	).Order(beamersent.Desc(sessionpublishedversion.FieldPublishedRevision)).First(ctx)
	if err != nil {
		return false, err
	}
	laneIDs, err := version.QueryLanes().IDs(ctx)
	if err != nil {
		return false, err
	}
	if len(laneIDs) == 0 {
		return false, nil
	}
	for _, laneID := range laneIDs {
		if !identity.CanOperateLane(found.EventID, laneID) {
			return false, nil
		}
	}
	return true, nil
}

func onlyFields(mutation ent.Mutation, allowed ...string) bool {
	fields := mutation.Fields()
	if len(fields) == 0 {
		return false
	}
	accepted := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		accepted[field] = struct{}{}
	}
	for _, field := range fields {
		if _, ok := accepted[field]; !ok {
			return false
		}
	}
	return true
}

func allowLocationOwnedMutation() privacy.MutationRule {
	type locationOwnedMutation interface {
		LocationID() (int, bool)
		OldLocationID(context.Context) (int, error)
		Client() *beamersent.Client
	}
	return privacy.MutationRuleFunc(func(ctx context.Context, mutation ent.Mutation) error {
		identity, _ := viewer.FromContext(ctx)
		owned, ok := mutation.(locationOwnedMutation)
		if !ok {
			return privacy.Skip
		}
		locationID, exists := owned.LocationID()
		if !exists {
			var err error
			locationID, err = owned.OldLocationID(ctx)
			if err != nil {
				return privacy.Denyf("read mutation Location ownership: %v", err)
			}
		}
		found, err := owned.Client().Location.Get(
			privacy.DecisionContext(ctx, privacy.Allow),
			locationID,
		)
		if err != nil {
			return privacy.Denyf("read mutation Event ownership: %v", err)
		}
		if identity.CanProduceEvent(found.EventID) {
			return privacy.Allow
		}
		return privacy.Skip
	})
}

func allowLocationOwnedCreation() privacy.MutationRule {
	owned := allowLocationOwnedMutation()
	return privacy.MutationRuleFunc(func(ctx context.Context, mutation ent.Mutation) error {
		if !mutation.Op().Is(ent.OpCreate) {
			return privacy.Skip
		}
		return owned.EvalMutation(ctx, mutation)
	})
}

func allowLaneOwnedMutation() privacy.MutationRule {
	type laneOwnedMutation interface {
		LaneID() (int, bool)
		OldLaneID(context.Context) (int, error)
		Client() *beamersent.Client
	}
	return privacy.MutationRuleFunc(func(ctx context.Context, mutation ent.Mutation) error {
		identity, _ := viewer.FromContext(ctx)
		owned, ok := mutation.(laneOwnedMutation)
		if !ok {
			return privacy.Skip
		}
		laneID, exists := owned.LaneID()
		if !exists {
			var err error
			laneID, err = owned.OldLaneID(ctx)
			if err != nil {
				return privacy.Denyf("read mutation Lane ownership: %v", err)
			}
		}
		found, err := owned.Client().Lane.Get(
			privacy.DecisionContext(ctx, privacy.Allow),
			laneID,
		)
		if err != nil {
			return privacy.Denyf("read mutation Event ownership: %v", err)
		}
		if identity.CanProduceEvent(found.EventID) {
			return privacy.Allow
		}
		return privacy.Skip
	})
}

func allowLaneOwnedCreation() privacy.MutationRule {
	owned := allowLaneOwnedMutation()
	return privacy.MutationRuleFunc(func(ctx context.Context, mutation ent.Mutation) error {
		if !mutation.Op().Is(ent.OpCreate) {
			return privacy.Skip
		}
		return owned.EvalMutation(ctx, mutation)
	})
}

func allowTrackOwnedMutation() privacy.MutationRule {
	type trackOwnedMutation interface {
		TrackID() (int, bool)
		OldTrackID(context.Context) (int, error)
		Client() *beamersent.Client
	}
	return privacy.MutationRuleFunc(func(ctx context.Context, mutation ent.Mutation) error {
		identity, _ := viewer.FromContext(ctx)
		owned, ok := mutation.(trackOwnedMutation)
		if !ok {
			return privacy.Skip
		}
		trackID, exists := owned.TrackID()
		if !exists {
			var err error
			trackID, err = owned.OldTrackID(ctx)
			if err != nil {
				return privacy.Denyf("read mutation Track ownership: %v", err)
			}
		}
		found, err := owned.Client().Track.Get(
			privacy.DecisionContext(ctx, privacy.Allow),
			trackID,
		)
		if err != nil {
			return privacy.Denyf("read mutation Event ownership: %v", err)
		}
		if identity.CanProduceEvent(found.EventID) {
			return privacy.Allow
		}
		return privacy.Skip
	})
}

func allowTrackOwnedCreation() privacy.MutationRule {
	owned := allowTrackOwnedMutation()
	return privacy.MutationRuleFunc(func(ctx context.Context, mutation ent.Mutation) error {
		if !mutation.Op().Is(ent.OpCreate) {
			return privacy.Skip
		}
		return owned.EvalMutation(ctx, mutation)
	})
}

func allowSessionOwnedMutation() privacy.MutationRule {
	type sessionOwnedMutation interface {
		SessionID() (int, bool)
		OldSessionID(context.Context) (int, error)
		Client() *beamersent.Client
	}
	return privacy.MutationRuleFunc(func(ctx context.Context, mutation ent.Mutation) error {
		identity, _ := viewer.FromContext(ctx)
		owned, ok := mutation.(sessionOwnedMutation)
		if !ok {
			return privacy.Skip
		}
		sessionID, exists := owned.SessionID()
		if !exists {
			var err error
			sessionID, err = owned.OldSessionID(ctx)
			if err != nil {
				return privacy.Denyf("read mutation Session ownership: %v", err)
			}
		}
		found, err := owned.Client().Session.Get(
			privacy.DecisionContext(ctx, privacy.Allow),
			sessionID,
		)
		if err != nil {
			return privacy.Denyf("read mutation Event ownership: %v", err)
		}
		if identity.CanProduceEvent(found.EventID) {
			return privacy.Allow
		}
		return privacy.Skip
	})
}

func allowSessionOwnedCreation() privacy.MutationRule {
	owned := allowSessionOwnedMutation()
	return privacy.MutationRuleFunc(func(ctx context.Context, mutation ent.Mutation) error {
		if !mutation.Op().Is(ent.OpCreate) {
			return privacy.Skip
		}
		return owned.EvalMutation(ctx, mutation)
	})
}

func allowAdministratorMutation() privacy.MutationRule {
	return privacy.MutationRuleFunc(func(ctx context.Context, _ ent.Mutation) error {
		identity, _ := viewer.FromContext(ctx)
		if identity.Administrator {
			return privacy.Allow
		}
		return privacy.Skip
	})
}

func allowAuthenticatedAuditCreation() privacy.MutationRule {
	type actorMutation interface {
		ActorAccountID() (int, bool)
	}
	return privacy.MutationRuleFunc(func(ctx context.Context, mutation ent.Mutation) error {
		identity, _ := viewer.FromContext(ctx)
		actor, ok := mutation.(actorMutation)
		if !ok {
			return privacy.Skip
		}
		actorID, hasActor := actor.ActorAccountID()
		if hasActor && mutation.Op().Is(ent.OpCreate) && actorID == identity.AccountID {
			return privacy.Allow
		}
		return privacy.Skip
	})
}

func systemOnlyPolicy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), privacy.AlwaysDenyRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), privacy.AlwaysDenyRule(),
		},
	}
}

func appendOnlySystemPolicy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), privacy.AlwaysDenyRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), privacy.AlwaysDenyRule(),
		},
	}
}
