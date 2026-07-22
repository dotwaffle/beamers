package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Account records one installation-wide individual identity.
type Account struct {
	ent.Schema
}

// Policy confines Account administration and selection to Administrators.
func (Account) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), allowSystemViewer(), allowAdministrator(), privacy.AlwaysDenyRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowSystemViewer(), allowAdministratorMutation(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields defines Account persistence.
func (Account) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().MaxLen(200),
		field.String("normalized_name").NotEmpty().MaxLen(200).Unique().Immutable(),
		field.Bool("administrator").Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("disabled_at").Optional().Nillable(),
	}
}

// Edges defines Account relationships.
func (Account) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("password_credential", PasswordCredential.Type).Unique(),
		edge.To("sessions", AccountSession.Type),
		edge.To("event_grants", EventGrant.Type),
		edge.To("audit_entries", AuditEntry.Type),
		edge.To("command_receipts", CommandReceipt.Type),
	}
}
