package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// AuditEntry is an immutable record of a relevant authenticated action.
type AuditEntry struct {
	ent.Schema
}

// Mixin applies the fail-closed authorization tripwire to AuditEntry.
func (AuditEntry) Mixin() []ent.Mixin {
	return []ent.Mixin{AuthorizationTripwire{}}
}

// Fields defines Audit Entry persistence.
func (AuditEntry) Fields() []ent.Field {
	return []ent.Field{
		field.Int("actor_account_id").Optional().Immutable(),
		field.Enum("actor_kind").
			Values("Account", "UploadLink", "Host").
			Default("Account").
			Immutable(),
		// Retain the former identifier only to render immutable historical evidence.
		field.Int("actor_upload_link_id").Optional().Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.String("action").NotEmpty().MaxLen(100).Immutable(),
		field.String("target_type").NotEmpty().MaxLen(100).Immutable(),
		field.String("target_id").NotEmpty().MaxLen(100).Immutable(),
		field.Enum("result").Values("Succeeded", "Rejected").Immutable(),
		field.String("reason").Optional().MaxLen(1000).Immutable(),
		field.String("note").Optional().MaxLen(1000).Immutable(),
	}
}

// Edges defines Audit Entry relationships.
func (AuditEntry) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("actor", Account.Type).
			Ref("audit_entries").
			Field("actor_account_id").
			Unique().
			Immutable(),
	}
}
