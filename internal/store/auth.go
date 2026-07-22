package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/account"
	"github.com/dotwaffle/beamers/ent/accountsession"
	"github.com/dotwaffle/beamers/ent/auditentry"
	"github.com/dotwaffle/beamers/ent/bootstrapcredential"
	"github.com/dotwaffle/beamers/ent/eventgrant"
	"github.com/dotwaffle/beamers/ent/passwordcredential"
	"github.com/dotwaffle/beamers/internal/viewer"
)

var (
	// ErrBootstrapUnavailable means an Administrator or active bootstrap credential already exists.
	ErrBootstrapUnavailable = errors.New("administrator bootstrap is unavailable")
	// ErrInvalidBootstrap means the supplied bootstrap credential cannot create an Administrator.
	ErrInvalidBootstrap = errors.New("invalid bootstrap credential")
	// ErrInvalidSession means the supplied session is expired, revoked, or unknown.
	ErrInvalidSession = errors.New("invalid account session")
	// ErrAccountExists means an Account already uses the requested normalized name.
	ErrAccountExists = errors.New("account already exists")
)

// AccountCredential is the authentication projection of an Account.
type AccountCredential struct {
	ID            int                 `json:"id"`
	Name          string              `json:"name"`
	PasswordHash  string              `json:"-"`
	Administrator bool                `json:"administrator"`
	EventRoles    map[int]viewer.Role `json:"-"`
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

// CreateAccountParams contains the values committed atomically for a new Account.
type CreateAccountParams struct {
	ActorAccountID int
	Name           string
	NormalizedName string
	PasswordHash   string
	Now            time.Time
	CommandID      string
	PayloadHash    string
}

// CreateAccount atomically records an individual non-Administrator Account,
// its credential, and its Audit Entry.
func (installation *SQLite) CreateAccount(
	ctx context.Context,
	params CreateAccountParams,
) (AccountCredential, error) {
	transaction, err := installation.client.Tx(ctx)
	if err != nil {
		return AccountCredential{}, opaqueError("begin Account creation", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	receipt := commandReceiptParams{
		ActorAccountID: params.ActorAccountID, CommandID: params.CommandID,
		PayloadHash: params.PayloadHash, Action: "CreateAccount", TargetType: "Account", Now: params.Now,
	}
	outcome, retry, err := findCommandReceipt(ctx, transaction, receipt)
	if errors.Is(err, ErrCommandConflict) {
		return AccountCredential{}, rejectCommandConflict(ctx, transaction, receipt)
	}
	if err != nil {
		return AccountCredential{}, err
	}
	if retry {
		var original AccountCredential
		if decodeErr := decodeCommandReceipt(outcome, &original, "decode Account Command Receipt"); decodeErr != nil {
			return AccountCredential{}, decodeErr
		}
		return original, nil
	}

	created, err := transaction.Account.Create().
		SetName(params.Name).
		SetNormalizedName(params.NormalizedName).
		SetAdministrator(false).
		SetCreatedAt(params.Now).
		Save(ctx)
	if ent.IsConstraintError(err) {
		return AccountCredential{}, ErrAccountExists
	}
	if err != nil {
		return AccountCredential{}, opaqueError("create Account", err)
	}
	if _, credentialErr := transaction.PasswordCredential.Create().
		SetAccountID(created.ID).
		SetPasswordHash(params.PasswordHash).
		SetCreatedAt(params.Now).
		Save(viewer.SystemContext(ctx)); credentialErr != nil {
		return AccountCredential{}, opaqueError("create Account credential", credentialErr)
	}
	projected := accountCredential(created, params.PasswordHash)
	outcomeJSON, err := json.Marshal(projected)
	if err != nil {
		return AccountCredential{}, opaqueError("encode Account command outcome", err)
	}
	receipt.TargetID = strconv.Itoa(created.ID)
	receipt.OutcomeJSON = string(outcomeJSON)
	if err := createCommandReceipt(ctx, transaction, receipt); err != nil {
		return AccountCredential{}, opaqueError("record Account Command Receipt", err)
	}
	if _, err := transaction.AuditEntry.Create().
		SetActorAccountID(params.ActorAccountID).
		SetCreatedAt(params.Now).
		SetAction("CreateAccount").
		SetTargetType("Account").
		SetTargetID(strconv.Itoa(created.ID)).
		SetResult(auditentry.ResultSucceeded).
		Save(ctx); err != nil {
		return AccountCredential{}, opaqueError("audit Account creation", err)
	}
	if err := transaction.Commit(); err != nil {
		return AccountCredential{}, opaqueError("commit Account creation", err)
	}
	return projected, nil
}

// IssueBootstrap records one active credential when no Account or unexpired
// bootstrap credential exists.
func (installation *SQLite) IssueBootstrap(
	ctx context.Context,
	tokenHash string,
	now time.Time,
	expiresAt time.Time,
) error {
	ctx = viewer.SystemContext(ctx)
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
	ctx = viewer.SystemContext(ctx)
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
	ctx = viewer.SystemContext(ctx)
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

// ListAccounts returns enabled Accounts in stable creation order.
func (installation *SQLite) ListAccounts(ctx context.Context) ([]AccountCredential, error) {
	found, err := installation.client.Account.Query().
		Where(account.DisabledAtIsNil()).
		Order(ent.Asc(account.FieldID)).
		All(ctx)
	if err != nil {
		return nil, opaqueError("list Accounts", err)
	}
	accounts := make([]AccountCredential, 0, len(found))
	for _, item := range found {
		accounts = append(accounts, accountCredential(item, ""))
	}
	return accounts, nil
}

// CreateAccountSession persists a new session for an authenticated Account.
func (installation *SQLite) CreateAccountSession(
	ctx context.Context,
	accountID int,
	tokenHash string,
	now time.Time,
	expiresAt time.Time,
) error {
	ctx = viewer.SystemContext(ctx)
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
	ctx = viewer.SystemContext(ctx)
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
	credential := accountCredential(found, "")
	credential.EventRoles, err = installation.findEventRoles(ctx, found.ID)
	if err != nil {
		return AccountCredential{}, err
	}
	return credential, nil
}

func (installation *SQLite) findEventRoles(
	ctx context.Context,
	accountID int,
) (map[int]viewer.Role, error) {
	found, err := installation.client.EventGrant.Query().
		Where(eventgrant.AccountIDEQ(accountID)).
		All(viewer.SystemContext(ctx))
	if err != nil {
		return nil, opaqueError("read Account Event Grants", err)
	}
	roles := make(map[int]viewer.Role, len(found))
	for _, grant := range found {
		roles[grant.EventID] = viewer.Role(grant.Role)
	}
	return roles, nil
}

// RevokeAccountSession makes a session durably unusable. Unknown and already
// revoked session tokens have the same successful result.
func (installation *SQLite) RevokeAccountSession(
	ctx context.Context,
	tokenHash string,
	now time.Time,
) error {
	ctx = viewer.SystemContext(ctx)
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
