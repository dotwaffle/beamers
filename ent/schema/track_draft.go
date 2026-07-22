package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// TrackDraft stores the current editable state of one Track.
type TrackDraft struct {
	ent.Schema
}

// Policy confines Track Draft access to granted Event roles.
func (TrackDraft) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), filterGrantedTrackDrafts(), privacy.AlwaysAllowRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowTrackOwnedMutation(), privacy.AlwaysDenyRule(),
		},
	}
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
