package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AttachmentVersion is one immutable uploaded revision.
type AttachmentVersion struct {
	ent.Schema
}

// Policy keeps Attachment Versions behind application services.
func (AttachmentVersion) Policy() ent.Policy {
	return appendOnlySystemPolicy()
}

// Fields defines immutable file metadata and attribution.
func (AttachmentVersion) Fields() []ent.Field {
	return []ent.Field{
		field.Int("attachment_id").Immutable(),
		field.Int("version").Positive().Immutable(),
		field.String("original_filename").NotEmpty().MaxLen(255).Immutable(),
		field.String("media_type").Optional().MaxLen(255).Immutable(),
		field.Int64("size_bytes").NonNegative().Immutable(),
		field.String("sha256").NotEmpty().MaxLen(64).Immutable(),
		field.String("storage_key").NotEmpty().MaxLen(200).Immutable(),
		field.Enum("uploader_type").Values("UploadLink", "Crew").Immutable(),
		field.Int("uploader_id").Positive().Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges defines logical Attachment ownership.
func (AttachmentVersion) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("attachment", Attachment.Type).
			Ref("versions").
			Field("attachment_id").
			Unique().
			Immutable().
			Required(),
	}
}

// Indexes preserves monotonic version numbering.
func (AttachmentVersion) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("attachment_id", "version").Unique(),
	}
}
