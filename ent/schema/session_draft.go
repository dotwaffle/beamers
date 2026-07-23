package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// SessionDraft stores the current editable structural state of one Session.
type SessionDraft struct {
	ent.Schema
}

// Hooks enforce Event ownership for every structural membership.
func (SessionDraft) Hooks() []ent.Hook {
	return []ent.Hook{validateSessionMembershipOwnership}
}

// Policy confines Session Draft access to granted Event roles.
func (SessionDraft) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), filterGrantedSessionDrafts(), privacy.AlwaysAllowRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowSessionDraftDeletion(), allowSessionOwnedMutation(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields defines Session Draft persistence.
func (SessionDraft) Fields() []ent.Field {
	return []ent.Field{
		field.Int("session_id").Unique().Immutable(),
		field.String("title").NotEmpty().MaxLen(200),
		field.String("speaker").Optional().MaxLen(200),
		field.Enum("type").Values(
			"Presentation", "Competition", "Break", "Activity", "Ceremony", "Performance", "Hold",
		),
		field.Enum("audience_visibility").Values("Public", "CrewOnly"),
		field.String("public_details").Optional().MaxLen(10000),
		field.String("crew_notes").Optional().MaxLen(10000),
		field.Time("planned_start"),
		field.Time("planned_end"),
		field.Enum("timing_policy").Values("FixedEnd", "FixedDuration", "ManualEnd"),
		field.Int("minimum_duration_seconds").NonNegative(),
		field.Enum("start_boundary").Values("Hard", "Soft"),
		field.Enum("end_boundary").Values("Hard", "Soft"),
	}
}

// Edges defines Session Draft relationships to stable structural identities.
func (SessionDraft) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("session", Session.Type).
			Ref("draft").
			Field("session_id").
			Unique().
			Immutable().
			Required(),
		edge.To("lanes", Lane.Type).
			StorageKey(edge.Table("session_draft_lanes"), edge.Columns("session_draft_id", "lane_id")),
		edge.To("locations", Location.Type).
			StorageKey(edge.Table("session_draft_locations"), edge.Columns("session_draft_id", "location_id")),
		edge.To("tracks", Track.Type).
			StorageKey(edge.Table("session_draft_tracks"), edge.Columns("session_draft_id", "track_id")),
	}
}
