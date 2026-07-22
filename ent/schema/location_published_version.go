package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// LocationPublishedVersion stores immutable Location state from one Publish.
type LocationPublishedVersion struct {
	ent.Schema
}

// Policy makes Published Location versions append-only and application-owned.
func (LocationPublishedVersion) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), filterGrantedLocationPublishedVersions(), privacy.AlwaysAllowRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowLocationOwnedCreation(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields defines Published Location version persistence.
func (LocationPublishedVersion) Fields() []ent.Field {
	return []ent.Field{
		field.Int("location_id").Immutable(),
		field.Int("published_revision").NonNegative().Immutable(),
		field.String("name").NotEmpty().MaxLen(200).Immutable(),
		field.Bool("retired").Default(false).Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges defines Published Location version relationships.
func (LocationPublishedVersion) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("location", Location.Type).
			Ref("published_versions").
			Field("location_id").
			Unique().
			Immutable().
			Required(),
	}
}

// Indexes prevents two Published versions for one Location at one revision.
func (LocationPublishedVersion) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("location_id", "published_revision").Unique(),
	}
}
