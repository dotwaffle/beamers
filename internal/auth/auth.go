// Package auth establishes and authenticates individual Beamers Accounts.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"

	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/viewer"
)

const (
	defaultBootstrapTTL     = 15 * time.Minute
	defaultRecoveryTokenTTL = 15 * time.Minute
	defaultSessionTTL       = 12 * time.Hour
	recoveryCodeCount       = 8
	tokenBytes              = 32
	webAuthnUserHandleBytes = 64
	saltBytes               = 16
	passwordHashBytes       = 32
	argonTime               = 3
	argonMemory             = 64 * 1024
	argonThreads            = 4
	// Each Argon2id operation reserves 64 MiB. Cap simultaneous KDF memory at
	// 128 MiB while permitting two independent Crew Members to authenticate.
	passwordMemoryBudget = 128 * 1024
	passwordConcurrency  = passwordMemoryBudget / argonMemory
)

var (
	// ErrAuthenticationFailed is the public classification for invalid Account
	// or bootstrap credentials.
	ErrAuthenticationFailed = errors.New("authentication failed")
	// ErrAuthenticationBusy means password work is at its safe concurrency bound.
	ErrAuthenticationBusy = errors.New("authentication busy")
	// ErrInvalidAccountDetails means a proposed first Account is not valid.
	ErrInvalidAccountDetails = errors.New("invalid account details")
	// ErrInvalidSession means a session token is expired, revoked, or unknown.
	ErrInvalidSession = errors.New("authentication required")
	// ErrBootstrapUnavailable means host bootstrap cannot issue a credential.
	ErrBootstrapUnavailable = store.ErrBootstrapUnavailable
	// ErrAdministratorRequired means Account administration lacked installation authority.
	ErrAdministratorRequired = errors.New("administrator authority required")
	// ErrAccountExists means the requested Account name is already in use.
	ErrAccountExists = store.ErrAccountExists
	// ErrRegistrationClosed means visitors cannot create new Accounts.
	ErrRegistrationClosed = store.ErrRegistrationClosed
	// ErrProfileEntryUnavailable means a Profile selected an unreleased Entry.
	ErrProfileEntryUnavailable = store.ErrProfileEntryUnavailable
	// ErrDisableAccountNotFound means the target is unknown or already disabled.
	ErrDisableAccountNotFound = store.ErrDisableAccountNotFound
	// ErrLastAdministrator means retirement would remove all installation administration.
	ErrLastAdministrator = store.ErrLastAdministrator
	// ErrDisableReasonRequired means retirement lacks safe audit evidence.
	ErrDisableReasonRequired = errors.New("disable reason is required")
	// ErrRecoveryReasonRequired means assisted recovery lacks offline verification evidence.
	ErrRecoveryReasonRequired = errors.New("recovery verification reason is required")
	// ErrRecoveryAccountNotFound means assisted recovery targeted no enabled Account.
	ErrRecoveryAccountNotFound = errors.New("recovery Account not found")
	// ErrRecoveryTokenAlreadyIssued means a retry cannot reveal a write-only token.
	ErrRecoveryTokenAlreadyIssued = errors.New("recovery token was already issued")
	// ErrRecoveryCodesAlreadyReplaced means a retry cannot reveal write-only codes.
	ErrRecoveryCodesAlreadyReplaced = errors.New("recovery codes were already replaced")
	// ErrRecoveryAlreadyCompleted means a retry cannot recreate its write-only session.
	ErrRecoveryAlreadyCompleted = errors.New("Account recovery was already completed")
	// ErrCommandConflict means a Command ID was reused for different Account work.
	ErrCommandConflict = store.ErrCommandConflict
	// ErrStorageDegraded means only previously validated sessions may continue
	// to the nondurable Emergency Alert path.
	ErrStorageDegraded = errors.New("storage is degraded")
	// ErrFinalCredential means removal would leave an enabled Account unable to authenticate.
	ErrFinalCredential = store.ErrFinalCredential
)

// Account is the authenticated identity exposed above the persistence boundary.
type Account struct {
	ID            int
	Handle        string
	Name          string
	Administrator bool
	EventRoles    map[int]viewer.Role
	EventNames    map[int]string
	EventScopes   map[int]viewer.EventScope
}

// Profile is one Account's private settings or public projection.
type Profile struct {
	Handle           string
	DisplayName      string
	Published        bool
	Entries          []ProfileEntry
	AvailableEntries []ProfileEntry
}

// ProfileEntry is one released Entry selected for a Public Profile.
type ProfileEntry struct {
	ID       int
	Name     string
	Selected bool
}

// AuditEntry is one Administrator-readable authenticated action.
type AuditEntry struct {
	ID             int       `json:"id"`
	ActorKind      string    `json:"actor_kind,omitempty"`
	ActorAccountID int       `json:"actor_account_id"`
	ActorName      string    `json:"actor_name"`
	ServerTime     time.Time `json:"server_time"`
	Action         string    `json:"action"`
	TargetType     string    `json:"target_type"`
	TargetID       string    `json:"target_id"`
	Outcome        string    `json:"outcome"`
	Reason         string    `json:"reason,omitempty"`
	Note           string    `json:"note,omitempty"`
}

// Session is a newly authenticated session. Token is returned only to the
// transport that will place it in a protected cookie.
type Session struct {
	Token     string
	ExpiresAt time.Time
	Account   Account
}

// RecoveryToken is a write-only Administrator-assisted Account recovery credential.
type RecoveryToken struct {
	Token     string
	ExpiresAt time.Time
}

// SessionCounts is token-free bounded authentication diagnostic data.
type SessionCounts struct {
	Active          int
	Cached          int
	Stored          int
	PerAccountLimit int
}

// Config contains explicit authentication dependencies and lifetimes.
type Config struct {
	Now              func() time.Time
	Random           io.Reader
	BootstrapTTL     time.Duration
	RecoveryTokenTTL time.Duration
	SessionTTL       time.Duration
	StorageState     StorageState
	// AllowDemoPassword permits the fixed weak credential only while seeding demo data.
	AllowDemoPassword bool
}

// StorageState reports whether runtime storage has entered degraded operation.
type StorageState interface {
	Degraded() bool
	PrepareEmergencyStorage(context.Context) error
}

// DefaultConfig returns production authentication dependencies and lifetimes.
func DefaultConfig() Config {
	return Config{
		Now:              time.Now,
		Random:           rand.Reader,
		BootstrapTTL:     defaultBootstrapTTL,
		RecoveryTokenTTL: defaultRecoveryTokenTTL,
		SessionTTL:       defaultSessionTTL,
	}
}

// Service owns credential hashing and session lifecycle rules.
type Service struct {
	storage            *store.SQLite
	now                func() time.Time
	random             io.Reader
	bootstrapTTL       time.Duration
	recoveryTokenTTL   time.Duration
	sessionTTL         time.Duration
	dummyHash          string
	passwordWork       chan struct{}
	sessionMu          sync.RWMutex
	sessions           map[string]validatedSession
	webAuthnMu         sync.Mutex
	webAuthnCeremonies map[string]webAuthnCeremony
	storageState       StorageState
	allowDemoPassword  bool
}

