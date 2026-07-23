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
		if _, blocked := limiter.blocked(clientKey, credentialKey); blocked {
			t.Fatal("Upload Link blocked before its conservative request limit")
		}
		limiter.record(clientKey, credentialKey)
	}
	if _, blocked := limiter.blocked(clientKey, credentialKey); !blocked {
		t.Fatal("Upload Link remains unbounded after its request limit")
	}

	now = now.Add(authFailureWindow)
	if _, blocked := limiter.blocked(clientKey, credentialKey); blocked {
		t.Fatal("Upload Link limit did not expire after its fixed window")
	}
}
