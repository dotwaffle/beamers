package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// RegistrationPolicy records the installation-wide Account creation policy.
type RegistrationPolicy struct {
	ent.Schema
}

// Mixin applies the fail-closed authorization tripwire to RegistrationPolicy.
func (RegistrationPolicy) Mixin() []ent.Mixin {
	return []ent.Mixin{AuthorizationTripwire{}}
}

// Fields defines Registration Policy persistence.
func (RegistrationPolicy) Fields() []ent.Field {
	return []ent.Field{
		field.Bool("registration_open").Default(true),
	}
}
