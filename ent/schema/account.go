package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Account records one installation-wide individual identity.
type Account struct {
	ent.Schema
}

// Policy confines Account administration and selection to Administrators.
func (Account) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			denyMissingViewer(), allowAdministrator(), privacy.AlwaysDenyRule(),
		},
		Mutation: privacy.MutationPolicy{
			allowRegistrationAccountCreation(), denyMissingViewer(),
			allowAdministratorMutation(), allowOwnAccountNameMutation(),
			privacy.AlwaysDenyRule(),
		},
	}
}

// Fields defines Account persistence.
func (Account) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().MaxLen(200),
		field.String("normalized_name").NotEmpty().MaxLen(200).Unique(),
		field.Bytes("webauthn_user_handle").Optional().Unique().Immutable().Sensitive(),
		field.Bool("administrator").Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("disabled_at").Optional().Nillable(),
	}
}

// Edges defines Account relationships.
func (Account) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("password_credential", PasswordCredential.Type).Unique(),
		edge.To("webauthn_credentials", WebAuthnCredential.Type),
		edge.To("federated_identities", FederatedIdentity.Type),
		edge.To("preference", AccountPreference.Type).Unique(),
		edge.To("profile", AccountProfile.Type).Unique(),
		edge.To("sessions", AccountSession.Type),
		edge.To("recovery_codes", RecoveryCode.Type),
		edge.To("recovery_tokens", RecoveryToken.Type),
		edge.To("event_grants", EventGrant.Type),
		edge.To("favorite_sessions", FavoriteSession.Type),
		edge.To("competition_entries", CompetitionEntry.Type),
		edge.To("submitted_presentations", Session.Type),
		edge.To("voting_eligibilities", VotingEligibility.Type),
		edge.To("audit_entries", AuditEntry.Type),
		edge.To("command_receipts", CommandReceipt.Type),
		edge.To("draft_edits", DraftEdit.Type),
	}
}
