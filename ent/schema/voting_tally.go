package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/dotwaffle/beamers/internal/votingvalue"
)

// VotingTally is one immutable aggregate snapshot created when voting closes.
type VotingTally struct {
	ent.Schema
}

// Mixin applies the fail-closed authorization tripwire to VotingTally.
func (VotingTally) Mixin() []ent.Mixin {
	return []ent.Mixin{AuthorizationTripwire{}}
}

// Policy keeps aggregate evidence behind Crew application services.
func (VotingTally) Policy() ent.Policy {
	return systemOnlyPolicy()
}

// Fields define aggregate evidence without Account-level Vote data.
func (VotingTally) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.Int("competition_session_id").Immutable(),
		field.Enum("method").Values("Range1To5").Immutable(),
		field.Enum("self_vote_policy").Values("Allowed", "Neutral").Immutable(),
		field.Int("participating").NonNegative().Immutable(),
		field.JSON("entries", []votingvalue.Entry{}).Immutable(),
		field.Int("created_by_account_id").Positive().Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges define Event and Competition ownership plus seeded Draft revisions.
func (VotingTally) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("voting_tallies").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
		edge.From("competition", Session.Type).
			Ref("voting_tallies").
			Field("competition_session_id").
			Unique().
			Immutable().
			Required(),
		edge.To("results_drafts", CompetitionResultsDraft.Type),
	}
}

// Indexes permit exactly one Tally per Competition.
func (VotingTally) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("competition_session_id").Unique(),
		index.Fields("event_id", "competition_session_id"),
	}
}
