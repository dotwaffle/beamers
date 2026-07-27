package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// FavoriteSession records one Account's private Session bookmark.
type FavoriteSession struct {
	ent.Schema
}

// Policy confines Favorite Sessions to the store's private Account-scoped methods.
func (FavoriteSession) Policy() ent.Policy {
	return privacy.Policy{
		Query:    privacy.QueryPolicy{privacy.AlwaysDenyRule()},
		Mutation: privacy.MutationPolicy{privacy.AlwaysDenyRule()},
	}
}

// Fields defines Favorite Session persistence.
func (FavoriteSession) Fields() []ent.Field {
	return []ent.Field{
		field.Int("account_id").Immutable(),
		field.Int("session_id").Immutable(),
	}
}

// Edges defines the owning Account and bookmarked Session.
func (FavoriteSession) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("account", Account.Type).
			Ref("favorite_sessions").
			Field("account_id").
			Unique().
			Immutable().
			Required(),
		edge.From("session", Session.Type).
			Ref("favorites").
			Field("session_id").
			Unique().
			Immutable().
			Required(),
	}
}

// Indexes prevents duplicate bookmarks.
func (FavoriteSession) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("account_id", "session_id").Unique(),
	}
}
