package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// DraftChangeDependency records one transitive Publish prerequisite edge.
type DraftChangeDependency struct {
	ent.Schema
}

// Mixin applies the fail-closed authorization tripwire to DraftChangeDependency.
func (DraftChangeDependency) Mixin() []ent.Mixin {
	return []ent.Mixin{AuthorizationTripwire{}}
}

// Fields defines Draft Change dependency persistence.
func (DraftChangeDependency) Fields() []ent.Field {
	return []ent.Field{
		field.Int("change_id").Immutable(),
		field.Int("depends_on_id").Immutable(),
	}
}

// Edges defines dependency endpoints.
func (DraftChangeDependency) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("change", DraftChange.Type).
			Ref("dependencies").
			Field("change_id").
			Unique().
			Immutable().
			Required(),
		edge.From("depends_on", DraftChange.Type).
			Ref("dependents").
			Field("depends_on_id").
			Unique().
			Immutable().
			Required(),
	}
}

// Indexes prevents duplicate dependency edges.
func (DraftChangeDependency) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("change_id", "depends_on_id").Unique(),
	}
}
