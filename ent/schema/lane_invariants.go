package schema

import (
	"context"
	"errors"

	"entgo.io/ent"
	"entgo.io/ent/privacy"

	beamersent "github.com/dotwaffle/beamers/ent"
)

var errCrossEventLaneLocation = errors.New("lane and location must belong to the same Event")

type laneLocationMutation interface {
	LaneID() (int, bool)
	OldLaneID(context.Context) (int, error)
	LocationID() (int, bool)
	OldLocationID(context.Context) (int, error)
	Client() *beamersent.Client
}

func validateLaneLocationOwnership(next ent.Mutator) ent.Mutator {
	return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
		if !mutation.Op().Is(ent.OpCreate | ent.OpUpdate | ent.OpUpdateOne) {
			return next.Mutate(ctx, mutation)
		}
		placement, ok := mutation.(laneLocationMutation)
		if !ok {
			return nil, errors.New("unexpected lane placement mutation")
		}
		laneID, exists := placement.LaneID()
		if !exists {
			var err error
			laneID, err = placement.OldLaneID(ctx)
			if err != nil {
				return nil, err
			}
		}
		locationID, exists := placement.LocationID()
		if !exists {
			var err error
			locationID, err = placement.OldLocationID(ctx)
			if err != nil {
				return nil, err
			}
		}
		internalContext := privacy.DecisionContext(ctx, privacy.Allow)
		lane, err := placement.Client().Lane.Get(internalContext, laneID)
		if err != nil {
			return nil, err
		}
		location, err := placement.Client().Location.Get(internalContext, locationID)
		if err != nil {
			return nil, err
		}
		if lane.EventID != location.EventID {
			return nil, errCrossEventLaneLocation
		}
		return next.Mutate(ctx, mutation)
	})
}
