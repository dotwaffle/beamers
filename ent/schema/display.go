package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Display is one persistently enrolled screen identity.
type Display struct {
	ent.Schema
}

// Policy keeps Display identity behind the Display application module.
func (Display) Policy() ent.Policy {
	return systemOnlyPolicy()
}

// Fields defines durable Display identity.
func (Display) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().MaxLen(200),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("enrolled_at").Immutable(),
	}
}

// Edges defines Display credentials and Event-specific Assignments.
func (Display) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("credentials", DisplayCredential.Type),
		edge.To("assignments", DisplayAssignment.Type),
	}
}
