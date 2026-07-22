package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// TrackPublishedVersion stores immutable Track state from one Publish.
type TrackPublishedVersion struct {
	ent.Schema
}

// Policy makes Published Track versions append-only and application-owned.
func (TrackPublishedVersion) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), allowSystemViewer(), filterGrantedTrackPublishedVersions(), privacy.AlwaysAllowRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowSystemCreation(), allowTrackOwnedCreation(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields defines Published Track version persistence.
func (TrackPublishedVersion) Fields() []ent.Field {
	return []ent.Field{
		field.Int("track_id").Immutable(),
		field.Int("published_revision").NonNegative().Immutable(),
		field.String("name").NotEmpty().MaxLen(200).Immutable(),
		field.Bool("retired").Default(false).Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges defines Published Track version relationships.
func (TrackPublishedVersion) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("track", Track.Type).
			Ref("published_versions").
			Field("track_id").
			Unique().
			Immutable().
			Required(),
	}
}

// Indexes prevents two Published versions for one Track at one revision.
func (TrackPublishedVersion) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("track_id", "published_revision").Unique(),
	}
}
