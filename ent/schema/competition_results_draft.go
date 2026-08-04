package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/dotwaffle/beamers/internal/awardvalue"
)

// CompetitionResultsDraft is one immutable Competition Results revision.
type CompetitionResultsDraft struct {
	ent.Schema
}

// Mixin applies the fail-closed authorization tripwire to CompetitionResultsDraft.
func (CompetitionResultsDraft) Mixin() []ent.Mixin {
	return []ent.Mixin{AuthorizationTripwire{}}
}

// Fields defines one versioned Results Draft.
func (CompetitionResultsDraft) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.Int("competition_session_id").Immutable(),
		field.Int("revision").Positive().Immutable(),
		field.Enum("disposition").
			Values("Pending", "Publish", "NoPublicResults").
			Immutable(),
		field.String("no_public_crew_reason").Optional().MaxLen(10000).Immutable(),
		field.String("public_explanation").Optional().MaxLen(10000).Immutable(),
		field.Enum("score_type").Values("None", "Decimal", "Duration").Immutable(),
		field.Enum("score_visibility").Values("Public", "CrewOnly").Default("Public").Immutable(),
		field.String("score_unit").Optional().MaxLen(100).Immutable(),
		field.Int("score_precision").Default(0).NonNegative().Immutable(),
		field.Enum("score_requirement").Values("Optional", "Required").Default("Optional").Immutable(),
		field.Enum("score_interpretation").
			Values("HigherWins", "LowerWins", "Informational").
			Default("Informational").
			Immutable(),
		field.JSON("awards", []awardvalue.Competition{}).Optional().Immutable(),
		field.Int("voting_tally_id").Optional().Nillable().Positive().Immutable(),
		field.String("tally_override_crew_reason").Optional().MaxLen(1000).Immutable(),
		field.Int("ready_by_account_id").Optional().Nillable().Positive(),
		field.Time("ready_at").Optional().Nillable(),
		field.Int("created_by_account_id").Positive().Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges define Event and Competition ownership plus immutable Standings.
func (CompetitionResultsDraft) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("competition_results_drafts").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
		edge.From("competition", Session.Type).
			Ref("competition_results_drafts").
			Field("competition_session_id").
			Unique().
			Immutable().
			Required(),
		edge.From("voting_tally", VotingTally.Type).
			Ref("results_drafts").
			Field("voting_tally_id").
			Unique().
			Immutable(),
		edge.To("standings", CompetitionResultStanding.Type),
	}
}

// Indexes enforce one revision sequence per Competition.
func (CompetitionResultsDraft) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("competition_session_id", "revision").Unique(),
		index.Fields("event_id", "competition_session_id", "revision"),
	}
}
