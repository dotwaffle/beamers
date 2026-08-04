package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// TrackDraft stores the current editable state of one Track.
type TrackDraft struct {
	ent.Schema
}

// Mixin applies the fail-closed authorization tripwire to TrackDraft.
func (TrackDraft) Mixin() []ent.Mixin {
	return []ent.Mixin{AuthorizationTripwire{}}
}

// Fields defines Track Draft persistence.
func (TrackDraft) Fields() []ent.Field {
	return []ent.Field{
		field.Int("track_id").Unique().Immutable(),
		field.String("name").NotEmpty().MaxLen(200),
		field.Bool("retired").Default(false),
	}
}

// Edges defines Track Draft relationships.
func (TrackDraft) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("track", Track.Type).
			Ref("draft").
			Field("track_id").
			Unique().
			Immutable().
			Required(),
	}
}
