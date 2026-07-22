package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// DraftEdit records one atomic edit to shared Draft state.
type DraftEdit struct {
	ent.Schema
}

// Policy keeps Draft Edit evidence internal and append-only.
func (DraftEdit) Policy() ent.Policy {
	return appendOnlySystemPolicy()
}

// Fields defines Draft Edit persistence.
func (DraftEdit) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.Int("actor_account_id").Immutable(),
		field.Int("revision").Positive().Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges defines Draft Edit relationships.
func (DraftEdit) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("draft_edits").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
		edge.From("actor", Account.Type).
			Ref("draft_edits").
			Field("actor_account_id").
			Unique().
			Immutable().
			Required(),
		edge.To("changes", DraftChange.Type),
	}
}

// Indexes permits one successful Draft Edit per Event revision.
func (DraftEdit) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("event_id", "revision").Unique(),
	}
}
