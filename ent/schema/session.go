package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Session is the stable identity of one planned unit within a Rundown.
type Session struct {
	ent.Schema
}

// Policy confines Session access to granted Event roles.
func (Session) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), allowSystemViewer(), filterGrantedSessions(), privacy.AlwaysAllowRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowSystemViewer(), allowEventOwnedMutation(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields defines stable Session identity persistence.
func (Session) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges defines Session relationships.
func (Session) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("sessions").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
		edge.To("draft", SessionDraft.Type).Unique(),
		edge.To("published_versions", SessionPublishedVersion.Type),
	}
}
