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

func allowSystemViewer() privacy.QueryMutationRule {
	return privacy.ContextQueryMutationRule(func(ctx context.Context) error {
		identity, _ := viewer.FromContext(ctx)
		if identity.System {
			return privacy.Allow
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
		ids, err := grantedEventIDs(ctx)
		if err != nil {
			return err
		}
		filter, ok := query.(selectorFilter)
		if !ok {
			return privacy.Denyf("unexpected Lane query %T", query)
		}
		filter.Where(func(selector *sql.Selector) {
			selector.Where(sql.InInts(selector.C("event_id"), ids...))
		})
		return privacy.Skip
	})
}

func filterGrantedLaneDrafts() privacy.QueryRule {
	type selectorFilter interface {
		Where(...predicate.LaneDraft) *beamersent.LaneDraftQuery
	}
	return eventQueryRule(func(ctx context.Context, query ent.Query) error {
		ids, err := grantedEventIDs(ctx)
		if err != nil {
			return err
		}
		filter, ok := query.(selectorFilter)
		if !ok {
			return privacy.Denyf("unexpected Lane Draft query %T", query)
		}
		filter.Where(lanedraft.HasLaneWith(lane.EventIDIn(ids...)))
		return privacy.Skip
	})
}

func filterGrantedLanePublishedVersions() privacy.QueryRule {
	type selectorFilter interface {
		Where(...predicate.LanePublishedVersion) *beamersent.LanePublishedVersionQuery
	}
	return eventQueryRule(func(ctx context.Context, query ent.Query) error {
		ids, err := grantedEventIDs(ctx)
		if err != nil {
			return err
		}
		filter, ok := query.(selectorFilter)
		if !ok {
			return privacy.Denyf("unexpected Published Lane version query %T", query)
		}
		filter.Where(lanepublishedversion.HasLaneWith(lane.EventIDIn(ids...)))
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
		ids, err := grantedEventIDs(ctx)
		if err != nil {
			return err
		}
		filter, ok := query.(selectorFilter)
		if !ok {
			return privacy.Denyf("unexpected Session query %T", query)
		}
		filter.Where(func(selector *sql.Selector) {
			selector.Where(sql.InInts(selector.C("event_id"), ids...))
		})
		return privacy.Skip
	})
}

func filterGrantedSessionDrafts() privacy.QueryRule {
	type selectorFilter interface {
		Where(...predicate.SessionDraft) *beamersent.SessionDraftQuery
	}
	return eventQueryRule(func(ctx context.Context, query ent.Query) error {
		ids, err := grantedEventIDs(ctx)
		if err != nil {
			return err
		}
		filter, ok := query.(selectorFilter)
		if !ok {
			return privacy.Denyf("unexpected Session Draft query %T", query)
		}
		filter.Where(sessiondraft.HasSessionWith(session.EventIDIn(ids...)))
		return privacy.Skip
	})
}

func filterGrantedSessionPublishedVersions() privacy.QueryRule {
	type selectorFilter interface {
		Where(...predicate.SessionPublishedVersion) *beamersent.SessionPublishedVersionQuery
	}
	return eventQueryRule(func(ctx context.Context, query ent.Query) error {
		ids, err := grantedEventIDs(ctx)
		if err != nil {
			return err
		}
		filter, ok := query.(selectorFilter)
		if !ok {
			return privacy.Denyf("unexpected Published Session version query %T", query)
		}
		filter.Where(sessionpublishedversion.HasSessionWith(session.EventIDIn(ids...)))
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
			denyMissingViewer(), allowSystemViewer(), privacy.AlwaysDenyRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowSystemViewer(), privacy.AlwaysDenyRule(),
		},
	}
}

func allowSystemCreation() privacy.MutationRule {
	return privacy.MutationRuleFunc(func(ctx context.Context, mutation ent.Mutation) error {
		identity, _ := viewer.FromContext(ctx)
		if identity.System && mutation.Op().Is(ent.OpCreate) {
			return privacy.Allow
		}
		return privacy.Skip
	})
}
