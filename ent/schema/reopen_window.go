package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ReopenWindow is one bounded exception for an existing upload owner.
type ReopenWindow struct {
	ent.Schema
}

// Policy keeps Reopen Windows behind application services.
func (ReopenWindow) Policy() ent.Policy {
	return appendOnlySystemPolicy()
}

// Fields defines audited, automatically expiring access.
func (ReopenWindow) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.Enum("target_type").Values("Presentation", "Entry").Immutable(),
		field.Int("target_id").Positive().Immutable(),
		field.String("reason").NotEmpty().MaxLen(1000).Immutable(),
		field.Time("expires_at"),
		field.Time("closed_at").Optional(),
		field.Int("created_by_account_id").Positive().Immutable(),
		field.Int("revision").Default(1).Positive(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now),
	}
}

// Indexes supports current target access lookup.
func (ReopenWindow) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("event_id", "target_type", "target_id", "created_at"),
	}
}
