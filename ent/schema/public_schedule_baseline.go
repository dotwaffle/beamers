package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// PublicScheduleBaseline marks the one immutable attendee schedule baseline for an Event.
type PublicScheduleBaseline struct {
	ent.Schema
}

// Mixin applies the fail-closed authorization tripwire to PublicScheduleBaseline.
func (PublicScheduleBaseline) Mixin() []ent.Mixin {
	return []ent.Mixin{AuthorizationTripwire{}}
}

// Fields defines immutable baseline capture evidence.
func (PublicScheduleBaseline) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Unique().Immutable(),
		field.Int("source_published_revision").NonNegative().Immutable(),
		field.Time("captured_at").Default(time.Now).Immutable(),
	}
}

// Edges defines Event ownership and immutable Session entries.
func (PublicScheduleBaseline) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("public_schedule_baseline").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
		edge.To("entries", PublicScheduleBaselineEntry.Type),
	}
}
