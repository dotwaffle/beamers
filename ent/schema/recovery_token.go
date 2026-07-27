package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// RecoveryToken records one short-lived Administrator-assisted recovery digest.
type RecoveryToken struct {
	ent.Schema
}

// Policy keeps Recovery Tokens inside authentication storage paths.
func (RecoveryToken) Policy() ent.Policy {
	return systemOnlyPolicy()
}

// Fields defines Recovery Token persistence.
func (RecoveryToken) Fields() []ent.Field {
	return []ent.Field{
		field.Int("account_id").Immutable(),
		field.String("token_hash").MinLen(64).MaxLen(64).Unique().Immutable().Sensitive(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("expires_at").Immutable(),
		field.Time("used_at").Optional().Nillable(),
	}
}

// Edges defines Recovery Token relationships.
func (RecoveryToken) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("account", Account.Type).
			Ref("recovery_tokens").
			Field("account_id").
			Unique().
			Immutable().
			Required(),
	}
}
