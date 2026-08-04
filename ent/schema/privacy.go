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

// Hooks denies every mutation that names no authorization. Ent evaluates an
// explicit decision carried in the context before it reaches any policy rule,
// so the policy alone can be waved through by a bare allow decision. The hook
// runs unconditionally, like the AppendOnly schema invariant, which is what
// makes naming the actor the only way past the tripwire.
func (AuthorizationTripwire) Hooks() []ent.Hook {
	return []ent.Hook{denyUnnamedMutation}
}

// Interceptors denies every query that names no authorization, for the same
// reason Hooks exists: interceptors run ahead of, and independently of, the
// privacy policy the context can short-circuit.
func (AuthorizationTripwire) Interceptors() []ent.Interceptor {
	return []ent.Interceptor{denyUnnamedQuery()}
}

func denyUnnamedMutation(next ent.Mutator) ent.Mutator {
	return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
		if !authorizationNamed(ctx) {
			return nil, errUnnamedAuthorization(mutation.Type())
		}
		return next.Mutate(ctx, mutation)
	})
}

func denyUnnamedQuery() ent.Interceptor {
	return ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, query ent.Query) (ent.Value, error) {
			if !authorizationNamed(ctx) {
				return nil, errUnnamedAuthorization("query")
			}
			return next.Query(ctx, query)
		})
	})
}

func denyUnnamedAuthorization() privacy.QueryMutationRule {
	return privacy.ContextQueryMutationRule(func(ctx context.Context) error {
		if authorizationNamed(ctx) {
			return privacy.Skip
		}
		return errUnnamedAuthorization("")
	})
}

// errUnnamedAuthorization names the refusal every tripwire path returns, so a
// denial reads the same whether the policy, the hook, or the interceptor
// caught it.
func errUnnamedAuthorization(subject string) error {
	if subject != "" {
		subject = " " + subject
	}
	return privacy.Denyf(
		"authorization was never named: the%s context carries neither a "+
			"viewer identity nor a named System Actor", subject,
	)
}

// authorizationNamed reports whether the context says who is acting.
func authorizationNamed(ctx context.Context) bool {
	if identity, ok := viewer.FromContext(ctx); ok && identity.AccountID > 0 {
		return true
	}
	_, named := systemactor.FromContext(ctx)
	return named
}
