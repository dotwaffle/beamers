package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Track is the stable identity of one thematic Session grouping.
type Track struct {
	ent.Schema
}

// Policy confines Track access to granted Event roles.
func (Track) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), allowSystemViewer(), filterGrantedTracks(), privacy.AlwaysAllowRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowSystemViewer(), allowEventOwnedMutation(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields defines stable Track identity persistence.
func (Track) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges defines Track relationships.
func (Track) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("tracks").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
		edge.To("draft", TrackDraft.Type).Unique(),
		edge.To("published_versions", TrackPublishedVersion.Type),
		edge.From("session_drafts", SessionDraft.Type).Ref("tracks"),
		edge.From("session_published_versions", SessionPublishedVersion.Type).Ref("tracks"),
	}
}
