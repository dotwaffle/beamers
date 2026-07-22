package store

import (
	"context"
	"errors"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/account"
	"github.com/dotwaffle/beamers/ent/accountsession"
	"github.com/dotwaffle/beamers/ent/bootstrapcredential"
	"github.com/dotwaffle/beamers/ent/passwordcredential"
)

var (
	// ErrBootstrapUnavailable means an Administrator or active bootstrap credential already exists.
	ErrBootstrapUnavailable = errors.New("administrator bootstrap is unavailable")
	// ErrInvalidBootstrap means the supplied bootstrap credential cannot create an Administrator.
	ErrInvalidBootstrap = errors.New("invalid bootstrap credential")
	// ErrInvalidSession means the supplied session is expired, revoked, or unknown.
	ErrInvalidSession = errors.New("invalid account session")
)

// AccountCredential is the authentication projection of an Account.
type AccountCredential struct {
	ID            int
	Name          string
	PasswordHash  string
	Administrator bool
}

// BootstrapAdministratorParams contains the values committed atomically when
// the first Administrator consumes a bootstrap credential.
type BootstrapAdministratorParams struct {
	BootstrapHash  string
	Name           string
	NormalizedName string
	PasswordHash   string
	SessionHash    string
	Now            time.Time
	SessionExpiry  time.Time
}

