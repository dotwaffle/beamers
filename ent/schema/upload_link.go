package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// UploadLink is one revocable credential scoped to one upload owner.
type UploadLink struct {
	ent.Schema
}

// Policy confines Upload Link metadata to granted Event crew.
func (UploadLink) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), filterGrantedUploadLinks(), privacy.AlwaysAllowRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowEventOwnedMutation(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields defines hashed, target-scoped Upload Link persistence.
func (UploadLink) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.Enum("target_type").Values("Presentation", "Entry").Immutable(),
		field.Int("target_id").Positive().Immutable(),
		field.String("token_hash").NotEmpty().MaxLen(64).Unique().Immutable().Sensitive(),
		field.Time("revoked_at").Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges defines Event ownership.
func (UploadLink) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("upload_links").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
	}
}

// Indexes supports active target credential lookup.
func (UploadLink) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("event_id", "target_type", "target_id", "created_at"),
	}
}