type validatedSession struct {
	account   Account
	expiresAt time.Time
}

// New creates an authentication Service with explicit dependencies.
func New(storage *store.SQLite, config Config) (*Service, error) {
	if storage == nil {
		return nil, errors.New("authentication storage is required")
	}
	if config.Now == nil {
		return nil, errors.New("authentication clock is required")
	}
	if config.Random == nil {
		return nil, errors.New("authentication randomness is required")
	}
	if config.BootstrapTTL <= 0 {
		return nil, errors.New("bootstrap lifetime must be positive")
	}
	if config.RecoveryTokenTTL <= 0 {
		return nil, errors.New("recovery token lifetime must be positive")
	}
	if config.SessionTTL <= 0 {
		return nil, errors.New("session lifetime must be positive")
	}
	dummyHash := formatPasswordHash(
		[]byte("BeamersAuthSalt!"),
		make([]byte, passwordHashBytes),
		argonParameters{
			time:    argonTime,
			memory:  argonMemory,
			threads: argonThreads,
		},
	)
	return &Service{
		storage:            storage,
		now:                config.Now,
		random:             config.Random,
		bootstrapTTL:       config.BootstrapTTL,
		recoveryTokenTTL:   config.RecoveryTokenTTL,
		sessionTTL:         config.SessionTTL,
		dummyHash:          dummyHash,
		passwordWork:       make(chan struct{}, passwordConcurrency),
		sessions:           make(map[string]validatedSession),
		webAuthnCeremonies: make(map[string]webAuthnCeremony),
		storageState:       config.StorageState,
		allowDemoPassword:  config.AllowDemoPassword,
	}, nil
}

// IssueBootstrap creates a host-authorized short-lived first-Administrator credential.
func (service *Service) IssueBootstrap(ctx context.Context) (string, error) {
	if service.storageDegraded() {
		return "", ErrStorageDegraded
	}
	token, err := service.newToken()
	if err != nil {
		return "", err
	}
	now := service.now().UTC()
	if err := service.storage.IssueBootstrap(
		ctx,
		tokenDigest(token),
		now,
		now.Add(service.bootstrapTTL),
	); err != nil {
		return "", err
	}
	return token, nil
}

// SetupRequired reports whether browser setup still needs a first Account.
func (service *Service) SetupRequired(ctx context.Context) (bool, error) {
	if service.storageDegraded() {
		return false, ErrStorageDegraded
	}
	return service.storage.SetupRequired(ctx)
}

// BootstrapAdministrator consumes a bootstrap credential and creates the first
// Administrator together with an authenticated session.
func (service *Service) BootstrapAdministrator(
	ctx context.Context,
	bootstrapToken string,
	name string,
	password string,
) (Session, error) {
	return service.BootstrapFirstAccount(ctx, bootstrapToken, name, name, password)
}

// BootstrapFirstAccount consumes a bootstrap credential and creates the first
// Administrator Account with separate sign-in and display names.
func (service *Service) BootstrapFirstAccount(
	ctx context.Context,
	bootstrapToken string,
	handle string,
	displayName string,
	password string,
) (Session, error) {
	if service.storageDegraded() {
		return Session{}, ErrStorageDegraded
	}
	normalizedName, _, handleErr := normalizeAccountName(handle)
	displayName, displayErr := normalizeDisplayName(displayName)
	if handleErr != nil || displayErr != nil ||
		!service.validPassword(password) || !validToken(bootstrapToken) {
		return Session{}, ErrInvalidAccountDetails
	}
	passwordHash, err := service.hashPassword(password)
	if err != nil {
		return Session{}, err
	}
	sessionToken, err := service.newToken()
	if err != nil {
		return Session{}, err
	}
	userHandle, err := service.newWebAuthnUserHandle()
	if err != nil {
		return Session{}, err
	}
	now := service.now().UTC()
	expiresAt := now.Add(service.sessionTTL)
	created, err := service.storage.BootstrapAdministrator(
		ctx,
		store.BootstrapAdministratorParams{
			BootstrapHash:      tokenDigest(bootstrapToken),
			Name:               displayName,
			NormalizedName:     normalizedName,
			WebAuthnUserHandle: userHandle,
			PasswordHash:       passwordHash,
			SessionHash:        tokenDigest(sessionToken),
			Now:                now,
			SessionExpiry:      expiresAt,
		},
	)
	if errors.Is(err, store.ErrInvalidBootstrap) {
		return Session{}, ErrAuthenticationFailed
	}
	if err != nil {
		return Session{}, err
	}
	session := newSession(sessionToken, expiresAt, created)
	service.rememberSession(sessionToken, session.Account, expiresAt)
	return session, nil
}

// SignIn verifies an Account credential without distinguishing unknown,
// disabled, and incorrect credentials.
func (service *Service) SignIn(ctx context.Context, name, password string) (Session, error) {
	if service.storageDegraded() {
		return Session{}, ErrStorageDegraded
	}
	normalizedName, _, nameErr := normalizeAccountName(name)
	credential, found, err := service.storage.FindAccountCredential(ctx, normalizedName)
	if err != nil {
		return Session{}, err
	}
	passwordHash := service.dummyHash
	if found && nameErr == nil {
		passwordHash = credential.PasswordHash
	}
	matches, err := service.comparePassword(passwordHash, password)
	if errors.Is(err, ErrAuthenticationBusy) {
		return Session{}, ErrAuthenticationBusy
	}
	if err != nil {
		return Session{}, errors.New("verify Account credential")
	}
	if nameErr != nil || !found || !matches {
		return Session{}, ErrAuthenticationFailed
	}
	if service.storageState != nil {
		prepareErr := service.storageState.PrepareEmergencyStorage(ctx)
		if prepareErr != nil {
			if errors.Is(prepareErr, context.Canceled) ||
				errors.Is(prepareErr, context.DeadlineExceeded) {
				return Session{}, prepareErr
			}
			return Session{}, ErrStorageDegraded
		}
		if service.storageDegraded() {
			return Session{}, ErrStorageDegraded
		}
	}

	token, err := service.newToken()
	if err != nil {
		return Session{}, err
	}
	now := service.now().UTC()
	expiresAt := now.Add(service.sessionTTL)
	revoked, err := service.storage.CreateAccountSession(
		ctx,
		credential.ID,
		tokenDigest(token),
		now,
		expiresAt,
	)
	if err != nil {
		return Session{}, err
	}
	service.pruneSessionCache(now, revoked)
	session := newSession(token, expiresAt, credential)
	service.rememberSession(token, session.Account, expiresAt)
	return session, nil
}

