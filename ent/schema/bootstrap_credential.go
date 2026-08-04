package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// BootstrapCredential records a short-lived, single-use first-Administrator credential.
type BootstrapCredential struct {
	ent.Schema
}

// Mixin applies the fail-closed authorization tripwire to BootstrapCredential.
func (BootstrapCredential) Mixin() []ent.Mixin {
	return []ent.Mixin{AuthorizationTripwire{}}
}

// Fields defines BootstrapCredential persistence.
func (BootstrapCredential) Fields() []ent.Field {
	return []ent.Field{
		field.String("token_hash").MinLen(64).MaxLen(64).Unique().Immutable().Sensitive(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("expires_at").Immutable(),
		field.Time("used_at").Optional().Nillable(),
	}
}
