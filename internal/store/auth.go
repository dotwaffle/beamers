package store

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/account"
	"github.com/dotwaffle/beamers/ent/accountpreference"
	"github.com/dotwaffle/beamers/ent/accountprofile"
	"github.com/dotwaffle/beamers/ent/accountsession"
	"github.com/dotwaffle/beamers/ent/bootstrapcredential"
	"github.com/dotwaffle/beamers/ent/eventgrant"
	"github.com/dotwaffle/beamers/ent/favoritesession"
	"github.com/dotwaffle/beamers/ent/federatedidentity"
	"github.com/dotwaffle/beamers/ent/passwordcredential"
	"github.com/dotwaffle/beamers/ent/recoverycode"
	"github.com/dotwaffle/beamers/ent/recoverytoken"
	"github.com/dotwaffle/beamers/ent/webauthncredential"
	"github.com/dotwaffle/beamers/internal/profilevalue"
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
	// ErrRegistrationClosed means the installation is not accepting new Accounts.
	ErrRegistrationClosed = errors.New("registration is closed")
	// ErrProfileEntryUnavailable means a Public Profile selected an unreleased Entry.
	ErrProfileEntryUnavailable = errors.New("profile Entry is unavailable")
	// ErrDisableAccountNotFound means an Account cannot be retired by the command.
	ErrDisableAccountNotFound = errors.New("account not found")
	// ErrLastAdministrator means retirement would leave no installation Administrator.
	ErrLastAdministrator = errors.New("last Administrator cannot be disabled")
	// ErrInvalidRecovery means a recovery credential is unknown, expired, or already used.
	ErrInvalidRecovery = errors.New("invalid Account recovery credential")
	// ErrFinalCredential means a mutation would leave an enabled Account unable to authenticate.
	ErrFinalCredential = errors.New("final Account credential cannot be removed")
)

// MaxActiveSessionsPerAccount is the durable concurrent Account session cap.
const MaxActiveSessionsPerAccount = 8

// SetupRequired reports whether the installation has no Account Credential.
func (installation *SQLite) SetupRequired(ctx context.Context) (bool, error) {
	ctx = systemContext(ctx)
	credentials, err := activeCredentialCount(ctx, installation.client)
	if err != nil {
		return false, err
	}
	return credentials == 0, nil
}

// AccountCredential is the authentication projection of an Account.
type AccountCredential struct {
	ID                 int                       `json:"id"`
	Handle             string                    `json:"handle"`
	Name               string                    `json:"name"`
	PasswordHash       string                    `json:"-"`
	WebAuthnUserHandle []byte                    `json:"-"`
	Administrator      bool                      `json:"administrator"`
	EventRoles         map[int]viewer.Role       `json:"-"`
	EventNames         map[int]string            `json:"-"`
	EventScopes        map[int]viewer.EventScope `json:"-"`
	SessionExpiresAt   time.Time                 `json:"-"`
}

// AccountProfile is one Account's private profile settings or public projection.
type AccountProfile struct {
	Handle           string
	DisplayName      string
	Published        bool
	Entries          []ProfileEntry
	AvailableEntries []ProfileEntry
}

// ReleasedProfileEntries lists Entries eligible for Public Profile selection.
func (installation *SQLite) ReleasedProfileEntries(ctx context.Context) ([]ProfileEntry, error) {
	found, err := installation.client.ReleasedProfileEntry.Query().All(ctx)
	if err != nil {
		return nil, opaqueError("read released Profile Entries", err)
	}
	if len(found) == 0 {
		return nil, err
	}
	entries := make([]ProfileEntry, 0, len(found))
	for _, entry := range found {
		entries = append(entries, ProfileEntry{ID: entry.EntryID, Name: entry.Name})
	}
	slices.SortFunc(entries, func(left, right ProfileEntry) int {
		return cmp.Compare(left.ID, right.ID)
	})
	return entries, nil
}

// ProfileEntry is one released Entry selected for a Public Profile.
type ProfileEntry struct {
	ID   int    `json:"entry_id"`
	Name string `json:"name"`
}

// AccountReducedEffects returns one Account's presentation preference.
func (installation *SQLite) AccountReducedEffects(
	ctx context.Context,
	accountID int,
) (bool, error) {
	preference, err := installation.client.AccountPreference.Query().
		Where(accountpreference.AccountIDEQ(accountID)).
		Only(systemContext(ctx))
	if ent.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, opaqueError("read Account Reduced Effects", err)
	}
	return preference.ReducedEffects, nil
}