// ReplaceRecoveryCodes invalidates prior codes and returns one write-only replacement set.
func (service *Service) ReplaceRecoveryCodes(
	ctx context.Context,
	actor Account,
	commandID string,
) ([]string, error) {
	if service.storageDegraded() {
		return nil, ErrStorageDegraded
	}
	if actor.ID <= 0 {
		return nil, ErrInvalidSession
	}
	if err := command.ValidateID(commandID); err != nil {
		return nil, ErrInvalidAccountDetails
	}
	codes := make([]string, recoveryCodeCount)
	hashes := make([]string, recoveryCodeCount)
	for index := range codes {
		code, err := service.newToken()
		if err != nil {
			return nil, err
		}
		codes[index] = code
		hashes[index] = tokenDigest(code)
	}
	now := service.now().UTC()
	result, err := command.Execute(actor.Context(ctx), command.Plan[[]string]{
		Storage: service.storage,
		Identity: store.CommandIdentity{
			ActorAccountID: actor.ID,
			CommandID:      commandID,
			PayloadHash:    command.PayloadHash(strconv.Itoa(actor.ID)),
			Action:         "ReplaceRecoveryCodes",
			TargetType:     "Account",
			TargetID:       strconv.Itoa(actor.ID),
			Now:            now,
		},
		Replay: func(outcome string) ([]string, error) {
			var original struct {
				Count int `json:"count"`
			}
			if decodeErr := store.DecodeCommandReceipt(outcome, &original); decodeErr != nil {
				return nil, decodeErr
			}
			return nil, nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[[]string], error) {
			if replaceErr := transaction.ReplaceRecoveryCodes(
				actor.Context(ctx),
				actor.ID,
				hashes,
				now,
			); replaceErr != nil {
				return command.Execution[[]string]{}, replaceErr
			}
			outcome, encodeErr := json.Marshal(struct {
				Count int `json:"count"`
			}{Count: len(codes)})
			if encodeErr != nil {
				return command.Execution[[]string]{}, errors.New(
					"encode Recovery Code replacement outcome",
				)
			}
			return command.Success(codes, string(outcome)), nil
		},
	})
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, ErrRecoveryCodesAlreadyReplaced
	}
	return result, nil
}

// Recover consumes one Account or Administrator recovery credential and
// replaces the Account password and sessions atomically.
func (service *Service) Recover(
	ctx context.Context,
	handle string,
	secret string,
	password string,
	commandID string,
) (Session, error) {
	if service.storageDegraded() {
		return Session{}, ErrStorageDegraded
	}
	normalizedName, _, nameErr := normalizeAccountName(handle)
	if nameErr != nil || !validToken(secret) {
		return Session{}, ErrAuthenticationFailed
	}
	if err := command.ValidateID(commandID); err != nil {
		return Session{}, ErrInvalidAccountDetails
	}
	if !service.validPassword(password) {
		return Session{}, ErrInvalidAccountDetails
	}
	passwordHash, err := service.hashPassword(password)
	if err != nil {
		return Session{}, err
	}
	sessionToken, err := service.newToken()
	if err != nil {
		return Session{}, err
	}
	now := service.now().UTC()
	expiresAt := now.Add(service.sessionTTL)
	secretHash := tokenDigest(secret)
	recoverable, err := service.storage.FindRecoveryTarget(ctx, normalizedName)
	if errors.Is(err, store.ErrInvalidRecovery) {
		return Session{}, ErrAuthenticationFailed
	}
	if err != nil {
		return Session{}, err
	}
	type recoveryCommandResult struct {
		account store.AccountCredential
		applied bool
	}
	recovered, err := command.Execute(
		account(recoverable).Context(ctx),
		command.Plan[recoveryCommandResult]{
			Storage: service.storage,
			Identity: store.CommandIdentity{
				ActorAccountID: recoverable.ID,
				CommandID:      commandID,
				PayloadHash:    command.PayloadHash(normalizedName),
				Action:         "RecoverAccount",
				TargetType:     "Account",
				TargetID:       strconv.Itoa(recoverable.ID),
				Now:            now,
			},
			Replay: func(outcome string) (recoveryCommandResult, error) {
				var original store.AccountCredential
				if decodeErr := store.DecodeCommandReceipt(outcome, &original); decodeErr != nil {
					return recoveryCommandResult{}, decodeErr
				}
				return recoveryCommandResult{account: original}, nil
			},
			Apply: func(transaction *store.CommandTx) (
				command.Execution[recoveryCommandResult],
				error,
			) {
				updated, recoverErr := transaction.RecoverAccount(
					account(recoverable).Context(ctx),
					store.RecoverAccountParams{
						NormalizedName: normalizedName,
						SecretHash:     secretHash,
						PasswordHash:   passwordHash,
						SessionHash:    tokenDigest(sessionToken),
						Now:            now,
						SessionExpiry:  expiresAt,
					},
				)
				if recoverErr != nil {
					return command.Execution[recoveryCommandResult]{}, recoverErr
				}
				outcome, encodeErr := json.Marshal(updated)
				if encodeErr != nil {
					return command.Execution[recoveryCommandResult]{}, errors.New(
						"encode Account recovery outcome",
					)
				}
				return command.Success(
					recoveryCommandResult{account: updated, applied: true},
					string(outcome),
				), nil
			},
		},
	)
	if errors.Is(err, store.ErrInvalidRecovery) || errors.Is(err, store.ErrLastAdministrator) {
		return Session{}, ErrAuthenticationFailed
	}
	if err != nil {
		return Session{}, err
	}
	if !recovered.applied {
		return Session{}, ErrRecoveryAlreadyCompleted
	}
	service.forgetAccountSessions(recovered.account.ID)
	session := newSession(sessionToken, expiresAt, recovered.account)
	service.rememberSession(sessionToken, session.Account, expiresAt)
	return session, nil
}

