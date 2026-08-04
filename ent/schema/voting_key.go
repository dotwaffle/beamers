package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// VotingKey is one Event-scoped single-use bearer code.
type VotingKey struct {
	ent.Schema
}

// Mixin applies the fail-closed authorization tripwire to VotingKey.
func (VotingKey) Mixin() []ent.Mixin {
	return []ent.Mixin{AuthorizationTripwire{}}
}

// Fields defines protected key and consumption state.
func (VotingKey) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.String("token_hash").MinLen(64).MaxLen(64).Unique().Immutable().Sensitive(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("expires_at").Immutable(),
		field.Time("revoked_at").Optional().Nillable(),
		field.Time("redeemed_at").Optional().Nillable(),
		field.Int("redeemed_by_account_id").Optional(),
	}
}

// Edges defines Event scope and the eventual redeeming Account.
func (VotingKey) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("voting_keys").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
		edge.From("redeemed_by", Account.Type).
			Ref("redeemed_voting_keys").
			Field("redeemed_by_account_id").
			Unique(),
	}
}

// Indexes supports bounded Event inventory reads.
func (VotingKey) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("event_id", "created_at"),
	}
}
