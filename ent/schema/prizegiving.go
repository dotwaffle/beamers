package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Prizegiving designates one Ceremony Session for Results release.
type Prizegiving struct {
	ent.Schema
}

// Policy enforces crew-only Results reads and Producer-only designation.
func (Prizegiving) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), filterViewablePrizegivings(), privacy.AlwaysAllowRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowProducerResultsMutation(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields define one immutable Ceremony designation.
func (Prizegiving) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.Int("ceremony_session_id").Immutable(),
		field.Int("created_by_account_id").Positive().Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges define Event and Ceremony Session ownership.
func (Prizegiving) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("prizegivings").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
		edge.From("ceremony", Session.Type).
			Ref("prizegiving").
			Field("ceremony_session_id").
			Unique().
			Immutable().
			Required(),
	}
}

// Indexes allow one Prizegiving designation per Ceremony Session.
func (Prizegiving) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("event_id", "ceremony_session_id").Unique(),
	}
}
