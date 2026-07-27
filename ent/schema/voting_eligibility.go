package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// VotingEligibility records one private Event-scoped Account qualification.
type VotingEligibility struct {
	ent.Schema
}

// Policy keeps Voting Eligibility behind Account-scoped application services.
func (VotingEligibility) Policy() ent.Policy {
	return systemOnlyPolicy()
}

// Fields defines durable Event and Account ownership.
func (VotingEligibility) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.Int("account_id").Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges defines the qualified Account and Event.
func (VotingEligibility) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("voting_eligibilities").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
		edge.From("account", Account.Type).
			Ref("voting_eligibilities").
			Field("account_id").
			Unique().
			Immutable().
			Required(),
	}
}

// Indexes prevents duplicate Event eligibility.
func (VotingEligibility) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("event_id", "account_id").Unique(),
	}
}
