package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Attachment is one logical file owned by a Presentation or Entry.
type Attachment struct {
	ent.Schema
}

// Policy keeps Attachment metadata behind application services.
func (Attachment) Policy() ent.Policy {
	return appendOnlySystemPolicy()
}

// Fields defines stable logical Attachment ownership.
func (Attachment) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.Enum("owner_type").Values("Presentation", "Entry").Immutable(),
		field.Int("owner_id").Positive().Immutable(),
		field.String("name").NotEmpty().MaxLen(200).Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges defines immutable versions.
func (Attachment) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("versions", AttachmentVersion.Type),
	}
}

// Indexes makes one name identify one logical file per owner.
func (Attachment) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("event_id", "owner_type", "owner_id", "name").Unique(),
	}
}
