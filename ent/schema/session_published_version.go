package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SessionPublishedVersion stores immutable structural state from one Publish.
type SessionPublishedVersion struct {
	ent.Schema
}

// Hooks enforce Event ownership for every structural membership.
func (SessionPublishedVersion) Hooks() []ent.Hook {
	return []ent.Hook{validateSessionMembershipOwnership}
}

// Policy makes Published Session versions append-only and application-owned.
func (SessionPublishedVersion) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), filterGrantedSessionPublishedVersions(), privacy.AlwaysAllowRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowSessionOwnedCreation(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields defines Published Session version persistence.
func (SessionPublishedVersion) Fields() []ent.Field {
	return []ent.Field{
		field.Int("session_id").Immutable(),
		field.Int("published_revision").NonNegative().Immutable(),
		field.String("title").NotEmpty().MaxLen(200).Immutable(),
		field.Enum("type").Values(
			"Presentation", "Competition", "Break", "Activity", "Ceremony", "Performance", "Hold",
		).Immutable(),
		field.Enum("audience_visibility").Values("Public", "CrewOnly").Immutable(),
		field.String("public_details").Optional().MaxLen(10000).Immutable(),
		field.String("crew_notes").Optional().MaxLen(10000).Immutable(),
		field.Time("planned_start").Immutable(),
		field.Time("planned_end").Immutable(),
		field.Enum("timing_policy").Values("FixedEnd", "FixedDuration", "ManualEnd").Immutable(),
		field.Int("minimum_duration_seconds").NonNegative().Immutable(),
		field.Enum("start_boundary").Values("Hard", "Soft").Immutable(),
		field.Enum("end_boundary").Values("Hard", "Soft").Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges defines Published Session relationships to stable structural identities.
func (SessionPublishedVersion) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("session", Session.Type).
			Ref("published_versions").
			Field("session_id").
			Unique().
			Immutable().
			Required(),
		edge.To("lanes", Lane.Type).
			StorageKey(edge.Table("session_published_version_lanes"), edge.Columns("session_published_version_id", "lane_id")),
		edge.To("locations", Location.Type).
			StorageKey(edge.Table("session_published_version_locations"), edge.Columns("session_published_version_id", "location_id")),
		edge.To("tracks", Track.Type).
			StorageKey(edge.Table("session_published_version_tracks"), edge.Columns("session_published_version_id", "track_id")),
	}
}

// Indexes prevents two Published versions for one Session at one revision.
func (SessionPublishedVersion) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("session_id", "published_revision").Unique(),
	}
}
