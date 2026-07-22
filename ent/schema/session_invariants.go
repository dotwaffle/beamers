package schema

import (
	"context"
	"errors"

	"entgo.io/ent"
	"entgo.io/ent/privacy"

	beamersent "github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/lane"
	"github.com/dotwaffle/beamers/ent/location"
	"github.com/dotwaffle/beamers/ent/track"
)

var errCrossEventSessionMembership = errors.New("Session memberships must belong to the same Event")

type sessionMembershipMutation interface {
	SessionID() (int, bool)
	OldSessionID(context.Context) (int, error)
	LanesIDs() []int
	LocationsIDs() []int
	TracksIDs() []int
	Client() *beamersent.Client
}

func validateSessionMembershipOwnership(next ent.Mutator) ent.Mutator {
	return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
		if !mutation.Op().Is(ent.OpCreate | ent.OpUpdate | ent.OpUpdateOne) {
			return next.Mutate(ctx, mutation)
		}
		membership, ok := mutation.(sessionMembershipMutation)
		if !ok {
			return nil, errors.New("unexpected Session membership mutation")
		}
		sessionID, exists := membership.SessionID()
		if !exists {
			var err error
			sessionID, err = membership.OldSessionID(ctx)
			if err != nil {
				return nil, err
			}
		}
		internalContext := privacy.DecisionContext(ctx, privacy.Allow)
		session, err := membership.Client().Session.Get(internalContext, sessionID)
		if err != nil {
			return nil, err
		}
		lanes, err := membership.Client().Lane.Query().
			Where(lane.IDIn(membership.LanesIDs()...), lane.EventIDEQ(session.EventID)).
			Count(internalContext)
		if err != nil {
			return nil, err
		}
		locations, err := membership.Client().Location.Query().
			Where(location.IDIn(membership.LocationsIDs()...), location.EventIDEQ(session.EventID)).
			Count(internalContext)
		if err != nil {
			return nil, err
		}
		tracks, err := membership.Client().Track.Query().
			Where(track.IDIn(membership.TracksIDs()...), track.EventIDEQ(session.EventID)).
			Count(internalContext)
		if err != nil {
			return nil, err
		}
		if lanes != len(membership.LanesIDs()) ||
			locations != len(membership.LocationsIDs()) ||
			tracks != len(membership.TracksIDs()) {
			return nil, errCrossEventSessionMembership
		}
		return next.Mutate(ctx, mutation)
	})
}
