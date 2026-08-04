package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// EventSlug reserves one current or retained public Event URL identifier.
type EventSlug struct {
	ent.Schema
}

// Mixin applies the fail-closed authorization tripwire to EventSlug.
func (EventSlug) Mixin() []ent.Mixin {
	return []ent.Mixin{AuthorizationTripwire{}}
}

// Fields defines Event Slug persistence.
func (EventSlug) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.String("slug").NotEmpty().MaxLen(200).Unique().Immutable(),
		field.Bool("exposed").Default(false),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges defines Event Slug relationships.
func (EventSlug) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("slugs").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
	}
}
