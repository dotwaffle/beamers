package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRegistrationLimiterBoundsEachHandleAndClient(t *testing.T) {
	limiter := newAuthFailureLimiter(time.Now, nil)
	request := httptest.NewRequestWithContext(t.Context(), "POST", "/register", http.NoBody)
	request.RemoteAddr = "192.0.2.1:1234"
	clientKey, handleKey := registrationFailureKeys(request, "participant")

	for range handleKey.limit {
		if _, blocked := limiter.reserve(clientKey, handleKey); blocked {
			t.Fatal("registration blocked before its conservative request limit")
		}
	}
	if _, blocked := limiter.reserve(clientKey, handleKey); !blocked {
		t.Fatal("registration remains unbounded after its request limit")
	}
}

func TestRecoveryLimiterBoundsEachHandleAndClient(t *testing.T) {
	limiter := newAuthFailureLimiter(time.Now, nil)
	request := httptest.NewRequestWithContext(t.Context(), "POST", "/recover", http.NoBody)
	request.RemoteAddr = "192.0.2.1:1234"
	clientKey, handleKey := recoveryFailureKeys(request, "participant")

	for range handleKey.limit {
		if _, blocked := limiter.reserve(clientKey, handleKey); blocked {
			t.Fatal("Account recovery blocked before its conservative request limit")
		}
	}
	if _, blocked := limiter.reserve(clientKey, handleKey); !blocked {
		t.Fatal("Account recovery remains unbounded after its request limit")
	}
}

func TestLimiterEvictsExpiredEntriesAtSaturation(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC)
	fresh := func(limiter *authFailureLimiter, now time.Time) {
		limiter.failures["live|"+now.String()] = authFailureState{started: now, count: 1}
	}
	tests := []struct {
		name        string
		fillExpired int
		fillLive    int
		elapsed     time.Duration
		wantBlocked bool
		wantWarned  bool
	}{
		{
			name: "expired entries make room", fillExpired: maxAuthFailureEntries,
			elapsed: authFailureWindow + time.Second,
		},
		{
			name: "one expired entry is enough", fillExpired: 1,
			fillLive: maxAuthFailureEntries - 1, elapsed: authFailureWindow + time.Second,
		},
		{
			name: "live entries still deny", fillLive: maxAuthFailureEntries,
			elapsed: time.Second, wantBlocked: true, wantWarned: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var logs bytes.Buffer
			now := start
			limiter := newAuthFailureLimiter(
				func() time.Time { return now },
				slog.New(slog.NewTextHandler(&logs, nil)),
			)
			for index := range test.fillExpired {
				limiter.failures["expired|"+strconv.Itoa(index)] = authFailureState{
					started: start, count: 1,
				}
			}
			for index := range test.fillLive {
				fresh(limiter, start.Add(time.Duration(index+1)*time.Nanosecond))
			}
			// The recent prune makes the interval gate skip its sweep, which is
			// exactly the state in which unseen principals used to be denied.
			limiter.lastPrune = start
			now = start.Add(test.elapsed)
			retryAfter, blocked := limiter.reserve(authFailureKey{
				value: "sign-in|unseen", limit: principalFailureLimit,
			})
			if blocked != test.wantBlocked {
				t.Fatalf("blocked = %t, want %t", blocked, test.wantBlocked)
			}
			if blocked && retryAfter <= 0 {
				t.Fatalf("retry after = %s, want a positive delay", retryAfter)
			}
			if warned := strings.Contains(logs.String(), "saturated"); warned != test.wantWarned {
				t.Fatalf("saturation warning = %t, want %t", warned, test.wantWarned)
			}
			if !blocked {
				if _, found := limiter.failures["sign-in|unseen"]; !found {
					t.Fatal("accepted reservation was not recorded")
				}
			}
		})
	}
}

func TestLimiterSaturationWarningIsRateLimited(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC)
	var logs bytes.Buffer
	now := start
	limiter := newAuthFailureLimiter(
		func() time.Time { return now },
		slog.New(slog.NewTextHandler(&logs, nil)),
	)
	for index := range maxAuthFailureEntries {
		limiter.failures["live|"+strconv.Itoa(index)] = authFailureState{
			started: start, count: 1,
		}
	}
	limiter.lastPrune = start
	unseen := authFailureKey{value: "sign-in|unseen", limit: principalFailureLimit}
	for _, elapsed := range []time.Duration{
		time.Second,
		2 * time.Second,
		authSaturationWarnInterval + time.Second,
	} {
		now = start.Add(elapsed)
		if _, blocked := limiter.reserve(unseen); !blocked {
			t.Fatalf("saturated limiter accepted an unseen principal after %s", elapsed)
		}
	}
	if warnings := strings.Count(logs.String(), "saturated"); warnings != 2 {
		t.Fatalf("saturation warnings = %d, want 2", warnings)
	}
}

func TestLimiterReservationIsAtomicAndReleasable(t *testing.T) {
	limiter := newAuthFailureLimiter(time.Now, nil)
	key := authFailureKey{value: "enrollment|client", limit: 1}
	if _, blocked := limiter.reserve(key); blocked {
		t.Fatal("first reservation was blocked")
	}
	if _, blocked := limiter.reserve(key); !blocked {
		t.Fatal("second reservation bypassed the in-flight limit")
	}
	limiter.release(key)
	if _, blocked := limiter.reserve(key); blocked {
		t.Fatal("released reservation remained blocked")
	}
}