// IssueRecoveryToken creates one audited Administrator-assisted recovery grant.
func (service *Service) IssueRecoveryToken(
	ctx context.Context,
	actor Account,
	accountID int,
	reason string,
	commandID string,
) (RecoveryToken, error) {
	if service.storageDegraded() {
		return RecoveryToken{}, ErrStorageDegraded
	}
	if err := command.ValidateID(commandID); err != nil {
		return RecoveryToken{}, ErrInvalidAccountDetails
	}
	reason = strings.TrimSpace(reason)
	token, err := service.newToken()
	if err != nil {
		return RecoveryToken{}, err
	}
	now := service.now().UTC()
	expiresAt := now.Add(service.recoveryTokenTTL)
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID,
		CommandID:      commandID,
		PayloadHash:    command.PayloadHash(strconv.Itoa(accountID), reason),
		Action:         "IssueAccountRecoveryToken",
		TargetType:     "Account",
		TargetID:       strconv.Itoa(accountID),
		Now:            now,
	}
	result, err := command.Execute(actor.Context(ctx), command.Plan[RecoveryToken]{
		Storage:  service.storage,
		Identity: identity,
		Replay: func(outcome string) (RecoveryToken, error) {
			var original struct {
				AccountID int `json:"account_id"`
			}
			if decodeErr := store.DecodeCommandReceipt(outcome, &original); decodeErr != nil {
				return RecoveryToken{}, restoreRejected(decodeErr)
			}
			return RecoveryToken{}, nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[RecoveryToken], error) {
			switch {
			case !actor.Administrator:
				return accountRejectionExecution[RecoveryToken](ErrAdministratorRequired), nil
			case reason == "" || len(reason) > 1000:
				return accountRejectionExecution[RecoveryToken](ErrRecoveryReasonRequired), nil
			}
			issueErr := transaction.IssueRecoveryToken(actor.Context(ctx), store.RecoveryTokenParams{
				AccountID: accountID,
				TokenHash: tokenDigest(token),
				Now:       now,
				ExpiresAt: expiresAt,
			})
			switch {
			case errors.Is(issueErr, store.ErrInvalidRecovery):
				return accountRejectionExecution[RecoveryToken](ErrRecoveryAccountNotFound), nil
			case errors.Is(issueErr, store.ErrLastAdministrator):
				return accountRejectionExecution[RecoveryToken](ErrLastAdministrator), nil
			case issueErr != nil:
				return command.Execution[RecoveryToken]{}, issueErr
			}
			outcome, encodeErr := json.Marshal(struct {
				AccountID int `json:"account_id"`
			}{AccountID: accountID})
			if encodeErr != nil {
				return command.Execution[RecoveryToken]{}, errors.New("encode recovery token outcome")
			}
			return command.Success(
				RecoveryToken{Token: token, ExpiresAt: expiresAt},
				string(outcome),
			).WithAudit(store.AuditDetails{Reason: reason}), nil
		},
	})
	if err != nil {
		return RecoveryToken{}, err
	}
	if result.Token == "" {
		return RecoveryToken{}, ErrRecoveryTokenAlreadyIssued
	}
	service.forgetAccountSessions(accountID)
	return result, nil
}

// SessionCounts returns token-free active durable and in-memory session counts.
func (service *Service) SessionCounts(ctx context.Context) (SessionCounts, error) {
	now := service.now().UTC()
	durable, err := service.storage.CountAccountSessions(ctx, now)
	if err != nil {
		return SessionCounts{}, err
	}
	service.pruneSessionCache(now, nil)
	service.sessionMu.RLock()
	cached := len(service.sessions)
	service.sessionMu.RUnlock()
	return SessionCounts{
		Active:          durable.Active,
		Cached:          cached,
		Stored:          durable.Stored,
		PerAccountLimit: store.MaxActiveSessionsPerAccount,
	}, nil
}

// Reauthenticate verifies the current Administrator without issuing a session.
func (service *Service) Reauthenticate(
	ctx context.Context,
	actor Account,
	password string,
) error {
	if !actor.Administrator {
		return ErrAdministratorRequired
	}
	handle := actor.Handle
	if handle == "" {
		handle = actor.Name
	}
	normalizedName, _, err := normalizeAccountName(handle)
	if err != nil {
		return ErrAuthenticationFailed
	}
	credential, found, err := service.storage.FindAccountCredential(ctx, normalizedName)
	if err != nil {
		return err
	}
	if !found || credential.ID != actor.ID {
		return ErrAuthenticationFailed
	}
	matches, err := service.comparePassword(credential.PasswordHash, password)
	if err != nil {
		return err
	}
	if !matches {
		return ErrAuthenticationFailed
	}
	return nil
}

// RegistrationOpen reports whether visitors may create Accounts.
func (service *Service) RegistrationOpen(ctx context.Context) (bool, error) {
	if service.storageDegraded() {
		return false, ErrStorageDegraded
	}
	return service.storage.RegistrationOpen(ctx)
}

// SetRegistrationOpen updates the installation Registration Policy.
func (service *Service) SetRegistrationOpen(
	ctx context.Context,
	actor Account,
	open bool,
) error {
	if service.storageDegraded() {
		return ErrStorageDegraded
	}
	if !actor.Administrator {
		return ErrAdministratorRequired
	}
	return service.storage.SetRegistrationOpen(actor.Context(ctx), open)
}

// Register creates one visitor Account without requiring email.
func (service *Service) Register(
	ctx context.Context,
	handle string,
	displayName string,
	password string,
) (Account, error) {
	if service.storageDegraded() {
		return Account{}, ErrStorageDegraded
	}
	normalizedHandle, _, handleErr := normalizeAccountName(handle)
	displayName, displayErr := normalizeDisplayName(displayName)
	if handleErr != nil || displayErr != nil || !service.validPassword(password) {
		return Account{}, ErrInvalidAccountDetails
	}
	passwordHash, err := service.hashPassword(password)
	if err != nil {
		return Account{}, err
	}
	userHandle, err := service.newWebAuthnUserHandle()
	if err != nil {
		return Account{}, err
	}
	created, err := service.storage.RegisterAccount(ctx, store.CreateAccountParams{
		Name:               displayName,
		NormalizedName:     normalizedHandle,
		WebAuthnUserHandle: userHandle,
		PasswordHash:       passwordHash,
		Now:                service.now().UTC(),
	})
	if err != nil {
		return Account{}, err
	}
	return account(created), nil
}

// Profile returns the authenticated Account's private Profile settings.
func (service *Service) Profile(ctx context.Context, actor Account) (Profile, error) {
	found, exists, err := service.storage.AccountProfile(actor.Context(ctx), actor.ID)
	if err != nil {
		return Profile{}, err
	}
	result := Profile{Handle: actor.Handle, DisplayName: actor.Name}
	if exists {
		result = profile(found)
	}
	available, err := service.storage.ReleasedProfileEntries(actor.Context(ctx))
	if err != nil {
		return Profile{}, err
	}
	selected := make(map[int]struct{}, len(result.Entries))
	for _, entry := range result.Entries {
		selected[entry.ID] = struct{}{}
	}
	result.AvailableEntries = make([]ProfileEntry, 0, len(available))
	for _, entry := range available {
		_, ok := selected[entry.ID]
		result.AvailableEntries = append(
			result.AvailableEntries,
			ProfileEntry{ID: entry.ID, Name: entry.Name, Selected: ok},
		)
	}
	return result, nil
}

// PublicProfile returns the published Profile matching a Handle.
func (service *Service) PublicProfile(
	ctx context.Context,
	handle string,
) (Profile, bool, error) {
	normalizedHandle, valid := publicProfileHandle(handle)
	if !valid {
		return Profile{}, false, nil
	}
	found, ok, err := service.storage.PublicProfile(ctx, normalizedHandle)
	if err != nil || !ok {
		return Profile{}, ok, err
	}
	return profile(found), true, nil
}

func publicProfileHandle(handle string) (string, bool) {
	normalized, _, err := normalizeAccountName(handle)
	return normalized, err == nil
}

