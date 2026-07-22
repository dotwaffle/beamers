package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Installation records the durable identity of an initialized Beamers data
// directory.
type Installation struct {
	ent.Schema
}

// Policy confines Active Event routing to Administrators and internal operations.
func (Installation) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), allowAdministrator(), privacy.AlwaysDenyRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowAdministratorMutation(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields defines installation persistence.
func (Installation) Fields() []ent.Field {
	return []ent.Field{
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Int("active_event_id").Optional().Nillable(),
		field.Int("activation_generation").Default(0).NonNegative(),
	}
}

// Edges defines installation-wide routing state.
func (Installation) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("active_event", Event.Type).
			Field("active_event_id").
			Unique(),
	}
}