// IssueBootstrap records one active credential when no Account or unexpired
// bootstrap credential exists.
func (installation *SQLite) IssueBootstrap(
	ctx context.Context,
	tokenHash string,
	now time.Time,
	expiresAt time.Time,
) error {
	transaction, err := installation.client.Tx(ctx)
	if err != nil {
		return opaqueError("begin bootstrap issuance", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()

	accountCount, err := transaction.Account.Query().Count(ctx)
	if err != nil {
		return opaqueError("count Accounts before bootstrap", err)
	}
	if accountCount != 0 {
		return ErrBootstrapUnavailable
	}
	activeCount, err := transaction.BootstrapCredential.Query().
		Where(
			bootstrapcredential.UsedAtIsNil(),
			bootstrapcredential.ExpiresAtGT(now),
		).
		Count(ctx)
	if err != nil {
		return opaqueError("count active bootstrap credentials", err)
	}
	if activeCount != 0 {
		return ErrBootstrapUnavailable
	}
	if _, err := transaction.BootstrapCredential.Create().
		SetTokenHash(tokenHash).
		SetCreatedAt(now).
		SetExpiresAt(expiresAt).
		Save(ctx); err != nil {
		return opaqueError("record bootstrap credential", err)
	}
	if err := transaction.Commit(); err != nil {
		return opaqueError("commit bootstrap credential", err)
	}
	return nil
}

// BootstrapAdministrator consumes a bootstrap credential and commits the
// first Administrator and initial session in one transaction.
func (installation *SQLite) BootstrapAdministrator(
	ctx context.Context,
	params BootstrapAdministratorParams,
) (AccountCredential, error) {
	transaction, err := installation.client.Tx(ctx)
	if err != nil {
		return AccountCredential{}, opaqueError("begin Administrator bootstrap", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()

	accountCount, err := transaction.Account.Query().Count(ctx)
	if err != nil {
		return AccountCredential{}, opaqueError("count Accounts during bootstrap", err)
	}
	if accountCount != 0 {
		return AccountCredential{}, ErrInvalidBootstrap
	}
	credential, err := transaction.BootstrapCredential.Query().
		Where(
			bootstrapcredential.TokenHashEQ(params.BootstrapHash),
			bootstrapcredential.UsedAtIsNil(),
			bootstrapcredential.ExpiresAtGT(params.Now),
		).
		Only(ctx)
	if ent.IsNotFound(err) {
		return AccountCredential{}, ErrInvalidBootstrap
	}
	if err != nil {
		return AccountCredential{}, opaqueError("read bootstrap credential", err)
	}

	created, err := transaction.Account.Create().
		SetName(params.Name).
		SetNormalizedName(params.NormalizedName).
		SetAdministrator(true).
		SetCreatedAt(params.Now).
		Save(ctx)
	if err != nil {
		return AccountCredential{}, opaqueError("create first Administrator", err)
	}
	_, credentialCreateErr := transaction.PasswordCredential.Create().
		SetAccountID(created.ID).
		SetPasswordHash(params.PasswordHash).
		SetCreatedAt(params.Now).
		Save(ctx)
	if credentialCreateErr != nil {
		return AccountCredential{}, opaqueError("create Administrator credential", credentialCreateErr)
	}
	updated, err := transaction.BootstrapCredential.Update().
		Where(
			bootstrapcredential.IDEQ(credential.ID),
			bootstrapcredential.UsedAtIsNil(),
			bootstrapcredential.ExpiresAtGT(params.Now),
		).
		SetUsedAt(params.Now).
		Save(ctx)
	if err != nil {
		return AccountCredential{}, opaqueError("consume bootstrap credential", err)
	}
	if updated != 1 {
		return AccountCredential{}, ErrInvalidBootstrap
	}
	if err := createAccountSession(
		ctx,
		transaction.AccountSession,
		created.ID,
		params.SessionHash,
		params.Now,
		params.SessionExpiry,
	); err != nil {
		return AccountCredential{}, opaqueError("create initial Account session", err)
	}
	if err := transaction.Commit(); err != nil {
		return AccountCredential{}, opaqueError("commit first Administrator", err)
	}
	return accountCredential(created, params.PasswordHash), nil
}

// FindAccountCredential returns the enabled Account matching a normalized name.
func (installation *SQLite) FindAccountCredential(
	ctx context.Context,
	normalizedName string,
) (AccountCredential, bool, error) {
	found, err := installation.client.PasswordCredential.Query().
		Where(
			passwordcredential.RevokedAtIsNil(),
			passwordcredential.HasAccountWith(
				account.NormalizedNameEQ(normalizedName),
				account.DisabledAtIsNil(),
			),
		).
		WithAccount().
		Only(ctx)
	if ent.IsNotFound(err) {
		return AccountCredential{}, false, nil
	}
	if err != nil {
		return AccountCredential{}, false, opaqueError("read Account credential", err)
	}
	foundAccount, err := found.Edges.AccountOrErr()
	if err != nil {
		return AccountCredential{}, false, opaqueError("read credential Account", err)
	}
	return accountCredential(foundAccount, found.PasswordHash), true, nil
}

// CreateAccountSession persists a new session for an authenticated Account.
func (installation *SQLite) CreateAccountSession(
	ctx context.Context,
	accountID int,
	tokenHash string,
	now time.Time,
	expiresAt time.Time,
) error {
	if err := createAccountSession(
		ctx,
		installation.client.AccountSession,
		accountID,
		tokenHash,
		now,
		expiresAt,
	); err != nil {
		return opaqueError("create Account session", err)
	}
	return nil
}

// FindAccountSession returns the enabled Account for an active session.
func (installation *SQLite) FindAccountSession(
	ctx context.Context,
	tokenHash string,
	now time.Time,
) (AccountCredential, error) {
	session, err := installation.client.AccountSession.Query().
		Where(
			accountsession.TokenHashEQ(tokenHash),
			accountsession.RevokedAtIsNil(),
			accountsession.ExpiresAtGT(now),
			accountsession.HasAccountWith(account.DisabledAtIsNil()),
		).
		WithAccount().
		Only(ctx)
	if ent.IsNotFound(err) {
		return AccountCredential{}, ErrInvalidSession
	}
	if err != nil {
		return AccountCredential{}, opaqueError("read Account session", err)
	}
	found, err := session.Edges.AccountOrErr()
	if err != nil {
		return AccountCredential{}, opaqueError("read session Account", err)
	}
	return accountCredential(found, ""), nil
}

// RevokeAccountSession makes a session durably unusable. Unknown and already
// revoked session tokens have the same successful result.
func (installation *SQLite) RevokeAccountSession(
	ctx context.Context,
	tokenHash string,
	now time.Time,
) error {
	if _, err := installation.client.AccountSession.Update().
		Where(
			accountsession.TokenHashEQ(tokenHash),
			accountsession.RevokedAtIsNil(),
		).
		SetRevokedAt(now).
		Save(ctx); err != nil {
		return opaqueError("revoke Account session", err)
	}
	return nil
}

func createAccountSession(
	ctx context.Context,
	sessions *ent.AccountSessionClient,
	accountID int,
	tokenHash string,
	now time.Time,
	expiresAt time.Time,
) error {
	_, err := sessions.Create().
		SetAccountID(accountID).
		SetTokenHash(tokenHash).
		SetCreatedAt(now).
		SetExpiresAt(expiresAt).
		Save(ctx)
	return err
}

func accountCredential(found *ent.Account, passwordHash string) AccountCredential {
	return AccountCredential{
		ID:            found.ID,
		Name:          found.Name,
		PasswordHash:  passwordHash,
		Administrator: found.Administrator,
	}
}

func opaqueError(action string, err error) error {
	return errors.New(action + ": " + err.Error())
}
