package store

import (
	"context"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/account"
	"github.com/dotwaffle/beamers/ent/federatedidentity"
	"github.com/dotwaffle/beamers/ent/passwordcredential"
	"github.com/dotwaffle/beamers/ent/registrationpolicy"
	"github.com/dotwaffle/beamers/ent/webauthncredential"
)

// FederatedIdentity is one Account-owned provider Credential.
type FederatedIdentity struct {
	ID         int
	AccountID  int
	Provider   string
	Subject    string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// LinkFederatedIdentity persists a provider subject inside a command transaction.
func (transaction *CommandTx) LinkFederatedIdentity(
	ctx context.Context,
	accountID int,
	provider string,
	subject string,
	now time.Time,
) (FederatedIdentity, error) {
	if err := requireActor(ctx, "CommandTx.LinkFederatedIdentity"); err != nil {
		return FederatedIdentity{}, err
	}

	if _, err := transaction.transaction.Account.Query().
		Where(account.IDEQ(accountID), account.DisabledAtIsNil()).
		Only(ctx); err != nil {
		return FederatedIdentity{}, opaqueError("read Federated Identity Account", err)
	}
	found, err := transaction.transaction.FederatedIdentity.Query().
		Where(
			federatedidentity.ProviderEQ(provider),
			federatedidentity.SubjectEQ(subject),
		).
		Only(ctx)
	switch {
	case ent.IsNotFound(err):
		created, createErr := transaction.transaction.FederatedIdentity.Create().
			SetAccountID(accountID).
			SetProvider(provider).
			SetSubject(subject).
			SetCreatedAt(now).
			Save(ctx)
		if ent.IsConstraintError(createErr) {
			return FederatedIdentity{}, ErrAccountExists
		}
		if createErr != nil {
			return FederatedIdentity{}, opaqueError("link Federated Identity", createErr)
		}
		return federatedIdentity(created), nil
	case err != nil:
		return FederatedIdentity{}, opaqueError("read Federated Identity", err)
	case found.AccountID != accountID:
		return FederatedIdentity{}, ErrAccountExists
	}
	if found.RevokedAt != nil {
		found, err = found.Update().ClearRevokedAt().Save(ctx)
		if err != nil {
			return FederatedIdentity{}, opaqueError("reactivate Federated Identity", err)
		}
	}
	return federatedIdentity(found), nil
}

// ListFederatedIdentities returns provider Credential metadata for one Account.
func (installation *SQLite) ListFederatedIdentities(
	ctx context.Context,
	accountID int,
) ([]FederatedIdentity, error) {
	if err := requireActor(ctx, "SQLite.ListFederatedIdentities"); err != nil {
		return []FederatedIdentity(nil), err
	}

	found, err := installation.client.FederatedIdentity.Query().
		Where(
			federatedidentity.AccountIDEQ(accountID),
			federatedidentity.HasAccountWith(account.DisabledAtIsNil()),
		).
		Order(
			ent.Asc(federatedidentity.FieldCreatedAt),
			ent.Asc(federatedidentity.FieldID),
		).
		All(ctx)
	if err != nil {
		return nil, opaqueError("list Federated Identities", err)
	}
	result := make([]FederatedIdentity, 0, len(found))
	for _, identity := range found {
		result = append(result, federatedIdentity(identity))
	}
	return result, nil
}

// CreateFederatedSession signs in an existing identity or atomically creates its Account.
func (installation *SQLite) CreateFederatedSession(
	ctx context.Context,
	params FederatedSessionParams,
) (AccountCredential, []string, bool, error) {
	if err := requireActor(ctx, "SQLite.CreateFederatedSession"); err != nil {
		return AccountCredential{}, []string(nil), false, err
	}

	result, err := withTx(ctx, installation.client, "federated sign-in", func(transaction *ent.Tx) (federatedSessionResult, error) {
		return createFederatedSession(ctx, transaction, params)
	})
	if err != nil {
		return AccountCredential{}, nil, false, err
	}
	return result.credential, result.revoked, result.created, nil
}

// federatedSessionResult is one committed federated sign-in outcome.
type federatedSessionResult struct {
	credential AccountCredential
	revoked    []string
	created    bool
}

func createFederatedSession(
	ctx context.Context,
	transaction *ent.Tx,
	params FederatedSessionParams,
) (federatedSessionResult, error) {
	identity, err := transaction.FederatedIdentity.Query().
		Where(
			federatedidentity.ProviderEQ(params.Provider),
			federatedidentity.SubjectEQ(params.Subject),
		).
		WithAccount().
		Only(ctx)
	created := false
	var found *ent.Account
	switch {
	case ent.IsNotFound(err):
		if !params.AllowAccountCreation {
			return federatedSessionResult{}, ErrRegistrationClosed
		}
		policy, policyErr := transaction.RegistrationPolicy.Query().
			Where(registrationpolicy.RegistrationOpenEQ(true)).
			Exist(ctx)
		if policyErr != nil {
			return federatedSessionResult{}, opaqueError(
				"read Registration Policy for federation",
				policyErr,
			)
		}
		if !policy {
			return federatedSessionResult{}, ErrRegistrationClosed
		}
		found, err = transaction.Account.Create().
			SetName(params.Name).
			SetNormalizedName(params.NormalizedName).
			SetWebauthnUserHandle(params.WebAuthnUserHandle).
			SetAdministrator(false).
			SetCreatedAt(params.Now).
			Save(ctx)
		if ent.IsConstraintError(err) {
			return federatedSessionResult{}, ErrAccountExists
		}
		if err != nil {
			return federatedSessionResult{}, opaqueError("create federated Account", err)
		}
		_, err = transaction.FederatedIdentity.Create().
			SetAccountID(found.ID).
			SetProvider(params.Provider).
			SetSubject(params.Subject).
			SetCreatedAt(params.Now).
			SetLastUsedAt(params.Now).
			Save(ctx)
		if ent.IsConstraintError(err) {
			return federatedSessionResult{}, ErrAccountExists
		}
		if err != nil {
			return federatedSessionResult{}, opaqueError("create Federated Identity", err)
		}
		created = true
	case err != nil:
		return federatedSessionResult{}, opaqueError("read Federated Identity", err)
	default:
		found, err = identity.Edges.AccountOrErr()
		if err != nil {
			return federatedSessionResult{}, opaqueError(
				"read Federated Identity Account",
				err,
			)
		}
		if identity.RevokedAt != nil || found.DisabledAt != nil {
			return federatedSessionResult{}, ErrInvalidSession
		}
		if _, err = identity.Update().SetLastUsedAt(params.Now).Save(ctx); err != nil {
			return federatedSessionResult{}, opaqueError(
				"update Federated Identity use",
				err,
			)
		}
	}
	revoked, err := createBoundedAccountSession(
		ctx,
		transaction,
		found.ID,
		params.SessionHash,
		params.Now,
		params.SessionExpiry,
	)
	if err != nil {
		return federatedSessionResult{}, err
	}
	return federatedSessionResult{
		credential: accountCredential(found, ""),
		revoked:    revoked,
		created:    created,
	}, nil
}

// FederatedSessionParams contains one verified provider identity and new session proof.
type FederatedSessionParams struct {
	Provider             string
	Subject              string
	Name                 string
	NormalizedName       string
	WebAuthnUserHandle   []byte
	AllowAccountCreation bool
	SessionHash          string
	Now                  time.Time
	SessionExpiry        time.Time
}

func federatedIdentity(found *ent.FederatedIdentity) FederatedIdentity {
	return FederatedIdentity{
		ID: found.ID, AccountID: found.AccountID,
		Provider: found.Provider, Subject: found.Subject,
		CreatedAt: found.CreatedAt, LastUsedAt: found.LastUsedAt, RevokedAt: found.RevokedAt,
	}
}

func activeAccountCredentialCount(
	ctx context.Context,
	client *ent.Client,
	accountID int,
) (int, error) {
	passwords, err := client.PasswordCredential.Query().
		Where(
			passwordcredential.AccountIDEQ(accountID),
			passwordcredential.RevokedAtIsNil(),
		).
		Count(ctx)
	if err != nil {
		return 0, opaqueError("count password Credentials", err)
	}
	webAuthn, err := client.WebAuthnCredential.Query().
		Where(
			webauthncredential.AccountIDEQ(accountID),
			webauthncredential.RevokedAtIsNil(),
		).
		Count(ctx)
	if err != nil {
		return 0, opaqueError("count WebAuthn Credentials", err)
	}
	federated, err := client.FederatedIdentity.Query().
		Where(
			federatedidentity.AccountIDEQ(accountID),
			federatedidentity.RevokedAtIsNil(),
		).
		Count(ctx)
	if err != nil {
		return 0, opaqueError("count Federated Identities", err)
	}
	return passwords + webAuthn + federated, nil
}

func activeCredentialCount(ctx context.Context, client *ent.Client) (int, error) {
	passwords, err := client.PasswordCredential.Query().
		Where(passwordcredential.RevokedAtIsNil()).
		Count(ctx)
	if err != nil {
		return 0, opaqueError("count active password Credentials", err)
	}
	webAuthn, err := client.WebAuthnCredential.Query().
		Where(webauthncredential.RevokedAtIsNil()).
		Count(ctx)
	if err != nil {
		return 0, opaqueError("count active WebAuthn Credentials", err)
	}
	federated, err := client.FederatedIdentity.Query().
		Where(federatedidentity.RevokedAtIsNil()).
		Count(ctx)
	if err != nil {
		return 0, opaqueError("count active Federated Identities", err)
	}
	return passwords + webAuthn + federated, nil
}