// SetAccountReducedEffects updates one Account preference.
func (installation *SQLite) SetAccountReducedEffects(
	ctx context.Context,
	accountID int,
	enabled bool,
) error {
	internalContext := systemContext(ctx)
	preference, err := installation.client.AccountPreference.Query().
		Where(accountpreference.AccountIDEQ(accountID)).
		Only(internalContext)
	if ent.IsNotFound(err) {
		_, err = installation.client.AccountPreference.Create().
			SetAccountID(accountID).
			SetReducedEffects(enabled).
			Save(internalContext)
	} else if err == nil {
		_, err = preference.Update().
			SetReducedEffects(enabled).
			Save(internalContext)
	}
	if err != nil {
		return opaqueError("set Account Reduced Effects", err)
	}
	return nil
}

// AccountSessionCounts is token-free durable session diagnostic data.
type AccountSessionCounts struct {
	Active int
	Stored int
}

// BootstrapAdministratorParams contains the values committed atomically when
// the first Administrator consumes a bootstrap credential.
type BootstrapAdministratorParams struct {
	BootstrapHash      string
	Name               string
	NormalizedName     string
	WebAuthnUserHandle []byte
	PasswordHash       string
	SessionHash        string
	Now                time.Time
	SessionExpiry      time.Time
}

// CreateAccountParams contains the values committed atomically for a new Account.
type CreateAccountParams struct {
	ActorAccountID     int
	Name               string
	NormalizedName     string
	WebAuthnUserHandle []byte
	PasswordHash       string
	Now                time.Time
	CommandID          string
	PayloadHash        string
}

// RecoverAccountParams contains the values needed to replace an Account password.
type RecoverAccountParams struct {
	NormalizedName string
	SecretHash     string
	PasswordHash   string
	SessionHash    string
	Now            time.Time
	SessionExpiry  time.Time
}

// RecoveryTokenParams contains one Administrator-assisted recovery grant.
type RecoveryTokenParams struct {
	AccountID int
	TokenHash string
	Now       time.Time
	ExpiresAt time.Time
}

// RegistrationOpen reports whether visitors may create Accounts.
func (installation *SQLite) RegistrationOpen(ctx context.Context) (bool, error) {
	found, err := installation.client.RegistrationPolicy.Query().Only(ctx)
	if ent.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, opaqueError("read Registration Policy", err)
	}
	return found.RegistrationOpen, nil
}

// SetRegistrationOpen updates the installation Registration Policy.
func (installation *SQLite) SetRegistrationOpen(ctx context.Context, open bool) error {
	found, err := installation.client.RegistrationPolicy.Query().Only(ctx)
	if ent.IsNotFound(err) {
		_, err = installation.client.RegistrationPolicy.Create().
			SetRegistrationOpen(open).
			Save(ctx)
	} else if err == nil {
		_, err = found.Update().SetRegistrationOpen(open).Save(ctx)
	}
	if err != nil {
		return opaqueError("set Registration Policy", err)
	}
	return nil
}

