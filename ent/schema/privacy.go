package schema

import (
	"context"

	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/mixin"

	"github.com/dotwaffle/beamers/internal/viewer"
)

// AuthorizationTripwire carries the single global privacy rule every schema
// mixes in. Authorization is enforced at the store and command surface, so the
// Ent layer only refuses work that names no authorization at all.
type AuthorizationTripwire struct {
	mixin.Schema
}

// Policy denies every query and mutation whose context carries neither a viewer
// identity nor the store's explicit allow decision.
func (AuthorizationTripwire) Policy() ent.Policy {
	rule := denyUndecidedAuthorization()
	return privacy.Policy{
		Query:    privacy.QueryPolicy{rule},
		Mutation: privacy.MutationPolicy{rule},
	}
}

func denyUndecidedAuthorization() privacy.QueryMutationRule {
	return privacy.ContextQueryMutationRule(func(ctx context.Context) error {
		if _, ok := viewer.FromContext(ctx); ok {
			return privacy.Skip
		}
		return privacy.Denyf(
			"authorization was never decided: the context carries neither a " +
				"viewer identity nor an explicit store decision",
		)
	})
}
