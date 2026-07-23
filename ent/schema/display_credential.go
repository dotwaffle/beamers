package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// DisplayCredential authenticates one enrolled Display without Crew authority.
type DisplayCredential struct {
	ent.Schema
}

// Policy confines Display credentials to authentication storage paths.
func (DisplayCredential) Policy() ent.Policy {
	return systemOnlyPolicy()
}

// Fields defines a hashed persistent Display credential.
func (DisplayCredential) Fields() []ent.Field {
	return []ent.Field{
		field.Int("display_id").Immutable(),
		field.String("token_hash").MinLen(64).MaxLen(64).Unique().Immutable().Sensitive(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("revoked_at").Optional().Nillable(),
	}
}

// Edges defines Display ownership.
func (DisplayCredential) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("display", Display.Type).
			Ref("credentials").
			Field("display_id").
			Unique().
			Immutable().
			Required(),
	}
}
