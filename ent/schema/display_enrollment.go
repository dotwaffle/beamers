package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// DisplayEnrollment is one short-lived, single-use Display claim.
type DisplayEnrollment struct {
	ent.Schema
}

// Policy confines Enrollment secrets to Display application paths.
func (DisplayEnrollment) Policy() ent.Policy {
	return systemOnlyPolicy()
}

// Fields defines hashed claim and candidate credential values.
func (DisplayEnrollment) Fields() []ent.Field {
	return []ent.Field{
		field.String("code_hash").MinLen(64).MaxLen(64).Unique().Immutable().Sensitive(),
		field.String("credential_hash").MinLen(64).MaxLen(64).Unique().Immutable().Sensitive(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("expires_at").Immutable(),
		field.Time("used_at").Optional().Nillable(),
	}
}
