package server

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	authFailureWindow     = 15 * time.Minute
	principalFailureLimit = 5
	clientFailureLimit    = 20
	maxAuthFailureEntries = 10_000
	// authSaturationWarnInterval bounds how often saturation is reported. A
	// saturated limiter denies principals it has never seen, which operators
	// must hear about without the log itself becoming the flood.
	authSaturationWarnInterval = time.Minute
)

type authFailureKey struct {
	value string
	limit int
}

type authFailureState struct {
	started time.Time
	count   int
}

type authFailureLimiter struct {
	mutex     sync.Mutex
	now       func() time.Time
	logger    *slog.Logger
	failures  map[string]authFailureState
	lastPrune time.Time
	warnedAt  time.Time
}

func newAuthFailureLimiter(now func() time.Time, logger *slog.Logger) *authFailureLimiter {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &authFailureLimiter{
		now:      now,
		logger:   logger,
		failures: make(map[string]authFailureState),
	}
}

func (limiter *authFailureLimiter) reserve(keys ...authFailureKey) (time.Duration, bool) {
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()

	now := limiter.now()
	limiter.prune(now)
	if retryAfter, blocked := limiter.blockedLocked(now, keys...); blocked {
		return retryAfter, true
	}
	if !limiter.roomForLocked(now, keys...) {
		limiter.warnSaturatedLocked(now)
		return authFailureWindow, true
	}
	limiter.recordLocked(now, keys...)
	return 0, false
}

// roomForLocked reports whether the requested keys fit. Entries whose window
// has elapsed are evicted first, so a principal the limiter has never seen is
// only denied once the live entries alone fill the table.
func (limiter *authFailureLimiter) roomForLocked(
	now time.Time,
	keys ...authFailureKey,
) bool {
	if limiter.fitsLocked(keys...) {
		return true
	}
	if limiter.lastPrune.Equal(now) {
		// The periodic sweep already ran for this instant. Scanning the whole
		// table again would only hand an attacker a second pass to pay for.
		return false
	}
	limiter.evictExpiredLocked(now)
	return limiter.fitsLocked(keys...)
}

func (limiter *authFailureLimiter) fitsLocked(keys ...authFailureKey) bool {
	newKeys := 0
	for _, key := range keys {
		if _, found := limiter.failures[key.value]; !found {
			newKeys++
		}
	}
	return len(limiter.failures)+newKeys <= maxAuthFailureEntries
}

func (limiter *authFailureLimiter) warnSaturatedLocked(now time.Time) {
	if !limiter.warnedAt.IsZero() &&
		now.Sub(limiter.warnedAt) < authSaturationWarnInterval {
		return
	}
	limiter.warnedAt = now
	limiter.logger.Warn(
		"authentication failure limiter saturated; unseen principals are being denied",
		slog.String("component", "auth"),
		slog.Int("entries", len(limiter.failures)),
		slog.Int("capacity", maxAuthFailureEntries),
	)
}

func (limiter *authFailureLimiter) blockedLocked(
	now time.Time,
	keys ...authFailureKey,
) (time.Duration, bool) {
	var longest time.Duration
	for _, key := range keys {
		state, found := limiter.failures[key.value]
		if !found {
			continue
		}
		if !now.Before(state.started.Add(authFailureWindow)) {
			delete(limiter.failures, key.value)
			continue
		}
		if state.count < key.limit {
			continue
		}
		remaining := state.started.Add(authFailureWindow).Sub(now)
		if remaining > longest {
			longest = remaining
		}
	}
	return longest, longest > 0
}

func (limiter *authFailureLimiter) recordLocked(now time.Time, keys ...authFailureKey) {
	for _, key := range keys {
		state, found := limiter.failures[key.value]
		if !found {
			if len(limiter.failures) >= maxAuthFailureEntries {
				continue
			}
			state.started = now
		}
		state.count++
		limiter.failures[key.value] = state
	}
}

func (limiter *authFailureLimiter) release(keys ...authFailureKey) {
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()
	for _, key := range keys {
		state, found := limiter.failures[key.value]
		if !found {
			continue
		}
		if state.count <= 1 {
			delete(limiter.failures, key.value)
			continue
		}
		state.count--
		limiter.failures[key.value] = state
	}
}

func (limiter *authFailureLimiter) reset(key authFailureKey) {
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()
	delete(limiter.failures, key.value)
}

func (limiter *authFailureLimiter) prune(now time.Time) {
	if len(limiter.failures) < maxAuthFailureEntries &&
		!limiter.lastPrune.IsZero() &&
		now.Sub(limiter.lastPrune) < time.Minute {
		return
	}
	limiter.evictExpiredLocked(now)
}

func (limiter *authFailureLimiter) evictExpiredLocked(now time.Time) {
	for key, state := range limiter.failures {
		if !now.Before(state.started.Add(authFailureWindow)) {
			delete(limiter.failures, key)
		}
	}
	limiter.lastPrune = now
}

func signInFailureKeys(request *http.Request, name string) (authFailureKey, authFailureKey) {
	client := authClientAddress(request)
	return authFailureKey{value: "client|" + client, limit: clientFailureLimit},
		authFailureKey{
			value: "sign-in|" + client + "|" + authFingerprint(name),
			limit: principalFailureLimit,
		}
}

func bootstrapFailureKeys(request *http.Request) (authFailureKey, authFailureKey) {
	client := authClientAddress(request)
	return authFailureKey{value: "client|" + client, limit: clientFailureLimit},
		authFailureKey{value: "bootstrap|" + client, limit: principalFailureLimit}
}

func registrationFailureKeys(
	request *http.Request,
	handle string,
) (authFailureKey, authFailureKey) {
	client := authClientAddress(request)
	return authFailureKey{value: "client|" + client, limit: clientFailureLimit},
		authFailureKey{
			value: "registration|" + client + "|" + authFingerprint(handle),
			limit: principalFailureLimit,
		}
}

func recoveryFailureKeys(
	request *http.Request,
	handle string,
) (authFailureKey, authFailureKey) {
	client := authClientAddress(request)
	return authFailureKey{value: "client|" + client, limit: clientFailureLimit},
		authFailureKey{
			value: "recovery|" + client + "|" + authFingerprint(handle),
			limit: principalFailureLimit,
		}
}

func accountUploadLimitKeys(
	request *http.Request,
	accountID int,
) (authFailureKey, authFailureKey) {
	client := authClientAddress(request)
	return authFailureKey{value: "upload-client|" + client, limit: 60},
		authFailureKey{
			value: fmt.Sprintf("account-upload|%d", accountID),
			limit: 20,
		}
}

func votingKeyFailureKeys(
	request *http.Request,
	token string,
) (authFailureKey, authFailureKey) {
	client := authClientAddress(request)
	return authFailureKey{value: "voting-client|" + client, limit: clientFailureLimit},
		authFailureKey{
			value: "voting-key|" + authFingerprint(token),
			limit: principalFailureLimit,
		}
}

func authClientAddress(request *http.Request) string {
	return requestClientAddress(request)
}

func authClientAddressFromRemote(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err == nil {
		return host
	}
	return remote
}

func authFingerprint(value string) string {
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", digest)
}
