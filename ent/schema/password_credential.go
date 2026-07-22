package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// PasswordCredential records the revocable password hash for one Account.
type PasswordCredential struct {
	ent.Schema
}

// Policy keeps password credential access inside authentication storage paths.
func (PasswordCredential) Policy() ent.Policy {
	return systemOnlyPolicy()
}

// Fields defines PasswordCredential persistence.
func (PasswordCredential) Fields() []ent.Field {
	return []ent.Field{
		field.Int("account_id").Unique().Immutable(),
		field.String("password_hash").NotEmpty().Sensitive(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("revoked_at").Optional().Nillable(),
	}
}

// Edges defines PasswordCredential relationships.
func (PasswordCredential) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("account", Account.Type).
			Ref("password_credential").
			Field("account_id").
			Unique().
			Immutable().
			Required(),
	}
}
