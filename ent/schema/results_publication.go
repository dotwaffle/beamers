package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/dotwaffle/beamers/internal/prizegivingvalue"
)

// ResultsPublication records one immutable public release-manifest revision.
type ResultsPublication struct {
	ent.Schema
}

// Policy keeps publication persistence behind application modules.
func (ResultsPublication) Policy() ent.Policy {
	return appendOnlySystemPolicy()
}

// Fields define one immutable publication manifest.
func (ResultsPublication) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.Enum("scope").Values("Prizegiving", "Standalone").Immutable(),
		field.Int("scope_session_id").Positive().Immutable(),
		field.Int("revision").Positive().Immutable(),
		field.Enum("release_policy").
			Values("AllAtCue", "ProgressiveOnReveal", "AtCeremonyEnd", "Standalone").
			Immutable(),
		field.Enum("status").Values("Partial", "Final").Immutable(),
		field.JSON("items", []prizegivingvalue.ItemRef{}).Immutable(),
		field.JSON("prizegiving_lock", prizegivingvalue.Lock{}).Optional().Immutable(),
		field.JSON("results_text_template", prizegivingvalue.Template{}).Optional().Immutable(),
		field.String("rendered_html").Optional().Immutable(),
		field.String("rendered_text").Optional().Immutable(),
		field.String("rendered_json").Optional().Immutable(),
		field.Int("created_by_account_id").Optional().Nillable().Positive().Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges define Event ownership.
func (ResultsPublication) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("results_publications").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
	}
}

// Indexes enforce one append-only revision sequence per release scope.
func (ResultsPublication) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("event_id", "scope", "scope_session_id", "revision").Unique(),
		index.Fields("event_id", "scope", "scope_session_id"),
	}
}
