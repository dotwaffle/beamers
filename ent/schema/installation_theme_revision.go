package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"

	"github.com/dotwaffle/beamers/internal/themevalue"
)

// InstallationThemeRevision is one immutable controlled browser presentation.
type InstallationThemeRevision struct {
	ent.Schema
}

// Policy keeps Theme Revisions behind the application service.
func (InstallationThemeRevision) Policy() ent.Policy {
	return systemOnlyPolicy()
}

// Fields define immutable Theme configuration and authorship.
func (InstallationThemeRevision) Fields() []ent.Field {
	return []ent.Field{
		field.JSON("config", themevalue.Config{}).Immutable(),
		field.Int("created_by_account_id").Positive().Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}
