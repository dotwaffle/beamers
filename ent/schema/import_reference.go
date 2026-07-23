package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ImportReference retains one external key as duplicate-detection evidence.
type ImportReference struct {
	ent.Schema
}

// Policy keeps Import References append-only behind application services.
func (ImportReference) Policy() ent.Policy {
	return appendOnlySystemPolicy()
}

// Fields defines external evidence without making it canonical identity.
func (ImportReference) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.Enum("source_format").Values("CSV").Immutable(),
		field.Enum("record_type").Values("Session", "CompetitionEntry").Immutable(),
		field.String("external_key").NotEmpty().MaxLen(500).Immutable(),
		field.String("target_type").NotEmpty().MaxLen(100).Immutable(),
		field.Int("target_id").Positive().Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges defines Event ownership while leaving target identities independent.
func (ImportReference) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("import_references").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
	}
}

// Indexes block duplicate keys within one source record family.
func (ImportReference) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("event_id", "source_format", "record_type", "external_key").Unique(),
		index.Fields("target_type", "target_id"),
	}
}
