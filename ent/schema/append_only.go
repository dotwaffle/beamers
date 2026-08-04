package schema

import (
	"context"
	"errors"
	"fmt"

	"entgo.io/ent"
	"entgo.io/ent/schema/mixin"
)

// ErrAppendOnly reports that a mutation attempted to update or delete an
// append-only entity. Callers should use errors.Is to distinguish this
// schema invariant from an authorization denial.
var ErrAppendOnly = errors.New("append-only entity refuses update and delete")

// AppendOnly carries the mutation hook that machine-enforces append-only
// storage: published-version entities and Results Publications may only be
// created, never updated or deleted. This is a schema invariant, not an
// authorization decision: the hook runs unconditionally for every mutation
// reaching this entity and is never consulted by, or bypassable through, the
// AuthorizationTripwire privacy policy or an explicit store allow decision.
type AppendOnly struct {
	mixin.Schema
}

// Hooks denies every Update, UpdateOne, Delete, and DeleteOne mutation.
func (AppendOnly) Hooks() []ent.Hook {
	return []ent.Hook{denyAppendOnlyMutation}
}

func denyAppendOnlyMutation(next ent.Mutator) ent.Mutator {
	return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
		if mutation.Op().Is(ent.OpUpdate | ent.OpUpdateOne | ent.OpDelete | ent.OpDeleteOne) {
			return nil, fmt.Errorf("%s %s: %w", mutation.Type(), mutation.Op(), ErrAppendOnly)
		}
		return next.Mutate(ctx, mutation)
	})
}
