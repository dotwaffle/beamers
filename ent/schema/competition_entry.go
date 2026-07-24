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
			denyMissingViewer(), allowEventOwnedMutation(),
			allowScopedCompetitionEntryPresentationMutation(), privacy.AlwaysDenyRule(),
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
		field.Time("upload_closed_at").Optional(),
		field.Int("content_revision").Default(1).Positive(),
		field.Int("reviewed_content_revision").Optional().Positive(),
		field.Int("reviewed_by_account_id").Optional().Positive(),
		field.Time("reviewed_at").Optional(),
		field.Time("first_presented_at").Optional(),
		field.Enum("presentation_status").
			Values("Scheduled", "Deferred", "Presented", "NotPresented").
			Default("Scheduled"),
		field.Int("deferred_sequence").Optional().Positive(),
		field.Bool("resolution_required").Default(false),
		field.Enum("result_disposition").
			Values("Eligible", "Disqualified", "Withheld").
			Default("Eligible"),
		field.String("technical_failure_reason").Optional().MaxLen(10000),
		field.String("resolution_crew_reason").Optional().MaxLen(10000),
		field.String("public_disqualification_message").Optional().MaxLen(10000),
		field.Bool("release_hold").Default(false),
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
		edge.To("result_standings", CompetitionResultStanding.Type),
	}
}

// Indexes supports ordered Competition Entry queries.
func (CompetitionEntry) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("competition_session_id", "created_at"),
	}
}
