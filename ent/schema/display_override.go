package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// DisplayOverride is one immutable activation targeting a logical Display Group.
type DisplayOverride struct {
	ent.Schema
}

// Policy keeps Override state behind the Override application module.
func (DisplayOverride) Policy() ent.Policy {
	return systemOnlyPolicy()
}

// Fields defines one auditable Override activation.
func (DisplayOverride) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.String("target_group_key").NotEmpty().MaxLen(100).Immutable(),
		field.Enum("kind").Values("StageMessage", "TechnicalDifficulties").Immutable(),
		field.String("text").NotEmpty().MaxLen(2000).Immutable(),
		field.Enum("emphasis").Values("Normal", "Attention", "Urgent").Default("Normal").Immutable(),
		field.String("preset_key").Optional().MaxLen(100).Immutable(),
		field.Bool("until_cleared").Default(false).Immutable(),
		field.Time("expires_at").Optional().Nillable().Immutable(),
		field.Time("cleared_at").Optional().Nillable(),
		field.Int("revision").Default(1).Positive(),
		field.Int("created_by_account_id").Positive().Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges defines Event ownership and current per-Display state.
func (DisplayOverride) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("display_overrides").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
		edge.To("states", DisplayOverrideState.Type),
	}
}

// Indexes support active target and expiry queries.
func (DisplayOverride) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("event_id", "kind", "target_group_key", "created_at"),
		index.Fields("event_id", "cleared_at", "expires_at"),
	}
}