// UpdateProfile changes one Account's Display Name and public selection.
func (service *Service) UpdateProfile(
	ctx context.Context,
	actor Account,
	displayName string,
	published bool,
	entryIDs []int,
	commandID string,
) error {
	if service.storageDegraded() {
		return ErrStorageDegraded
	}
	displayName, err := normalizeDisplayName(displayName)
	if err != nil {
		return ErrInvalidAccountDetails
	}
	for _, entryID := range entryIDs {
		if entryID <= 0 {
			return ErrProfileEntryUnavailable
		}
	}
	if validateErr := command.ValidateID(commandID); validateErr != nil {
		return ErrInvalidAccountDetails
	}
	// Canonicalize EntryIDs before hashing so equivalent submissions (same
	// Entries in a different order, or with duplicates) share one command
	// identity instead of colliding as a payload mismatch. This mirrors the
	// canonicalization the store applies before persisting.
	entryIDs = slices.Compact(slices.Sorted(slices.Values(entryIDs)))
	payload, err := json.Marshal(struct {
		DisplayName string `json:"display_name"`
		Published   bool   `json:"published"`
		EntryIDs    []int  `json:"entry_ids"`
	}{DisplayName: displayName, Published: published, EntryIDs: entryIDs})
	if err != nil {
		return errors.New("encode Update Profile command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: commandID,
		PayloadHash: command.PayloadHash(string(payload)), Action: "UpdateAccountProfile",
		TargetType: "Account", TargetID: strconv.Itoa(actor.ID), Now: service.now().UTC(),
	}
	_, err = command.Execute(actor.Context(ctx), command.Plan[struct{}]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (struct{}, error) {
			var original store.AccountProfile
			if decodeErr := store.DecodeCommandReceipt(outcome, &original); decodeErr != nil {
				return struct{}{}, restoreRejected(decodeErr)
			}
			return struct{}{}, nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[struct{}], error) {
			updated, updateErr := transaction.UpdateAccountProfile(actor.Context(ctx), store.UpdateAccountProfileParams{
				AccountID: actor.ID, AccountHandle: actor.Handle,
				DisplayName: displayName, Published: published, EntryIDs: entryIDs,
			})
			if errors.Is(updateErr, ErrProfileEntryUnavailable) {
				return accountRejectionExecution[struct{}](ErrProfileEntryUnavailable), nil
			}
			if updateErr != nil {
				return command.Execution[struct{}]{}, updateErr
			}
			encoded, encodeErr := json.Marshal(updated)
			if encodeErr != nil {
				return command.Execution[struct{}]{}, errors.New("encode Update Profile outcome")
			}
			return command.Success(struct{}{}, string(encoded)), nil
		},
		// Applied fires only after a fresh Apply commits, never after a
		// Replay of an earlier command. Adopting displayName into the
		// session cache here (rather than from command.Execute's return
		// value) guards against a replayed older command clobbering the
		// cache with its own stale, historical Display Name.
		Applied: func() {
			service.sessionMu.Lock()
			for token, session := range service.sessions {
				if session.account.ID == actor.ID {
					session.account.Name = displayName
					service.sessions[token] = session
				}
			}
			service.sessionMu.Unlock()
		},
	})
	if err != nil {
		return err
	}
	return nil
}

func profile(found store.AccountProfile) Profile {
	entries := make([]ProfileEntry, 0, len(found.Entries))
	for _, entry := range found.Entries {
		entries = append(entries, ProfileEntry{ID: entry.ID, Name: entry.Name})
	}
	return Profile{
		Handle: found.Handle, DisplayName: found.DisplayName,
		Published: found.Published, Entries: entries,
	}
}

// CreateAccount creates an individual non-Administrator Account.
func (service *Service) CreateAccount(
	ctx context.Context,
	actor Account,
	name string,
	password string,
	commandID string,
) (Account, error) {
	return service.createAccount(ctx, actor, name, "", false, password, commandID)
}

// CreateAccountWithDisplayName creates an Account with a distinct Handle and Display Name.
func (service *Service) CreateAccountWithDisplayName(
	ctx context.Context,
	actor Account,
	handle string,
	displayName string,
	password string,
	commandID string,
) (Account, error) {
	return service.createAccount(ctx, actor, handle, displayName, true, password, commandID)
}

func (service *Service) createAccount(
	ctx context.Context,
	actor Account,
	handle string,
	displayName string,
	requireDisplayName bool,
	password string,
	commandID string,
) (Account, error) {
	if service.storageDegraded() {
		return Account{}, ErrStorageDegraded
	}
	identityName := handle
	defaultDisplayName := handle
	if normalizedName, normalizedDisplayName, normalizeErr := normalizeAccountName(handle); normalizeErr == nil {
		identityName = normalizedName
		defaultDisplayName = normalizedDisplayName
	}
	identityDisplayName := displayName
	switch {
	case requireDisplayName:
		if normalizedDisplayName, normalizeErr := normalizeDisplayName(displayName); normalizeErr == nil {
			identityDisplayName = normalizedDisplayName
		}
	case displayName == "":
		identityDisplayName = defaultDisplayName
	default:
		if normalizedDisplayName, normalizeErr := normalizeDisplayName(displayName); normalizeErr == nil {
			identityDisplayName = normalizedDisplayName
		}
	}
	payloadHash := command.PayloadHash(identityName)
	if requireDisplayName {
		payloadHash = command.PayloadHash(identityName, identityDisplayName)
	}
	if err := command.ValidateID(commandID); err != nil {
		return Account{}, ErrInvalidAccountDetails
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: commandID, PayloadHash: payloadHash,
		Action: "CreateAccount", TargetType: "Account", TargetID: "unidentified", Now: service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[Account]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (Account, error) {
			var original store.AccountCredential
			if decodeErr := store.DecodeCommandReceipt(outcome, &original); decodeErr != nil {
				return Account{}, restoreRejected(decodeErr)
			}
			return account(original), nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[Account], error) {
			if !actor.Administrator {
				return accountRejectionExecution[Account](ErrAdministratorRequired), nil
			}
			normalizedName, normalizedDisplayName, normalizeErr := normalizeAccountName(handle)
			if requireDisplayName && normalizeErr == nil {
				normalizedDisplayName, normalizeErr = normalizeDisplayName(displayName)
			}
			if normalizeErr != nil || !service.validPassword(password) {
				// The executor returns this rejection only after its Receipt and Audit Entry commit.
				//nolint:nilerr // A callback error would roll back the durable rejection.
				return accountRejectionExecution[Account](ErrInvalidAccountDetails), nil
			}
			passwordHash, hashErr := service.hashPassword(password)
			if hashErr != nil {
				return command.Execution[Account]{}, hashErr
			}
			userHandle, handleErr := service.newWebAuthnUserHandle()
			if handleErr != nil {
				return command.Execution[Account]{}, handleErr
			}
			created, createErr := transaction.CreateAccount(actor.Context(ctx), store.CreateAccountParams{
				ActorAccountID:     actor.ID,
				Name:               normalizedDisplayName,
				NormalizedName:     normalizedName,
				WebAuthnUserHandle: userHandle,
				PasswordHash:       passwordHash,
				Now:                identity.Now,
				CommandID:          commandID,
				PayloadHash:        payloadHash,
			})
			if errors.Is(createErr, ErrAccountExists) {
				return accountRejectionExecution[Account](createErr), nil
			}
			if createErr != nil {
				return command.Execution[Account]{}, createErr
			}
			encoded, encodeErr := json.Marshal(created)
			if encodeErr != nil {
				return command.Execution[Account]{}, errors.New("encode Account creation outcome")
			}
			return command.Success(account(created), string(encoded)).WithTargetID(strconv.Itoa(created.ID)), nil
		},
	})
}

