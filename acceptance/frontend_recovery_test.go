package acceptance_test

import (
	"net/http"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/dotwaffle/beamers/internal/store/storetest"
)

func TestBrowserRecoveryPagesPreserveProgrammaticContracts(t *testing.T) {
	_, server := startAuthenticatedAdministratorWithPublicListener(t)
	defer server.stop(t)

	client := authenticatedClient(t)
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	missing := getFrontendPage(t, client, server.publicAddress, "/missing")
	assertFrontendRecovery(t, missing, http.StatusNotFound, "Page not found")
	hidden := getFrontendPage(t, client, server.publicAddress, "/backstage")
	assertFrontendRecovery(t, hidden, http.StatusNotFound, "Page not found")
	if hidden.body != missing.body {
		t.Error("Crew-only and unknown public recovery pages differ")
	}
	for _, accept := range []string{"", "*/*", "text/html;q=0"} {
		missing = getFrontendPageAccept(t, client, server.publicAddress, "/missing", accept)
		hidden = getFrontendPageAccept(t, client, server.publicAddress, "/backstage", accept)
		if hidden.status != missing.status ||
			hidden.header.Get("Content-Type") != missing.header.Get("Content-Type") ||
			hidden.body != missing.body {
			t.Errorf("Crew-only and unknown public responses differ for Accept %q", accept)
		}
	}

	forbidden := postFrontendForm(t, client, server.address, "/effects", url.Values{})
	assertFrontendRecovery(t, forbidden, http.StatusForbidden, "Access denied")

	machine := getFrontendPage(
		t,
		client,
		server.publicAddress,
		"/results/events/999/revisions/1/results.json",
	)
	if machine.status != http.StatusNotFound ||
		machine.header.Get("Content-Type") != "text/plain; charset=utf-8" ||
		machine.body != "404 page not found\n" {
		t.Fatalf(
			"machine Results recovery = %d %q %q",
			machine.status,
			machine.header.Get("Content-Type"),
			machine.body,
		)
	}
	webAuthn := requestJSON(
		t.Context(),
		client,
		server.address,
		"/auth/webauthn/sign-in/begin",
		map[string]string{"handle": "Ada Admin"},
	)
	if webAuthn.status != http.StatusForbidden ||
		webAuthn.header.Get("Content-Type") != "text/plain; charset=utf-8" ||
		webAuthn.body != "WebAuthn requires a secure origin\n" {
		t.Fatalf(
			"WebAuthn recovery = %d %q %q",
			webAuthn.status,
			webAuthn.header.Get("Content-Type"),
			webAuthn.body,
		)
	}
	unknownAPI := requestJSON(
		t.Context(),
		client,
		server.address,
		"/auth/unknown",
		map[string]string{},
	)
	if unknownAPI.status != http.StatusNotFound ||
		unknownAPI.header.Get("Content-Type") != "text/plain; charset=utf-8" ||
		unknownAPI.body != "404 page not found\n" {
		t.Fatalf(
			"unknown API recovery = %d %q %q",
			unknownAPI.status,
			unknownAPI.header.Get("Content-Type"),
			unknownAPI.body,
		)
	}
	unknownAPIRequest, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"http://"+server.address+"/auth/unknown",
		http.NoBody,
	)
	if err != nil {
		t.Fatalf("create unknown API request: %v", err)
	}
	unknownAPIRequest.Header.Set("Accept", "text/html")
	unknownAPIPage := doFrontendRequest(t, client, unknownAPIRequest)
	if unknownAPIPage.status != http.StatusNotFound ||
		unknownAPIPage.header.Get("Content-Type") != "text/plain; charset=utf-8" ||
		unknownAPIPage.body != "404 page not found\n" {
		t.Fatalf(
			"unknown API HTML recovery = %d %q %q",
			unknownAPIPage.status,
			unknownAPIPage.header.Get("Content-Type"),
			unknownAPIPage.body,
		)
	}

	signIn := getFrontendPage(t, client, server.address, "/sign-in")
	databasePath := filepath.Join(server.dataDir, "beamers.db")
	if err := storetest.FailCommandEvidence(t.Context(), databasePath); err != nil {
		t.Fatalf("fail command evidence: %v", err)
	}
	defer func() {
		if err := storetest.AllowCommandEvidence(t.Context(), databasePath); err != nil {
			t.Errorf("restore command evidence: %v", err)
		}
	}()

	browserFailure := postFrontendForm(t, client, server.address, "/sign-in", url.Values{
		"csrf_token": {requireFrontendCSRF(t, signIn)},
		"handle":     {"Ada Admin"},
		"password":   {"correct horse battery staple"},
	})
	assertFrontendRecovery(
		t,
		browserFailure,
		http.StatusInternalServerError,
		"Something went wrong",
	)
	apiFailure := requestJSON(
		t.Context(),
		client,
		server.address,
		"/auth/sign-in",
		map[string]string{
			"name":     "Ada Admin",
			"password": "correct horse battery staple",
		},
	)
	if apiFailure.status != http.StatusInternalServerError ||
		apiFailure.header.Get("Content-Type") != "text/plain; charset=utf-8" ||
		apiFailure.body != "authentication unavailable\n" {
		t.Fatalf(
			"programmatic sign-in failure = %d %q %q",
			apiFailure.status,
			apiFailure.header.Get("Content-Type"),
			apiFailure.body,
		)
	}
}
