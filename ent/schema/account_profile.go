package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"

	"github.com/dotwaffle/beamers/internal/profilevalue"
)

// AccountProfile records private-by-default attendee Profile settings.
type AccountProfile struct {
	ent.Schema
}

// Mixin applies the fail-closed authorization tripwire to AccountProfile.
func (AccountProfile) Mixin() []ent.Mixin {
	return []ent.Mixin{AuthorizationTripwire{}}
}

// Policy confines Profile persistence to application services.
func (AccountProfile) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			filterVisibleAccountProfiles(), privacy.AlwaysAllowRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowAdministratorMutation(),
			allowOwnAccountProfileMutation(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields defines Profile persistence.
func (AccountProfile) Fields() []ent.Field {
	return []ent.Field{
		field.Int("account_id").Unique().Immutable(),
		field.String("normalized_handle").NotEmpty().MaxLen(200).Unique().Immutable(),
		field.String("display_name").NotEmpty().MaxLen(200),
		field.Bool("published").Default(false),
		field.JSON("selected_entries", []profilevalue.Entry{}).Optional(),
	}
}

// Edges defines the owning Account.
func (AccountProfile) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("account", Account.Type).
			Ref("profile").
			Field("account_id").
			Unique().
			Immutable().
			Required(),
	}
}
