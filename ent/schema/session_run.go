package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SessionRun preserves one execution attempt and its immutable Published snapshot.
type SessionRun struct {
	ent.Schema
}

// Policy keeps Session Run persistence behind application services.
func (SessionRun) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), filterGrantedSessionRuns(), privacy.AlwaysAllowRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowScopedSessionRunMutation(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields defines durable Session Run history.
func (SessionRun) Fields() []ent.Field {
	return []ent.Field{
		field.Int("session_id").Immutable(),
		field.Time("actual_start").Immutable(),
		field.Time("actual_end").Optional(),
		field.Int("target_adjustment_seconds").Default(0),
		field.Time("target_adjusted_at").Optional(),
		field.Text("snapshot_json").NotEmpty().Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges defines Session Run ownership.
func (SessionRun) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("session", Session.Type).
			Ref("runs").
			Field("session_id").
			Unique().
			Immutable().
			Required(),
		edge.To("amendments", SessionRunAmendment.Type),
	}
}

// Indexes supports current-run lookup while retaining every completed Run.
func (SessionRun) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("session_id", "actual_end"),
	}
}
