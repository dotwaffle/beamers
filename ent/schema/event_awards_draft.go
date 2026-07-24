package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/dotwaffle/beamers/internal/awardvalue"
)

// EventAwardsDraft is one versioned Event Awards snapshot with path review evidence.
type EventAwardsDraft struct {
	ent.Schema
}

// Policy enforces separate unreleased Results access.
func (EventAwardsDraft) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), filterViewableEventAwardsDrafts(),
			privacy.AlwaysAllowRule(),
		},
		Mutation: privacy.MutationPolicy{
			denyMissingViewer(), allowEventAwardsMutation(), privacy.AlwaysDenyRule(),
		},
	}
}

// Fields define immutable Award content and mutable per-path review evidence.
func (EventAwardsDraft) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.Int("revision").Positive().Immutable(),
		field.JSON("awards", []awardvalue.Event{}).Optional().Immutable(),
		field.JSON("path_states", []awardvalue.PathState{}).Optional(),
		field.Int("created_by_account_id").Positive().Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges define Event ownership.
func (EventAwardsDraft) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("event_awards_drafts").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
	}
}

// Indexes enforce one Event Awards revision sequence per Event.
func (EventAwardsDraft) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("event_id", "revision").Unique(),
	}
}
