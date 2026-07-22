package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// LanePublishedVersion stores immutable Lane state from one Publish.
type LanePublishedVersion struct {
	ent.Schema
}

// Hooks enforce Event ownership across the immutable Lane placement.
func (LanePublishedVersion) Hooks() []ent.Hook {
	return []ent.Hook{validateLaneLocationOwnership}
}

// Policy makes Published Lane versions append-only and application-owned.
func (LanePublishedVersion) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), allowSystemViewer(), filterGrantedLanePublishedVersions(), privacy.AlwaysAllowRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowSystemCreation(), allowLaneOwnedCreation(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields defines Published Lane version persistence.
func (LanePublishedVersion) Fields() []ent.Field {
	return []ent.Field{
		field.Int("lane_id").Immutable(),
		field.Int("location_id").Immutable(),
		field.Int("published_revision").NonNegative().Immutable(),
		field.String("name").NotEmpty().MaxLen(200).Immutable(),
		field.Bool("retired").Default(false).Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges defines Published Lane version relationships.
func (LanePublishedVersion) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("lane", Lane.Type).
			Ref("published_versions").
			Field("lane_id").
			Unique().
			Immutable().
			Required(),
		edge.From("location", Location.Type).
			Ref("lane_published_versions").
			Field("location_id").
			Unique().
			Immutable().
			Required(),
	}
}

// Indexes prevents two Published versions for one Lane at one revision.
func (LanePublishedVersion) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("lane_id", "published_revision").Unique(),
	}
}