// RegisterAccount creates one visitor Account while registration remains open.
func (installation *SQLite) RegisterAccount(
	ctx context.Context,
	params CreateAccountParams,
) (AccountCredential, error) {
	requestContext := ctx
	internalContext := systemContext(ctx)
	transaction, err := installation.client.Tx(ctx)
	if err != nil {
		return AccountCredential{}, opaqueError("begin Account registration", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	credentials, err := activeCredentialCount(internalContext, transaction.Client())
	if err != nil {
		return AccountCredential{}, err
	}
	policy, err := transaction.RegistrationPolicy.Query().Only(internalContext)
	if err != nil && !ent.IsNotFound(err) {
		return AccountCredential{}, opaqueError("read Registration Policy", err)
	}
	if credentials == 0 || (policy != nil && !policy.RegistrationOpen) {
		return AccountCredential{}, ErrRegistrationClosed
	}
	created, err := transaction.Account.Create().
		SetName(params.Name).
		SetNormalizedName(params.NormalizedName).
		SetWebauthnUserHandle(params.WebAuthnUserHandle).
		SetAdministrator(false).
		SetCreatedAt(params.Now).
		Save(requestContext)
	if ent.IsConstraintError(err) {
		return AccountCredential{}, ErrAccountExists
	}
	if err != nil {
		return AccountCredential{}, opaqueError("register Account", err)
	}
	if _, err = transaction.PasswordCredential.Create().
		SetAccountID(created.ID).
		SetPasswordHash(params.PasswordHash).
		SetCreatedAt(params.Now).
		Save(internalContext); err != nil {
		return AccountCredential{}, opaqueError("create registered Account credential", err)
	}
	if err = transaction.Commit(); err != nil {
		return AccountCredential{}, opaqueError("commit Account registration", err)
	}
	return accountCredential(created, params.PasswordHash), nil
}

// AccountProfile returns one enabled Account's private profile settings.
func (installation *SQLite) AccountProfile(
	ctx context.Context,
	accountID int,
) (AccountProfile, bool, error) {
	found, err := installation.client.AccountProfile.Query().
		Where(accountprofile.AccountIDEQ(accountID)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return AccountProfile{}, false, nil
	}
	if err != nil {
		return AccountProfile{}, false, opaqueError("read Account Profile", err)
	}
	return accountProfile(found), true, nil
}

// PublicProfile returns the published profile matching a case-insensitive Handle.
func (installation *SQLite) PublicProfile(
	ctx context.Context,
	normalizedHandle string,
) (AccountProfile, bool, error) {
	found, err := installation.client.AccountProfile.Query().
		Where(
			accountprofile.NormalizedHandleEQ(normalizedHandle),
			accountprofile.PublishedEQ(true),
		).
		Only(ctx)
	if ent.IsNotFound(err) {
		return AccountProfile{}, false, nil
	}
	if err != nil {
		return AccountProfile{}, false, opaqueError("read Public Profile", err)
	}
	return accountProfile(found), true, nil
}

// UpdateAccountProfile replaces one Account's profile and released Entry selection.
func (installation *SQLite) UpdateAccountProfile(
	ctx context.Context,
	accountID int,
	accountHandle string,
	displayName string,
	published bool,
	entryIDs []int,
) error {
	transaction, err := installation.client.Tx(ctx)
	if err != nil {
		return opaqueError("begin Profile update", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	entryIDs = slices.Compact(slices.Sorted(slices.Values(entryIDs)))
	selectedEntries := make([]profilevalue.Entry, 0, len(entryIDs))
	if len(entryIDs) != 0 {
		released, releaseErr := transaction.ReleasedProfileEntry.Query().All(ctx)
		if releaseErr != nil {
			return opaqueError("read released Profile Entries", releaseErr)
		}
		releasedByID := make(map[int]*ent.ReleasedProfileEntry, len(released))
		for _, entry := range released {
			releasedByID[entry.EntryID] = entry
		}
		for _, entryID := range entryIDs {
			entry, ok := releasedByID[entryID]
			if !ok {
				return ErrProfileEntryUnavailable
			}
			selectedEntries = append(
				selectedEntries,
				profilevalue.Entry{ID: entry.EntryID, Name: entry.Name},
			)
		}
	}
	if _, err = transaction.Account.UpdateOneID(accountID).
		SetName(displayName).
		Save(ctx); err != nil {
		return opaqueError("update Account Profile", err)
	}
	found, err := transaction.AccountProfile.Query().
		Where(accountprofile.AccountIDEQ(accountID)).
		Only(ctx)
	switch {
	case ent.IsNotFound(err):
		create := transaction.AccountProfile.Create().
			SetAccountID(accountID).
			SetNormalizedHandle(accountHandle).
			SetDisplayName(displayName).
			SetSelectedEntries(selectedEntries).
			SetPublished(published)
		if _, err = create.Save(ctx); err != nil {
			return opaqueError("create Account Profile", err)
		}
	case err != nil:
		return opaqueError("read Account Profile", err)
	default:
		update := found.Update().
			SetDisplayName(displayName).
			SetSelectedEntries(selectedEntries).
			SetPublished(published)
		if _, err = update.Save(ctx); err != nil {
			return opaqueError("update Account Profile", err)
		}
	}
	if err = transaction.Commit(); err != nil {
		return opaqueError("commit Account Profile", err)
	}
	return nil
}

func accountProfile(found *ent.AccountProfile) AccountProfile {
	entries := make([]ProfileEntry, 0, len(found.SelectedEntries))
	for _, entry := range found.SelectedEntries {
		entries = append(entries, ProfileEntry{ID: entry.ID, Name: entry.Name})
	}
	return AccountProfile{
		Handle: found.NormalizedHandle, DisplayName: found.DisplayName,
		Published: found.Published, Entries: entries,
	}
}

// DisabledAccount is the durable outcome of retiring one enabled Account.
type DisabledAccount struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// DisableAccount retires an enabled Account and revokes all current access atomically.
func (transaction *CommandTx) DisableAccount(
	ctx context.Context,
	accountID int,
	now time.Time,
) (DisabledAccount, error) {
	authorizedContext := ctx
	internalContext := systemContext(ctx)
	target, err := transaction.transaction.Account.Query().Where(
		account.IDEQ(accountID),
		account.DisabledAtIsNil(),
	).Only(authorizedContext)
	if ent.IsNotFound(err) {
		return DisabledAccount{}, ErrDisableAccountNotFound
	}
	if err != nil {
		return DisabledAccount{}, opaqueError("load Account to disable", err)
	}
	if target.Administrator {
		administrators, countErr := transaction.transaction.Account.Query().Where(
			account.AdministratorEQ(true), account.DisabledAtIsNil(),
		).Count(authorizedContext)
		if countErr != nil {
			return DisabledAccount{}, opaqueError("count enabled Administrators", countErr)
		}
		if administrators <= 1 {
			return DisabledAccount{}, ErrLastAdministrator
		}
	}
	placeholder := "\x1fdisabled-account-" + strconv.Itoa(accountID)
	if _, deleteErr := transaction.transaction.AccountProfile.Delete().
		Where(accountprofile.AccountIDEQ(accountID)).
		Exec(authorizedContext); deleteErr != nil {
		return DisabledAccount{}, opaqueError("detach Account Profile", deleteErr)
	}
	if _, deleteErr := transaction.transaction.FavoriteSession.Delete().
		Where(favoritesession.AccountIDEQ(accountID)).
		Exec(internalContext); deleteErr != nil {
		return DisabledAccount{}, opaqueError("detach Favorite Sessions", deleteErr)
	}
	if _, deleteErr := transaction.transaction.RecoveryCode.Delete().
		Where(recoverycode.AccountIDEQ(accountID)).
		Exec(internalContext); deleteErr != nil {
		return DisabledAccount{}, opaqueError("detach Recovery Codes", deleteErr)
	}
	if _, deleteErr := transaction.transaction.RecoveryToken.Delete().
		Where(recoverytoken.AccountIDEQ(accountID)).
		Exec(internalContext); deleteErr != nil {
		return DisabledAccount{}, opaqueError("detach Recovery Tokens", deleteErr)
	}
	updated, err := transaction.transaction.Account.Update().Where(
		account.IDEQ(accountID),
		account.DisabledAtIsNil(),
	).
		SetNormalizedName(placeholder).
		SetDisabledAt(now).
		Save(authorizedContext)
	if err != nil {
		return DisabledAccount{}, opaqueError("disable Account", err)
	}
	if updated != 1 {
		return DisabledAccount{}, ErrDisableAccountNotFound
	}
	if _, err := transaction.transaction.AccountSession.Update().Where(
		accountsession.AccountIDEQ(accountID),
		accountsession.RevokedAtIsNil(),
	).SetRevokedAt(now).Save(internalContext); err != nil {
		return DisabledAccount{}, opaqueError("revoke Account sessions", err)
	}
	if _, err := transaction.transaction.PasswordCredential.Update().Where(
		passwordcredential.AccountIDEQ(accountID),
		passwordcredential.RevokedAtIsNil(),
	).SetRevokedAt(now).Save(internalContext); err != nil {
		return DisabledAccount{}, opaqueError("revoke Account credentials", err)
	}
	if _, err := transaction.transaction.WebAuthnCredential.Update().Where(
		webauthncredential.AccountIDEQ(accountID),
		webauthncredential.RevokedAtIsNil(),
	).SetRevokedAt(now).Save(internalContext); err != nil {
		return DisabledAccount{}, opaqueError("revoke Account WebAuthn Credentials", err)
	}
	if _, err := transaction.transaction.FederatedIdentity.Update().Where(
		federatedidentity.AccountIDEQ(accountID),
		federatedidentity.RevokedAtIsNil(),
	).SetRevokedAt(now).Save(internalContext); err != nil {
		return DisabledAccount{}, opaqueError("revoke Account Federated Identities", err)
	}
	if _, err := transaction.transaction.EventGrant.Delete().Where(
		eventgrant.AccountIDEQ(accountID),
	).Exec(internalContext); err != nil {
		return DisabledAccount{}, opaqueError("revoke Account Event Grants", err)
	}
	return DisabledAccount{ID: target.ID, Name: target.Name}, nil
}

// CreateAccount mutates Account state without owning command lifecycle evidence.
func (transaction *CommandTx) CreateAccount(
	ctx context.Context,
	params CreateAccountParams,
) (AccountCredential, error) {
	created, err := transaction.transaction.Account.Create().
		SetName(params.Name).
		SetNormalizedName(params.NormalizedName).
		SetWebauthnUserHandle(params.WebAuthnUserHandle).
		SetAdministrator(false).
		SetCreatedAt(params.Now).
		Save(ctx)
	if ent.IsConstraintError(err) {
		return AccountCredential{}, ErrAccountExists
	}
	if err != nil {
		return AccountCredential{}, opaqueError("create Account", err)
	}
	if _, credentialErr := transaction.transaction.PasswordCredential.Create().
		SetAccountID(created.ID).
		SetPasswordHash(params.PasswordHash).
		SetCreatedAt(params.Now).
		Save(systemContext(ctx)); credentialErr != nil {
		return AccountCredential{}, opaqueError("create Account credential", credentialErr)
	}
	return accountCredential(created, params.PasswordHash), nil
}

// IssueBootstrap records one active credential for a new or fully sanitized
// installation when no unexpired bootstrap credential exists.
func (installation *SQLite) IssueBootstrap(
	ctx context.Context,
	tokenHash string,
	now time.Time,
	expiresAt time.Time,
) error {
	ctx = systemContext(ctx)
	transaction, err := installation.client.Tx(ctx)
	if err != nil {
		return opaqueError("begin bootstrap issuance", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()

	credentialCount, err := activeCredentialCount(ctx, transaction.Client())
	if err != nil {
		return err
	}
	if credentialCount != 0 {
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

// BootstrapAdministrator consumes a bootstrap credential and either commits
// the first Administrator or re-credentials one preserved Administrator.
func (installation *SQLite) BootstrapAdministrator(
	ctx context.Context,
	params BootstrapAdministratorParams,
) (AccountCredential, error) {
	ctx = systemContext(ctx)
	transaction, err := installation.client.Tx(ctx)
	if err != nil {
		return AccountCredential{}, opaqueError("begin Administrator bootstrap", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()

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

	credentialCount, err := activeCredentialCount(ctx, transaction.Client())
	if err != nil {
		return AccountCredential{}, err
	}
	if credentialCount != 0 {
		return AccountCredential{}, ErrInvalidBootstrap
	}
	accounts, err := transaction.Account.Query().All(ctx)
	if err != nil {
		return AccountCredential{}, opaqueError("read Accounts during bootstrap", err)
	}
	var administrator *ent.Account
	switch len(accounts) {
	case 0:
		administrator, err = transaction.Account.Create().
			SetName(params.Name).
			SetNormalizedName(params.NormalizedName).
			SetWebauthnUserHandle(params.WebAuthnUserHandle).
			SetAdministrator(true).
			SetCreatedAt(params.Now).
			Save(ctx)
		if err != nil {
			return AccountCredential{}, opaqueError("create first Administrator", err)
		}
	default:
		for _, candidate := range accounts {
			if candidate.NormalizedName == params.NormalizedName &&
				candidate.Administrator &&
				candidate.DisabledAt == nil {
				administrator = candidate
				break
			}
		}
		if administrator == nil {
			return AccountCredential{}, ErrInvalidBootstrap
		}
	}
	_, credentialCreateErr := transaction.PasswordCredential.Create().
		SetAccountID(administrator.ID).
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
		administrator.ID,
		params.SessionHash,
		params.Now,
		params.SessionExpiry,
	); err != nil {
		return AccountCredential{}, opaqueError("create initial Account session", err)
	}
	if err := transaction.Commit(); err != nil {
		return AccountCredential{}, opaqueError("commit first Administrator", err)
	}
	return accountCredential(administrator, params.PasswordHash), nil
}

// FindAccountCredential returns the enabled Account matching a normalized name.
func (installation *SQLite) FindAccountCredential(
	ctx context.Context,
	normalizedName string,
) (AccountCredential, bool, error) {
	if installation.applied < 55 {
		var found AccountCredential
		err := installation.database.QueryRowContext(
			ctx,
			`SELECT accounts.id, accounts.normalized_name, accounts.name,
				accounts.administrator, password_credentials.password_hash
			FROM password_credentials
			JOIN accounts ON accounts.id = password_credentials.account_id
			WHERE accounts.normalized_name = ? AND accounts.disabled_at IS NULL
				AND password_credentials.revoked_at IS NULL`,
			normalizedName,
		).Scan(
			&found.ID,
			&found.Handle,
			&found.Name,
			&found.Administrator,
			&found.PasswordHash,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return AccountCredential{}, false, nil
		}
		if err != nil {
			return AccountCredential{}, false, opaqueError("read migration Account credential", err)
		}
		return found, true, nil
	}
	ctx = systemContext(ctx)
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

// ReplaceRecoveryCodes invalidates prior codes and stores one replacement digest set.
func (transaction *CommandTx) ReplaceRecoveryCodes(
	ctx context.Context,
	accountID int,
	codeHashes []string,
	now time.Time,
) error {
	ctx = systemContext(ctx)
	if _, err := transaction.transaction.RecoveryCode.Delete().
		Where(recoverycode.AccountIDEQ(accountID)).
		Exec(ctx); err != nil {
		return opaqueError("replace prior Recovery Codes", err)
	}
	builders := make([]*ent.RecoveryCodeCreate, 0, len(codeHashes))
	for _, codeHash := range codeHashes {
		builders = append(builders, transaction.transaction.RecoveryCode.Create().
			SetAccountID(accountID).
			SetCodeHash(codeHash).
			SetCreatedAt(now))
	}
	if _, err := transaction.transaction.RecoveryCode.CreateBulk(builders...).Save(ctx); err != nil {
		return opaqueError("store Recovery Codes", err)
	}
	return nil
}

// IssueRecoveryToken records one short-lived token and revokes the target's sessions.
func (transaction *CommandTx) IssueRecoveryToken(
	ctx context.Context,
	params RecoveryTokenParams,
) error {
	authorizedContext := ctx
	internalContext := systemContext(ctx)
	target, err := transaction.transaction.Account.Query().Where(
		account.IDEQ(params.AccountID),
		account.DisabledAtIsNil(),
	).Only(authorizedContext)
	if ent.IsNotFound(err) {
		return ErrInvalidRecovery
	}
	if err != nil {
		return opaqueError("load recovery Account", err)
	}
	if target.Administrator {
		administrators, countErr := transaction.transaction.Account.Query().Where(
			account.AdministratorEQ(true),
			account.DisabledAtIsNil(),
		).Count(authorizedContext)
		if countErr != nil {
			return opaqueError("count enabled Administrators for recovery", countErr)
		}
		if administrators <= 1 {
			return ErrLastAdministrator
		}
	}
	if _, err = transaction.transaction.RecoveryToken.Delete().
		Where(recoverytoken.AccountIDEQ(params.AccountID)).
		Exec(internalContext); err != nil {
		return opaqueError("replace prior Recovery Tokens", err)
	}
	if _, err = transaction.transaction.RecoveryToken.Create().
		SetAccountID(params.AccountID).
		SetTokenHash(params.TokenHash).
		SetCreatedAt(params.Now).
		SetExpiresAt(params.ExpiresAt).
		Save(internalContext); err != nil {
		return opaqueError("store Recovery Token", err)
	}
	if _, err = transaction.transaction.AccountSession.Update().
		Where(
			accountsession.AccountIDEQ(params.AccountID),
			accountsession.RevokedAtIsNil(),
		).
		SetRevokedAt(params.Now).
		Save(internalContext); err != nil {
		return opaqueError("revoke Account sessions for recovery", err)
	}
	return nil
}

// FindRecoveryTarget identifies an enabled Account before its proof is
// validated inside the recovery command transaction.
func (installation *SQLite) FindRecoveryTarget(
	ctx context.Context,
	normalizedName string,
) (AccountCredential, error) {
	ctx = systemContext(ctx)
	found, err := findEnabledRecoveryAccount(ctx, installation.client, normalizedName)
	if err != nil {
		return AccountCredential{}, err
	}
	return accountCredential(found, ""), nil
}

// RecoverAccount consumes one Account or Administrator recovery credential,
// replaces the password, and creates the only post-recovery session.
func (transaction *CommandTx) RecoverAccount(
	ctx context.Context,
	params RecoverAccountParams,
) (AccountCredential, error) {
	ctx = systemContext(ctx)
	client := transaction.transaction.Client()
	found, codeID, tokenID, err := findRecoveryAccount(
		ctx,
		client,
		params.NormalizedName,
		params.SecretHash,
		params.Now,
	)
	if err != nil {
		return AccountCredential{}, err
	}
	if codeID != 0 {
		updated, updateErr := client.RecoveryCode.Update().Where(
			recoverycode.IDEQ(codeID),
			recoverycode.UsedAtIsNil(),
		).SetUsedAt(params.Now).Save(ctx)
		if updateErr != nil {
			return AccountCredential{}, opaqueError("consume Recovery Code", updateErr)
		}
		if updated != 1 {
			return AccountCredential{}, ErrInvalidRecovery
		}
	} else {
		updated, updateErr := client.RecoveryToken.Update().Where(
			recoverytoken.IDEQ(tokenID),
			recoverytoken.UsedAtIsNil(),
			recoverytoken.ExpiresAtGT(params.Now),
		).SetUsedAt(params.Now).Save(ctx)
		if updateErr != nil {
			return AccountCredential{}, opaqueError("consume Recovery Token", updateErr)
		}
		if updated != 1 {
			return AccountCredential{}, ErrInvalidRecovery
		}
	}
	credential, err := client.PasswordCredential.Query().
		Where(passwordcredential.AccountIDEQ(found.ID)).
		Only(ctx)
	switch {
	case ent.IsNotFound(err):
		_, err = client.PasswordCredential.Create().
			SetAccountID(found.ID).
			SetPasswordHash(params.PasswordHash).
			SetCreatedAt(params.Now).
			Save(ctx)
	case err == nil:
		_, err = credential.Update().
			SetPasswordHash(params.PasswordHash).
			ClearRevokedAt().
			Save(ctx)
	}
	if err != nil {
		return AccountCredential{}, opaqueError("replace Account password", err)
	}
	if _, err = client.AccountSession.Update().
		Where(
			accountsession.AccountIDEQ(found.ID),
			accountsession.RevokedAtIsNil(),
		).
		SetRevokedAt(params.Now).
		Save(ctx); err != nil {
		return AccountCredential{}, opaqueError("revoke recovered Account sessions", err)
	}
	if err = createAccountSession(
		ctx,
		client.AccountSession,
		found.ID,
		params.SessionHash,
		params.Now,
		params.SessionExpiry,
	); err != nil {
		return AccountCredential{}, opaqueError("create recovered Account session", err)
	}
	return accountCredential(found, params.PasswordHash), nil
}

func findRecoveryAccount(
	ctx context.Context,
	client *ent.Client,
	normalizedName string,
	secretHash string,
	now time.Time,
) (*ent.Account, int, int, error) {
	found, err := findEnabledRecoveryAccount(ctx, client, normalizedName)
	if err != nil {
		return nil, 0, 0, err
	}
	if found.Administrator {
		administrators, countErr := client.Account.Query().Where(
			account.AdministratorEQ(true),
			account.DisabledAtIsNil(),
		).Count(ctx)
		if countErr != nil {
			return nil, 0, 0, opaqueError(
				"count enabled Administrators for recovery",
				countErr,
			)
		}
		if administrators <= 1 {
			return nil, 0, 0, ErrLastAdministrator
		}
	}
	code, codeErr := client.RecoveryCode.Query().Where(
		recoverycode.AccountIDEQ(found.ID),
		recoverycode.CodeHashEQ(secretHash),
		recoverycode.UsedAtIsNil(),
	).Only(ctx)
	if code != nil {
		return found, code.ID, 0, nil
	}
	if codeErr != nil && !ent.IsNotFound(codeErr) {
		return nil, 0, 0, opaqueError("read Recovery Code", codeErr)
	}
	token, tokenErr := client.RecoveryToken.Query().Where(
		recoverytoken.AccountIDEQ(found.ID),
		recoverytoken.TokenHashEQ(secretHash),
		recoverytoken.UsedAtIsNil(),
		recoverytoken.ExpiresAtGT(now),
	).Only(ctx)
	if ent.IsNotFound(tokenErr) {
		return nil, 0, 0, ErrInvalidRecovery
	}
	if tokenErr != nil {
		return nil, 0, 0, opaqueError("read Recovery Token", tokenErr)
	}
	return found, 0, token.ID, nil
}

func findEnabledRecoveryAccount(
	ctx context.Context,
	client *ent.Client,
	normalizedName string,
) (*ent.Account, error) {
	found, err := client.Account.Query().Where(
		account.NormalizedNameEQ(normalizedName),
		account.DisabledAtIsNil(),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrInvalidRecovery
	}
	if err != nil {
		return nil, opaqueError("load Account for recovery", err)
	}
	return found, nil
}

// CreateAccountSession persists a new session for an authenticated Account.
func (installation *SQLite) CreateAccountSession(
	ctx context.Context,
	accountID int,
	tokenHash string,
	now time.Time,
	expiresAt time.Time,
) ([]string, error) {
	ctx = systemContext(ctx)
	transaction, err := installation.client.Tx(ctx)
	if err != nil {
		return nil, opaqueError("begin Account session creation", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	revoked, err := createBoundedAccountSession(
		ctx,
		transaction,
		accountID,
		tokenHash,
		now,
		expiresAt,
	)
	if err != nil {
		return nil, err
	}
	if err = transaction.Commit(); err != nil {
		return nil, opaqueError("commit Account session creation", err)
	}
	return revoked, nil
}

func createBoundedAccountSession(
	ctx context.Context,
	transaction *ent.Tx,
	accountID int,
	tokenHash string,
	now time.Time,
	expiresAt time.Time,
) ([]string, error) {
	if _, err := transaction.AccountSession.Delete().Where(
		accountsession.Or(
			accountsession.RevokedAtNotNil(),
			accountsession.ExpiresAtLTE(now),
		),
	).Exec(ctx); err != nil {
		return nil, opaqueError("prune inactive Account sessions", err)
	}
	if createErr := createAccountSession(
		ctx,
		transaction.AccountSession,
		accountID,
		tokenHash,
		now,
		expiresAt,
	); createErr != nil {
		return nil, opaqueError("create Account session", createErr)
	}
	active, err := transaction.AccountSession.Query().
		Where(
			accountsession.AccountIDEQ(accountID),
			accountsession.RevokedAtIsNil(),
			accountsession.ExpiresAtGT(now),
		).
		Order(
			ent.Asc(accountsession.FieldCreatedAt),
			ent.Asc(accountsession.FieldID),
		).
		All(ctx)
	if err != nil {
		return nil, opaqueError("list active Account sessions", err)
	}
	var revoked []string
	if excess := len(active) - MaxActiveSessionsPerAccount; excess > 0 {
		ids := make([]int, excess)
		revoked = make([]string, excess)
		for index, session := range active[:excess] {
			ids[index] = session.ID
			revoked[index] = session.TokenHash
		}
		if _, err = transaction.AccountSession.Delete().
			Where(accountsession.IDIn(ids...)).
			Exec(ctx); err != nil {
			return nil, opaqueError("revoke oldest Account sessions", err)
		}
	}
	return revoked, nil
}

// CountAccountSessions returns token-free durable session counts.
func (installation *SQLite) CountAccountSessions(
	ctx context.Context,
	now time.Time,
) (AccountSessionCounts, error) {
	internalContext := systemContext(ctx)
	stored, err := installation.client.AccountSession.Query().Count(internalContext)
	if err != nil {
		return AccountSessionCounts{}, opaqueError("count stored Account sessions", err)
	}
	active, err := installation.client.AccountSession.Query().
		Where(
			accountsession.RevokedAtIsNil(),
			accountsession.ExpiresAtGT(now),
			accountsession.HasAccountWith(account.DisabledAtIsNil()),
		).
		Count(internalContext)
	if err != nil {
		return AccountSessionCounts{}, opaqueError("count active Account sessions", err)
	}
	return AccountSessionCounts{Active: active, Stored: stored}, nil
}

// FindAccountSession returns the enabled Account for an active session.
func (installation *SQLite) FindAccountSession(
	ctx context.Context,
	tokenHash string,
	now time.Time,
) (AccountCredential, error) {
	if installation.applied < 55 {
		var found AccountCredential
		err := installation.database.QueryRowContext(
			ctx,
			`SELECT accounts.id, accounts.normalized_name, accounts.name,
				accounts.administrator, account_sessions.expires_at
			FROM account_sessions
			JOIN accounts ON accounts.id = account_sessions.account_id
			WHERE account_sessions.token_hash = ?
				AND account_sessions.revoked_at IS NULL
				AND account_sessions.expires_at > ?
				AND accounts.disabled_at IS NULL`,
			tokenHash,
			now,
		).Scan(
			&found.ID,
			&found.Handle,
			&found.Name,
			&found.Administrator,
			&found.SessionExpiresAt,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return AccountCredential{}, ErrInvalidSession
		}
		if err != nil {
			return AccountCredential{}, opaqueError("read migration Account session", err)
		}
		return found, nil
	}
	ctx = systemContext(ctx)
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
	credential.EventRoles, credential.EventNames, credential.EventScopes, err =
		installation.findEventAccess(ctx, found.ID)
	if err != nil {
		return AccountCredential{}, err
	}
	credential.SessionExpiresAt = session.ExpiresAt
	return credential, nil
}

func (installation *SQLite) findEventAccess(
	ctx context.Context,
	accountID int,
) (map[int]viewer.Role, map[int]string, map[int]viewer.EventScope, error) {
	found, err := installation.client.EventGrant.Query().
		Where(eventgrant.AccountIDEQ(accountID)).
		WithEvent().
		All(systemContext(ctx))
	if err != nil {
		return nil, nil, nil, opaqueError("read Account Event Grants", err)
	}
	roles := make(map[int]viewer.Role, len(found))
	names := make(map[int]string, len(found))
	scopes := make(map[int]viewer.EventScope, len(found))
	for _, grant := range found {
		grantedEvent, eventErr := grant.Edges.EventOrErr()
		if eventErr != nil {
			return nil, nil, nil, opaqueError("read granted Event", eventErr)
		}
		roles[grant.EventID] = viewer.Role(grant.Role)
		names[grant.EventID] = grantedEvent.Name
		scope := viewer.EventScope{
			LaneIDs:          make(map[int]struct{}, len(grant.LaneIds)),
			DisplayGroupKeys: make(map[string]struct{}, len(grant.DisplayGroupKeys)),
			Capabilities:     make(map[viewer.Capability]struct{}, len(grant.Capabilities)),
		}
		for _, laneID := range grant.LaneIds {
			scope.LaneIDs[laneID] = struct{}{}
		}
		for _, key := range grant.DisplayGroupKeys {
			scope.DisplayGroupKeys[key] = struct{}{}
		}
		for _, capability := range grant.Capabilities {
			scope.Capabilities[viewer.Capability(capability)] = struct{}{}
		}
		scopes[grant.EventID] = scope
	}
	return roles, names, scopes, nil
}

// RevokeAccountSession makes a session durably unusable. Unknown and already
// revoked session tokens have the same successful result.
func (installation *SQLite) RevokeAccountSession(
	ctx context.Context,
	tokenHash string,
	now time.Time,
) error {
	ctx = systemContext(ctx)
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
		ID:                 found.ID,
		Handle:             found.NormalizedName,
		Name:               found.Name,
		PasswordHash:       passwordHash,
		WebAuthnUserHandle: slices.Clone(found.WebauthnUserHandle),
		Administrator:      found.Administrator,
	}
}

func opaqueError(action string, err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("%s: %w", action, context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%s: %w", action, context.DeadlineExceeded)
	}
	return errors.New(action + ": " + err.Error())
}
