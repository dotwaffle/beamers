package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// DraftChange records one independently publishable structural change.
type DraftChange struct {
	ent.Schema
}

// Policy keeps Draft Change evidence behind the command transaction boundary.
func (DraftChange) Policy() ent.Policy {
	return systemOnlyPolicy()
}

// Fields defines Draft Change persistence.
func (DraftChange) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.Int("draft_edit_id").Immutable(),
		field.Int("revision").Positive().Immutable(),
		field.String("kind").NotEmpty().MaxLen(100).Immutable(),
		field.String("target_type").NotEmpty().MaxLen(100).Immutable(),
		field.Int("target_id").Positive().Immutable(),
		field.String("fact_key").NotEmpty().MaxLen(200).Immutable(),
		field.String("payload_json").NotEmpty().Immutable(),
		field.Enum("status").Values("Effective", "Published", "Superseded", "Discarded", "Reverted").Default("Effective"),
		field.Int("published_revision").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges defines Draft Change relationships and dependency evidence.
func (DraftChange) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("draft_changes").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
		edge.From("draft_edit", DraftEdit.Type).
			Ref("changes").
			Field("draft_edit_id").
			Unique().
			Immutable().
			Required(),
		edge.To("dependencies", DraftChangeDependency.Type),
		edge.To("dependents", DraftChangeDependency.Type),
	}
}

// Indexes supports overlap checks and effective-change selection.
func (DraftChange) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("event_id", "revision"),
		index.Fields("event_id", "target_type", "target_id", "fact_key", "status"),
	}
}
