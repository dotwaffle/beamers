package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// WebAuthnCredential records one independently revocable public-key Credential.
type WebAuthnCredential struct {
	ent.Schema
}

// Mixin applies the fail-closed authorization tripwire to WebAuthnCredential.
func (WebAuthnCredential) Mixin() []ent.Mixin {
	return []ent.Mixin{AuthorizationTripwire{}}
}

// Fields defines WebAuthn Credential persistence.
func (WebAuthnCredential) Fields() []ent.Field {
	return []ent.Field{
		field.Int("account_id").Immutable(),
		field.Bytes("credential_id").NotEmpty().Immutable().Sensitive(),
		field.String("name").NotEmpty().MaxLen(200),
		field.Bytes("credential").NotEmpty().Sensitive(),
		field.String("attachment").Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("last_used_at").Optional().Nillable(),
		field.Time("revoked_at").Optional().Nillable(),
	}
}

// Edges defines WebAuthn Credential ownership.
func (WebAuthnCredential) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("account", Account.Type).
			Ref("webauthn_credentials").
			Field("account_id").
			Unique().
			Immutable().
			Required(),
	}
}

// Indexes prevents one authenticator Credential from belonging to multiple Accounts.
func (WebAuthnCredential) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("credential_id").Unique(),
	}
}
