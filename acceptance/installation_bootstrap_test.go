package acceptance_test

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdministratorBootstrapAndSessionLifecycle(t *testing.T) {
	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)

	bootstrapToken := strings.TrimSpace(runBeamersOutput(t, bin, "bootstrap", "--data-dir", dataDir))
	if bootstrapToken == "" {
		t.Fatal("bootstrap produced an empty credential")
	}
	runBeamersFails(t, bin, "bootstrap", "--data-dir", dataDir)

	client := authenticatedClient(t)
	first := startBeamers(t, bin, dataDir)
	bootstrapHeaders := assertJSONRequest(
		t,
		client,
		first.address,
		"/auth/bootstrap",
		map[string]string{
			"bootstrap_token": bootstrapToken,
			"name":            "Ada Admin",
			"password":        "correct horse battery staple",
		},
		http.StatusCreated,
		"",
	)
	assertProtectedSessionCookie(t, bootstrapHeaders)
	assertAuthenticated(t, client, first.address, "Ada Admin")
	assertJSONRequest(
		t,
		authenticatedClient(t),
		first.address,
		"/auth/bootstrap",
		map[string]string{
			"bootstrap_token": bootstrapToken,
			"name":            "Another Admin",
			"password":        "another correct horse battery staple",
		},
		http.StatusUnauthorized,
		"authentication failed\n",
	)
	first.stop(t)

	second := startBeamers(t, bin, dataDir)
	assertAuthenticated(t, client, second.address, "Ada Admin")
	assertJSONRequest(t, client, second.address, "/auth/sign-out", nil, http.StatusNoContent, "")
	assertSessionRejected(t, client, second.address)
	second.stop(t)

	third := startBeamers(t, bin, dataDir)
	assertSessionRejected(t, client, third.address)
	assertJSONRequest(
		t,
		client,
		third.address,
		"/auth/sign-in",
		map[string]string{"name": "Ada Admin", "password": "wrong password"},
		http.StatusUnauthorized,
		"authentication failed\n",
	)
	assertJSONRequest(
		t,
		client,
		third.address,
		"/auth/sign-in",
		map[string]string{
			"name":     "Ada Admin",
			"password": "correct horse battery staple",
		},
		http.StatusNoContent,
		"",
	)
	assertAuthenticated(t, client, third.address, "Ada Admin")
	third.stop(t)

	runBeamersFails(t, bin, "bootstrap", "--data-dir", dataDir)
}
