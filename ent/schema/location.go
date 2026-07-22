package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Location is the stable identity of one operational area within an Event.
type Location struct {
	ent.Schema
}

// Policy confines Location access to granted Event roles.
func (Location) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), allowSystemViewer(), filterGrantedLocations(), privacy.AlwaysAllowRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowSystemViewer(), allowEventOwnedMutation(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields defines stable Location identity persistence.
func (Location) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges defines Location relationships.
func (Location) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("locations").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
		edge.To("draft", LocationDraft.Type).Unique(),
		edge.To("published_versions", LocationPublishedVersion.Type),
		edge.To("lane_drafts", LaneDraft.Type),
		edge.To("lane_published_versions", LanePublishedVersion.Type),
		edge.From("session_drafts", SessionDraft.Type).Ref("locations"),
		edge.From("session_published_versions", SessionPublishedVersion.Type).Ref("locations"),
	}
}
