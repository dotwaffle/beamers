package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// CompetitionEntry is one retained submission to a Competition Session.
type CompetitionEntry struct {
	ent.Schema
}

// Policy confines Competition Entries to granted Event crew.
func (CompetitionEntry) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), filterGrantedCompetitionEntries(), privacy.AlwaysAllowRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowEventOwnedMutation(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields defines stable Competition Entry persistence.
func (CompetitionEntry) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.Int("competition_session_id").Immutable(),
		field.String("name").NotEmpty().MaxLen(200),
		field.String("public_details").Optional().MaxLen(10000),
		field.String("crew_notes").Optional().MaxLen(10000),
		field.Enum("disposition").Values("Pending", "Included", "Rejected"),
		field.Int("revision").Default(1).Positive(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges defines Event and Competition ownership.
func (CompetitionEntry) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("competition_entries").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
		edge.From("competition", Session.Type).
			Ref("competition_entries").
			Field("competition_session_id").
			Unique().
			Immutable().
			Required(),
	}
}

// Indexes supports ordered Competition Entry queries.
func (CompetitionEntry) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("competition_session_id", "created_at"),
	}
}
