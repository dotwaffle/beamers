package acceptance_test

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/descope/virtualwebauthn"
)

func TestBrowserWebAuthnCredentialsSurviveRestartAndRevokeIndependently(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	_, port, splitErr := net.SplitHostPort(server.address)
	if splitErr != nil {
		t.Fatalf("split WebAuthn server address: %v", splitErr)
	}
	webAuthnAddress := net.JoinHostPort("localhost", port)
	administrator.Jar.SetCookies(
		&url.URL{Scheme: "http", Host: webAuthnAddress},
		administrator.Jar.Cookies(&url.URL{Scheme: "http", Host: server.address}),
	)
	rp := virtualwebauthn.RelyingParty{
		ID: "localhost", Origin: "http://" + webAuthnAddress, Name: "Beamers",
	}

	type ceremony struct {
		ID      string          `json:"ceremony_id"`
		Options json.RawMessage `json:"options"`
	}
	beginRegistration := func() (ceremony, virtualwebauthn.AttestationOptions) {
		t.Helper()
		result := requestJSON(
			t.Context(),
			administrator,
			webAuthnAddress,
			"/auth/webauthn/register/begin",
			map[string]any{},
		)
		var begin ceremony
		if result.err != nil || result.status != http.StatusOK {
			t.Fatalf("begin WebAuthn registration = %d %q, %v", result.status, result.body, result.err)
		}
		if err := json.Unmarshal([]byte(result.body), &begin); err != nil {
			t.Fatalf("decode WebAuthn registration: %v", err)
		}
		options, err := virtualwebauthn.ParseAttestationOptions(string(begin.Options))
		if err != nil {
			t.Fatalf("parse WebAuthn registration options: %v", err)
		}
		return begin, *options
	}
	finishRegistration := func(
		begin ceremony,
		name string,
		response string,
		wantStatus int,
	) int {
		t.Helper()
		result := requestJSON(
			t.Context(),
			administrator,
			webAuthnAddress,
			"/auth/webauthn/register/finish",
			map[string]any{
				"ceremony_id": begin.ID,
				"name":        name,
				"credential":  json.RawMessage(response),
			},
		)
		if result.err != nil || result.status != wantStatus {
			t.Fatalf("finish WebAuthn registration = %d %q, %v", result.status, result.body, result.err)
		}
		if wantStatus != http.StatusOK {
			return 0
		}
		var registered struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal([]byte(result.body), &registered); err != nil ||
			registered.ID <= 0 {
			t.Fatalf("decode registered WebAuthn Credential = %q, %v", result.body, err)
		}
		return registered.ID
	}

	for _, invalid := range []struct {
		name string
		make func(virtualwebauthn.AttestationOptions) string
	}{
		{
			name: "origin",
			make: func(options virtualwebauthn.AttestationOptions) string {
				attacker := rp
				attacker.Origin = "https://attacker.test"
				return virtualwebauthn.CreateAttestationResponse(
					attacker,
					virtualwebauthn.NewAuthenticator(),
					virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2),
					options,
				)
			},
		},
		{
			name: "challenge",
			make: func(options virtualwebauthn.AttestationOptions) string {
				options.Challenge = []byte("wrong challenge")
				return virtualwebauthn.CreateAttestationResponse(
					rp,
					virtualwebauthn.NewAuthenticator(),
					virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2),
					options,
				)
			},
		},
		{
			name: "Relying Party",
			make: func(options virtualwebauthn.AttestationOptions) string {
				attacker := rp
				attacker.ID = "attacker.test"
				return virtualwebauthn.CreateAttestationResponse(
					attacker,
					virtualwebauthn.NewAuthenticator(),
					virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2),
					options,
				)
			},
		},
		{
			name: "user presence",
			make: func(options virtualwebauthn.AttestationOptions) string {
				return virtualwebauthn.CreateAttestationResponse(
					rp,
					virtualwebauthn.NewAuthenticatorWithOptions(
						virtualwebauthn.AuthenticatorOptions{UserNotPresent: true},
					),
					virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2),
					options,
				)
			},
		},
	} {
		t.Run("rejects invalid "+invalid.name, func(t *testing.T) {
			begin, options := beginRegistration()
			finishRegistration(
				begin,
				"Invalid Credential",
				invalid.make(options),
				http.StatusUnauthorized,
			)
			finishRegistration(
				begin,
				"Replayed Credential",
				invalid.make(options),
				http.StatusUnauthorized,
			)
		})
	}

	register := func(name string) (virtualwebauthn.Authenticator, virtualwebauthn.Credential, int) {
		t.Helper()
		authenticator := virtualwebauthn.NewAuthenticator()
		credential := virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)
		begin, options := beginRegistration()
		id := finishRegistration(
			begin,
			name,
			virtualwebauthn.CreateAttestationResponse(rp, authenticator, credential, options),
			http.StatusOK,
		)
		authenticator.AddCredential(credential)
		return authenticator, credential, id
	}
	firstAuthenticator, firstCredential, firstID := register("Laptop passkey")
	secondAuthenticator, secondCredential, secondID := register("Backup security key")

	profile := getFrontendPage(t, administrator, server.address, "/profile")
	for _, want := range []string{
		"Laptop passkey",
		"Backup security key",
		`src="/assets/webauthn-v1.js"`,
		"Checking WebAuthn availability",
	} {
		if profile.status != http.StatusOK || !strings.Contains(profile.body, want) {
			t.Fatalf("WebAuthn Profile lacks %q: %d %q", want, profile.status, profile.body)
		}
	}
	removed := postFrontendForm(t, administrator, server.address, "/profile", url.Values{
		"csrf_token": {requireFrontendCSRF(t, profile)},
		"action":     {"remove-password"},
		"command_id": {"remove-webauthn-test-password"},
	})
	if removed.status != http.StatusSeeOther {
		t.Fatalf("remove password = %d %q", removed.status, removed.body)
	}
	password := requestJSON(
		t.Context(),
		authenticatedClient(t),
		server.address,
		"/auth/sign-in",
		map[string]string{"name": "Ada Admin", "password": "correct horse battery staple"},
	)
	if password.status != http.StatusUnauthorized {
		t.Fatalf("removed password sign-in = %d %q", password.status, password.body)
	}

	bin, dataDir := server.bin, server.dataDir
	server.stop(t)
	server = startBeamersWithPublicListener(t, bin, dataDir)
	server.address = server.publicAddress
	_, port, splitErr = net.SplitHostPort(server.address)
	if splitErr != nil {
		t.Fatalf("split restarted WebAuthn server address: %v", splitErr)
	}
	webAuthnAddress = net.JoinHostPort("localhost", port)
	rp.Origin = "http://" + webAuthnAddress

	beginSignIn := func(client *http.Client) (ceremony, virtualwebauthn.AssertionOptions) {
		t.Helper()
		result := requestJSON(
			t.Context(),
			client,
			webAuthnAddress,
			"/auth/webauthn/sign-in/begin",
			map[string]string{"handle": "Ada Admin"},
		)
		var begin ceremony
		if result.err != nil || result.status != http.StatusOK {
			t.Fatalf("begin WebAuthn sign-in = %d %q, %v", result.status, result.body, result.err)
		}
		if err := json.Unmarshal([]byte(result.body), &begin); err != nil {
			t.Fatalf("decode WebAuthn sign-in: %v", err)
		}
		options, err := virtualwebauthn.ParseAssertionOptions(string(begin.Options))
		if err != nil {
			t.Fatalf("parse WebAuthn assertion options: %v", err)
		}
		return begin, *options
	}
	finishSignIn := func(
		client *http.Client,
		begin ceremony,
		response string,
		wantStatus int,
	) {
		t.Helper()
		result := requestJSON(
			t.Context(),
			client,
			webAuthnAddress,
			"/auth/webauthn/sign-in/finish",
			map[string]any{
				"ceremony_id": begin.ID,
				"credential":  json.RawMessage(response),
			},
		)
		if result.err != nil || result.status != wantStatus {
			t.Fatalf("finish WebAuthn sign-in = %d %q, %v", result.status, result.body, result.err)
		}
	}

	invalidClient := authenticatedClient(t)
	invalidBegin, invalidOptions := beginSignIn(invalidClient)
	wrongKey := virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)
	wrongKey.ID = secondCredential.ID
	finishSignIn(
		invalidClient,
		invalidBegin,
		virtualwebauthn.CreateAssertionResponse(
			rp,
			secondAuthenticator,
			wrongKey,
			invalidOptions,
		),
		http.StatusUnauthorized,
	)

	signedIn := authenticatedClient(t)
	validBegin, validOptions := beginSignIn(signedIn)
	finishSignIn(
		signedIn,
		validBegin,
		virtualwebauthn.CreateAssertionResponse(
			rp,
			secondAuthenticator,
			secondCredential,
			validOptions,
		),
		http.StatusNoContent,
	)
	if page := getFrontendPage(t, signedIn, webAuthnAddress, "/profile"); page.status != http.StatusOK ||
		!strings.Contains(page.body, "Ada Admin") {
		t.Fatalf("WebAuthn Account session Profile = %d %q", page.status, page.body)
	}
	signedIn.CheckRedirect = administrator.CheckRedirect

	profile = getFrontendPage(t, signedIn, webAuthnAddress, "/profile")
	revoked := postFrontendForm(t, signedIn, webAuthnAddress, "/profile", url.Values{
		"csrf_token":    {requireFrontendCSRF(t, profile)},
		"action":        {"revoke-webauthn"},
		"credential_id": {strconv.Itoa(firstID)},
		"command_id":    {"revoke-first-webauthn-test-credential"},
	})
	if revoked.status != http.StatusSeeOther {
		t.Fatalf("revoke first WebAuthn Credential = %d %q", revoked.status, revoked.body)
	}

	revokedClient := authenticatedClient(t)
	revokedBegin, revokedOptions := beginSignIn(revokedClient)
	finishSignIn(
		revokedClient,
		revokedBegin,
		virtualwebauthn.CreateAssertionResponse(
			rp,
			firstAuthenticator,
			firstCredential,
			revokedOptions,
		),
		http.StatusUnauthorized,
	)

	profile = getFrontendPage(t, signedIn, webAuthnAddress, "/profile")
	final := postFrontendForm(t, signedIn, webAuthnAddress, "/profile", url.Values{
		"csrf_token":    {requireFrontendCSRF(t, profile)},
		"action":        {"revoke-webauthn"},
		"credential_id": {strconv.Itoa(secondID)},
		"command_id":    {"revoke-final-webauthn-test-credential"},
	})
	if final.status != http.StatusConflict {
		t.Fatalf("revoke final WebAuthn Credential = %d %q", final.status, final.body)
	}
	assertAccessibleFormErrors(t, final, nil)
	if !strings.Contains(final.body, "The final active Credential cannot be removed.") {
		t.Fatalf("final WebAuthn Credential failure = %q", final.body)
	}
	server.stop(t)
}
