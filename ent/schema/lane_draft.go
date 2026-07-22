package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// LaneDraft stores the current editable state of one Lane.
type LaneDraft struct {
	ent.Schema
}

// Hooks enforce Event ownership across the editable Lane placement.
func (LaneDraft) Hooks() []ent.Hook {
	return []ent.Hook{validateLaneLocationOwnership}
}

// Policy confines Lane Draft access to granted Event roles.
func (LaneDraft) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), filterGrantedLaneDrafts(), privacy.AlwaysAllowRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowLaneOwnedMutation(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields defines Lane Draft persistence.
func (LaneDraft) Fields() []ent.Field {
	return []ent.Field{
		field.Int("lane_id").Unique().Immutable(),
		field.Int("location_id"),
		field.String("name").NotEmpty().MaxLen(200),
		field.Bool("retired").Default(false),
	}
}

// Edges defines Lane Draft relationships.
func (LaneDraft) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("lane", Lane.Type).
			Ref("draft").
			Field("lane_id").
			Unique().
			Immutable().
			Required(),
		edge.From("location", Location.Type).
			Ref("lane_drafts").
			Field("location_id").
			Unique().
			Required(),
	}
}

// Indexes keeps each active Draft Location bound to at most one Lane.
func (LaneDraft) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("location_id").
			Unique().
			Annotations(entsql.IndexWhere("NOT retired")),
	}
}
