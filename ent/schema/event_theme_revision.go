package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/dotwaffle/beamers/internal/themevalue"
)

// EventThemeRevision is one immutable controlled Event presentation.
type EventThemeRevision struct {
	ent.Schema
}

// Policy keeps Event Theme Revisions behind the application service.
func (EventThemeRevision) Policy() ent.Policy {
	return systemOnlyPolicy()
}

// Fields define immutable Event Theme configuration and authorship.
func (EventThemeRevision) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Positive().Immutable(),
		field.JSON("config", themevalue.EventConfig{}).Immutable(),
		field.Int("created_by_account_id").Positive().Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges bind every revision to its owning Event.
func (EventThemeRevision) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("theme_revisions").
			Field("event_id").
			Unique().
			Required().
			Immutable(),
	}
}

// Indexes keep per-Event revision history bounded to one lookup.
func (EventThemeRevision) Indexes() []ent.Index {
	return []ent.Index{index.Fields("event_id")}
}
