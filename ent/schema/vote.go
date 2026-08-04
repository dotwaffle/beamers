package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Vote is one private Account score for one presented Competition Entry.
type Vote struct {
	ent.Schema
}

// Mixin applies the fail-closed authorization tripwire to Vote.
func (Vote) Mixin() []ent.Mixin {
	return []ent.Mixin{AuthorizationTripwire{}}
}

// Policy keeps Vote values behind Account-scoped voting projections.
func (Vote) Policy() ent.Policy {
	return systemOnlyPolicy()
}

// Fields defines durable Ballot ownership and score.
func (Vote) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.Int("competition_session_id").Immutable(),
		field.Int("account_id").Immutable(),
		field.Int("entry_id").Immutable(),
		field.Int("value").Min(1).Max(5),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now),
	}
}

// Edges define the private Ballot scope.
func (Vote) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("votes").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
		edge.From("competition", Session.Type).
			Ref("votes").
			Field("competition_session_id").
			Unique().
			Immutable().
			Required(),
		edge.From("account", Account.Type).
			Ref("votes").
			Field("account_id").
			Unique().
			Immutable().
			Required(),
		edge.From("entry", CompetitionEntry.Type).
			Ref("votes").
			Field("entry_id").
			Unique().
			Immutable().
			Required(),
	}
}

// Indexes allow one mutable score per Account and Entry.
func (Vote) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("competition_session_id", "account_id", "entry_id").Unique(),
		index.Fields("competition_session_id", "account_id"),
	}
}
