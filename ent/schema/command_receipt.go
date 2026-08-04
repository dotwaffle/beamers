package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// CommandReceipt preserves one command's identity and committed outcome.
type CommandReceipt struct {
	ent.Schema
}

// Mixin applies the fail-closed authorization tripwire to CommandReceipt.
func (CommandReceipt) Mixin() []ent.Mixin {
	return []ent.Mixin{AuthorizationTripwire{}}
}

// Fields defines Command Receipt persistence.
func (CommandReceipt) Fields() []ent.Field {
	return []ent.Field{
		field.Int("actor_account_id").Optional().Immutable(),
		// UploadLink is retained only for immutable historical command identity.
		field.Enum("actor_kind").Values("Account", "UploadLink").Default("Account").Immutable(),
		field.Int("actor_upload_link_id").Optional().Immutable(),
		field.String("command_id").NotEmpty().MaxLen(200).Unique().Immutable(),
		field.String("payload_hash").NotEmpty().MaxLen(64).Immutable(),
		field.String("action").NotEmpty().MaxLen(100).Immutable(),
		field.String("target_type").NotEmpty().MaxLen(100).Immutable(),
		field.String("target_id").NotEmpty().MaxLen(100).Immutable(),
		field.String("outcome_json").NotEmpty().Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges defines Command Receipt relationships.
func (CommandReceipt) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("actor", Account.Type).
			Ref("command_receipts").
			Field("actor_account_id").
			Unique().
			Immutable(),
	}
}
