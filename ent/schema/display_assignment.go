package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// DisplayAssignment binds one Display to one Event Location and normal View.
type DisplayAssignment struct {
	ent.Schema
}

// Policy keeps Assignment access behind the Display application module.
func (DisplayAssignment) Policy() ent.Policy {
	return systemOnlyPolicy()
}

// Fields defines Event-specific Display routing.
func (DisplayAssignment) Fields() []ent.Field {
	return []ent.Field{
		field.Int("display_id").Immutable(),
		field.Int("event_id").Immutable(),
		field.Int("location_id"),
		field.String("view_key").NotEmpty().MaxLen(100),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now),
	}
}

// Edges defines durable Display, Event, and Location references.
func (DisplayAssignment) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("display", Display.Type).
			Ref("assignments").
			Field("display_id").
			Unique().
			Immutable().
			Required(),
		edge.From("event", Event.Type).
			Ref("display_assignments").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
		edge.From("location", Location.Type).
			Ref("display_assignments").
			Field("location_id").
			Unique().
			Required(),
	}
}

// Indexes prevents two Assignments for one Display and Event.
func (DisplayAssignment) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("display_id", "event_id").Unique(),
		index.Fields("event_id", "location_id"),
	}
}
