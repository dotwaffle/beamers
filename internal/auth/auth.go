// Package auth establishes and authenticates individual Beamers Accounts.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"maps"
	"sync"
	"time"

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

func (service *Service) storageDegraded() bool {
	return service.storageState != nil && service.storageState.Degraded()
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

func validToken(token string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == tokenBytes &&
		base64.RawURLEncoding.EncodeToString(decoded) == token
}

func tokenDigest(token string) string {
	digest := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", digest)
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
