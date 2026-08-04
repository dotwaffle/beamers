package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// AccountPreference records browser presentation preferences.
type AccountPreference struct {
	ent.Schema
}

// Mixin applies the fail-closed authorization tripwire to AccountPreference.
func (AccountPreference) Mixin() []ent.Mixin {
	return []ent.Mixin{AuthorizationTripwire{}}
}

// Fields defines Account preference persistence.
func (AccountPreference) Fields() []ent.Field {
	return []ent.Field{
		field.Int("account_id").Immutable(),
		field.Bool("reduced_effects").Default(false),
	}
}

// Edges defines the owning Account.
func (AccountPreference) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("account", Account.Type).
			Ref("preference").
			Field("account_id").
			Unique().
			Immutable().
			Required(),
	}
}
