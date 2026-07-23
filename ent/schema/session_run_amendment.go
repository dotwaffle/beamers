package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SessionRunAmendment preserves one immutable Live Detail Correction.
type SessionRunAmendment struct {
	ent.Schema
}

// Policy keeps Run Amendment evidence behind application services.
func (SessionRunAmendment) Policy() ent.Policy {
	return appendOnlySystemPolicy()
}

// Indexes supports ordered amendment history lookup by Run.
func (SessionRunAmendment) Indexes() []ent.Index {
	return []ent.Index{index.Fields("session_run_id")}
}

// Fields defines immutable correction evidence.
func (SessionRunAmendment) Fields() []ent.Field {
	return []ent.Field{
		field.Int("session_run_id").Immutable(),
		field.Int("actor_account_id").Positive().Immutable(),
		field.Text("details_json").NotEmpty().Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges defines the amended Run.
func (SessionRunAmendment) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("session_run", SessionRun.Type).
			Ref("amendments").
			Field("session_run_id").
			Unique().
			Immutable().
			Required(),
	}
}
