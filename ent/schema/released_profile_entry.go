package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/field"
)

// ReleasedProfileEntry is the public-safe Entry identity projection.
type ReleasedProfileEntry struct {
	ent.Schema
}

// Mixin applies the fail-closed authorization tripwire to ReleasedProfileEntry.
func (ReleasedProfileEntry) Mixin() []ent.Mixin {
	return []ent.Mixin{AuthorizationTripwire{}}
}

// Policy permits public reads and confines writes to Results Publication paths.
func (ReleasedProfileEntry) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			privacy.AlwaysAllowRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields defines the released Entry identity.
func (ReleasedProfileEntry) Fields() []ent.Field {
	return []ent.Field{
		field.Int("entry_id").Positive().Unique().Immutable(),
		field.String("name").NotEmpty(),
	}
}
