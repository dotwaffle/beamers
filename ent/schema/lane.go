package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Lane is the stable identity of one independently progressing Session sequence.
type Lane struct {
	ent.Schema
}

// Policy confines Lane access to granted Event roles.
func (Lane) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), filterGrantedLanes(), privacy.AlwaysAllowRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowEventOwnedMutation(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields defines stable Lane identity persistence.
func (Lane) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges defines Lane relationships.
func (Lane) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("lanes").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
		edge.To("draft", LaneDraft.Type).Unique(),
		edge.To("published_versions", LanePublishedVersion.Type),
		edge.From("session_drafts", SessionDraft.Type).Ref("lanes"),
		edge.From("session_published_versions", SessionPublishedVersion.Type).Ref("lanes"),
	}
}
