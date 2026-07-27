package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// RecoveryCode records one single-use Account recovery secret digest.
type RecoveryCode struct {
	ent.Schema
}

// Policy keeps Recovery Codes inside authentication storage paths.
func (RecoveryCode) Policy() ent.Policy {
	return systemOnlyPolicy()
}

// Fields defines Recovery Code persistence.
func (RecoveryCode) Fields() []ent.Field {
	return []ent.Field{
		field.Int("account_id").Immutable(),
		field.String("code_hash").MinLen(64).MaxLen(64).Unique().Immutable().Sensitive(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("used_at").Optional().Nillable(),
	}
}

// Edges defines Recovery Code relationships.
func (RecoveryCode) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("account", Account.Type).
			Ref("recovery_codes").
			Field("account_id").
			Unique().
			Immutable().
			Required(),
	}
}
