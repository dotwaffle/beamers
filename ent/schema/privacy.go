package schema

import (
	"context"

	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/mixin"

	"github.com/dotwaffle/beamers/internal/systemactor"
	"github.com/dotwaffle/beamers/internal/viewer"
)

// AuthorizationTripwire carries the single global privacy rule every schema
// mixes in. Authorization is enforced at the store and command surface, so the
// Ent layer only refuses work that names no authorization at all.
type AuthorizationTripwire struct {
	mixin.Schema
}

// Policy denies every query and mutation whose context names neither a viewer
// identity naming an Account nor a System Actor.
func (AuthorizationTripwire) Policy() ent.Policy {
	rule := denyUnnamedAuthorization()
	return privacy.Policy{
		Query:    privacy.QueryPolicy{rule},
		Mutation: privacy.MutationPolicy{rule},
	}
}

func denyUnnamedAuthorization() privacy.QueryMutationRule {
	return privacy.ContextQueryMutationRule(func(ctx context.Context) error {
		if authorizationNamed(ctx) {
			return privacy.Skip
		}
		return privacy.Denyf(
			"authorization was never named: the context carries neither a " +
				"viewer identity nor a named System Actor",
		)
	})
}

// authorizationNamed reports whether the context says who is acting.
func authorizationNamed(ctx context.Context) bool {
	if identity, ok := viewer.FromContext(ctx); ok && identity.AccountID > 0 {
		return true
	}
	_, named := systemactor.FromContext(ctx)
	return named
}
