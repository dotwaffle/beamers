package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// PrizegivingCompetition assigns one Competition to at most one Prizegiving.
type PrizegivingCompetition struct {
	ent.Schema
}

// Policy confines plans to Results viewers and Producer mutations.
func (PrizegivingCompetition) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), filterViewablePrizegivingCompetitions(),
			privacy.AlwaysAllowRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowProducerPrizegivingCompetitionMutation(),
			privacy.AlwaysDenyRule(),
		},
	}
}

// Fields define immutable Event, Prizegiving, and Competition ownership.
func (PrizegivingCompetition) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.Int("prizegiving_id").Positive().Immutable(),
		field.Int("competition_session_id").Positive().Immutable(),
	}
}

// Edges define the assigned Prizegiving and Competition.
func (PrizegivingCompetition) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("prizegiving_competitions").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
		edge.From("prizegiving", Prizegiving.Type).
			Ref("competitions").
			Field("prizegiving_id").
			Unique().
			Immutable().
			Required(),
		edge.From("competition", Session.Type).
			Ref("prizegiving_assignment").
			Field("competition_session_id").
			Unique().
			Immutable().
			Required(),
	}
}

// Indexes enforce one Prizegiving assignment per Competition.
func (PrizegivingCompetition) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("event_id", "prizegiving_id", "competition_session_id").Unique(),
	}
}
