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
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"

	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/viewer"
)

const (
	defaultBootstrapTTL = 15 * time.Minute
	defaultSessionTTL   = 12 * time.Hour
	tokenBytes          = 32
	saltBytes           = 16
	passwordHashBytes   = 32
	argonTime           = 3
	argonMemory         = 64 * 1024
	argonThreads        = 4
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
	// ErrCommandConflict means a Command ID was reused for different Account work.
	ErrCommandConflict = store.ErrCommandConflict
)

// Account is the authenticated identity exposed above the persistence boundary.
type Account struct {
	ID            int
	Name          string
	Administrator bool
	EventRoles    map[int]viewer.Role
}

// Session is a newly authenticated session. Token is returned only to the
// transport that will place it in a protected cookie.
type Session struct {
	Token     string
	ExpiresAt time.Time
	Account   Account
}

// Config contains explicit authentication dependencies and lifetimes.
type Config struct {
	Now          func() time.Time
	Random       io.Reader
	BootstrapTTL time.Duration
	SessionTTL   time.Duration
}

// DefaultConfig returns production authentication dependencies and lifetimes.
func DefaultConfig() Config {
	return Config{
		Now:          time.Now,
		Random:       rand.Reader,
		BootstrapTTL: defaultBootstrapTTL,
		SessionTTL:   defaultSessionTTL,
	}
}

// Service owns credential hashing and session lifecycle rules.
type Service struct {
	storage      *store.SQLite
	now          func() time.Time
	random       io.Reader
	bootstrapTTL time.Duration
	sessionTTL   time.Duration
	dummyHash    string
	passwordWork chan struct{}
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
		storage:      storage,
		now:          config.Now,
		random:       config.Random,
		bootstrapTTL: config.BootstrapTTL,
		sessionTTL:   config.SessionTTL,
		dummyHash:    dummyHash,
		passwordWork: make(chan struct{}, passwordConcurrency),
	}, nil
}

