package store

import (
	"context"
	"slices"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/account"
	"github.com/dotwaffle/beamers/ent/passwordcredential"
	"github.com/dotwaffle/beamers/ent/predicate"
	"github.com/dotwaffle/beamers/ent/webauthncredential"
)

// WebAuthnCredential is one Account-owned public-key Credential record.
type WebAuthnCredential struct {
	ID           int
	AccountID    int
	CredentialID []byte
	Name         string
	Credential   []byte
	Attachment   string
	CreatedAt    time.Time
	LastUsedAt   *time.Time
	RevokedAt    *time.Time
}

// WebAuthnAccount contains the Account identity and its active Credentials.
type WebAuthnAccount struct {
	Account     AccountCredential
	Credentials []WebAuthnCredential
}

// PasswordActive reports whether one enabled Account has an active password Credential.
func (installation *SQLite) PasswordActive(ctx context.Context, accountID int) (bool, error) {
	found, err := installation.client.PasswordCredential.Query().
		Where(
			passwordcredential.AccountIDEQ(accountID),
			passwordcredential.RevokedAtIsNil(),
			passwordcredential.HasAccountWith(account.DisabledAtIsNil()),
		).
		Exist(systemContext(ctx))
	if err != nil {
		return false, opaqueError("inspect Account password Credential", err)
	}
	return found, nil
}

// ListWebAuthnCredentials returns all Credential metadata for one enabled Account.
func (installation *SQLite) ListWebAuthnCredentials(
	ctx context.Context,
	accountID int,
) ([]WebAuthnCredential, error) {
	found, err := installation.client.WebAuthnCredential.Query().
		Where(
			webauthncredential.AccountIDEQ(accountID),
			webauthncredential.HasAccountWith(account.DisabledAtIsNil()),
		).
		Order(ent.Asc(webauthncredential.FieldCreatedAt), ent.Asc(webauthncredential.FieldID)).
		All(systemContext(ctx))
	if err != nil {
		return nil, opaqueError("list WebAuthn Credentials", err)
	}
	result := make([]WebAuthnCredential, 0, len(found))
	for _, credential := range found {
		result = append(result, webAuthnCredential(credential))
	}
	return result, nil
}

// WebAuthnAccountByID returns one enabled Account and its active Credentials.
func (installation *SQLite) WebAuthnAccountByID(
	ctx context.Context,
	accountID int,
) (WebAuthnAccount, error) {
	return installation.webAuthnAccount(ctx, account.IDEQ(accountID))
}

// WebAuthnAccountByName returns one enabled Account and its active Credentials.
func (installation *SQLite) WebAuthnAccountByName(
	ctx context.Context,
	normalizedName string,
) (WebAuthnAccount, error) {
	return installation.webAuthnAccount(ctx, account.NormalizedNameEQ(normalizedName))
}

func (installation *SQLite) webAuthnAccount(
	ctx context.Context,
	match predicate.Account,
) (WebAuthnAccount, error) {
	ctx = systemContext(ctx)
	found, err := installation.client.Account.Query().
		Where(match, account.DisabledAtIsNil()).
		WithWebauthnCredentials(func(query *ent.WebAuthnCredentialQuery) {
			query.Where(webauthncredential.RevokedAtIsNil()).
				Order(ent.Asc(webauthncredential.FieldCreatedAt), ent.Asc(webauthncredential.FieldID))
		}).
		Only(ctx)
	if ent.IsNotFound(err) {
		return WebAuthnAccount{}, ErrInvalidSession
	}
	if err != nil {
		return WebAuthnAccount{}, opaqueError("read WebAuthn Account", err)
	}
	credentials, err := found.Edges.WebauthnCredentialsOrErr()
	if err != nil {
		return WebAuthnAccount{}, opaqueError("read Account WebAuthn Credentials", err)
	}
	result := WebAuthnAccount{
		Account:     accountCredential(found, ""),
		Credentials: make([]WebAuthnCredential, 0, len(credentials)),
	}
	for _, credential := range credentials {
		result.Credentials = append(result.Credentials, webAuthnCredential(credential))
	}
	return result, nil
}

// AddWebAuthnCredential persists one verified Credential inside a command.
func (transaction *CommandTx) AddWebAuthnCredential(
	ctx context.Context,
	accountID int,
	credentialID []byte,
	name string,
	credentialJSON []byte,
	attachment string,
	now time.Time,
) (WebAuthnCredential, error) {
	internalContext := systemContext(ctx)
	if _, err := transaction.transaction.Account.Query().
		Where(account.IDEQ(accountID), account.DisabledAtIsNil()).
		Only(internalContext); err != nil {
		return WebAuthnCredential{}, opaqueError("read WebAuthn Credential Account", err)
	}
	created, err := transaction.transaction.WebAuthnCredential.Create().
		SetAccountID(accountID).
		SetCredentialID(credentialID).
		SetName(name).
		SetCredential(credentialJSON).
		SetAttachment(attachment).
		SetCreatedAt(now).
		Save(internalContext)
	if ent.IsConstraintError(err) {
		return WebAuthnCredential{}, ErrAccountExists
	}
	if err != nil {
		return WebAuthnCredential{}, opaqueError("save WebAuthn Credential", err)
	}
	return webAuthnCredential(created), nil
}

