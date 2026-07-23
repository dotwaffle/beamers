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

func (limiter *authFailureLimiter) blocked(keys ...authFailureKey) (time.Duration, bool) {
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()

	now := limiter.now()
	limiter.prune(now)
	var longest time.Duration
	for _, key := range keys {
		state, found := limiter.failures[key.value]
		if !found || state.count < key.limit {
			continue
		}
		remaining := state.started.Add(authFailureWindow).Sub(now)
		if remaining > longest {
			longest = remaining
		}
	}
	return longest, longest > 0
}

func (limiter *authFailureLimiter) record(keys ...authFailureKey) {
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()

	now := limiter.now()
	limiter.prune(now)
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

func uploadLimitKeys(request *http.Request, token string) (authFailureKey, authFailureKey) {
	client := authClientAddress(request)
	return authFailureKey{value: "upload-client|" + client, limit: 60},
		authFailureKey{
			value: "upload-link|" + client + "|" + authFingerprint(token),
			limit: 20,
		}
}

func authClientAddress(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err == nil {
		return host
	}
	return request.RemoteAddr
}

func authFingerprint(value string) string {
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", digest)
}
