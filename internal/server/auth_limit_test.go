package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestUploadLimiterBoundsEachCredentialAndClient(t *testing.T) {
	now := time.Date(2026, time.July, 23, 15, 0, 0, 0, time.UTC)
	limiter := newAuthFailureLimiter(func() time.Time { return now })
	request := httptest.NewRequestWithContext(t.Context(), "POST", "/upload/secret", http.NoBody)
	request.RemoteAddr = "192.0.2.1:1234"
	clientKey, credentialKey := uploadLimitKeys(request, "secret")

	for range credentialKey.limit {
		if _, blocked := limiter.reserve(clientKey, credentialKey); blocked {
			t.Fatal("Upload Link blocked before its conservative request limit")
		}
	}
	if _, blocked := limiter.reserve(clientKey, credentialKey); !blocked {
		t.Fatal("Upload Link remains unbounded after its request limit")
	}

	now = now.Add(authFailureWindow)
	limiter.lastPrune = now // Exercise requested-key expiry while full-map pruning is throttled.
	if _, blocked := limiter.reserve(clientKey, credentialKey); blocked {
		t.Fatal("Upload Link limit did not expire after its fixed window")
	}
}

func TestRegistrationLimiterBoundsEachHandleAndClient(t *testing.T) {
	limiter := newAuthFailureLimiter(time.Now)
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
	limiter := newAuthFailureLimiter(time.Now)
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

func TestLimiterReservationIsAtomicAndReleasable(t *testing.T) {
	limiter := newAuthFailureLimiter(time.Now)
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
