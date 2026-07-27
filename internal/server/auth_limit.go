package server

import (
	"crypto/sha256"
	"fmt"
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
	failures  map[string]authFailureState
	lastPrune time.Time
}

func newAuthFailureLimiter(now func() time.Time) *authFailureLimiter {
	return &authFailureLimiter{now: now, failures: make(map[string]authFailureState)}
}

func (limiter *authFailureLimiter) reserve(keys ...authFailureKey) (time.Duration, bool) {
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()

	now := limiter.now()
	limiter.prune(now)
	if retryAfter, blocked := limiter.blockedLocked(now, keys...); blocked {
		return retryAfter, true
	}
	newKeys := 0
	for _, key := range keys {
		if _, found := limiter.failures[key.value]; !found {
			newKeys++
		}
	}
	if len(limiter.failures)+newKeys > maxAuthFailureEntries {
		return authFailureWindow, true
	}
	limiter.recordLocked(now, keys...)
	return 0, false
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
