package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/dotwaffle/beamers/internal/prizegivingvalue"
)

// ResultsCorrection records one append-only correction review revision.
type ResultsCorrection struct {
	ent.Schema
}

// Policy keeps correction persistence behind the Results application module.
func (ResultsCorrection) Policy() ent.Policy {
	return appendOnlySystemPolicy()
}

// Fields define one immutable correction lifecycle revision.
func (ResultsCorrection) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.Enum("scope").Values("Prizegiving", "Standalone").Immutable(),
		field.Int("scope_session_id").Positive().Immutable(),
		field.Int("revision").Positive().Immutable(),
		field.Int("base_publication_revision").Positive().Immutable(),
		field.Enum("status").Values("Draft", "Ready", "Published").Immutable(),
		field.JSON("publication_order", []prizegivingvalue.ItemRef{}).Immutable(),
		field.String("items_json").NotEmpty().Immutable(),
		field.JSON("results_text_template", prizegivingvalue.Template{}).Immutable(),
		field.String("crew_reason").NotEmpty().MaxLen(10000).Immutable(),
		field.String("public_note").Optional().MaxLen(10000).Immutable(),
		field.Int("published_results_revision").Optional().Positive().Immutable(),
		field.Int("created_by_account_id").Positive().Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges define Event ownership.
func (ResultsCorrection) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("results_corrections").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
	}
}

// Indexes enforce one append-only revision sequence per release scope.
func (ResultsCorrection) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("event_id", "scope", "scope_session_id", "revision").Unique(),
		index.Fields("event_id", "scope", "scope_session_id"),
	}
}
