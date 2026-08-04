package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Installation records the durable identity of an initialized Beamers data
// directory.
type Installation struct {
	ent.Schema
}

// Mixin applies the fail-closed authorization tripwire to Installation.
func (Installation) Mixin() []ent.Mixin {
	return []ent.Mixin{AuthorizationTripwire{}}
}

// Fields defines installation persistence.
func (Installation) Fields() []ent.Field {
	return []ent.Field{
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Int("active_event_id").Optional().Nillable(),
		field.Int("activation_generation").Default(0).NonNegative(),
		field.Int("active_theme_revision_id").Optional().Nillable(),
	}
}

// Edges defines installation-wide routing state.
func (Installation) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("active_event", Event.Type).
			Field("active_event_id").
			Unique(),
		edge.To("active_theme_revision", InstallationThemeRevision.Type).
			Field("active_theme_revision_id").
			Unique(),
	}
}