// IssueBootstrap creates a host-authorized short-lived first-Administrator credential.
func (service *Service) IssueBootstrap(ctx context.Context) (string, error) {
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

// BootstrapAdministrator consumes a bootstrap credential and creates the first
// Administrator together with an authenticated session.
func (service *Service) BootstrapAdministrator(
	ctx context.Context,
	bootstrapToken string,
	name string,
	password string,
) (Session, error) {
	normalizedName, displayName, err := normalizeAccountName(name)
	if err != nil || !validPassword(password) || !validToken(bootstrapToken) {
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
	created, err := service.storage.BootstrapAdministrator(
		ctx,
		store.BootstrapAdministratorParams{
			BootstrapHash:  tokenDigest(bootstrapToken),
			Name:           displayName,
			NormalizedName: normalizedName,
			PasswordHash:   passwordHash,
			SessionHash:    tokenDigest(sessionToken),
			Now:            now,
			SessionExpiry:  expiresAt,
		},
	)
	if errors.Is(err, store.ErrInvalidBootstrap) {
		return Session{}, ErrAuthenticationFailed
	}
	if err != nil {
		return Session{}, err
	}
	return newSession(sessionToken, expiresAt, created), nil
}

// SignIn verifies an Account credential without distinguishing unknown,
// disabled, and incorrect credentials.
func (service *Service) SignIn(ctx context.Context, name, password string) (Session, error) {
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

	token, err := service.newToken()
	if err != nil {
		return Session{}, err
	}
	now := service.now().UTC()
	expiresAt := now.Add(service.sessionTTL)
	if err := service.storage.CreateAccountSession(
		ctx,
		credential.ID,
		tokenDigest(token),
		now,
		expiresAt,
	); err != nil {
		return Session{}, err
	}
	return newSession(token, expiresAt, credential), nil
}

// CreateAccount creates an individual non-Administrator Account.
func (service *Service) CreateAccount(
	ctx context.Context,
	actor Account,
	name string,
	password string,
	commandID string,
) (Account, error) {
	payloadHash := command.PayloadHash(name, password)
	if err := command.ValidateID(commandID); err != nil {
		return Account{}, ErrInvalidAccountDetails
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: commandID, PayloadHash: payloadHash,
		Action: "CreateAccount", TargetType: "Account", TargetID: "unidentified", Now: service.now().UTC(),
	}
	transaction, err := service.storage.BeginCommand(actor.Context(ctx))
	if err != nil {
		return Account{}, err
	}
	defer func() { _ = transaction.Rollback() }()
	outcome, retry, err := transaction.LookupReceipt(ctx, identity)
	if errors.Is(err, ErrCommandConflict) {
		if commitErr := transaction.CommitConflict(actor.Context(ctx), identity); commitErr != nil {
			return Account{}, commitErr
		}
		return Account{}, ErrCommandConflict
	}
	if err != nil {
		return Account{}, err
	}
	if retry {
		var original store.AccountCredential
		if decodeErr := store.DecodeCommandReceipt(outcome, &original); decodeErr != nil {
			return Account{}, restoreRejected(decodeErr)
		}
		return account(original), nil
	}
	if !actor.Administrator {
		return Account{}, service.rejectTransaction(actor.Context(ctx), transaction, identity, ErrAdministratorRequired)
	}
	normalizedName, displayName, err := normalizeAccountName(name)
	if err != nil || !validPassword(password) {
		return Account{}, service.rejectTransaction(actor.Context(ctx), transaction, identity, ErrInvalidAccountDetails)
	}
	passwordHash, err := service.hashPassword(password)
	if err != nil {
		return Account{}, err
	}
	created, err := transaction.CreateAccount(actor.Context(ctx), store.CreateAccountParams{
		ActorAccountID: actor.ID,
		Name:           displayName,
		NormalizedName: normalizedName,
		PasswordHash:   passwordHash,
		Now:            identity.Now,
		CommandID:      commandID,
		PayloadHash:    command.PayloadHash(normalizedName, password),
	})
	if err != nil {
		if errors.Is(err, ErrAccountExists) {
			return Account{}, service.rejectTransaction(actor.Context(ctx), transaction, identity, err)
		}
		return Account{}, err
	}
	identity.TargetID = strconv.Itoa(created.ID)
	encoded, err := json.Marshal(created)
	if err != nil {
		return Account{}, errors.New("encode Account creation outcome")
	}
	if err := transaction.RecordOutcome(actor.Context(ctx), identity, string(encoded), false); err != nil {
		return Account{}, err
	}
	if err := transaction.Commit(); err != nil {
		return Account{}, err
	}
	return account(created), nil
}

func (service *Service) rejectTransaction(
	ctx context.Context,
	transaction *store.CommandTx,
	identity store.CommandIdentity,
	reason error,
) error {
	if err := transaction.RecordRejection(ctx, identity, accountRejection(reason)); err != nil {
		return errors.Join(reason, err)
	}
	if err := transaction.Commit(); err != nil {
		return errors.Join(reason, err)
	}
	return reason
}

func accountRejection(reason error) store.CommandRejection {
	switch {
	case errors.Is(reason, ErrAdministratorRequired):
		return store.CommandRejection{Code: "administrator_required"}
	case errors.Is(reason, ErrInvalidAccountDetails):
		return store.CommandRejection{Code: "invalid_account_details"}
	case errors.Is(reason, ErrAccountExists):
		return store.CommandRejection{Code: "account_exists"}
	default:
		return store.CommandRejection{Code: "unavailable"}
	}
}

func restoreRejected(err error) error {
	var rejected *store.RejectedCommandError
	if !errors.As(err, &rejected) {
		return err
	}
	switch rejected.Rejection.Code {
	case "administrator_required":
		return ErrAdministratorRequired
	case "invalid_account_details":
		return ErrInvalidAccountDetails
	case "account_exists":
		return ErrAccountExists
	default:
		return errors.New("Account command unavailable")
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

// Authenticate returns the Account for an active durable session.
func (service *Service) Authenticate(ctx context.Context, token string) (Account, error) {
	if !validToken(token) {
		return Account{}, ErrInvalidSession
	}
	found, err := service.storage.FindAccountSession(ctx, tokenDigest(token), service.now().UTC())
	if errors.Is(err, store.ErrInvalidSession) {
		return Account{}, ErrInvalidSession
	}
	if err != nil {
		return Account{}, err
	}
	return account(found), nil
}

// SignOut durably revokes a session. Invalid tokens have the same successful result.
func (service *Service) SignOut(ctx context.Context, token string) error {
	if !validToken(token) {
		return nil
	}
	return service.storage.RevokeAccountSession(ctx, tokenDigest(token), service.now().UTC())
}

func (service *Service) newToken() (string, error) {
	contents := make([]byte, tokenBytes)
	if _, err := io.ReadFull(service.random, contents); err != nil {
		return "", errors.New("generate authentication token")
	}
	return base64.RawURLEncoding.EncodeToString(contents), nil
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
	display = strings.TrimSpace(name)
	if display == "" || utf8.RuneCountInString(display) > 200 || !utf8.ValidString(display) {
		return "", "", ErrInvalidAccountDetails
	}
	for _, character := range display {
		if unicode.IsControl(character) {
			return "", "", ErrInvalidAccountDetails
		}
	}
	return strings.ToLower(display), display, nil
}

func validPassword(password string) bool {
	length := utf8.RuneCountInString(password)
	return utf8.ValidString(password) && length >= 12 && length <= 1024
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
		ID: found.ID, Name: found.Name, Administrator: found.Administrator,
		EventRoles: found.EventRoles,
	}
}

// Context adds the Account's authenticated authorization facts for Ent privacy.
func (account Account) Context(ctx context.Context) context.Context {
	return viewer.NewContext(ctx, viewer.Identity{
		AccountID: account.ID, Administrator: account.Administrator, EventRoles: account.EventRoles,
	})
}

// CanProduceEvent reports whether the Account has explicit Producer authority.
func (account Account) CanProduceEvent(eventID int) bool {
	return account.EventRoles[eventID] == viewer.Producer
}