// DisableAccount retires one enabled Account while preserving a final Administrator.
func (service *Service) DisableAccount(
	ctx context.Context,
	actor Account,
	accountID int,
	commandID string,
	reason string,
) error {
	if service.storageDegraded() {
		return ErrStorageDegraded
	}
	if err := command.ValidateID(commandID); err != nil {
		return ErrInvalidAccountDetails
	}
	reason = strings.TrimSpace(reason)
	payload, err := json.Marshal(struct {
		AccountID int    `json:"account_id"`
		Reason    string `json:"reason"`
	}{AccountID: accountID, Reason: reason})
	if err != nil {
		return errors.New("encode Disable Account command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: commandID,
		PayloadHash: command.PayloadHash(string(payload)), Action: "DisableAccount",
		TargetType: "Account", TargetID: strconv.Itoa(accountID), Now: service.now().UTC(),
	}
	_, err = command.Execute(actor.Context(ctx), command.Plan[struct{}]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (struct{}, error) {
			var original store.DisabledAccount
			if decodeErr := store.DecodeCommandReceipt(outcome, &original); decodeErr != nil {
				return struct{}{}, restoreRejected(decodeErr)
			}
			return struct{}{}, nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[struct{}], error) {
			if !actor.Administrator {
				return accountRejectionExecution[struct{}](ErrAdministratorRequired), nil
			}
			if !validDisableReason(reason) {
				return accountRejectionExecution[struct{}](ErrDisableReasonRequired), nil
			}
			disabled, disableErr := transaction.DisableAccount(actor.Context(ctx), accountID, identity.Now)
			if errors.Is(disableErr, ErrDisableAccountNotFound) || errors.Is(disableErr, ErrLastAdministrator) {
				return accountRejectionExecution[struct{}](disableErr), nil
			}
			if disableErr != nil {
				return command.Execution[struct{}]{}, disableErr
			}
			encoded, encodeErr := json.Marshal(disabled)
			if encodeErr != nil {
				return command.Execution[struct{}]{}, errors.New("encode Disable Account outcome")
			}
			return command.Success(struct{}{}, string(encoded)).WithAudit(store.AuditDetails{Reason: reason}), nil
		},
	})
	return err
}

func accountRejectionExecution[T any](reason error) command.Execution[T] {
	rejection := accountRejection(reason)
	var zero T
	return command.Reject(zero, rejection, reason)
}

// accountRejections is the single source for Account command rejection codes
// in both directions. An unclassified failure records "unavailable", which no
// sentinel claims, so a replay of it reports the command as unavailable rather
// than inventing a specific cause.
var accountRejections = command.RejectionTable{
	Rejections: []command.Rejection{
		{Err: ErrAdministratorRequired, Code: "administrator_required"},
		{Err: ErrInvalidAccountDetails, Code: "invalid_account_details"},
		{Err: ErrAccountExists, Code: "account_exists"},
		{Err: ErrDisableAccountNotFound, Code: "account_not_found"},
		{Err: ErrDisableReasonRequired, Code: "disable_reason_required"},
		{Err: ErrRecoveryReasonRequired, Code: "recovery_reason_required"},
		{Err: ErrRecoveryAccountNotFound, Code: "recovery_account_not_found"},
		{Err: ErrLastAdministrator, Code: "last_administrator"},
		{Err: ErrFinalCredential, Code: "final_credential"},
		{Err: ErrProfileEntryUnavailable, Code: "profile_entry_unavailable"},
		{Err: store.ErrInvalidSession, Code: "credential_not_found", Restored: ErrInvalidSession},
	},
}

func accountRejection(reason error) store.CommandRejection {
	rejection, known := accountRejections.Rejection(reason)
	if !known {
		return store.CommandRejection{Code: "unavailable"}
	}
	return rejection
}

func restoreRejected(err error) error {
	var rejected *store.RejectedCommandError
	if !errors.As(err, &rejected) {
		return err
	}
	if sentinel := accountRejections.Sentinel(rejected.Rejection.Code); sentinel != nil {
		return sentinel
	}
	return errors.New("Account command unavailable")
}

func validDisableReason(value string) bool {
	switch value {
	case "crew_departed", "access_revoked", "account_compromised", "duplicate_account":
		return true
	default:
		return false
	}
}

// ListAccounts returns selectable enabled Accounts for an Administrator.
func (service *Service) ListAccounts(
	ctx context.Context,
	actor Account,
) ([]Account, error) {
	if !actor.Administrator {
		return nil, ErrAdministratorRequired
	}
	found, err := service.storage.ListAccounts(actor.Context(ctx))
	if err != nil {
		return nil, err
	}
	accounts := make([]Account, 0, len(found))
	for _, item := range found {
		accounts = append(accounts, account(item))
	}
	return accounts, nil
}

// ListAuditEntries returns installation history to an Administrator.
func (service *Service) ListAuditEntries(
	ctx context.Context,
	actor Account,
) ([]AuditEntry, error) {
	if !actor.Administrator {
		return nil, ErrAdministratorRequired
	}
	found, err := service.storage.ListAuditEntries(actor.Context(ctx))
	if err != nil {
		return nil, err
	}
	entries := make([]AuditEntry, 0, len(found))
	for _, item := range found {
		actorKind := ""
		if item.ActorKind != "Account" {
			actorKind = item.ActorKind
		}
		entries = append(entries, AuditEntry{
			ID: item.ID, ActorKind: actorKind,
			ActorAccountID: item.ActorAccountID, ActorName: item.ActorName,
			ServerTime: item.ServerTime, Action: item.Action,
			TargetType: item.TargetType, TargetID: item.TargetID,
			Outcome: item.Outcome, Reason: item.Reason, Note: item.Note,
		})
	}
	return entries, nil
}

// Authenticate returns the Account for an active durable session.
func (service *Service) Authenticate(ctx context.Context, token string) (Account, error) {
	return service.authenticate(ctx, token)
}

// SetReducedEffects persists the preference for the authenticated Account.
func (service *Service) SetReducedEffects(
	ctx context.Context,
	token string,
	enabled bool,
) error {
	authenticated, err := service.authenticate(ctx, token)
	if err != nil {
		return err
	}
	return service.storage.SetAccountReducedEffects(ctx, authenticated.ID, enabled)
}

