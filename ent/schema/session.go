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
			denyMissingViewer(), filterGrantedSessions(), privacy.AlwaysAllowRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowSessionDeletion(), allowEventOwnedMutation(),
			allowScopedSessionLiveMutation(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields defines stable Session identity persistence.
func (Session) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.Enum("lifecycle").Values("Scheduled", "Live", "Ended", "Canceled").Default("Scheduled"),
		field.Int("live_state_revision").Default(0).NonNegative(),
		field.Time("forecast_start").Optional(),
		field.Time("forecast_end").Optional(),
		field.String("corrected_title").Optional().Nillable().MaxLen(200),
		field.String("corrected_speaker").Optional().Nillable().MaxLen(200),
		field.String("corrected_public_details").Optional().Nillable().MaxLen(10000),
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
		edge.To("runs", SessionRun.Type),
	}
}
