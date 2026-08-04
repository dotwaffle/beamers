package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/privacy"
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

// Policy confines Registration Policy persistence to application services.
func (RegistrationPolicy) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			privacy.AlwaysAllowRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowAdministratorMutation(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields defines Registration Policy persistence.
func (RegistrationPolicy) Fields() []ent.Field {
	return []ent.Field{
		field.Bool("registration_open").Default(true),
	}
}