// ReducedEffects returns the preference for the authenticated Account.
func (service *Service) ReducedEffects(
	ctx context.Context,
	token string,
) (bool, error) {
	authenticated, err := service.authenticate(ctx, token)
	if err != nil {
		return false, err
	}
	return service.storage.AccountReducedEffects(ctx, authenticated.ID)
}

// AuthenticatePreviouslyValidated returns only a pre-failure Account snapshot
// while storage is degraded. It exists solely for the Emergency Alert path.
func (service *Service) AuthenticatePreviouslyValidated(
	ctx context.Context,
	token string,
) (Account, error) {
	if !validToken(token) {
		return Account{}, ErrInvalidSession
	}
	now := service.now().UTC()
	tokenHash := tokenDigest(token)
	cached, previouslyValidated := service.validatedSession(tokenHash, now)
	if service.storageDegraded() {
		if previouslyValidated {
			return cached, nil
		}
		return Account{}, ErrStorageDegraded
	}
	found, err := service.storage.FindAccountSession(ctx, tokenHash, now)
	if errors.Is(err, store.ErrInvalidSession) {
		service.forgetSession(tokenHash)
		return Account{}, ErrInvalidSession
	}
	if err != nil {
		if previouslyValidated {
			if service.storageState != nil {
				_ = service.storageState.PrepareEmergencyStorage(ctx)
				if !service.storageDegraded() {
					return Account{}, err
				}
			}
			return cached, nil
		}
		return Account{}, err
	}
	authenticated := account(found)
	if service.storageState != nil {
		probeErr := service.storageState.PrepareEmergencyStorage(ctx)
		if probeErr != nil || service.storageDegraded() {
			if errors.Is(probeErr, context.Canceled) ||
				errors.Is(probeErr, context.DeadlineExceeded) {
				return Account{}, probeErr
			}
			if previouslyValidated {
				return cached, nil
			}
			return Account{}, ErrStorageDegraded
		}
	}
	service.rememberSessionHash(
		tokenHash,
		authenticated,
		found.SessionExpiresAt,
	)
	return cloneAccount(authenticated), nil
}

func (service *Service) authenticate(ctx context.Context, token string) (Account, error) {
	if !validToken(token) {
		return Account{}, ErrInvalidSession
	}
	now := service.now().UTC()
	tokenHash := tokenDigest(token)
	if service.storageDegraded() {
		return Account{}, ErrStorageDegraded
	}
	found, err := service.storage.FindAccountSession(ctx, tokenHash, now)
	if errors.Is(err, store.ErrInvalidSession) {
		service.forgetSession(tokenHash)
		return Account{}, ErrInvalidSession
	}
	if err != nil {
		return Account{}, err
	}
	authenticated := account(found)
	service.rememberSessionHash(
		tokenHash,
		authenticated,
		found.SessionExpiresAt,
	)
	return cloneAccount(authenticated), nil
}

// SignOut durably revokes a session. Invalid tokens have the same successful result.
func (service *Service) SignOut(ctx context.Context, token string) error {
	if !validToken(token) {
		return nil
	}
	if service.storageDegraded() {
		return ErrStorageDegraded
	}
	tokenHash := tokenDigest(token)
	if err := service.storage.RevokeAccountSession(ctx, tokenHash, service.now().UTC()); err != nil {
		return err
	}
	service.forgetSession(tokenHash)
	return nil
}

func (service *Service) storageDegraded() bool {
	return service.storageState != nil && service.storageState.Degraded()
}

func (service *Service) rememberSession(token string, account Account, expiresAt time.Time) {
	service.rememberSessionHash(tokenDigest(token), account, expiresAt)
}

func (service *Service) rememberSessionHash(
	tokenHash string,
	account Account,
	expiresAt time.Time,
) {
	service.sessionMu.Lock()
	defer service.sessionMu.Unlock()
	service.sessions[tokenHash] = validatedSession{
		account: cloneAccount(account), expiresAt: expiresAt,
	}
}

func (service *Service) validatedSession(
	tokenHash string,
	now time.Time,
) (Account, bool) {
	service.sessionMu.RLock()
	cached, ok := service.sessions[tokenHash]
	service.sessionMu.RUnlock()
	if !ok || !cached.expiresAt.After(now) {
		if ok {
			service.forgetSession(tokenHash)
		}
		return Account{}, false
	}
	return cloneAccount(cached.account), true
}

func (service *Service) forgetSession(tokenHash string) {
	service.sessionMu.Lock()
	defer service.sessionMu.Unlock()
	delete(service.sessions, tokenHash)
}

func (service *Service) forgetAccountSessions(accountID int) {
	service.sessionMu.Lock()
	defer service.sessionMu.Unlock()
	for tokenHash, session := range service.sessions {
		if session.account.ID == accountID {
			delete(service.sessions, tokenHash)
		}
	}
}

func (service *Service) pruneSessionCache(now time.Time, revoked []string) {
	service.sessionMu.Lock()
	defer service.sessionMu.Unlock()
	for tokenHash, cached := range service.sessions {
		if !cached.expiresAt.After(now) {
			delete(service.sessions, tokenHash)
		}
	}
	for _, tokenHash := range revoked {
		delete(service.sessions, tokenHash)
	}
}

func cloneAccount(source Account) Account {
	cloned := Account{
		ID: source.ID, Handle: source.Handle, Name: source.Name,
		Administrator: source.Administrator,
		EventRoles:    make(map[int]viewer.Role, len(source.EventRoles)),
		EventNames:    make(map[int]string, len(source.EventNames)),
		EventScopes:   make(map[int]viewer.EventScope, len(source.EventScopes)),
	}
	maps.Copy(cloned.EventRoles, source.EventRoles)
	maps.Copy(cloned.EventNames, source.EventNames)
	for eventID, scope := range source.EventScopes {
		clonedScope := viewer.EventScope{
			LaneIDs:          make(map[int]struct{}, len(scope.LaneIDs)),
			DisplayGroupKeys: make(map[string]struct{}, len(scope.DisplayGroupKeys)),
			Capabilities:     make(map[viewer.Capability]struct{}, len(scope.Capabilities)),
		}
		for laneID := range scope.LaneIDs {
			clonedScope.LaneIDs[laneID] = struct{}{}
		}
		for key := range scope.DisplayGroupKeys {
			clonedScope.DisplayGroupKeys[key] = struct{}{}
		}
		for capability := range scope.Capabilities {
			clonedScope.Capabilities[capability] = struct{}{}
		}
		cloned.EventScopes[eventID] = clonedScope
	}
	return cloned
}

func (service *Service) newToken() (string, error) {
	contents := make([]byte, tokenBytes)
	if _, err := io.ReadFull(service.random, contents); err != nil {
		return "", errors.New("generate authentication token")
	}
	return base64.RawURLEncoding.EncodeToString(contents), nil
}

func (service *Service) newWebAuthnUserHandle() ([]byte, error) {
	handle := make([]byte, webAuthnUserHandleBytes)
	if _, err := io.ReadFull(service.random, handle); err != nil {
		return nil, errors.New("generate WebAuthn user handle")
	}
	return handle, nil
}

