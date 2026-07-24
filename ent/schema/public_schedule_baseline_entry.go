package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// PublicScheduleBaselineEntry preserves one Session's first captured public Forecast Start.
type PublicScheduleBaselineEntry struct {
	ent.Schema
}

// Policy keeps Public Schedule Baseline entries behind application services.
func (PublicScheduleBaselineEntry) Policy() ent.Policy {
	return appendOnlySystemPolicy()
}

// Fields defines one immutable baseline entry.
func (PublicScheduleBaselineEntry) Fields() []ent.Field {
	return []ent.Field{
		field.Int("baseline_id").Immutable(),
		field.Int("session_id").Unique().Immutable(),
		field.Time("forecast_start").Immutable(),
		field.Int("source_published_revision").NonNegative().Immutable(),
		field.Time("recorded_at").Default(time.Now).Immutable(),
	}
}

// Edges defines baseline and Session ownership.
func (PublicScheduleBaselineEntry) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("baseline", PublicScheduleBaseline.Type).
			Ref("entries").
			Field("baseline_id").
			Unique().
			Immutable().
			Required(),
		edge.From("session", Session.Type).
			Ref("public_schedule_baseline_entry").
			Field("session_id").
			Unique().
			Immutable().
			Required(),
	}
}
