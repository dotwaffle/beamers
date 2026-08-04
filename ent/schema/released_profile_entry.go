package schema

import (
	"entgo.io/ent"
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

// Fields defines the released Entry identity.
func (ReleasedProfileEntry) Fields() []ent.Field {
	return []ent.Field{
		field.Int("entry_id").Positive().Unique().Immutable(),
		field.String("name").NotEmpty(),
	}
}
