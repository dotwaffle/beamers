package schema

import (
	"context"

	"entgo.io/ent"
	"entgo.io/ent/dialect/sql"
	"entgo.io/ent/privacy"

	beamersent "github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/predicate"
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
