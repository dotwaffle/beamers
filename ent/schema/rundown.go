package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Rundown stores the independent Draft and Published revisions for one Event.
type Rundown struct {
	ent.Schema
}

// Policy confines Rundown access to granted Event roles.
func (Rundown) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), allowSystemViewer(), filterGrantedRundowns(), privacy.AlwaysAllowRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowSystemViewer(), allowEventOwnedMutation(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields defines Rundown persistence.
func (Rundown) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Unique().Immutable(),
		field.Int("draft_revision").Default(0).NonNegative(),
		field.Int("published_revision").Default(0).NonNegative(),
	}
}

// Edges defines Rundown relationships.
func (Rundown) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("rundown").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
	}
}
