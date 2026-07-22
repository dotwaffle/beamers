package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// Installation records the durable identity of an initialized Beamers data
// directory.
type Installation struct {
	ent.Schema
}

// Fields defines installation persistence.
func (Installation) Fields() []ent.Field {
	return []ent.Field{
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}
