package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EventGrant gives one Account a role for one Event.
type EventGrant struct {
	ent.Schema
}

// Mixin applies the fail-closed authorization tripwire to EventGrant.
func (EventGrant) Mixin() []ent.Mixin {
	return []ent.Mixin{AuthorizationTripwire{}}
}

// Fields defines Event Grant persistence.
func (EventGrant) Fields() []ent.Field {
	return []ent.Field{
		field.Int("event_id").Immutable(),
		field.Int("account_id").Immutable(),
		field.Enum("role").Values("Producer", "Operator", "Observer").Immutable(),
		field.JSON("lane_ids", []int{}).Optional().Immutable(),
		field.JSON("display_group_keys", []string{}).Optional().Immutable(),
		field.JSON("capabilities", []string{}).Optional().Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges defines Event Grant relationships.
func (EventGrant) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("grants").
			Field("event_id").
			Unique().
			Immutable().
			Required(),
		edge.From("account", Account.Type).
			Ref("event_grants").
			Field("account_id").
			Unique().
			Immutable().
			Required(),
	}
}

// Indexes prevents duplicate roles for one Account and Event.
func (EventGrant) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("event_id", "account_id").Unique(),
	}
}
