package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// DisplayOverrideState selects the one current Override of a kind for a Display.
type DisplayOverrideState struct {
	ent.Schema
}

// Policy keeps Display Override state behind the Override application module.
func (DisplayOverrideState) Policy() ent.Policy {
	return systemOnlyPolicy()
}

// Fields defines a replace-in-place per-Display selection.
func (DisplayOverrideState) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.Int("display_id").Immutable(),
		field.Int("override_id"),
		field.Enum("kind").Values("StageMessage", "TechnicalDifficulties").Immutable(),
		field.Int("revision").Default(1).Positive(),
		field.Time("updated_at").Default(time.Now),
	}
}

// Edges defines selected Override and Display ownership.
func (DisplayOverrideState) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("display", Display.Type).
			Ref("override_states").
			Field("display_id").
			Unique().
			Immutable().
			Required(),
		edge.From("override", DisplayOverride.Type).
			Ref("states").
			Field("override_id").
			Unique().
			Required(),
	}
}

// Indexes enforce one active selection per Display and Override kind.
func (DisplayOverrideState) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("event_id", "display_id", "kind").Unique(),
		index.Fields("override_id"),
	}
}
