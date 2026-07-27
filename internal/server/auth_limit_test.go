package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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
