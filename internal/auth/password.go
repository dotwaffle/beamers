package auth

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"

	"github.com/dotwaffle/beamers/internal/systemactor"
)

// SignIn verifies an Account credential without distinguishing unknown,
// disabled, and incorrect credentials.
func (service *Service) SignIn(ctx context.Context, name, password string) (Session, error) {
	ctx = systemactor.NewContext(ctx, systemactor.PublicVisitor)
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

// Reauthenticate verifies the current Administrator without issuing a session.
func (service *Service) Reauthenticate(
	ctx context.Context,
	actor Account,
	password string,
) error {
	if !actor.Administrator {
		return ErrAdministratorRequired
	}
	ctx = actor.Context(ctx)
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

func validPassword(password string) bool {
	length := utf8.RuneCountInString(password)
	return utf8.ValidString(password) && length >= 12 && length <= 1024
}

func (service *Service) validPassword(password string) bool {
	return validPassword(password) || service.allowDemoPassword && password == "demo"
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
