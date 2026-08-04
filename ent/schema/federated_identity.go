package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// FederatedIdentity records one independently revocable provider Credential.
type FederatedIdentity struct {
	ent.Schema
}

// Mixin applies the fail-closed authorization tripwire to FederatedIdentity.
func (FederatedIdentity) Mixin() []ent.Mixin {
	return []ent.Mixin{AuthorizationTripwire{}}
}

// Fields defines Federated Identity persistence.
func (FederatedIdentity) Fields() []ent.Field {
	return []ent.Field{
		field.Int("account_id").Immutable(),
		field.String("provider").NotEmpty().MaxLen(64).Immutable(),
		field.String("subject").NotEmpty().MaxLen(1024).Immutable().Sensitive(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("last_used_at").Optional().Nillable(),
		field.Time("revoked_at").Optional().Nillable(),
	}
}

// Edges defines Federated Identity ownership.
func (FederatedIdentity) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("account", Account.Type).
			Ref("federated_identities").
			Field("account_id").
			Unique().
			Immutable().
			Required(),
	}
}

// Indexes prevents one provider subject from belonging to multiple Accounts.
func (FederatedIdentity) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("provider", "subject").Unique(),
	}
}
