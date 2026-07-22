package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// LocationDraft stores the current editable state of one Location.
type LocationDraft struct {
	ent.Schema
}

// Policy confines Location Draft access to granted Event roles.
func (LocationDraft) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), allowSystemViewer(), filterGrantedLocationDrafts(), privacy.AlwaysAllowRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowSystemViewer(), allowLocationOwnedMutation(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields defines Location Draft persistence.
func (LocationDraft) Fields() []ent.Field {
	return []ent.Field{
		field.Int("location_id").Unique().Immutable(),
		field.String("name").NotEmpty().MaxLen(200),
		field.Bool("retired").Default(false),
	}
}

// Edges defines Location Draft relationships.
func (LocationDraft) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("location", Location.Type).
			Ref("draft").
			Field("location_id").
			Unique().
			Immutable().
			Required(),
	}
}