// CreateWebAuthnSession updates a verified Credential and creates an Account session atomically.
func (installation *SQLite) CreateWebAuthnSession(
	ctx context.Context,
	accountID int,
	credentialID []byte,
	credentialJSON []byte,
	sessionHash string,
	now time.Time,
	expiresAt time.Time,
) (AccountCredential, []string, error) {
	internalContext := systemContext(ctx)
	type webAuthnSession struct {
		credential AccountCredential
		revoked    []string
	}
	result, err := withTx(internalContext, installation.client, "WebAuthn sign-in", func(transaction *ent.Tx) (webAuthnSession, error) {
		stored, err := transaction.WebAuthnCredential.Query().Where(
			webauthncredential.AccountIDEQ(accountID),
			webauthncredential.CredentialIDEQ(credentialID),
			webauthncredential.RevokedAtIsNil(),
			webauthncredential.HasAccountWith(account.DisabledAtIsNil()),
		).Only(internalContext)
		if ent.IsNotFound(err) {
			return webAuthnSession{}, ErrInvalidSession
		}
		if err != nil {
			return webAuthnSession{}, opaqueError("read verified WebAuthn Credential", err)
		}
		if _, err = stored.Update().
			SetCredential(credentialJSON).
			SetLastUsedAt(now).
			Save(internalContext); err != nil {
			return webAuthnSession{}, opaqueError("update WebAuthn Credential", err)
		}
		revoked, err := createBoundedAccountSession(
			internalContext,
			transaction,
			accountID,
			sessionHash,
			now,
			expiresAt,
		)
		if err != nil {
			return webAuthnSession{}, err
		}
		found, err := transaction.Account.Query().
			Where(account.IDEQ(accountID), account.DisabledAtIsNil()).
			Only(internalContext)
		if err != nil {
			return webAuthnSession{}, opaqueError("read WebAuthn Account session", err)
		}
		return webAuthnSession{credential: accountCredential(found, ""), revoked: revoked}, nil
	})
	if err != nil {
		return AccountCredential{}, nil, err
	}
	return result.credential, result.revoked, nil
}

// RemovePassword revokes a password inside a command only when another Credential remains active.
func (transaction *CommandTx) RemovePassword(
	ctx context.Context,
	accountID int,
	now time.Time,
) (int, error) {
	internalContext := systemContext(ctx)
	credential, err := transaction.transaction.PasswordCredential.Query().Where(
		passwordcredential.AccountIDEQ(accountID),
		passwordcredential.RevokedAtIsNil(),
	).Only(internalContext)
	if ent.IsNotFound(err) {
		return 0, ErrFinalCredential
	}
	if err != nil {
		return 0, opaqueError("read password Credential for removal", err)
	}
	credentialCount, err := activeAccountCredentialCount(
		internalContext,
		transaction.transaction.Client(),
		accountID,
	)
	if err != nil {
		return 0, err
	}
	if credentialCount <= 1 {
		return credential.ID, ErrFinalCredential
	}
	if _, err = credential.Update().SetRevokedAt(now).Save(internalContext); err != nil {
		return 0, opaqueError("remove password Credential", err)
	}
	return credential.ID, nil
}

// RevokeWebAuthnCredential revokes one Credential inside a command only when another remains active.
func (transaction *CommandTx) RevokeWebAuthnCredential(
	ctx context.Context,
	accountID int,
	credentialID int,
	now time.Time,
) error {
	internalContext := systemContext(ctx)
	stored, err := transaction.transaction.WebAuthnCredential.Query().Where(
		webauthncredential.IDEQ(credentialID),
		webauthncredential.AccountIDEQ(accountID),
		webauthncredential.RevokedAtIsNil(),
	).Only(internalContext)
	if ent.IsNotFound(err) {
		return ErrInvalidSession
	}
	if err != nil {
		return opaqueError("read WebAuthn Credential for revocation", err)
	}
	credentialCount, err := activeAccountCredentialCount(
		internalContext,
		transaction.transaction.Client(),
		accountID,
	)
	if err != nil {
		return err
	}
	if credentialCount <= 1 {
		return ErrFinalCredential
	}
	if _, err = stored.Update().SetRevokedAt(now).Save(internalContext); err != nil {
		return opaqueError("revoke WebAuthn Credential", err)
	}
	return nil
}

func webAuthnCredential(found *ent.WebAuthnCredential) WebAuthnCredential {
	return WebAuthnCredential{
		ID:           found.ID,
		AccountID:    found.AccountID,
		CredentialID: slices.Clone(found.CredentialID),
		Name:         found.Name,
		Credential:   slices.Clone(found.Credential),
		Attachment:   found.Attachment,
		CreatedAt:    found.CreatedAt,
		LastUsedAt:   found.LastUsedAt,
		RevokedAt:    found.RevokedAt,
	}
}
