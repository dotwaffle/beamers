package auth

import (
	"context"
	"errors"
	"time"

	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/systemactor"
)

// SessionCounts returns token-free active durable and in-memory session counts.
func (service *Service) SessionCounts(ctx context.Context) (SessionCounts, error) {
	ctx = systemactor.NewContext(ctx, systemactor.HostMaintenance)
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

// Authenticate returns the Account for an active durable session.
func (service *Service) Authenticate(ctx context.Context, token string) (Account, error) {
	return service.authenticate(ctx, token)
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
	ctx = systemactor.NewContext(ctx, systemactor.PublicVisitor)
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
	ctx = systemactor.NewContext(ctx, systemactor.PublicVisitor)
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
	ctx = systemactor.NewContext(ctx, systemactor.PublicVisitor)
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

func newSession(token string, expiresAt time.Time, found store.AccountCredential) Session {
	return Session{Token: token, ExpiresAt: expiresAt, Account: account(found)}
}
