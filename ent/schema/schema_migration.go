package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

// Migration records one committed migration applied to an installation.
type Migration struct {
	ent.Schema
}

// Fields defines committed migration history.
func (Migration) Fields() []ent.Field {
	return []ent.Field{
		field.Int("version").Positive().Unique().Immutable(),
		field.String("name").NotEmpty().Unique().Immutable(),
		field.String("checksum").MinLen(64).MaxLen(64).Immutable(),
		field.Time("applied_at").Default(time.Now).Immutable(),
	}
}

// Annotations pins the table name and checksum invariant used by the migration
// runner before generated Ent access is available.
func (Migration) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Table: "beamers_schema_migrations",
			Checks: map[string]string{
				"schema_migrations_checksum_length": "length(checksum) = 64",
			},
		},
	}
}
