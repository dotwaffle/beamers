package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// LocationDraft stores the current editable state of one Location.
type LocationDraft struct {
	ent.Schema
}

// Mixin applies the fail-closed authorization tripwire to LocationDraft.
func (LocationDraft) Mixin() []ent.Mixin {
	return []ent.Mixin{AuthorizationTripwire{}}
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
