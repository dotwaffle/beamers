package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// AccountSession records one revocable, expiring authenticated session.
type AccountSession struct {
	ent.Schema
}

// Fields defines AccountSession persistence.
func (AccountSession) Fields() []ent.Field {
	return []ent.Field{
		field.Int("account_id").Immutable(),
		field.String("token_hash").MinLen(64).MaxLen(64).Unique().Immutable().Sensitive(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("expires_at").Immutable(),
		field.Time("revoked_at").Optional().Nillable(),
	}
}

// Edges defines AccountSession relationships.
func (AccountSession) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("account", Account.Type).
			Ref("sessions").
			Field("account_id").
			Unique().
			Immutable().
			Required(),
	}
}
