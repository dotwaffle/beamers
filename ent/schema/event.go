package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Event records one live gathering and its local-time configuration.
type Event struct {
	ent.Schema
}

// Policy makes Event Grants the final read and mutation authorization boundary.
func (Event) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), allowSystemViewer(), filterGrantedEvents(), privacy.AlwaysAllowRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowSystemViewer(), allowEventMutation(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields defines Event persistence.
func (Event) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().MaxLen(200),
		field.String("planned_start_date").NotEmpty().MaxLen(10),
		field.String("planned_end_date").NotEmpty().MaxLen(10),
		field.String("timezone").NotEmpty().MaxLen(200),
		field.String("event_locale").NotEmpty().MaxLen(100),
		field.String("content_language").Optional().MaxLen(100),
		field.String("event_day_boundary").NotEmpty().MaxLen(5),
		field.Int("revision").Default(1),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges defines Event relationships.
func (Event) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("grants", EventGrant.Type),
		edge.To("rundown", Rundown.Type).Unique(),
		edge.To("locations", Location.Type),
		edge.To("lanes", Lane.Type),
		edge.To("tracks", Track.Type),
		edge.To("sessions", Session.Type),
		edge.To("draft_edits", DraftEdit.Type),
		edge.To("draft_changes", DraftChange.Type),
	}
}
