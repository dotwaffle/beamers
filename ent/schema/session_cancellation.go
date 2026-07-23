package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SessionCancellation preserves one immutable cancellation command.
type SessionCancellation struct {
	ent.Schema
}

// Policy keeps cancellation history behind application services.
func (SessionCancellation) Policy() ent.Policy {
	return appendOnlySystemPolicy()
}

// Fields defines immutable cancellation evidence.
func (SessionCancellation) Fields() []ent.Field {
	return []ent.Field{
		field.Int("session_id").Immutable(),
		field.Int("session_run_id").Optional().Nillable().Immutable(),
		field.String("public_message").Optional().MaxLen(10000).Immutable(),
		field.String("crew_notes").Optional().MaxLen(10000).Immutable(),
		field.Time("forecast_start").Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges defines the canceled Session.
func (SessionCancellation) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("session", Session.Type).
			Ref("cancellations").
			Field("session_id").
			Unique().
			Immutable().
			Required(),
	}
}

// Indexes supports ordered history lookup by Session.
func (SessionCancellation) Indexes() []ent.Index {
	return []ent.Index{index.Fields("session_id", "created_at")}
}
