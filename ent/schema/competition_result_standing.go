package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// CompetitionResultStanding is one immutable Entry result in a Draft.
type CompetitionResultStanding struct {
	ent.Schema
}

// Policy enforces separate unreleased Results access.
func (CompetitionResultStanding) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), filterViewableCompetitionResultStandings(),
			privacy.AlwaysAllowRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowManageResultsMutation(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields define one explicit Standing and optional exact Score.
func (CompetitionResultStanding) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.Int("results_draft_id").Immutable(),
		field.Int("competition_session_id").Immutable(),
		field.Int("entry_id").Immutable(),
		field.Enum("standing").Values("Placed", "Unplaced").Immutable(),
		field.Int("placement").Optional().Nillable().Positive().Immutable(),
		field.Int("display_order").Positive().Immutable(),
		field.String("decimal_score").Optional().Nillable().MaxLen(200).Immutable(),
		field.Int64("duration_score_nanos").Optional().Nillable().NonNegative().Immutable(),
	}
}

// Edges define immutable ownership.
func (CompetitionResultStanding) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("competition_result_standings").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
		edge.From("draft", CompetitionResultsDraft.Type).
			Ref("standings").
			Field("results_draft_id").
			Unique().
			Immutable().
			Required(),
		edge.From("competition", Session.Type).
			Ref("competition_result_standings").
			Field("competition_session_id").
			Unique().
			Immutable().
			Required(),
		edge.From("entry", CompetitionEntry.Type).
			Ref("result_standings").
			Field("entry_id").
			Unique().
			Immutable().
			Required(),
	}
}

// Indexes preserve one explicit Entry and display position per Draft.
func (CompetitionResultStanding) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("results_draft_id", "entry_id").Unique(),
		index.Fields("results_draft_id", "display_order").Unique(),
		index.Fields("competition_session_id", "results_draft_id"),
	}
}