func (service *Service) hashPassword(password string) (string, error) {
	if !service.beginPasswordWork() {
		return "", ErrAuthenticationBusy
	}
	defer service.endPasswordWork()

	salt := make([]byte, saltBytes)
	if _, err := io.ReadFull(service.random, salt); err != nil {
		return "", errors.New("generate password salt")
	}
	return encodePasswordHash(
		[]byte(password),
		salt,
		argonParameters{time: argonTime, memory: argonMemory, threads: argonThreads},
	), nil
}

func (service *Service) comparePassword(encoded, password string) (bool, error) {
	if !service.beginPasswordWork() {
		return false, ErrAuthenticationBusy
	}
	defer service.endPasswordWork()
	return comparePassword(encoded, password)
}

func (service *Service) beginPasswordWork() bool {
	select {
	case service.passwordWork <- struct{}{}:
		return true
	default:
		return false
	}
}

func (service *Service) endPasswordWork() {
	<-service.passwordWork
}

func normalizeAccountName(name string) (normalized, display string, err error) {
	display, err = normalizeDisplayName(name)
	if err != nil {
		return "", "", err
	}
	return strings.ToLower(display), display, nil
}

func normalizeDisplayName(name string) (string, error) {
	display := strings.TrimSpace(name)
	if display == "" || utf8.RuneCountInString(display) > 200 || !utf8.ValidString(display) {
		return "", ErrInvalidAccountDetails
	}
	for _, character := range display {
		if unicode.IsControl(character) {
			return "", ErrInvalidAccountDetails
		}
	}
	return display, nil
}

func validPassword(password string) bool {
	length := utf8.RuneCountInString(password)
	return utf8.ValidString(password) && length >= 12 && length <= 1024
}

func (service *Service) validPassword(password string) bool {
	return validPassword(password) || service.allowDemoPassword && password == "demo"
}

func validToken(token string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == tokenBytes &&
		base64.RawURLEncoding.EncodeToString(decoded) == token
}

func tokenDigest(token string) string {
	digest := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", digest)
}

type argonParameters struct {
	time    uint32
	memory  uint32
	threads uint8
}

func encodePasswordHash(password, salt []byte, parameters argonParameters) string {
	derived := argon2.IDKey(
		password,
		salt,
		parameters.time,
		parameters.memory,
		parameters.threads,
		passwordHashBytes,
	)
	return formatPasswordHash(salt, derived, parameters)
}

func formatPasswordHash(salt, derived []byte, parameters argonParameters) string {
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		parameters.memory,
		parameters.time,
		parameters.threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(derived),
	)
}

func comparePassword(encoded, password string) (bool, error) {
	parameters, salt, expected, err := parsePasswordHash(encoded)
	if err != nil {
		return false, err
	}
	derived := argon2.IDKey(
		[]byte(password),
		salt,
		parameters.time,
		parameters.memory,
		parameters.threads,
		passwordHashBytes,
	)
	return subtle.ConstantTimeCompare(expected, derived) == 1, nil
}

func parsePasswordHash(encoded string) (argonParameters, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return argonParameters{}, nil, nil, errors.New("invalid password hash format")
	}
	versionText, found := strings.CutPrefix(parts[2], "v=")
	if !found {
		return argonParameters{}, nil, nil, errors.New("invalid password hash version")
	}
	version, err := strconv.Atoi(versionText)
	if err != nil || version != argon2.Version {
		return argonParameters{}, nil, nil, errors.New("unsupported password hash version")
	}
	parameters, err := parseArgonParameters(parts[3])
	if err != nil {
		return argonParameters{}, nil, nil, err
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(salt) < saltBytes || len(salt) > 64 {
		return argonParameters{}, nil, nil, errors.New("invalid password hash salt")
	}
	expected, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(expected) != passwordHashBytes {
		return argonParameters{}, nil, nil, errors.New("invalid derived password hash")
	}
	return parameters, salt, expected, nil
}

func parseArgonParameters(encoded string) (argonParameters, error) {
	parts := strings.Split(encoded, ",")
	if len(parts) != 3 {
		return argonParameters{}, errors.New("invalid password hash parameters")
	}
	memory, err := parseUintParameter(parts[0], "m=", 32*1024, 256*1024, 32)
	if err != nil {
		return argonParameters{}, err
	}
	timeCost, err := parseUintParameter(parts[1], "t=", 1, 10, 32)
	if err != nil {
		return argonParameters{}, err
	}
	threads, err := parseUintParameter(parts[2], "p=", 1, 16, 8)
	if err != nil {
		return argonParameters{}, err
	}
	// parseUintParameter bounded these values to the documented safe ranges above.
	//nolint:gosec // The checked maxima fit their destination integer widths.
	return argonParameters{time: uint32(timeCost), memory: uint32(memory), threads: uint8(threads)}, nil
}

func parseUintParameter(
	encoded string,
	prefix string,
	minimum uint64,
	maximum uint64,
	bits int,
) (uint64, error) {
	valueText, found := strings.CutPrefix(encoded, prefix)
	if !found {
		return 0, errors.New("invalid password hash parameters")
	}
	value, err := strconv.ParseUint(valueText, 10, bits)
	if err != nil || value < minimum || value > maximum {
		return 0, errors.New("unsafe password hash parameters")
	}
	return value, nil
}

func newSession(token string, expiresAt time.Time, found store.AccountCredential) Session {
	return Session{Token: token, ExpiresAt: expiresAt, Account: account(found)}
}

func account(found store.AccountCredential) Account {
	return Account{
		ID: found.ID, Handle: found.Handle, Name: found.Name,
		Administrator: found.Administrator,
		EventRoles:    found.EventRoles, EventNames: found.EventNames,
		EventScopes: found.EventScopes,
	}
}

// Context adds the Account's authenticated authorization facts for Ent privacy.
func (account Account) Context(ctx context.Context) context.Context {
	return viewer.NewContext(ctx, viewer.Identity{
		AccountID: account.ID, Administrator: account.Administrator,
		EventRoles: account.EventRoles, EventScopes: account.EventScopes,
	})
}

// CanProduceEvent reports whether the Account has explicit Producer authority.
func (account Account) CanProduceEvent(eventID int) bool {
	return account.EventRoles[eventID] == viewer.Producer
}

// CanOperateEvent reports baseline live-control authority before scoped grants are applied.
func (account Account) CanOperateEvent(eventID int) bool {
	role := account.EventRoles[eventID]
	return role == viewer.Producer || role == viewer.Operator
}

// HasCapability reports role-default or explicitly granted Event authority.
func (account Account) HasCapability(eventID int, capability viewer.Capability) bool {
	return viewer.Identity{
		AccountID: account.ID, Administrator: account.Administrator,
		EventRoles: account.EventRoles, EventScopes: account.EventScopes,
	}.HasCapability(eventID, capability)
}
