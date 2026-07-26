package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
)

func TestBrowserSessionCookieIsSecureOnHTTPS(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"https://beamers.test/sign-in",
		http.NoBody,
	)
	setSessionCookie(response, request, auth.Session{
		Token:     "token",
		ExpiresAt: time.Now().Add(time.Hour),
	})
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name != sessionCookieName {
			continue
		}
		if !cookie.HttpOnly || !cookie.Secure ||
			cookie.SameSite != http.SameSiteLaxMode {
			t.Fatalf("HTTPS session cookie = %#v", cookie)
		}
		return
	}
	t.Fatal("response has no session cookie")
}
