package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Session is the stable identity of one planned unit within a Rundown.
type Session struct {
	ent.Schema
}

// Policy confines Session access to granted Event roles.
func (Session) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), filterGrantedSessions(), privacy.AlwaysAllowRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowSessionDeletion(), allowEventOwnedMutation(),
			allowScopedSessionLiveMutation(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields defines stable Session identity persistence.
func (Session) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.Enum("lifecycle").Values("Scheduled", "Live", "Ended", "Canceled").Default("Scheduled"),
		field.Int("live_state_revision").Default(0).NonNegative(),
		field.Time("forecast_start").Optional(),
		field.Time("forecast_end").Optional(),
		field.Time("communicated_start").Optional(),
		field.Time("communicated_end").Optional(),
		field.Time("previous_forecast_start").Optional(),
		field.JSON("forecast_lane_ids", []int{}).Optional(),
		field.JSON("forecast_location_ids", []int{}).Optional(),
		field.String("public_cancellation_message").Optional().MaxLen(10000),
		field.String("cancellation_crew_notes").Optional().MaxLen(10000),
		field.String("corrected_title").Optional().Nillable().MaxLen(200),
		field.String("corrected_speaker").Optional().Nillable().MaxLen(200),
		field.String("corrected_public_details").Optional().Nillable().MaxLen(10000),
		field.Bool("require_entry_review").Default(false),
		field.Bool("file_delivery_required").Optional().Nillable(),
		field.Int("readiness_revision").Default(0).NonNegative(),
		field.Enum("entry_order_policy").
			Values("SubmissionOrder", "ManualOrder", "DeterministicShuffle").
			Default("DeterministicShuffle"),
		field.Int64("entry_order_seed").Default(0).NonNegative(),
		field.JSON("entry_order_manual_ids", []int{}).Optional(),
		field.JSON("locked_entry_order_ids", []int{}).Optional(),
		field.Time("entry_order_locked_at").Optional(),
		field.Int("entry_order_revision").Default(0).NonNegative(),
		field.Enum("program_output_kind").
			Values("Standby", "Upcoming", "Starting", "Entry", "Ending").
			Default("Standby"),
		field.Int("program_output_entry_id").Optional().Nillable().Positive(),
		field.Int("program_output_revision").Default(0).NonNegative(),
		field.Int("program_cursor").Default(-1),
		field.Time("program_output_taken_at").Optional(),
		field.Enum("attachment_release_policy_override").
			Values("OnLive", "OnEnded", "OnEventReleaseCue").
			Optional().
			Nillable(),
		field.Int("attachment_release_revision").Default(0).NonNegative(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges defines Session relationships.
func (Session) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("sessions").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
		edge.To("draft", SessionDraft.Type).Unique(),
		edge.To("published_versions", SessionPublishedVersion.Type),
		edge.To("runs", SessionRun.Type),
		edge.To("cancellations", SessionCancellation.Type),
		edge.To("public_schedule_baseline_entry", PublicScheduleBaselineEntry.Type).Unique(),
		edge.To("competition_entries", CompetitionEntry.Type),
		edge.To("competition_results_drafts", CompetitionResultsDraft.Type),
		edge.To("competition_result_standings", CompetitionResultStanding.Type),
		edge.To("prizegiving", Prizegiving.Type).Unique(),
	}
}
