package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// AuditEntry is an immutable record of a relevant authenticated action.
type AuditEntry struct {
	ent.Schema
}

// Policy keeps Audit Entries append-only and Administrator-readable.
func (AuditEntry) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), allowSystemViewer(), allowAdministrator(), privacy.AlwaysDenyRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowSystemCreation(), allowAuthenticatedAuditCreation(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields defines Audit Entry persistence.
func (AuditEntry) Fields() []ent.Field {
	return []ent.Field{
		field.Int("actor_account_id").Immutable(),
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
			Immutable().
			Required(),
	}
}
