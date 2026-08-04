package acceptance_test

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"maps"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	virtualwebauthn "github.com/descope/virtualwebauthn"

	"google.golang.org/protobuf/proto"

	competitionv1 "github.com/dotwaffle/beamers/gen/beamers/competition/v1"
	"github.com/dotwaffle/beamers/gen/beamers/competition/v1/competitionv1connect"
	programv1 "github.com/dotwaffle/beamers/gen/beamers/program/v1"
	"github.com/dotwaffle/beamers/gen/beamers/program/v1/programv1connect"
	rundownv1 "github.com/dotwaffle/beamers/gen/beamers/rundown/v1"
	"github.com/dotwaffle/beamers/gen/beamers/rundown/v1/rundownv1connect"
	sessionv1 "github.com/dotwaffle/beamers/gen/beamers/session/v1"
	"github.com/dotwaffle/beamers/gen/beamers/session/v1/sessionv1connect"
	"github.com/dotwaffle/beamers/internal/backup"
	frontendui "github.com/dotwaffle/beamers/internal/frontend"
	"github.com/dotwaffle/beamers/internal/store/storetest"
)

var frontendCSRFInput = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)
var frontendSSEConnect = regexp.MustCompile(`sse-connect="([^"]+)"`)
var recoveryCodeOutput = regexp.MustCompile(`data-recovery-code>([^<]+)</code>`)
var recoveryTokenOutput = regexp.MustCompile(`data-recovery-token>([^<]+)</code>`)
var votingKeyOutput = regexp.MustCompile(`data-voting-key>([^<]+)</code>`)

func frontendSSEPath(t *testing.T, page string) string {
	t.Helper()
	match := frontendSSEConnect.FindStringSubmatch(page)
	if len(match) != 2 {
		t.Fatalf("page has no SSE connection path: %s", page)
	}
	return strings.ReplaceAll(match[1], "&amp;", "&")
}

func readBallotInvalidation(reader *bufio.Reader) error {
	for {
		line, err := reader.ReadString('\n')
		if err != nil || line == "event: ballot\n" {
			return err
		}
	}
}

func TestBrowserSetupAndSessionSurviveRestart(t *testing.T) {
	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)
	bootstrapToken := strings.TrimSpace(
		runBeamersOutput(t, bin, "bootstrap", "--data-dir", dataDir),
	)

	client := authenticatedClient(t)
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	server := startBeamers(t, bin, dataDir)

	root := getFrontendPage(t, client, server.address, "/")
	if root.status != http.StatusOK || !strings.Contains(root.body, "Set up Beamers") {
		t.Fatalf("unbootstrapped root = %d %q", root.status, root.body)
	}
	anonymousReduce := postFrontendForm(t, client, server.address, "/effects", url.Values{
		"csrf_token":     {requireFrontendCSRF(t, root)},
		"reduce_effects": {"true"},
	})
	if anonymousReduce.status != http.StatusSeeOther {
		t.Fatalf(
			"anonymous reduce effects response = %d %q",
			anonymousReduce.status,
			anonymousReduce.body,
		)
	}
	anonymousReduced := getFrontendPage(t, client, server.address, "/")
	if !strings.Contains(anonymousReduced.body, `data-reduced-effects="true"`) {
		t.Fatalf("anonymous Reduced Effects root = %q", anonymousReduced.body)
	}
	resumeEffects := postFrontendForm(t, client, server.address, "/effects", url.Values{
		"csrf_token":     {requireFrontendCSRF(t, anonymousReduced)},
		"reduce_effects": {"false"},
	})
	if resumeEffects.status != http.StatusSeeOther {
		t.Fatalf("anonymous resume effects = %d %q", resumeEffects.status, resumeEffects.body)
	}
	setup := getFrontendPage(t, client, server.address, "/setup")
	csrf := requireFrontendCSRF(t, setup)
	failedSetup := postFrontendForm(t, client, server.address, "/setup", url.Values{
		"csrf_token":      {csrf},
		"bootstrap_token": {base64.RawURLEncoding.EncodeToString(make([]byte, 32))},
		"handle":          {"ada"},
		"display_name":    {"Ada Lovelace"},
		"password":        {"correct horse battery staple"},
	})
	if failedSetup.status != http.StatusUnauthorized ||
		!strings.Contains(failedSetup.body, "invalid or expired") {
		t.Fatalf("failed setup = %d %q", failedSetup.status, failedSetup.body)
	}
	assertAccessibleFormErrors(t, failedSetup, map[string]string{
		"setup-token": "Setup token is invalid or expired.",
	})
	for _, value := range []string{`value="ada"`, `value="Ada Lovelace"`} {
		if !strings.Contains(failedSetup.body, value) {
			t.Errorf("failed setup did not preserve %s", value)
		}
	}
	if strings.Contains(failedSetup.body, base64.RawURLEncoding.EncodeToString(make([]byte, 32))) ||
		strings.Contains(failedSetup.body, "correct horse battery staple") {
		t.Error("failed setup retained a secret")
	}
	setupResponse := postFrontendForm(t, client, server.address, "/setup", url.Values{
		"csrf_token":      {csrf},
		"bootstrap_token": {bootstrapToken},
		"handle":          {"ada"},
		"display_name":    {"Ada Lovelace"},
		"password":        {"correct horse battery staple"},
	})
	if setupResponse.status != http.StatusSeeOther ||
		setupResponse.header.Get("Location") != "/" {
		t.Fatalf("setup response = %d %q", setupResponse.status, setupResponse.body)
	}
	sessionCookie := frontendResponseCookie(t, setupResponse.header, "beamers_session")
	if !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteLaxMode ||
		sessionCookie.Secure {
		t.Fatalf("session cookie = %#v", sessionCookie)
	}

	if replay := getFrontendPage(t, client, server.address, "/setup"); replay.status != http.StatusNotFound {
		t.Fatalf("consumed setup route = %d, want 404", replay.status)
	}
	signedIn := getFrontendPage(t, client, server.address, "/")
	if !strings.Contains(signedIn.body, "Ada Lovelace") ||
		!strings.Contains(signedIn.body, "Sign out") {
		t.Fatalf("signed-in root = %q", signedIn.body)
	}
	reduceEffects := postFrontendForm(t, client, server.address, "/effects", url.Values{
		"csrf_token":     {requireFrontendCSRF(t, signedIn)},
		"reduce_effects": {"true"},
	})
	if reduceEffects.status != http.StatusSeeOther {
		t.Fatalf(
			"reduce effects response = %d %q",
			reduceEffects.status,
			reduceEffects.body,
		)
	}
	effectsCookie := frontendResponseCookie(
		t,
		reduceEffects.header,
		"beamers_reduced_effects",
	)
	if !effectsCookie.HttpOnly ||
		effectsCookie.SameSite != http.SameSiteLaxMode ||
		effectsCookie.Secure {
		t.Fatalf("Reduced Effects cookie = %#v", effectsCookie)
	}
	reduced := getFrontendPage(t, client, server.address, "/")
	if !strings.Contains(reduced.body, `data-reduced-effects="true"`) ||
		!strings.Contains(reduced.body, "Resume effects") {
		t.Fatalf("reduced-effects root = %q", reduced.body)
	}
	signedInSignInPage := getFrontendPage(t, client, server.address, "/sign-in")
	if signedInSignInPage.status != http.StatusSeeOther ||
		signedInSignInPage.header.Get("Location") != "/" {
		t.Fatalf(
			"signed-in GET /sign-in = %d %q",
			signedInSignInPage.status,
			signedInSignInPage.body,
		)
	}

	server.stop(t)
	server = startBeamers(t, bin, dataDir)
	client = authenticatedClient(t)
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	origin, err := url.Parse("http://" + server.address)
	if err != nil {
		t.Fatalf("parse server origin: %v", err)
	}
	client.Jar.SetCookies(origin, []*http.Cookie{sessionCookie})
	restarted := getFrontendPage(t, client, server.address, "/")
	if !strings.Contains(restarted.body, "Ada Lovelace") ||
		!strings.Contains(restarted.body, `data-reduced-effects="true"`) {
		t.Fatalf("restarted root lost session: %q", restarted.body)
	}

	missingCSRF := postFrontendForm(t, client, server.address, "/sign-out", nil)
	if missingCSRF.status != http.StatusForbidden {
		t.Fatalf("sign-out without CSRF = %d, want 403", missingCSRF.status)
	}
	invalidCSRF := postFrontendForm(t, client, server.address, "/sign-out", url.Values{
		"csrf_token": {"invalid"},
	})
	if invalidCSRF.status != http.StatusForbidden {
		t.Fatalf("sign-out with invalid CSRF = %d, want 403", invalidCSRF.status)
	}
	signOut := postFrontendForm(t, client, server.address, "/sign-out", url.Values{
		"csrf_token": {requireFrontendCSRF(t, restarted)},
	})
	if signOut.status != http.StatusSeeOther {
		t.Fatalf("sign-out response = %d %q", signOut.status, signOut.body)
	}
	anonymous := getFrontendPage(t, client, server.address, "/")
	if !strings.Contains(anonymous.body, "Sign in") ||
		strings.Contains(anonymous.body, "Ada Lovelace") ||
		!strings.Contains(anonymous.body, `data-reduced-effects="true"`) {
		t.Fatalf("anonymous root = %q", anonymous.body)
	}

	signInPage := getFrontendPage(t, client, server.address, "/sign-in")
	failedSignIn := postFrontendForm(t, client, server.address, "/sign-in", url.Values{
		"csrf_token": {requireFrontendCSRF(t, signInPage)},
		"handle":     {"ada"},
		"password":   {"incorrect password"},
	})
	if failedSignIn.status != http.StatusUnauthorized ||
		!strings.Contains(failedSignIn.body, "Sign-in failed") {
		t.Fatalf("failed sign-in = %d %q", failedSignIn.status, failedSignIn.body)
	}
	assertAccessibleFormErrors(t, failedSignIn, map[string]string{
		"sign-in-handle":   "Sign-in failed.",
		"sign-in-password": "Sign-in failed.",
	})
	if !strings.Contains(failedSignIn.body, `value="ada"`) ||
		strings.Contains(failedSignIn.body, "incorrect password") {
		t.Errorf("failed sign-in did not preserve only the Account Handle: %q", failedSignIn.body)
	}
	signInPage = getFrontendPage(t, client, server.address, "/sign-in")
	signIn := postFrontendForm(t, client, server.address, "/sign-in", url.Values{
		"csrf_token": {requireFrontendCSRF(t, signInPage)},
		"handle":     {"ADA"},
		"password":   {"correct horse battery staple"},
	})
	if signIn.status != http.StatusSeeOther {
		t.Fatalf("sign-in response = %d %q", signIn.status, signIn.body)
	}

	for _, asset := range []string{
		"/assets/frontend.css",
		"/assets/chakra-petch-regular.ttf",
		"/assets/chakra-petch-bold.ttf",
		"/assets/open-sans.ttf",
		"/assets/htmx-2.0.10.min.js",
		"/assets/htmx-ext-sse-2.2.4.min.js",
		"/assets/webauthn-v1.js",
	} {
		response := getFrontendPage(t, client, server.address, asset)
		if response.status != http.StatusOK || response.body == "" {
			t.Fatalf("GET %s = %d, %q", asset, response.status, response.body)
		}
	}
	server.stop(t)
}

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

func TestBrowserPublishesEventsUnderCurrentSlugs(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	prepareActiveSchedule(t, administrator, server)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	second := validEventInput()
	second["name"] = "Summer Showcase"
	second["command_id"] = "create-public-event-2"
	assertJSONRequest(
		t, administrator, server.address, "/admin/events", second,
		http.StatusCreated,
		"{\"id\":2,\"name\":\"Summer Showcase\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/events/2/grants",
		map[string]any{
			"account_id": 1, "role": "Producer",
			"command_id": "grant-public-event-2",
		},
		http.StatusCreated,
		"{\"event_id\":2,\"account_id\":1,\"role\":\"Producer\"}\n",
	)
	draft := validEventInput()
	draft["name"] = "Secret Draft"
	draft["command_id"] = "create-private-event-3"
	assertJSONRequest(
		t, administrator, server.address, "/admin/events", draft,
		http.StatusCreated,
		"{\"id\":3,\"name\":\"Secret Draft\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)

	submitEvent := func(eventID int, name, slug, start, end, locale string, listed bool) frontendResponse {
		t.Helper()
		path := "/backstage/events/" + strconv.Itoa(eventID) + "/settings"
		page := getFrontendPage(t, administrator, server.address, path)
		values := frontendNamedValues(
			page.body,
			"command_id",
			"expected_event_revision",
		)
		values.Set("csrf_token", requireFrontendCSRF(t, page))
		values.Set("event_name", name)
		values.Set("public_slug", slug)
		values.Set("planned_start_date", start)
		values.Set("planned_end_date", end)
		values.Set("timezone", "Europe/Berlin")
		values.Set("event_locale", locale)
		values.Set("content_language", "en-GB")
		values.Set("event_day_boundary", "06:00")
		values.Set("entry_default_disposition", "Pending")
		values.Set("submission_eligibility", "AllAccounts")
		values.Set("voting_method", "Range1To5")
		values.Set("self_vote_policy", "Allowed")
		values.Set("target_adjustment_presets_seconds", "-300,300,600")
		if listed {
			values.Set("public", "true")
		}
		return postFrontendForm(t, administrator, server.address, path, values)
	}
	setListed := func(eventID int, name, slug, start, end, locale string, listed bool) {
		t.Helper()
		saved := submitEvent(eventID, name, slug, start, end, locale, listed)
		if saved.status != http.StatusSeeOther {
			t.Fatalf("set Event %d listing to %t = %d %q", eventID, listed, saved.status, saved.body)
		}
	}
	setListed(1, "BeamConf 2099", "beamconf-2099", "2099-08-21", "2099-08-23", "en-GB", true)
	setListed(2, "Summer Showcase", "summer-private", "2026-08-21", "2026-08-23", "de-DE", false)
	setListed(2, "Summer Showcase", "summer-showcase", "2026-08-21", "2026-08-23", "de-DE", true)

	root := getFrontendPage(t, authenticatedClient(t), server.publicAddress, "/")
	for _, want := range []string{
		"Featured Event",
		`href="/events/beamconf-2099"`,
		"BeamConf 2099",
		`href="/events/summer-showcase"`,
		"Summer Showcase",
	} {
		if root.status != http.StatusOK || !strings.Contains(root.body, want) {
			t.Fatalf("Public Event Listing lacks %q: %d %q", want, root.status, root.body)
		}
	}
	if strings.Contains(root.body, "Secret Draft") {
		t.Fatalf("Public Event Listing disclosed Draft Event: %q", root.body)
	}
	privateSlug := getFrontendPage(
		t,
		authenticatedClient(t),
		server.publicAddress,
		"/events/summer-private",
	)
	if privateSlug.status != http.StatusNotFound {
		t.Fatalf("never-public Event Slug = %d %q", privateSlug.status, privateSlug.body)
	}
	for path, name := range map[string]string{
		"/events/beamconf-2099":   "BeamConf 2099",
		"/events/summer-showcase": "Summer Showcase",
	} {
		page := getFrontendPage(t, authenticatedClient(t), server.publicAddress, path)
		if page.status != http.StatusOK || !strings.Contains(page.body, name) {
			t.Fatalf("public Event %s = %d %q", path, page.status, page.body)
		}
	}
	private := getFrontendPage(
		t, authenticatedClient(t), server.publicAddress, "/events/secret-draft",
	)
	unknown := getFrontendPage(
		t, authenticatedClient(t), server.publicAddress, "/events/unknown-event",
	)
	if private.status != http.StatusNotFound ||
		private.status != unknown.status ||
		private.body != unknown.body {
		t.Fatalf(
			"private and unknown Events differ: private=%d %q unknown=%d %q",
			private.status, private.body, unknown.status, unknown.body,
		)
	}

	setListed(2, "Summer Showcase", "summer-stage", "2026-08-21", "2026-08-23", "de-DE", true)
	setListed(2, "Summer Showcase", "summer-final", "2026-08-21", "2026-08-23", "de-DE", true)
	publicClient := authenticatedClient(t)
	publicClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	for _, alias := range []string{"summer-showcase", "summer-stage"} {
		for _, suffix := range []string{"", "/schedule", "/competitions", "/results"} {
			redirect := getFrontendPage(
				t,
				publicClient,
				server.publicAddress,
				"/events/"+alias+suffix,
			)
			if redirect.status != http.StatusFound ||
				redirect.header.Get("Location") != "/events/summer-final"+suffix {
				t.Fatalf(
					"Event Slug Alias %q = %d Location %q",
					alias+suffix,
					redirect.status,
					redirect.header.Get("Location"),
				)
			}
		}
	}
	collision := submitEvent(
		1,
		"BeamConf 2099",
		"summer-stage",
		"2099-08-21",
		"2099-08-23",
		"en-GB",
		true,
	)
	if collision.status != http.StatusConflict ||
		!strings.Contains(collision.body, "Event Slug is already in use.") {
		t.Fatalf("retained Event Slug collision = %d %q", collision.status, collision.body)
	}

	setListed(2, "Summer Showcase", "summer-final", "2026-08-21", "2026-08-23", "de-DE", false)
	privateAlias := getFrontendPage(
		t,
		publicClient,
		server.publicAddress,
		"/events/summer-stage",
	)
	unknownAlias := getFrontendPage(
		t,
		publicClient,
		server.publicAddress,
		"/events/unknown-alias",
	)
	if privateAlias.status != http.StatusNotFound ||
		privateAlias.status != unknownAlias.status ||
		privateAlias.body != unknownAlias.body {
		t.Fatalf(
			"private and unknown Event Slug Aliases differ: private=%d %q unknown=%d %q",
			privateAlias.status,
			privateAlias.body,
			unknownAlias.status,
			unknownAlias.body,
		)
	}
	setListed(2, "Summer Showcase", "summer-final", "2026-08-21", "2026-08-23", "de-DE", true)

	administration := getFrontendPage(
		t,
		administrator,
		server.address,
		"/backstage/administration",
	)
	aliasForm := regexp.MustCompile(
		`name="action" value="prune-event-slug-alias">\s*` +
			`<input type="hidden" name="command_id" value="([^"]+)">\s*` +
			`<input type="hidden" name="alias_id" value="([0-9]+)">`,
	).FindStringSubmatch(administration.body)
	if len(aliasForm) != 3 {
		t.Fatalf("Event Slug Alias prune form missing: %q", administration.body)
	}
	pruneValues := url.Values{
		"csrf_token": {requireFrontendCSRF(t, administration)},
		"action":     {"prune-event-slug-alias"},
		"command_id": {aliasForm[1]},
		"alias_id":   {aliasForm[2]},
	}
	unconfirmed := postFrontendForm(
		t,
		administrator,
		server.address,
		"/backstage/administration",
		pruneValues,
	)
	if unconfirmed.status != http.StatusUnprocessableEntity ||
		!strings.Contains(unconfirmed.body, "Confirm the Event Slug Alias pruning warning.") {
		t.Fatalf("unconfirmed Event Slug Alias pruning = %d %q", unconfirmed.status, unconfirmed.body)
	}
	administration = getFrontendPage(
		t,
		administrator,
		server.address,
		"/backstage/administration",
	)
	aliasForm = regexp.MustCompile(
		`name="action" value="prune-event-slug-alias">\s*` +
			`<input type="hidden" name="command_id" value="([^"]+)">\s*` +
			`<input type="hidden" name="alias_id" value="([0-9]+)">`,
	).FindStringSubmatch(administration.body)
	pruneValues = url.Values{
		"csrf_token": {requireFrontendCSRF(t, administration)},
		"action":     {"prune-event-slug-alias"},
		"command_id": {aliasForm[1]},
		"alias_id":   {aliasForm[2]},
		"confirmed":  {"true"},
	}
	pruned := postFrontendForm(
		t,
		administrator,
		server.address,
		"/backstage/administration",
		pruneValues,
	)
	if pruned.status != http.StatusSeeOther {
		t.Fatalf("prune Event Slug Alias = %d %q", pruned.status, pruned.body)
	}
	setListed(2, "Summer Showcase", "summer-showcase", "2026-08-21", "2026-08-23", "de-DE", true)

	root = getFrontendPage(t, administrator, server.address, "/")
	denied := postFrontendForm(
		t,
		administrator,
		server.address,
		"/backstage/events/3/settings",
		url.Values{
			"csrf_token": {requireFrontendCSRF(t, root)},
			"public":     {"true"},
		},
	)
	if denied.status != http.StatusNotFound {
		t.Fatalf("Administrator published without Event Grant: %d %q", denied.status, denied.body)
	}

	setListed(1, "BeamConf 2099", "beamconf-2099", "2099-08-21", "2099-08-23", "en-GB", false)
	root = getFrontendPage(t, authenticatedClient(t), server.publicAddress, "/")
	if strings.Contains(root.body, "BeamConf 2099") ||
		!strings.Contains(root.body, "Summer Showcase") {
		t.Fatalf("Public Event Listing followed Active Event instead of Producer state: %q", root.body)
	}
	if hidden := getFrontendPage(
		t, authenticatedClient(t), server.publicAddress, "/events/beamconf-2099",
	); hidden.status != http.StatusNotFound {
		t.Fatalf("unlisted Active Event = %d %q", hidden.status, hidden.body)
	}
	for _, suffix := range []string{"/schedule", "/competitions", "/results"} {
		if hidden := getFrontendPage(
			t,
			authenticatedClient(t),
			server.publicAddress,
			"/events/beamconf-2099"+suffix,
		); hidden.status != http.StatusNotFound {
			t.Fatalf("unlisted Active Event%s = %d %q", suffix, hidden.status, hidden.body)
		}
	}

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	server = startBeamersWithPublicListener(t, bin, dataDir)
	root = getFrontendPage(t, authenticatedClient(t), server.publicAddress, "/")
	if !strings.Contains(root.body, `href="/events/summer-showcase"`) ||
		strings.Contains(root.body, "BeamConf 2099") {
		t.Fatalf("restarted Public Event Listing = %d %q", root.status, root.body)
	}
	for _, alias := range []string{"summer-stage", "summer-final"} {
		redirect := getFrontendPage(
			t,
			publicClient,
			server.publicAddress,
			"/events/"+alias,
		)
		if redirect.status != http.StatusFound ||
			redirect.header.Get("Location") != "/events/summer-showcase" {
			t.Fatalf(
				"restarted Event Slug Alias %q = %d Location %q",
				alias,
				redirect.status,
				redirect.header.Get("Location"),
			)
		}
	}
	server.stop(t)
}

func TestBrowserFollowsCanonicalPublicEventJourney(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	presentationID := prepareActiveSchedule(t, administrator, server)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	settings := getFrontendPage(
		t,
		administrator,
		server.address,
		"/backstage/events/1/settings",
	)
	values := frontendNamedValues(
		settings.body,
		"command_id",
		"expected_event_revision",
	)
	values.Set("csrf_token", requireFrontendCSRF(t, settings))
	values.Set("event_name", "BeamConf 2099")
	values.Set("public_slug", "beamconf-2099")
	values.Set("planned_start_date", "2099-08-21")
	values.Set("planned_end_date", "2099-08-23")
	values.Set("timezone", "Europe/Berlin")
	values.Set("event_locale", "en-GB")
	values.Set("content_language", "en-GB")
	values.Set("event_day_boundary", "06:00")
	values.Set("entry_default_disposition", "Pending")
	values.Set("submission_eligibility", "AllAccounts")
	values.Set("voting_method", "Range1To5")
	values.Set("self_vote_policy", "Allowed")
	values.Set("target_adjustment_presets_seconds", "-300,300,600")
	values.Set("public", "true")
	if published := postFrontendForm(
		t,
		administrator,
		server.address,
		"/backstage/events/1/settings",
		values,
	); published.status != http.StatusSeeOther {
		t.Fatalf("publish Event = %d %q", published.status, published.body)
	}

	emptyCompetitions := getFrontendPage(
		t,
		authenticatedClient(t),
		server.publicAddress,
		"/events/beamconf-2099/competitions",
	)
	if emptyCompetitions.status != http.StatusOK ||
		!strings.Contains(
			emptyCompetitions.body,
			"No public Competitions are available yet.",
		) {
		t.Fatalf(
			"empty canonical Competitions = %d %q",
			emptyCompetitions.status,
			emptyCompetitions.body,
		)
	}
	competitionID, _ := addCompetitionSession(t, administrator, server)

	root := getFrontendPage(t, administrator, server.address, "/")
	if !strings.Contains(root.body, `href="/events/beamconf-2099"`) {
		t.Fatalf("root has no public Event journey: %q", root.body)
	}
	assertFrontendPrimaryNavigation(t, root, true)
	assertFrontendPrimaryNavigation(
		t,
		getFrontendPage(t, administrator, server.address, "/profile"),
		true,
	)
	assertFrontendPrimaryNavigation(
		t,
		getFrontendPage(t, administrator, server.address, "/my-participation"),
		true,
	)
	hubPath := frontendLinkPath(t, root, "BeamConf 2099")
	hub := getFrontendPage(
		t,
		administrator,
		server.address,
		hubPath,
	)
	for _, want := range []string{
		"Ada Admin",
		"2099-08-21",
		"2099-08-23",
		`href="/events/beamconf-2099/schedule"`,
		`href="/events/beamconf-2099/competitions"`,
		`href="/events/beamconf-2099/results"`,
	} {
		if hub.status != http.StatusOK || !strings.Contains(hub.body, want) {
			t.Fatalf("public Event hub lacks %q: %d %q", want, hub.status, hub.body)
		}
	}
	assertFrontendPrimaryNavigation(t, hub, true)
	assertFrontendEventShell(
		t,
		hub,
		hubPath,
		"Events",
		"BeamConf 2099",
	)

	publicClient := authenticatedClient(t)
	publicClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	schedule := getFrontendPage(
		t,
		publicClient,
		server.publicAddress,
		frontendLinkPath(t, hub, "Event Schedule"),
	)
	if schedule.status != http.StatusOK ||
		!strings.Contains(schedule.body, "Opening Keynote") ||
		!strings.Contains(
			schedule.body,
			`href="/events/beamconf-2099/schedule/sessions/`,
		) ||
		strings.Contains(schedule.body, "Private Soundcheck") {
		t.Fatalf("canonical Event Schedule = %d %q", schedule.status, schedule.body)
	}
	if got := schedule.header.Get("Cache-Control"); got != "public, max-age=15, must-revalidate" {
		t.Errorf("canonical Event Schedule Cache-Control = %q", got)
	}
	assertFrontendSignedOutNavigation(t, schedule)
	signedInSchedule := getFrontendPage(
		t,
		administrator,
		server.address,
		"/events/beamconf-2099/schedule",
	)
	assertFrontendPrimaryNavigation(t, signedInSchedule, true)
	assertFrontendEventShell(
		t,
		signedInSchedule,
		"/events/beamconf-2099/schedule",
		"Events",
		"BeamConf 2099",
		"Schedule",
	)
	publicListenerSchedule := getFrontendPage(
		t,
		administrator,
		server.publicAddress,
		"/events/beamconf-2099/schedule",
	)
	assertFrontendPrimaryNavigation(t, publicListenerSchedule, false)
	sessionPath := frontendLinkPath(t, schedule, "Opening Keynote")
	sessionPage := getFrontendPage(
		t,
		publicClient,
		server.publicAddress,
		sessionPath,
	)
	for _, want := range []string{
		"Welcome to BeamConf 2099",
		"Original Speaker",
		"Main Hall",
		"Status: Scheduled",
		`lang="en-GB"`,
		`data-locale="en-GB"`,
		`href="/assets/events/1/theme.css"`,
	} {
		if sessionPage.status != http.StatusOK || !strings.Contains(sessionPage.body, want) {
			t.Fatalf("canonical Session lacks %q: %d %q", want, sessionPage.status, sessionPage.body)
		}
	}
	if strings.Contains(sessionPage.body, "Call Pat") {
		t.Fatalf("canonical Session leaked Crew Notes: %q", sessionPage.body)
	}
	assertFrontendEventShell(
		t,
		sessionPage,
		"/events/beamconf-2099/schedule",
		"Events",
		"BeamConf 2099",
		"Schedule",
		"Opening Keynote",
	)
	initialSessionETag := sessionPage.header.Get("ETag")
	publicPresentationVersion := decodeAttachmentVersion(t, requestMultipart(
		t.Context(),
		administrator,
		server.address,
		"/crew/events/1/attachments",
		map[string]string{
			"target_type": "Presentation",
			"target_id":   strconv.FormatInt(presentationID, 10),
			"name":        "Keynote slides",
			"command_id":  "upload-public-presentation-file",
		},
		"slides.txt",
		"text/plain",
		[]byte("slides"),
	))
	crewPresentationVersion := decodeAttachmentVersion(t, requestMultipart(
		t.Context(),
		administrator,
		server.address,
		"/crew/events/1/attachments",
		map[string]string{
			"target_type": "Presentation",
			"target_id":   strconv.FormatInt(presentationID, 10),
			"name":        "Speaker notes",
			"command_id":  "upload-crew-presentation-file",
			"crew_only":   "true",
		},
		"notes.txt",
		"text/plain",
		[]byte("notes"),
	))
	sessionPage = getFrontendPage(t, publicClient, server.publicAddress, sessionPath)
	if strings.Contains(sessionPage.body, "slides.txt") ||
		strings.Contains(sessionPage.body, "notes.txt") {
		t.Fatalf("unreleased Session Attachment leaked: %q", sessionPage.body)
	}
	for _, versionID := range []int{publicPresentationVersion.ID, crewPresentationVersion.ID} {
		if err := storetest.MarkAttachmentVersionFinal(
			t.Context(),
			filepath.Join(server.dataDir, "beamers.db"),
			versionID,
		); err != nil {
			t.Fatalf("finalize Presentation Attachment %d: %v", versionID, err)
		}
	}
	if configured := requestJSONMethod(
		t.Context(),
		http.MethodPatch,
		administrator,
		server.address,
		"/crew/events/1/attachment-release",
		map[string]any{
			"policy": "OnLive", "expected_revision": 0,
			"command_id": "configure-public-page-release",
		},
	); configured.status != http.StatusOK {
		t.Fatalf("configure Event Attachment release = %d %q", configured.status, configured.body)
	}
	sessionClient := sessionv1connect.NewSessionControlServiceClient(
		administrator,
		"http://"+server.address,
		connect.WithProtoJSON(),
	)
	startedPresentation, err := sessionClient.StartSession(
		t.Context(),
		connect.NewRequest(&sessionv1.StartSessionRequest{
			EventId: 1, SessionId: presentationID,
			CommandId:                 "start-presentation-page-release",
			ExpectedLiveStateRevision: proto.Int64(0),
		}),
	)
	if err != nil {
		t.Fatalf("start Presentation for Attachment release: %v", err)
	}
	sessionPage = getFrontendPage(t, publicClient, server.publicAddress, sessionPath)
	for _, want := range []string{
		"Keynote slides (slides.txt)",
		fmt.Sprintf(`href="/public/attachments/%d"`, publicPresentationVersion.ID),
	} {
		if sessionPage.status != http.StatusOK || !strings.Contains(sessionPage.body, want) {
			t.Fatalf("released Session lacks %q: %d %q", want, sessionPage.status, sessionPage.body)
		}
	}
	if got := sessionPage.header.Get("ETag"); got == "" || got == initialSessionETag {
		t.Fatalf("released Session ETag = %q, initial %q", got, initialSessionETag)
	}
	if strings.Contains(sessionPage.body, "Speaker notes") ||
		strings.Contains(sessionPage.body, "notes.txt") {
		t.Fatalf("Crew Only Session Attachment leaked: %q", sessionPage.body)
	}
	if held := requestJSONMethod(
		t.Context(),
		http.MethodPatch,
		administrator,
		server.address,
		fmt.Sprintf(
			"/crew/events/1/attachment-versions/%d/release",
			publicPresentationVersion.ID,
		),
		map[string]any{
			"hold": true, "expected_revision": 0,
			"command_id": "hold-presentation-page-file",
		},
	); held.status != http.StatusOK {
		t.Fatalf("hold Session Attachment = %d %q", held.status, held.body)
	}
	sessionPage = getFrontendPage(t, publicClient, server.publicAddress, sessionPath)
	if strings.Contains(sessionPage.body, "slides.txt") {
		t.Fatalf("held Session Attachment leaked: %q", sessionPage.body)
	}
	crewOnly := getFrontendPage(
		t,
		publicClient,
		server.publicAddress,
		"/events/beamconf-2099/schedule/sessions/2",
	)
	unknown := getFrontendPage(
		t,
		publicClient,
		server.publicAddress,
		"/events/beamconf-2099/schedule/sessions/999999",
	)
	if crewOnly.status != http.StatusNotFound ||
		unknown.status != http.StatusNotFound ||
		crewOnly.body != unknown.body {
		t.Fatalf(
			"safe Session not-found mismatch: Crew Only %d %q, unknown %d %q",
			crewOnly.status,
			crewOnly.body,
			unknown.status,
			unknown.body,
		)
	}
	if _, err = sessionClient.EndSession(
		t.Context(),
		connect.NewRequest(&sessionv1.EndSessionRequest{
			EventId: 1, SessionId: presentationID,
			CommandId: "end-presentation-page-release",
			ExpectedLiveStateRevision: new(
				startedPresentation.Msg.GetState().GetLiveStateRevision(),
			),
		}),
	); err != nil {
		t.Fatalf("end Presentation after Attachment release: %v", err)
	}

	competitions := getFrontendPage(
		t,
		publicClient,
		server.publicAddress,
		frontendLinkPath(t, hub, "Competitions"),
	)
	if competitions.status != http.StatusOK ||
		!strings.Contains(competitions.body, "Demo Competition") ||
		!strings.Contains(competitions.body, "Projects presented by attendees") ||
		strings.Contains(competitions.body, "Browser Certified Result") {
		t.Fatalf("canonical Competitions index = %d %q", competitions.status, competitions.body)
	}
	assertFrontendEventShell(
		t,
		competitions,
		"/events/beamconf-2099/competitions",
		"Events",
		"BeamConf 2099",
		"Competitions",
	)
	competitionPath := frontendLinkPath(t, competitions, "Demo Competition")
	competition := getFrontendPage(
		t,
		publicClient,
		server.publicAddress,
		competitionPath,
	)
	if competition.status != http.StatusOK ||
		!strings.Contains(competition.body, "Results have not been published yet.") {
		t.Fatalf("canonical Competition state = %d %q", competition.status, competition.body)
	}
	assertFrontendEventShell(
		t,
		competition,
		"/events/beamconf-2099/competitions",
		"Events",
		"BeamConf 2099",
		"Competitions",
		"Demo Competition",
	)
	assertFrontendPrimaryNavigation(
		t,
		getFrontendPage(t, administrator, server.address, competitionPath),
		true,
	)
	initialCompetitionETag := competition.header.Get("ETag")

	results := getFrontendPage(
		t,
		publicClient,
		server.publicAddress,
		"/events/beamconf-2099/results",
	)
	if results.status != http.StatusOK ||
		!strings.Contains(results.body, "Results have not been published yet.") {
		t.Fatalf("canonical Event Results state = %d %q", results.status, results.body)
	}
	assertFrontendEventShell(
		t,
		results,
		"/events/beamconf-2099/results",
		"Events",
		"BeamConf 2099",
		"Results",
	)
	entryID := prepareReleasedBrowserResults(t, administrator, server, competitionID)
	publicVersion := decodeAttachmentVersion(t, requestMultipart(
		t.Context(),
		administrator,
		server.address,
		"/crew/events/1/attachments",
		map[string]string{
			"target_type": "Entry",
			"target_id":   strconv.FormatInt(entryID, 10),
			"name":        "Public download",
			"command_id":  "upload-public-competition-file",
		},
		"public.txt",
		"text/plain",
		[]byte("public"),
	))
	crewVersion := decodeAttachmentVersion(t, requestMultipart(
		t.Context(),
		administrator,
		server.address,
		"/crew/events/1/attachments",
		map[string]string{
			"target_type": "Entry",
			"target_id":   strconv.FormatInt(entryID, 10),
			"name":        "Crew secret",
			"command_id":  "upload-crew-competition-file",
			"crew_only":   "true",
		},
		"crew.txt",
		"text/plain",
		[]byte("crew"),
	))
	competition = getFrontendPage(
		t,
		publicClient,
		server.publicAddress,
		competitionPath,
	)
	if strings.Contains(competition.body, "public.txt") ||
		strings.Contains(competition.body, "crew.txt") {
		t.Fatalf("unreleased Competition Attachment leaked: %q", competition.body)
	}
	competitionClient := competitionv1connect.NewCompetitionServiceClient(
		administrator,
		"http://"+server.address,
		connect.WithProtoJSON(),
	)
	for _, version := range []attachmentVersionResponse{publicVersion, crewVersion} {
		if _, err = competitionClient.SetEntryAttachmentReadiness(
			t.Context(),
			connect.NewRequest(&competitionv1.SetEntryAttachmentReadinessRequest{
				EventId: 1, SessionId: competitionID, EntryId: entryID,
				AttachmentVersionId: int64(version.ID),
				CommandId:           fmt.Sprintf("finalize-competition-file-%d", version.ID),
				ExpectedRevision:    int64(version.ReadinessRevision),
				Final:               true,
				Primary:             version.ID == publicVersion.ID,
			}),
		); err != nil {
			t.Fatalf("finalize Competition Attachment %d: %v", version.ID, err)
		}
	}
	if _, err = sessionClient.StartSession(
		t.Context(),
		connect.NewRequest(&sessionv1.StartSessionRequest{
			EventId: 1, SessionId: competitionID,
			CommandId:                 "start-competition-page-release",
			ExpectedLiveStateRevision: proto.Int64(0),
		}),
	); err != nil {
		t.Fatalf("start Competition for Attachment release: %v", err)
	}
	competition = getFrontendPage(
		t,
		publicClient,
		server.publicAddress,
		competitionPath,
	)
	for _, want := range []string{
		fmt.Sprintf(`id="entry-%d"`, entryID),
		"Browser Certified Result",
		"Public download (public.txt)",
		fmt.Sprintf(`href="/public/attachments/%d"`, publicVersion.ID),
		`href="/events/beamconf-2099/results"`,
	} {
		if competition.status != http.StatusOK || !strings.Contains(competition.body, want) {
			t.Fatalf("published Competition lacks %q: %d %q", want, competition.status, competition.body)
		}
	}
	if got := competition.header.Get("ETag"); got == "" || got == initialCompetitionETag {
		t.Fatalf("published Competition ETag = %q, initial %q", got, initialCompetitionETag)
	}
	if strings.Contains(competition.body, "Crew secret") ||
		strings.Contains(competition.body, "crew.txt") {
		t.Fatalf("Crew Only Competition Attachment leaked: %q", competition.body)
	}
	if held := requestJSONMethod(
		t.Context(),
		http.MethodPatch,
		administrator,
		server.address,
		fmt.Sprintf("/crew/events/1/attachment-versions/%d/release", publicVersion.ID),
		map[string]any{
			"hold": true, "expected_revision": 0,
			"command_id": "hold-competition-page-file",
		},
	); held.status != http.StatusOK {
		t.Fatalf("hold Competition Attachment = %d %q", held.status, held.body)
	}
	competition = getFrontendPage(
		t,
		publicClient,
		server.publicAddress,
		competitionPath,
	)
	if strings.Contains(competition.body, "public.txt") {
		t.Fatalf("held Competition Attachment leaked: %q", competition.body)
	}
	results = getFrontendPage(
		t,
		publicClient,
		server.publicAddress,
		"/events/beamconf-2099/results",
	)
	if results.status != http.StatusOK ||
		!strings.Contains(results.body, "Browser Certified Result") ||
		!strings.Contains(results.body, `href="/events/beamconf-2099/schedule"`) ||
		!strings.Contains(results.body, `href="/assets/events/1/theme.css"`) ||
		!strings.Contains(results.body, `data-reduced-effects="false"`) ||
		!strings.Contains(results.body, `class="skip-link" href="#main-content"`) {
		t.Fatalf("canonical published Event Results = %d %q", results.status, results.body)
	}
	if strings.Contains(results.body, `href="/schedule"`) {
		t.Fatalf("canonical published Event Results lost Event context: %q", results.body)
	}
	signedInResults := getFrontendPage(
		t,
		administrator,
		server.address,
		"/events/beamconf-2099/results",
	)
	reducedResultsClient := authenticatedClient(t)
	resultsURL, err := url.Parse("http://" + server.publicAddress)
	if err != nil {
		t.Fatalf("parse public Results URL: %v", err)
	}
	reducedResultsClient.Jar.SetCookies(resultsURL, []*http.Cookie{{
		Name: "beamers_reduced_effects", Value: "true",
	}})
	reducedResults := getFrontendPage(
		t,
		reducedResultsClient,
		server.publicAddress,
		"/events/beamconf-2099/results",
	)
	for label, response := range map[string]frontendResponse{
		"signed in":       signedInResults,
		"reduced effects": reducedResults,
	} {
		if response.status != http.StatusOK ||
			response.body != results.body ||
			response.header.Get("ETag") != results.header.Get("ETag") {
			t.Fatalf(
				"%s Results differ from anonymous: %d %q %q",
				label,
				response.status,
				response.header.Get("ETag"),
				response.body,
			)
		}
	}
	if results.header.Get("Cache-Control") != "public, max-age=15, must-revalidate" ||
		results.header.Get("ETag") == "" ||
		results.header.Get("Vary") != "" {
		t.Fatalf("public Results cache headers = %#v", results.header)
	}

	removedPaths := []string{
		"/schedule",
		"/schedule/sessions/" + strconv.FormatInt(presentationID, 10),
		"/submissions",
		"/results/events/1",
		"/results/events/1/standalone/" + strconv.FormatInt(competitionID, 10),
		"/results/events/1/event-awards",
	}
	for _, listener := range []struct {
		name, address string
		client        *http.Client
	}{
		{"Backstage", server.address, administrator},
		{"public", server.publicAddress, publicClient},
	} {
		for _, path := range removedPaths {
			response := getFrontendPage(t, listener.client, listener.address, path)
			assertFrontendRecovery(t, response, http.StatusNotFound, "Page not found")
		}
	}

	for _, path := range []string{
		"/events/unknown/schedule",
		"/events/unknown/competitions",
		"/events/unknown/results",
	} {
		if response := getFrontendPage(
			t,
			publicClient,
			server.publicAddress,
			path,
		); response.status != http.StatusNotFound {
			t.Errorf("%s = %d %q, want 404", path, response.status, response.body)
		}
	}
	server.stop(t)
}

func TestBrowserRegistrationProfileAndDisablement(t *testing.T) {
	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)
	bootstrapToken := strings.TrimSpace(
		runBeamersOutput(t, bin, "bootstrap", "--data-dir", dataDir),
	)
	server := startBeamers(t, bin, dataDir)
	admin := authenticatedClient(t)
	admin.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	setup := getFrontendPage(t, admin, server.address, "/setup")
	setupResponse := postFrontendForm(t, admin, server.address, "/setup", url.Values{
		"csrf_token":      {requireFrontendCSRF(t, setup)},
		"bootstrap_token": {bootstrapToken},
		"handle":          {"admin"},
		"display_name":    {"Ada Admin"},
		"password":        {"administrator correct horse battery staple"},
	})
	if setupResponse.status != http.StatusSeeOther {
		t.Fatalf("setup response = %d %q", setupResponse.status, setupResponse.body)
	}
	assertJSONRequest(
		t,
		admin,
		server.address,
		"/admin/events",
		validEventInput(),
		http.StatusCreated,
		"{\"id\":1,\"name\":\"Revision 2026\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	assertJSONRequest(
		t,
		admin,
		server.address,
		"/admin/events/1/grants",
		map[string]any{
			"account_id": 1,
			"role":       "Producer",
			"command_id": "grant-browser-admin-producer",
		},
		http.StatusCreated,
		"{\"event_id\":1,\"account_id\":1,\"role\":\"Producer\"}\n",
	)

	participant := authenticatedClient(t)
	participant.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	registration := getFrontendPage(t, participant, server.address, "/register")
	if !strings.Contains(registration.body, `method="post" action="/register"`) ||
		strings.Contains(registration.body, "email") {
		t.Fatalf("registration form = %q", registration.body)
	}
	invalidRegistration := postFrontendForm(
		t,
		participant,
		server.address,
		"/register",
		url.Values{
			"csrf_token":   {requireFrontendCSRF(t, registration)},
			"handle":       {""},
			"display_name": {""},
			"password":     {"short"},
		},
	)
	if invalidRegistration.status != http.StatusBadRequest {
		t.Fatalf(
			"invalid registration = %d %q",
			invalidRegistration.status,
			invalidRegistration.body,
		)
	}
	assertAccessibleFormErrors(t, invalidRegistration, map[string]string{
		"register-handle":       "Enter an Account Handle.",
		"register-display-name": "Enter a Display Name.",
		"register-password":     "Enter a password of 12 to 1024 characters.",
	})
	if strings.Contains(invalidRegistration.body, "short") {
		t.Error("invalid registration retained a password")
	}
	registered := postFrontendForm(t, participant, server.address, "/register", url.Values{
		"csrf_token":   {requireFrontendCSRF(t, registration)},
		"handle":       {"participant"},
		"display_name": {"Private Person"},
		"password":     {"participant correct horse battery staple"},
	})
	if registered.status != http.StatusSeeOther ||
		registered.header.Get("Location") != "/sign-in" {
		t.Fatalf("registration response = %d %q", registered.status, registered.body)
	}
	duplicateClient := authenticatedClient(t)
	duplicatePage := getFrontendPage(t, duplicateClient, server.address, "/register")
	duplicate := postFrontendForm(
		t,
		duplicateClient,
		server.address,
		"/register",
		url.Values{
			"csrf_token":   {requireFrontendCSRF(t, duplicatePage)},
			"handle":       {"PARTICIPANT"},
			"display_name": {"Someone Else"},
			"password":     {"different correct horse battery staple"},
		},
	)
	if duplicate.status != http.StatusConflict ||
		!strings.Contains(duplicate.body, "already in use") {
		t.Fatalf("duplicate registration = %d %q", duplicate.status, duplicate.body)
	}
	if private := getFrontendPage(
		t,
		authenticatedClient(t),
		server.address,
		"/people/participant",
	); private.status != http.StatusNotFound {
		t.Fatalf("private Profile status = %d, want 404", private.status)
	}

	signInPage := getFrontendPage(t, participant, server.address, "/sign-in")
	signIn := postFrontendForm(t, participant, server.address, "/sign-in", url.Values{
		"csrf_token": {requireFrontendCSRF(t, signInPage)},
		"handle":     {"PARTICIPANT"},
		"password":   {"participant correct horse battery staple"},
	})
	if signIn.status != http.StatusSeeOther {
		t.Fatalf("participant sign-in = %d %q", signIn.status, signIn.body)
	}
	profile := getFrontendPage(t, participant, server.address, "/profile")
	if !strings.Contains(profile.body, `method="post" action="/profile"`) ||
		!strings.Contains(profile.body, "Private Person") {
		t.Fatalf("Profile form = %q", profile.body)
	}
	invalidProfile := postFrontendForm(t, participant, server.address, "/profile", url.Values{
		"csrf_token":   {requireFrontendCSRF(t, profile)},
		"display_name": {" "},
		"published":    {"true"},
	})
	if invalidProfile.status != http.StatusBadRequest {
		t.Fatalf("invalid Profile = %d %q", invalidProfile.status, invalidProfile.body)
	}
	assertAccessibleFormErrors(t, invalidProfile, map[string]string{
		"profile-display-name": "Enter a Display Name.",
	})
	if !strings.Contains(invalidProfile.body, `value=" "`) ||
		!strings.Contains(invalidProfile.body, `name="published" value="true" checked`) {
		t.Errorf("invalid Profile did not preserve submitted values: %q", invalidProfile.body)
	}
	saved := postFrontendForm(t, participant, server.address, "/profile", url.Values{
		"csrf_token":   {requireFrontendCSRF(t, profile)},
		"display_name": {"Public Person"},
		"published":    {"true"},
	})
	if saved.status != http.StatusSeeOther {
		t.Fatalf("Profile update = %d %q", saved.status, saved.body)
	}
	public := getFrontendPage(
		t,
		authenticatedClient(t),
		server.address,
		"/people/PARTICIPANT",
	)
	if public.status != http.StatusOK || !strings.Contains(public.body, "Public Person") ||
		strings.Contains(public.body, "Private Person") {
		t.Fatalf("Public Profile = %d %q", public.status, public.body)
	}

	policy := getFrontendPage(t, admin, server.address, "/admin/registration")
	closed := postFrontendForm(t, admin, server.address, "/admin/registration", url.Values{
		"csrf_token": {requireFrontendCSRF(t, policy)},
	})
	if closed.status != http.StatusSeeOther {
		t.Fatalf("close registration = %d %q", closed.status, closed.body)
	}
	closedPage := getFrontendPage(t, authenticatedClient(t), server.address, "/register")
	if !strings.Contains(closedPage.body, "Registration is closed") ||
		strings.Contains(closedPage.body, `action="/register"`) {
		t.Fatalf("closed registration page = %q", closedPage.body)
	}
	signedInRoot := getFrontendPage(t, participant, server.address, "/")
	signedOut := postFrontendForm(t, participant, server.address, "/sign-out", url.Values{
		"csrf_token": {requireFrontendCSRF(t, signedInRoot)},
	})
	if signedOut.status != http.StatusSeeOther {
		t.Fatalf("sign-out after registration closed = %d %q", signedOut.status, signedOut.body)
	}
	closedSignInPage := getFrontendPage(t, participant, server.address, "/sign-in")
	closedSignIn := postFrontendForm(t, participant, server.address, "/sign-in", url.Values{
		"csrf_token": {requireFrontendCSRF(t, closedSignInPage)},
		"handle":     {"participant"},
		"password":   {"participant correct horse battery staple"},
	})
	if closedSignIn.status != http.StatusSeeOther {
		t.Fatalf(
			"existing sign-in after registration closed = %d %q",
			closedSignIn.status,
			closedSignIn.body,
		)
	}
	rejectedEvent := validEventInput()
	rejectedEvent["command_id"] = "disabled-participant-event-attempt"
	assertJSONRequest(
		t,
		participant,
		server.address,
		"/admin/events",
		rejectedEvent,
		http.StatusForbidden,
		"Administrator authority required\n",
	)
	assertJSONRequest(
		t,
		admin,
		server.address,
		"/admin/accounts/2/disable",
		map[string]string{
			"command_id": "disable-browser-participant",
			"reason":     "access_revoked",
		},
		http.StatusNoContent,
		"",
	)
	if disabled := getFrontendPage(
		t,
		authenticatedClient(t),
		server.address,
		"/people/participant",
	); disabled.status != http.StatusNotFound {
		t.Fatalf("disabled Public Profile status = %d, want 404", disabled.status)
	}
	audits, _ := readAuditHistory(t, admin, server.address)
	retainedEventActor := false
	for _, entry := range audits {
		if entry.ActorAccountID == 2 &&
			entry.Action == "CreateEvent" &&
			entry.Outcome == "Rejected" {
			retainedEventActor = true
		}
	}
	if !retainedEventActor {
		t.Fatal("disablement erased the participant's retained Event history")
	}
	assertGETResponse(
		t,
		admin,
		server.address,
		"/crew/events/1",
		http.StatusOK,
		"{\"id\":1,\"name\":\"Revision 2026\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	server.stop(t)
}

func TestBrowserRecoversAccountWithoutEmail(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	const administrationPath = "/backstage/administration"
	page := getFrontendPage(t, administrator, server.address, administrationPath)
	created := postFrontendForm(
		t,
		administrator,
		server.address,
		administrationPath,
		url.Values{
			"csrf_token":     {requireFrontendCSRF(t, page)},
			"action":         {"create-account"},
			"command_id":     {"create-recovery-participant"},
			"account_handle": {"pat"},
			"display_name":   {"Pat Participant"},
			"password":       {"participant correct horse battery staple"},
		},
	)
	if created.status != http.StatusSeeOther {
		t.Fatalf("create recovery Account = %d %q", created.status, created.body)
	}

	participant := authenticatedClient(t)
	participant.CheckRedirect = administrator.CheckRedirect
	signInPage := getFrontendPage(t, participant, server.address, "/sign-in")
	signIn := postFrontendForm(t, participant, server.address, "/sign-in", url.Values{
		"csrf_token": {requireFrontendCSRF(t, signInPage)},
		"handle":     {"pat"},
		"password":   {"participant correct horse battery staple"},
	})
	if signIn.status != http.StatusSeeOther {
		t.Fatalf("sign in recovery Account = %d %q", signIn.status, signIn.body)
	}
	profile := getFrontendPage(t, participant, server.address, "/profile")
	profileCommandID := frontendNamedValues(profile.body, "command_id").Get("command_id")
	generated := postFrontendForm(t, participant, server.address, "/profile", url.Values{
		"csrf_token": {requireFrontendCSRF(t, profile)},
		"action":     {"replace-recovery-codes"},
		"command_id": {profileCommandID},
	})
	codeMatch := recoveryCodeOutput.FindStringSubmatch(generated.body)
	if generated.status != http.StatusOK || len(codeMatch) != 2 {
		t.Fatalf("generate Recovery Codes = %d %q", generated.status, generated.body)
	}
	code := codeMatch[1]
	if strings.Contains(getFrontendPage(t, participant, server.address, "/profile").body, code) {
		t.Fatal("Recovery Code remained visible after its one-time response")
	}

	database, err := sql.Open("sqlite", filepath.Join(server.dataDir, "beamers.db"))
	if err != nil {
		t.Fatalf("open recovery database: %v", err)
	}
	var plaintextCount int
	if err = database.QueryRowContext(
		t.Context(),
		"SELECT count(*) FROM recovery_codes WHERE code_hash = ?",
		code,
	).Scan(&plaintextCount); err != nil {
		t.Fatalf("inspect Recovery Code digest: %v", err)
	}
	if closeErr := database.Close(); closeErr != nil {
		t.Fatalf("close recovery database: %v", closeErr)
	}
	if plaintextCount != 0 {
		t.Fatal("Recovery Code was stored as plaintext")
	}

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	server = startBeamersWithPublicListener(t, bin, dataDir)
	recoveryClient := authenticatedClient(t)
	recoveryClient.CheckRedirect = administrator.CheckRedirect
	recoveryPage := getFrontendPage(t, recoveryClient, server.address, "/recover")
	recovered := postFrontendForm(t, recoveryClient, server.address, "/recover", url.Values{
		"csrf_token": {requireFrontendCSRF(t, recoveryPage)},
		"command_id": {frontendNamedValues(recoveryPage.body, "command_id").Get("command_id")},
		"handle":     {"PAT"},
		"credential": {code},
		"password":   {"recovered correct horse battery staple"},
	})
	if recovered.status != http.StatusSeeOther || recovered.header.Get("Location") != "/" {
		t.Fatalf("recover Account after restart = %d %q", recovered.status, recovered.body)
	}
	reuseClient := authenticatedClient(t)
	reusePage := getFrontendPage(t, reuseClient, server.address, "/recover")
	reuseCSRF := requireFrontendCSRF(t, reusePage)
	reuseCommandID := frontendNamedValues(reusePage.body, "command_id").Get("command_id")
	reused := postFrontendForm(t, reuseClient, server.address, "/recover", url.Values{
		"csrf_token": {reuseCSRF},
		"command_id": {reuseCommandID},
		"handle":     {"pat"},
		"credential": {code},
		"password":   {"another recovered horse battery staple"},
	})
	if reused.status != http.StatusUnauthorized ||
		!strings.Contains(reused.body, "Recovery failed") {
		t.Fatalf("reuse Recovery Code = %d %q", reused.status, reused.body)
	}
	assertAccessibleFormErrors(t, reused, map[string]string{
		"recover-handle":     "Recovery failed.",
		"recover-credential": "Recovery failed.",
	})
	if !strings.Contains(reused.body, `value="pat"`) ||
		strings.Contains(reused.body, code) ||
		strings.Contains(reused.body, "another recovered horse battery staple") {
		t.Errorf("failed recovery did not preserve only the Account Handle: %q", reused.body)
	}
	for attempt := 1; attempt < 5; attempt++ {
		reused = postFrontendForm(t, reuseClient, server.address, "/recover", url.Values{
			"csrf_token": {reuseCSRF},
			"command_id": {reuseCommandID},
			"handle":     {"pat"},
			"credential": {code},
			"password":   {"another recovered horse battery staple"},
		})
		if reused.status != http.StatusUnauthorized {
			t.Fatalf("recovery failure %d = %d %q", attempt+1, reused.status, reused.body)
		}
	}
	rateLimited := postFrontendForm(t, reuseClient, server.address, "/recover", url.Values{
		"csrf_token": {reuseCSRF},
		"command_id": {reuseCommandID},
		"handle":     {"pat"},
		"credential": {code},
		"password":   {"another recovered horse battery staple"},
	})
	if rateLimited.status != http.StatusTooManyRequests {
		t.Fatalf("recovery abuse limit = %d %q", rateLimited.status, rateLimited.body)
	}

	page = getFrontendPage(t, administrator, server.address, administrationPath)
	issued := postFrontendForm(t, administrator, server.address, administrationPath, url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"action":     {"issue-recovery-token"},
		"command_id": {"issue-browser-recovery-token"},
		"account_id": {"2"},
		"reason":     {"verified government ID in person"},
	})
	tokenMatch := recoveryTokenOutput.FindStringSubmatch(issued.body)
	if issued.status != http.StatusOK || len(tokenMatch) != 2 {
		t.Fatalf("issue Administrator Recovery Token = %d %q", issued.status, issued.body)
	}
	token := tokenMatch[1]
	if root := getFrontendPage(
		t,
		recoveryClient,
		server.address,
		"/",
	); !strings.Contains(root.body, "Sign in") {
		t.Fatalf("Administrator recovery retained old session: %q", root.body)
	}
	page = getFrontendPage(t, administrator, server.address, administrationPath)
	if !strings.Contains(page.body, "IssueAccountRecoveryToken") ||
		!strings.Contains(page.body, "verified government ID in person") ||
		strings.Contains(page.body, token) {
		t.Fatalf("Administrator recovery Audit Entry = %q", page.body)
	}
	server.stop(t)
}

func TestBrowserBuildsPrivateMyScheduleFromFavoriteSessions(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	sessionPath := "/events/beamconf-2099/schedule/sessions/" + strconv.FormatInt(sessionID, 10)
	favoritePath := "/schedule/sessions/" + strconv.FormatInt(sessionID, 10) + "/favorite"

	anonymous := authenticatedClient(t)
	anonymous.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	page := getFrontendPage(t, anonymous, server.publicAddress, sessionPath)
	if page.status != http.StatusOK ||
		!strings.Contains(page.body, "/sign-in?return_to=%2Fevents%2Fbeamconf-2099%2Fschedule%2Fsessions%2F") {
		t.Fatalf("anonymous Favorite invitation = %d %q", page.status, page.body)
	}

	const (
		accountName = "Favorite Attendee"
		password    = "favorite correct horse battery staple"
	)
	assertJSONRequest(
		t,
		administrator,
		server.address,
		"/admin/accounts",
		map[string]string{
			"name": accountName, "password": password,
			"command_id": "create-favorite-attendee",
		},
		http.StatusCreated,
		"{\"id\":2,\"name\":\"Favorite Attendee\",\"administrator\":false}\n",
	)

	signInPage := getFrontendPage(
		t,
		anonymous,
		server.address,
		"/sign-in?return_to="+url.QueryEscape(sessionPath),
	)
	signIn := postFrontendForm(t, anonymous, server.address, "/sign-in", url.Values{
		"csrf_token": {requireFrontendCSRF(t, signInPage)},
		"handle":     {accountName},
		"password":   {password},
		"return_to":  {sessionPath},
	})
	if signIn.status != http.StatusSeeOther ||
		signIn.header.Get("Location") != sessionPath {
		t.Fatalf("Favorite sign-in return = %d Location %q", signIn.status, signIn.header.Get("Location"))
	}

	page = getFrontendPage(t, anonymous, server.address, sessionPath)
	if page.header.Get("Cache-Control") != "private, no-store" ||
		page.header.Get("ETag") != "" {
		t.Fatalf(
			"private Schedule cache headers = Cache-Control %q ETag %q",
			page.header.Get("Cache-Control"),
			page.header.Get("ETag"),
		)
	}
	if !strings.Contains(page.body, frontendui.HTMXPath) {
		t.Fatalf("Favorite Session page lacks htmx: %q", page.body)
	}
	add := postFrontendForm(
		t,
		anonymous,
		server.address,
		favoritePath,
		url.Values{
			"csrf_token": {requireFrontendCSRF(t, page)},
			"favorite":   {"true"},
			"return_to":  {sessionPath},
		},
	)
	if add.status != http.StatusSeeOther || add.header.Get("Location") != sessionPath {
		t.Fatalf("no-JavaScript Favorite add = %d Location %q", add.status, add.header.Get("Location"))
	}

	page = getFrontendPage(t, anonymous, server.address, sessionPath)
	removeValues := url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"favorite":   {"false"},
		"return_to":  {sessionPath},
	}
	removeRequest, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"http://"+server.address+favoritePath,
		strings.NewReader(removeValues.Encode()),
	)
	if err != nil {
		t.Fatalf("create Favorite removal request: %v", err)
	}
	removeRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	removeRequest.Header.Set("HX-Request", "true")
	removedLive := doFrontendRequest(t, anonymous, removeRequest)
	if removedLive.status != http.StatusOK ||
		removedLive.header.Get("HX-Refresh") != "" ||
		!strings.Contains(removedLive.body, "Add to My Schedule") {
		t.Fatalf(
			"live Favorite removal = %d HX-Refresh %q body %q",
			removedLive.status,
			removedLive.header.Get("HX-Refresh"),
			removedLive.body,
		)
	}

	page = getFrontendPage(t, anonymous, server.address, sessionPath)
	add = postFrontendForm(
		t,
		anonymous,
		server.address,
		favoritePath,
		url.Values{
			"csrf_token": {requireFrontendCSRF(t, page)},
			"favorite":   {"true"},
			"return_to":  {sessionPath},
		},
	)
	if add.status != http.StatusSeeOther || add.header.Get("Location") != sessionPath {
		t.Fatalf("repeated no-JavaScript Favorite add = %d Location %q", add.status, add.header.Get("Location"))
	}

	page = getFrontendPage(t, anonymous, server.address, "/my-schedule")
	if page.status != http.StatusOK ||
		!strings.Contains(page.body, "Opening Keynote") ||
		!strings.Contains(page.body, "Remove from My Schedule") {
		t.Fatalf("My Schedule after Favorite = %d %q", page.status, page.body)
	}
	schedulePage := getFrontendPage(t, anonymous, server.address, "/events/beamconf-2099/schedule")
	if schedulePage.status != http.StatusOK ||
		!strings.Contains(schedulePage.body, "Opening Keynote") ||
		!strings.Contains(schedulePage.body, "Remove from My Schedule") {
		t.Fatalf("Schedule after Favorite = %d %q", schedulePage.status, schedulePage.body)
	}
	otherAccount := getFrontendPage(t, administrator, server.address, "/my-schedule")
	if otherAccount.status != http.StatusOK ||
		strings.Contains(otherAccount.body, "Opening Keynote") ||
		strings.Contains(otherAccount.body, accountName) {
		t.Fatalf("other Account inspected Favorites = %d %q", otherAccount.status, otherAccount.body)
	}
	public := getFrontendPage(
		t,
		authenticatedClient(t),
		server.publicAddress,
		"/events/beamconf-2099/schedule",
	)
	if !strings.HasPrefix(public.header.Get("Cache-Control"), "public,") ||
		public.header.Get("ETag") == "" {
		t.Fatalf(
			"public Schedule cache headers = Cache-Control %q ETag %q",
			public.header.Get("Cache-Control"),
			public.header.Get("ETag"),
		)
	}
	for _, private := range []string{accountName, "Favorite count", "endorsement", "notification"} {
		if strings.Contains(public.body, private) {
			t.Fatalf("public Schedule disclosed %q: %q", private, public.body)
		}
	}

	privateSession := postFrontendForm(
		t,
		anonymous,
		server.address,
		"/schedule/sessions/2/favorite",
		url.Values{
			"csrf_token": {requireFrontendCSRF(t, page)},
			"favorite":   {"true"},
			"return_to":  {"/events/beamconf-2099/schedule/sessions/2"},
		},
	)
	if privateSession.status != http.StatusNotFound {
		t.Fatalf("Crew Only Favorite = %d %q", privateSession.status, privateSession.body)
	}

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	server = startBeamersWithPublicListener(t, bin, dataDir)
	page = getFrontendPage(t, anonymous, server.address, "/my-schedule")
	if page.status != http.StatusOK || !strings.Contains(page.body, "Opening Keynote") {
		t.Fatalf("restarted My Schedule = %d %q", page.status, page.body)
	}

	removed := postFrontendForm(
		t,
		anonymous,
		server.address,
		favoritePath,
		url.Values{
			"csrf_token": {requireFrontendCSRF(t, page)},
			"favorite":   {"false"},
			"return_to":  {"/my-schedule"},
		},
	)
	if removed.status != http.StatusSeeOther ||
		removed.header.Get("Location") != "/my-schedule" {
		t.Fatalf("no-JavaScript Favorite removal = %d Location %q", removed.status, removed.header.Get("Location"))
	}
	page = getFrontendPage(t, anonymous, server.address, "/my-schedule")
	if page.status != http.StatusOK || strings.Contains(page.body, "Opening Keynote") {
		t.Fatalf("My Schedule after removal = %d %q", page.status, page.body)
	}

	for _, returnTo := range []string{"https://example.invalid/stolen", `/\evil.example`} {
		freshClient := authenticatedClient(t)
		freshClient.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
		signInPage = getFrontendPage(
			t,
			freshClient,
			server.address,
			"/sign-in?return_to="+url.QueryEscape(returnTo),
		)
		signIn = postFrontendForm(t, freshClient, server.address, "/sign-in", url.Values{
			"csrf_token": {requireFrontendCSRF(t, signInPage)},
			"handle":     {accountName},
			"password":   {password},
			"return_to":  {returnTo},
		})
		if signIn.status != http.StatusSeeOther || signIn.header.Get("Location") != "/" {
			t.Fatalf(
				"external sign-in return %q = %d Location %q",
				returnTo,
				signIn.status,
				signIn.header.Get("Location"),
			)
		}
	}
	server.stop(t)
}

func TestBackstageNavigationReflectsAuthorityAndInterface(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	assertJSONRequest(
		t, administrator, server.address, "/admin/events",
		validEventInput(), http.StatusCreated,
		"{\"id\":1,\"name\":\"Revision 2026\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	secondEvent := validEventInput()
	secondEvent["name"] = "Revision 2027"
	secondEvent["command_id"] = "create-event-2"
	assertJSONRequest(
		t, administrator, server.address, "/admin/events",
		secondEvent, http.StatusCreated,
		"{\"id\":2,\"name\":\"Revision 2027\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	const password = "backstage correct horse battery staple"
	for index, name := range []string{
		"Pat Producer",
		"Opal Operator",
		"Olive Observer",
		"Alex Attendee",
		"Casey Capability Operator",
	} {
		assertJSONRequest(
			t, administrator, server.address, "/admin/accounts",
			map[string]string{
				"name": name, "password": password,
				"command_id": "create-backstage-account-" + strconv.Itoa(index+2),
			},
			http.StatusCreated,
			"{\"id\":"+strconv.Itoa(index+2)+",\"name\":\""+name+"\",\"administrator\":false}\n",
		)
	}
	for _, grant := range []struct {
		eventID  int
		account  int
		role     string
		command  string
		extra    map[string]any
		response string
	}{
		{1, 1, "Producer", "grant-admin-producer", nil,
			"{\"event_id\":1,\"account_id\":1,\"role\":\"Producer\"}\n"},
		{2, 1, "Observer", "grant-admin-observer", nil,
			"{\"event_id\":2,\"account_id\":1,\"role\":\"Observer\"}\n"},
		{1, 2, "Producer", "grant-pat-producer", nil,
			"{\"event_id\":1,\"account_id\":2,\"role\":\"Producer\"}\n"},
		{1, 3, "Operator", "grant-opal-operator", map[string]any{
			"display_group_keys": []string{"stage"},
			"capabilities":       []string{"EmergencyAlert", "ViewResults"},
		}, "{\"event_id\":1,\"account_id\":3,\"role\":\"Operator\",\"display_group_keys\":[\"stage\"],\"capabilities\":[\"EmergencyAlert\",\"ViewResults\"]}\n"},
		{1, 4, "Observer", "grant-olive-observer", nil,
			"{\"event_id\":1,\"account_id\":4,\"role\":\"Observer\"}\n"},
		{1, 6, "Operator", "grant-casey-operator", map[string]any{
			"capabilities": []string{"EmergencyAlert"},
		}, "{\"event_id\":1,\"account_id\":6,\"role\":\"Operator\",\"capabilities\":[\"EmergencyAlert\"]}\n"},
	} {
		input := map[string]any{
			"account_id": grant.account,
			"role":       grant.role,
			"command_id": grant.command,
		}
		maps.Copy(input, grant.extra)
		assertJSONRequest(
			t,
			administrator,
			server.address,
			"/admin/events/"+strconv.Itoa(grant.eventID)+"/grants",
			input,
			http.StatusCreated,
			grant.response,
		)
	}

	assertBackstage := func(
		name string,
		want []string,
		absent []string,
	) *http.Client {
		t.Helper()
		client := authenticatedClient(t)
		client.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
		assertJSONRequest(
			t, client, server.address, "/auth/sign-in",
			map[string]string{"name": name, "password": password},
			http.StatusNoContent, "",
		)
		root := getFrontendPage(t, client, server.address, "/")
		if !strings.Contains(root.body, `href="/backstage"`) {
			t.Fatalf("%s root has no Backstage link: %q", name, root.body)
		}
		page := getFrontendPage(t, client, server.address, "/backstage")
		if page.status != http.StatusOK {
			t.Fatalf("%s Backstage = %d %q", name, page.status, page.body)
		}
		navigation := frontendBackstageNavigation(t, page)
		for _, text := range want {
			if !strings.Contains(navigation, text) {
				t.Errorf("%s Backstage lacks %q", name, text)
			}
		}
		for _, text := range absent {
			if strings.Contains(navigation, text) {
				t.Errorf("%s Backstage unexpectedly contains %q", name, text)
			}
		}
		return client
	}

	adminPage := getFrontendPage(t, administrator, server.address, "/backstage")
	adminNavigation := frontendBackstageNavigation(t, adminPage)
	for _, text := range []string{
		"Installation",
		"Revision 2026",
		"Producer",
		"Revision 2027",
		"Observer",
	} {
		if adminPage.status != http.StatusOK || !strings.Contains(adminNavigation, text) {
			t.Errorf("Administrator Backstage lacks %q: %d %q", text, adminPage.status, adminPage.body)
		}
	}
	producer := assertBackstage(
		"Pat Producer",
		[]string{
			"Event Displays",
			"Sessions and Competitions",
			"Plan and publish",
			"Results and Prizegiving",
		},
		[]string{"Installation"},
	)
	operator := assertBackstage(
		"Opal Operator",
		[]string{
			"Sessions and Competitions",
			"Live operations",
			"Program Output and Overrides",
			"Emergency Alerts",
			"Results and Prizegiving",
		},
		[]string{"Event Displays", "Plan and publish", "Installation"},
	)
	capabilityOperator := assertBackstage(
		"Casey Capability Operator",
		[]string{
			"Live operations",
			"Program Output and Overrides",
			"Emergency Alerts",
			"No Lane or Display Group scope assigned.",
		},
		[]string{
			`href="/backstage/events/1/operations"`,
			`href="/backstage/events/1/control"`,
			`href="/backstage/events/1/control/emergency-alerts`,
		},
	)
	if page := getFrontendPage(
		t,
		capabilityOperator,
		server.address,
		"/backstage/events/1/control",
	); page.status != http.StatusOK ||
		!strings.Contains(
			page.body,
			"No Lane or Display Group scope assigned. Program Output and Overrides are unavailable.",
		) ||
		strings.Contains(page.body, `name="action" value="preview-stage-message"`) {
		t.Errorf("capability-only Operator control = %d %q", page.status, page.body)
	}
	for _, path := range []string{
		"/backstage/events/1/operations",
		"/backstage/events/1/control",
		"/backstage/events/1/control/emergency-alerts",
	} {
		page := getFrontendPage(t, operator, server.address, path)
		if page.status != http.StatusOK {
			t.Errorf("display-scoped Operator %s = %d, want 200: %q", path, page.status, page.body)
		}
	}
	observer := assertBackstage(
		"Olive Observer",
		[]string{"Event overview", "Sessions and Competitions"},
		[]string{"Event Displays", "Live operations", "Results and Prizegiving", "Installation"},
	)
	for role, client := range map[string]*http.Client{
		"Producer": producer,
		"Operator": operator,
		"Observer": observer,
	} {
		if overview := getFrontendPage(
			t, client, server.address, "/backstage/events/1",
		); overview.status != http.StatusOK {
			t.Errorf("%s Event overview = %d, want 200", role, overview.status)
		}
		sessions := getFrontendPage(
			t,
			client,
			server.address,
			"/backstage/events/1/sessions",
		)
		if sessions.status != http.StatusOK {
			t.Errorf("%s Sessions and Competitions = %d, want 200", role, sessions.status)
		}
		if !strings.Contains(
			sessions.body,
			"No Sessions are available in your Event scope.",
		) {
			t.Errorf("%s empty Sessions state lacks explanation", role)
		}
		settings := getFrontendPage(t, client, server.address, "/backstage/events/1/settings")
		wantStatus := http.StatusNotFound
		if role == "Producer" {
			wantStatus = http.StatusOK
		}
		if settings.status != wantStatus {
			t.Errorf("%s Event settings = %d, want %d", role, settings.status, wantStatus)
		}
		displaySettingsPath := "/backstage/events/1/display-settings"
		displaySettings := getFrontendPage(t, client, server.address, displaySettingsPath)
		if displaySettings.status != wantStatus {
			t.Errorf(
				"%s Event Display settings = %d, want %d",
				role,
				displaySettings.status,
				wantStatus,
			)
		}
		if role != "Producer" {
			submitted := postFrontendForm(
				t,
				client,
				server.address,
				displaySettingsPath,
				url.Values{},
			)
			if submitted.status != http.StatusNotFound {
				t.Errorf(
					"%s Event Display settings submit = %d, want 404",
					role,
					submitted.status,
				)
			}
		}
	}
	if overview := getFrontendPage(
		t, administrator, server.address, "/backstage/events/2",
	); overview.status != http.StatusOK {
		t.Errorf("Administrator Observer Event overview = %d, want 200", overview.status)
	}
	if settings := getFrontendPage(
		t, administrator, server.address, "/backstage/events/2/settings",
	); settings.status != http.StatusNotFound {
		t.Errorf("Administrator Observer Event settings = %d, want 404", settings.status)
	}
	if settings := getFrontendPage(
		t, administrator, server.address, "/backstage/events/2/display-settings",
	); settings.status != http.StatusNotFound {
		t.Errorf("Administrator Observer Event Display settings = %d, want 404", settings.status)
	}
	if forbidden := getFrontendPage(
		t,
		producer,
		server.address,
		"/admin/registration",
	); forbidden.status != http.StatusForbidden {
		t.Fatalf("Producer direct administration = %d, want 403", forbidden.status)
	}
	attendee := authenticatedClient(t)
	attendee.CheckRedirect = producer.CheckRedirect
	assertJSONRequest(
		t, attendee, server.address, "/auth/sign-in",
		map[string]string{"name": "Alex Attendee", "password": password},
		http.StatusNoContent, "",
	)
	if root := getFrontendPage(t, attendee, server.address, "/"); strings.Contains(root.body, `href="/backstage"`) {
		t.Fatalf("attendee root exposes Backstage: %q", root.body)
	}
	if page := getFrontendPage(t, attendee, server.address, "/backstage"); page.status != http.StatusNotFound {
		t.Fatalf("attendee Backstage = %d, want 404", page.status)
	}
	if public := getFrontendPage(
		t,
		authenticatedClient(t),
		server.publicAddress,
		"/backstage",
	); public.status != http.StatusNotFound {
		t.Fatalf("public-listener Backstage = %d, want 404", public.status)
	}
	if frontend := getFrontendPage(
		t,
		administrator,
		server.publicAddress,
		"/",
	); frontend.status != http.StatusOK {
		t.Fatalf("public-listener Frontend = %d, want 200", frontend.status)
	} else if strings.Contains(frontend.body, `href="/backstage"`) {
		t.Fatalf("public-listener Frontend advertises private Backstage: %q", frontend.body)
	}
	server.stop(t)
}

func TestBackstageExportsFinalFiles(t *testing.T) {
	fixture := prepareReleasedEntryAttachments(t)
	administrator, server := fixture.administrator, fixture.server
	const path = "/backstage/events/1/final-files"

	backstage := getFrontendPage(t, administrator, server.address, "/backstage")
	if !strings.Contains(frontendBackstageNavigation(t, backstage), "Final Files Export") {
		t.Fatalf("Administrator Backstage lacks Final Files Export: %q", backstage.body)
	}
	preview := getFrontendPage(t, administrator, server.address, path)
	if preview.status != http.StatusOK {
		t.Fatalf("Final Files Export preview = %d %q", preview.status, preview.body)
	}
	for _, want := range []string{
		"Final Files Export",
		"Files",
		"Total size",
		"Collisions",
		"Demo Competition",
		"Release Project",
		"public.txt",
	} {
		if !strings.Contains(preview.body, want) {
			t.Fatalf("Final Files Export preview lacks %q: %q", want, preview.body)
		}
	}
	if strings.Contains(preview.body, `name="output"`) ||
		strings.Contains(preview.body, server.dataDir) {
		t.Fatalf("Final Files Export exposes a server destination: %q", preview.body)
	}
	digest := frontendNamedValues(preview.body, "preview_digest").Get("preview_digest")
	if digest == "" {
		t.Fatalf("Final Files Export preview lacks digest: %q", preview.body)
	}
	unconfirmed := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":     {requireFrontendCSRF(t, preview)},
		"preview_digest": {digest},
	})
	if unconfirmed.status != http.StatusBadRequest {
		t.Fatalf("unconfirmed Final Files Export = %d, want 400", unconfirmed.status)
	}
	assertAccessibleFormErrors(t, unconfirmed, map[string]string{
		"final-files-confirmed": "Confirm the ZIP download",
	})
	stale := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":     {requireFrontendCSRF(t, unconfirmed)},
		"preview_digest": {"obsolete-preview"},
		"confirmed":      {"true"},
	})
	if stale.status != http.StatusConflict {
		t.Fatalf("stale Final Files Export = %d, want 409", stale.status)
	}
	assertAccessibleFormErrors(t, stale, map[string]string{
		"final-files-confirmed": "Review the current files",
	})
	download := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":     {requireFrontendCSRF(t, preview)},
		"preview_digest": {digest},
		"confirmed":      {"true"},
	})
	if download.status != http.StatusOK ||
		download.header.Get("Content-Type") != "application/zip" ||
		download.header.Get("Content-Disposition") !=
			`attachment; filename="beamers-final-files.zip"` {
		t.Fatalf(
			"Final Files Export download = %d %q %q: %q",
			download.status,
			download.header.Get("Content-Type"),
			download.header.Get("Content-Disposition"),
			download.body,
		)
	}
	archive, err := zip.NewReader(
		bytes.NewReader([]byte(download.body)),
		int64(len(download.body)),
	)
	if err != nil {
		t.Fatalf("open Final Files Export download: %v", err)
	}
	if len(archive.File) != 2 ||
		archive.File[1].Name != "manifest.json" ||
		!strings.Contains(preview.body, archive.File[0].Name) {
		t.Fatalf("Final Files Export entries = %+v, preview %q", archive.File, preview.body)
	}
	file, err := archive.File[0].Open()
	if err != nil {
		t.Fatalf("open Final Files Export file: %v", err)
	}
	content, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if err = errors.Join(readErr, closeErr); err != nil || string(content) != "public release" {
		t.Fatalf("Final Files Export bytes = %q, %v", content, err)
	}
	manifest, err := archive.File[1].Open()
	if err != nil {
		t.Fatalf("open Final Files Export manifest: %v", err)
	}
	content, readErr = io.ReadAll(manifest)
	closeErr = manifest.Close()
	if err = errors.Join(readErr, closeErr); err != nil ||
		!bytes.Contains(content, []byte(`"original_filename":"public.txt"`)) ||
		bytes.Contains(content, []byte(`"original_filename":"crew.txt"`)) {
		t.Fatalf("Final Files Export manifest = %q, %v", content, err)
	}

	_, err = fixture.competitionClient.SetEntryAttachmentReadiness(
		t.Context(),
		connect.NewRequest(&competitionv1.SetEntryAttachmentReadinessRequest{
			EventId: 1, SessionId: fixture.competitionID, EntryId: fixture.entryID,
			AttachmentVersionId: int64(fixture.publicVersion.ID),
			CommandId:           "change-final-files-preview",
			ExpectedRevision:    int64(fixture.publicVersion.ReadinessRevision + 1),
		}),
	)
	if err != nil {
		t.Fatalf("change Final Files Export canonical state: %v", err)
	}
	conflict := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":     {requireFrontendCSRF(t, preview)},
		"preview_digest": {digest},
		"confirmed":      {"true"},
	})
	freshDigest := frontendNamedValues(conflict.body, "preview_digest").Get("preview_digest")
	if conflict.status != http.StatusConflict ||
		!strings.Contains(conflict.body, "Preview changed") ||
		!strings.Contains(conflict.body, `id="error-summary"`) ||
		freshDigest == "" ||
		freshDigest == digest ||
		strings.Contains(conflict.body, `name="confirmed" value="true" checked`) {
		t.Fatalf("stale Final Files Export = %d %q", conflict.status, conflict.body)
	}

	operator := provisionOperatorWithLanes(t, administrator, server, nil)
	if navigation := frontendBackstageNavigation(
		t,
		getFrontendPage(t, operator, server.address, "/backstage"),
	); strings.Contains(navigation, "Final Files Export") {
		t.Fatalf("Operator Backstage exposes Final Files Export: %q", navigation)
	}
	if denied := getFrontendPage(t, operator, server.address, path); denied.status != http.StatusNotFound {
		t.Fatalf("Operator Final Files Export = %d, want 404", denied.status)
	}
	if denied := postFrontendForm(
		t,
		operator,
		server.address,
		path,
		url.Values{},
	); denied.status != http.StatusNotFound {
		t.Fatalf("Operator Final Files Export submit = %d, want 404", denied.status)
	}
	if public := getFrontendPage(
		t,
		authenticatedClient(t),
		server.publicAddress,
		path,
	); public.status != http.StatusNotFound {
		t.Fatalf("public-listener Final Files Export = %d, want 404", public.status)
	}
	server.stop(t)
}

func TestBrowserEventOverviewAndSettings(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	assertJSONRequest(
		t, administrator, server.address, "/admin/events",
		validEventInput(), http.StatusCreated,
		"{\"id\":1,\"name\":\"Revision 2026\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/events/1/grants",
		map[string]any{
			"account_id": 1,
			"role":       "Producer",
			"command_id": "grant-overview-producer",
		},
		http.StatusCreated,
		"{\"event_id\":1,\"account_id\":1,\"role\":\"Producer\"}\n",
	)

	backstage := getFrontendPage(t, administrator, server.address, "/backstage")
	overviewPath := frontendLinkPath(t, backstage, "Event overview")
	overview := getFrontendPage(t, administrator, server.address, overviewPath)
	for _, want := range []string{
		"Revision 2026",
		"Not listed",
		"Not active",
		"No Rundown published",
		"On Ended",
		"Event settings",
		"Plan and publish",
	} {
		if overview.status != http.StatusOK || !strings.Contains(overview.body, want) {
			t.Fatalf("Event overview lacks %q: %d %q", want, overview.status, overview.body)
		}
	}
	if strings.Contains(overview.body, ">Attachment release</a>") {
		t.Fatalf("Event overview links unavailable release controls: %q", overview.body)
	}

	settingsPath := frontendLinkPath(t, overview, "Event settings")
	settings := getFrontendPage(t, administrator, server.address, settingsPath)
	invalid := postFrontendForm(t, administrator, server.address, settingsPath, url.Values{
		"csrf_token":                        {requireFrontendCSRF(t, settings)},
		"command_id":                        {"browser-invalid-event-settings"},
		"expected_event_revision":           {"1"},
		"event_name":                        {""},
		"planned_start_date":                {"2026-08-21"},
		"planned_end_date":                  {"2026-08-23"},
		"timezone":                          {"Europe/Berlin"},
		"event_locale":                      {"de-DE"},
		"content_language":                  {"fr"},
		"event_day_boundary":                {"06:00"},
		"entry_default_disposition":         {"Included"},
		"submission_eligibility":            {"AllAccounts"},
		"voting_method":                     {"Range1To5"},
		"self_vote_policy":                  {"Allowed"},
		"target_adjustment_presets_seconds": {"-300,300,600"},
	})
	if invalid.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalid.body, `name="content_language" value="fr"`) {
		t.Fatalf("invalid Event settings = %d %q", invalid.status, invalid.body)
	}
	assertAccessibleFormErrors(t, invalid, map[string]string{
		"event-name": "must be 1 to 200 characters without control characters",
	})

	configured := postFrontendForm(t, administrator, server.address, settingsPath, url.Values{
		"csrf_token":                        {requireFrontendCSRF(t, invalid)},
		"command_id":                        {"browser-update-event-settings"},
		"expected_event_revision":           {"1"},
		"event_name":                        {"Revision Browser"},
		"public":                            {"true"},
		"public_slug":                       {"revision-browser"},
		"planned_start_date":                {"2026-08-21"},
		"planned_end_date":                  {"2026-08-23"},
		"timezone":                          {"Europe/Berlin"},
		"event_locale":                      {"de-DE"},
		"content_language":                  {"en-GB"},
		"event_day_boundary":                {"06:00"},
		"entry_default_disposition":         {"Included"},
		"submission_eligibility":            {"AllAccounts"},
		"voting_method":                     {"Range1To5"},
		"self_vote_policy":                  {"Allowed"},
		"target_adjustment_presets_seconds": {"-300,300,600"},
	})
	if configured.status != http.StatusSeeOther ||
		configured.header.Get("Location") != settingsPath {
		t.Fatalf("Event settings update = %d Location %q %q", configured.status, configured.header.Get("Location"), configured.body)
	}
	settings = getFrontendPage(t, administrator, server.address, settingsPath)
	if !strings.Contains(settings.body, "Revision Browser") ||
		!strings.Contains(frontendBackstageNavigation(t, settings), "Revision Browser") {
		t.Fatalf("updated Event settings = %d %q", settings.status, settings.body)
	}

	stale := postFrontendForm(t, administrator, server.address, settingsPath, url.Values{
		"csrf_token":                        {requireFrontendCSRF(t, settings)},
		"command_id":                        {"browser-stale-event-settings"},
		"expected_event_revision":           {"1"},
		"event_name":                        {"Stale Event"},
		"public":                            {"true"},
		"public_slug":                       {"stale-event"},
		"planned_start_date":                {"2026-08-21"},
		"planned_end_date":                  {"2026-08-23"},
		"timezone":                          {"Europe/Berlin"},
		"event_locale":                      {"de-DE"},
		"content_language":                  {"en-GB"},
		"event_day_boundary":                {"06:00"},
		"entry_default_disposition":         {"Included"},
		"submission_eligibility":            {"AllAccounts"},
		"voting_method":                     {"Range1To5"},
		"self_vote_policy":                  {"Allowed"},
		"target_adjustment_presets_seconds": {"-300,300,600"},
	})
	if stale.status != http.StatusConflict ||
		!strings.Contains(stale.body, "Event changed") ||
		!strings.Contains(stale.body, `value="Stale Event"`) {
		t.Fatalf("stale Event settings = %d %q", stale.status, stale.body)
	}
	assertAccessibleFormErrors(t, stale, nil)

	planning := getFrontendPage(
		t,
		administrator,
		server.address,
		"/backstage/events/1/planning",
	)
	if planning.status != http.StatusOK ||
		strings.Contains(planning.body, `name="event_name"`) {
		t.Fatalf("Planning retained general Event settings: %d %q", planning.status, planning.body)
	}
	for _, path := range []string{overviewPath, settingsPath} {
		if public := getFrontendPage(
			t, administrator, server.publicAddress, path,
		); public.status != http.StatusNotFound {
			t.Fatalf("public-listener %s = %d, want 404", path, public.status)
		}
	}
	server.stop(t)
}

func TestBrowserControlsEventAttachmentRelease(t *testing.T) {
	fixture := prepareReleasedEntryAttachments(t)
	administrator, server := fixture.administrator, fixture.server
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	rundownClient := rundownv1connect.NewRundownServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	rundown, err := rundownClient.GetCrewRundown(t.Context(), connect.NewRequest(
		&rundownv1.GetCrewRundownRequest{EventId: 1},
	))
	if err != nil {
		t.Fatalf("load release cue Ceremony: %v", err)
	}
	var ceremonyID int64
	for _, session := range rundown.Msg.GetSessions() {
		if session.GetType() == rundownv1.SessionType_SESSION_TYPE_CEREMONY {
			ceremonyID = session.GetId()
			break
		}
	}
	if ceremonyID == 0 {
		t.Fatal("release cue Ceremony not found")
	}

	const settingsPath = "/backstage/events/1/settings"
	settings := getFrontendPage(t, administrator, server.address, settingsPath)
	for _, want := range []string{"Attachment release", "On Live", "Closing Session"} {
		if settings.status != http.StatusOK || !strings.Contains(settings.body, want) {
			t.Fatalf("Event release settings lack %q: %d %q", want, settings.status, settings.body)
		}
	}
	invalid := postFrontendForm(t, administrator, server.address, settingsPath, url.Values{
		"csrf_token":                {requireFrontendCSRF(t, settings)},
		"action":                    {"configure-attachment-release"},
		"command_id":                {"browser-invalid-event-release"},
		"expected_release_revision": {"1"},
		"release_policy":            {"Later"},
		"cue_session_id":            {strconv.FormatInt(ceremonyID, 10)},
	})
	if invalid.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalid.body, `value="`+strconv.FormatInt(ceremonyID, 10)+`" selected`) {
		t.Fatalf("invalid Event Attachment release = %d %q", invalid.status, invalid.body)
	}
	assertAccessibleFormErrors(t, invalid, map[string]string{
		"release-policy": "Check the Attachment release settings and confirmation.",
	})

	configured := postFrontendForm(t, administrator, server.address, settingsPath, url.Values{
		"csrf_token":                {requireFrontendCSRF(t, settings)},
		"action":                    {"configure-attachment-release"},
		"command_id":                {"browser-configure-event-release"},
		"expected_release_revision": {"1"},
		"release_policy":            {"OnEventReleaseCue"},
		"cue_session_id":            {strconv.FormatInt(ceremonyID, 10)},
	})
	if configured.status != http.StatusSeeOther {
		t.Fatalf("configure Event Attachment release = %d %q", configured.status, configured.body)
	}
	assertPublicAttachmentOnListeners(
		t, server, fixture.publicVersion.ID,
		http.StatusNotFound, "Attachment Version not found\n",
	)

	preview := getFrontendPage(t, administrator, server.address, settingsPath)
	for _, want := range []string{
		"On Event Release Cue",
		"Cue not fired",
		"Eligible Final Versions",
		">1</dd>",
		"Held Final Versions",
		">0</dd>",
		"Blocked Final Versions",
	} {
		if preview.status != http.StatusOK || !strings.Contains(preview.body, want) {
			t.Fatalf("Event release preview lacks %q: %d %q", want, preview.status, preview.body)
		}
	}
	if strings.Contains(preview.body, "Competition override") {
		t.Fatalf("Event settings expose Competition override editing: %q", preview.body)
	}
	releaseValues := frontendNamedValues(
		preview.body,
		"expected_release_revision",
		"preview_fingerprint",
	)
	staleFingerprint := releaseValues.Get("preview_fingerprint")
	held := requestJSONMethod(
		t.Context(), http.MethodPatch, administrator, server.address,
		"/crew/events/1/attachment-versions/"+
			strconv.Itoa(fixture.publicVersion.ID)+"/release",
		map[string]any{
			"expected_revision": fixture.publicVersion.ReleaseRevision,
			"hold":              true, "command_id": "hold-browser-release-preview",
		},
	)
	if held.status != http.StatusOK {
		t.Fatalf("hold previewed Attachment = %d %q", held.status, held.body)
	}
	stale := postFrontendForm(t, administrator, server.address, settingsPath, url.Values{
		"csrf_token":                {requireFrontendCSRF(t, preview)},
		"action":                    {"fire-attachment-release-cue"},
		"command_id":                {"browser-fire-stale-release-cue"},
		"expected_release_revision": {releaseValues.Get("expected_release_revision")},
		"preview_fingerprint":       {staleFingerprint},
		"confirmed":                 {"true"},
	})
	if stale.status != http.StatusConflict ||
		!strings.Contains(stale.body, "Release impact changed") {
		t.Fatalf("stale Event Release Cue = %d %q", stale.status, stale.body)
	}
	assertAccessibleFormErrors(t, stale, nil)
	if regexp.MustCompile(`<input[^>]+id="release-cue-confirmed"[^>]+checked`).MatchString(stale.body) {
		t.Fatalf("stale Event Release Cue retained confirmation: %q", stale.body)
	}
	assertPublicAttachmentOnListeners(
		t, server, fixture.publicVersion.ID,
		http.StatusNotFound, "Attachment Version not found\n",
	)

	lifted := requestJSONMethod(
		t.Context(), http.MethodPatch, administrator, server.address,
		"/crew/events/1/attachment-versions/"+
			strconv.Itoa(fixture.publicVersion.ID)+"/release",
		map[string]any{
			"expected_revision": fixture.publicVersion.ReleaseRevision + 1,
			"hold":              false, "command_id": "lift-browser-release-preview",
		},
	)
	if lifted.status != http.StatusOK {
		t.Fatalf("lift previewed Attachment hold = %d %q", lifted.status, lifted.body)
	}
	preview = getFrontendPage(t, administrator, server.address, settingsPath)
	releaseValues = frontendNamedValues(
		preview.body,
		"expected_release_revision",
		"preview_fingerprint",
	)
	unconfirmed := postFrontendForm(t, administrator, server.address, settingsPath, url.Values{
		"csrf_token":                {requireFrontendCSRF(t, preview)},
		"action":                    {"fire-attachment-release-cue"},
		"command_id":                {"browser-fire-unconfirmed-release-cue"},
		"expected_release_revision": {releaseValues.Get("expected_release_revision")},
		"preview_fingerprint":       {releaseValues.Get("preview_fingerprint")},
	})
	if unconfirmed.status != http.StatusUnprocessableEntity {
		t.Fatalf("unconfirmed Event Release Cue = %d %q", unconfirmed.status, unconfirmed.body)
	}
	assertAccessibleFormErrors(t, unconfirmed, map[string]string{
		"release-cue-confirmed": "Check the Attachment release settings and confirmation.",
	})
	fire := url.Values{
		"csrf_token":                {requireFrontendCSRF(t, preview)},
		"action":                    {"fire-attachment-release-cue"},
		"command_id":                {"browser-fire-release-cue"},
		"expected_release_revision": {releaseValues.Get("expected_release_revision")},
		"preview_fingerprint":       {releaseValues.Get("preview_fingerprint")},
		"confirmed":                 {"true"},
	}
	for attempt := range 2 {
		fired := postFrontendForm(t, administrator, server.address, settingsPath, fire)
		if fired.status != http.StatusSeeOther {
			t.Fatalf("fire Event Release Cue attempt %d = %d %q", attempt+1, fired.status, fired.body)
		}
	}
	assertPublicAttachmentOnListeners(
		t, server, fixture.publicVersion.ID, http.StatusOK, "public release",
	)
	assertPublicAttachmentOnListeners(
		t, server, fixture.crewVersion.ID,
		http.StatusNotFound, "Attachment Version not found\n",
	)
	server.stop(t)
}

func TestBrowserConfiguresEventDisplays(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	sessionID := prepareActiveSchedule(t, administrator, server)
	displayClient := enrollAndAssignDisplay(
		t,
		administrator,
		server,
		"Browser configured timer",
		"stage-timer",
	)

	const path = "/backstage/events/1/display-settings"
	page := getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{
		"Event Display settings",
		"Rotation interval",
		"Default timer thresholds",
		"Session-type overrides",
		"Opening Keynote",
		"Event Theme",
	} {
		if page.status != http.StatusOK || !strings.Contains(page.body, want) {
			t.Fatalf("Event Display settings lack %q: %d %q", want, page.status, page.body)
		}
	}
	if strings.Contains(page.body, "session_timer_thresholds") ||
		strings.Contains(page.body, "session_type_timer_thresholds") {
		t.Fatalf("Event Display settings expose configuration maps: %q", page.body)
	}

	form := url.Values{
		"csrf_token":                                   {requireFrontendCSRF(t, page)},
		"command_id":                                   {"browser-configure-event-displays"},
		"expected_event_revision":                      {"2"},
		"rotation_seconds":                             {"30"},
		"reduced_effects":                              {"true"},
		"timer_threshold_seconds":                      {"600"},
		"timer_threshold_emphasis":                     {"attention"},
		"session_type.Presentation.threshold_override": {"true"},
		"session_type.Presentation.threshold_seconds":  {"180"},
		"session_type.Presentation.threshold_emphasis": {"attention"},
		"session." + strconv.FormatInt(sessionID, 10) + ".threshold_override": {
			"true",
		},
		"session." + strconv.FormatInt(sessionID, 10) + ".threshold_seconds": {
			"45",
		},
		"session." + strconv.FormatInt(sessionID, 10) + ".threshold_emphasis": {
			"urgent",
		},
	}
	saved := postFrontendForm(t, administrator, server.address, path, form)
	if saved.status != http.StatusSeeOther || saved.header.Get("Location") != path {
		t.Fatalf(
			"save Event Display settings = %d Location %q %q",
			saved.status,
			saved.header.Get("Location"),
			saved.body,
		)
	}

	sessionClient := sessionv1connect.NewSessionControlServiceClient(
		administrator,
		"http://"+server.address,
		connect.WithProtoJSON(),
	)
	if _, err := sessionClient.StartSession(t.Context(), connect.NewRequest(
		&sessionv1.StartSessionRequest{
			EventId: 1, SessionId: sessionID,
			CommandId:                 "start-browser-configured-timer",
			ExpectedLiveStateRevision: proto.Int64(0),
		},
	)); err != nil {
		t.Fatalf("start browser-configured Session: %v", err)
	}
	snapshot := requestJSON(
		t.Context(),
		displayClient,
		server.address,
		"/beamers.display.v1.DisplayService/GetSnapshot",
		map[string]any{},
	)
	if snapshot.status != http.StatusOK ||
		!strings.Contains(snapshot.body, `"rotationSeconds":30`) ||
		!strings.Contains(snapshot.body, `"remainingSeconds":"45"`) ||
		strings.Contains(snapshot.body, `"remainingSeconds":"180"`) ||
		strings.Contains(snapshot.body, `"remainingSeconds":"600"`) {
		t.Fatalf("resolved browser Display configuration = %d %q", snapshot.status, snapshot.body)
	}

	latest := getFrontendPage(t, administrator, server.address, path)
	invalid := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":               {requireFrontendCSRF(t, latest)},
		"command_id":               {"reject-browser-display-threshold"},
		"expected_event_revision":  {"3"},
		"rotation_seconds":         {"30"},
		"timer_threshold_seconds":  {"600"},
		"timer_threshold_emphasis": {"attention"},
		"session." + strconv.FormatInt(sessionID, 10) + ".threshold_override": {
			"true",
		},
		"session." + strconv.FormatInt(sessionID, 10) + ".threshold_seconds": {
			"0",
		},
		"session." + strconv.FormatInt(sessionID, 10) + ".threshold_emphasis": {
			"urgent",
		},
	})
	if invalid.status != http.StatusUnprocessableEntity ||
		!strings.Contains(
			invalid.body,
			"Opening Keynote (Presentation) Session timer threshold remaining seconds row 1",
		) ||
		!strings.Contains(invalid.body, `value="0"`) ||
		!strings.Contains(invalid.body, `aria-invalid="true"`) ||
		!strings.Contains(
			invalid.body,
			`<details open><summary>Individual Session overrides</summary>`,
		) {
		t.Fatalf("invalid Event Display settings = %d %q", invalid.status, invalid.body)
	}
	assertAccessibleFormErrors(t, invalid, map[string]string{
		"session." + strconv.FormatInt(sessionID, 10) + ".threshold-seconds-0": "must be an integer from 1 to 86400",
	})

	stale := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":               {requireFrontendCSRF(t, latest)},
		"command_id":               {"stale-browser-display-settings"},
		"expected_event_revision":  {"1"},
		"rotation_seconds":         {"31"},
		"timer_threshold_seconds":  {"600"},
		"timer_threshold_emphasis": {"attention"},
	})
	if stale.status != http.StatusConflict ||
		!strings.Contains(stale.body, "Event changed") ||
		!strings.Contains(stale.body, `value="31"`) {
		t.Fatalf("stale Event Display settings = %d %q", stale.status, stale.body)
	}
	assertAccessibleFormErrors(t, stale, nil)
	committed := requestJSONMethod(
		t.Context(),
		http.MethodGet,
		administrator,
		server.address,
		"/crew/events/1/display-configuration",
		nil,
	)
	if committed.status != http.StatusOK ||
		!strings.Contains(committed.body, `"rotation_seconds":30`) ||
		!strings.Contains(committed.body, `"reduced_effects":true`) ||
		!strings.Contains(committed.body, `"Presentation":[{"remaining_seconds":180`) ||
		!strings.Contains(committed.body, `"`+strconv.FormatInt(sessionID, 10)+`":[{"remaining_seconds":45`) ||
		strings.Contains(committed.body, `"rotation_seconds":31`) {
		t.Fatalf("committed Event Display settings after conflict = %d %q", committed.status, committed.body)
	}
	if public := getFrontendPage(
		t,
		administrator,
		server.publicAddress,
		path,
	); public.status != http.StatusNotFound {
		t.Fatalf("public-listener Event Display settings = %d, want 404", public.status)
	}

	server.stop(t)
	restarted := startBeamers(t, server.bin, server.dataDir)
	persisted := getFrontendPage(t, administrator, restarted.address, path)
	if persisted.status != http.StatusOK ||
		!strings.Contains(persisted.body, `name="rotation_seconds"`) ||
		!strings.Contains(persisted.body, `value="30"`) ||
		!strings.Contains(persisted.body, `value="45"`) {
		t.Fatalf("persisted Event Display settings = %d %q", persisted.status, persisted.body)
	}
	restarted.stop(t)
}

func TestVotingKeysIssueRedeemAndSurviveRestart(t *testing.T) {
	const unavailableMessage = "Voting Key unavailable. Check the Event and key."
	csrfToken := func(response frontendResponse) string {
		match := frontendCSRFInput.FindStringSubmatch(response.body)
		if len(match) != 2 {
			t.Fatalf("Voting page lacks CSRF proof: %d %q", response.status, response.body)
		}
		return match[1]
	}
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	assertJSONRequest(
		t, administrator, server.address, "/admin/events",
		validEventInput(), http.StatusCreated,
		"{\"id\":1,\"name\":\"Revision 2026\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	otherEvent := validEventInput()
	otherEvent["name"] = "Other Event"
	otherEvent["command_id"] = "create-voting-event-2"
	assertJSONRequest(
		t, administrator, server.address, "/admin/events",
		otherEvent, http.StatusCreated,
		"{\"id\":2,\"name\":\"Other Event\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/events/1/grants",
		map[string]any{
			"account_id": 1, "role": "Producer", "command_id": "grant-voting-producer",
		},
		http.StatusCreated,
		"{\"event_id\":1,\"account_id\":1,\"role\":\"Producer\"}\n",
	)
	const password = "voting correct horse battery staple"
	for id, name := range []string{"Alice Voter", "Bob Voter"} {
		assertJSONRequest(
			t, administrator, server.address, "/admin/accounts",
			map[string]string{
				"name": name, "password": password,
				"command_id": "create-voter-" + strconv.Itoa(id+2),
			},
			http.StatusCreated,
			"{\"id\":"+strconv.Itoa(id+2)+",\"name\":\""+name+"\",\"administrator\":false}\n",
		)
	}

	path := "/backstage/events/1/voting-keys"
	page := getFrontendPage(t, administrator, server.address, path)
	commands := frontendNamedValues(page.body, "command_id")
	if page.status != http.StatusOK || len(commands["command_id"]) != 1 {
		t.Fatalf("Voting Key page = %d %q", page.status, page.body)
	}
	location, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("load Voting Event timezone: %v", err)
	}
	issued := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {csrfToken(page)},
		"command_id": {commands.Get("command_id")},
		"action":     {"issue"},
		"count":      {"2"},
		"expires_at": {time.Now().Add(24 * time.Hour).In(location).Format("2006-01-02T15:04")},
	})
	matches := votingKeyOutput.FindAllStringSubmatch(issued.body, -1)
	if issued.status != http.StatusOK || len(matches) != 2 {
		t.Fatalf("issued Voting Keys = %d %q", issued.status, issued.body)
	}
	firstKey, secondKey := matches[0][1], matches[1][1]

	database, err := sql.Open("sqlite", filepath.Join(server.dataDir, "beamers.db"))
	if err != nil {
		t.Fatalf("open Voting database: %v", err)
	}
	var storedHash string
	if err = database.QueryRowContext(
		t.Context(), "SELECT token_hash FROM voting_keys WHERE id = 1",
	).Scan(&storedHash); err != nil {
		t.Fatalf("read protected Voting Key: %v", err)
	}
	if storedHash == firstKey || len(storedHash) != 64 {
		t.Fatalf("stored Voting Key = %q", storedHash)
	}
	if err = database.Close(); err != nil {
		t.Fatalf("close Voting database: %v", err)
	}
	for _, route := range []string{"/admin/audit", "/diagnostics"} {
		found := getFrontendPage(t, administrator, server.address, route)
		if strings.Contains(found.body, firstKey) || strings.Contains(found.body, secondKey) {
			t.Fatalf("%s leaked Voting Key: %q", route, found.body)
		}
	}

	signIn := func(name string) *http.Client {
		t.Helper()
		client := authenticatedClient(t)
		client.CheckRedirect = administrator.CheckRedirect
		assertJSONRequest(
			t, client, server.address, "/auth/sign-in",
			map[string]string{"name": name, "password": password},
			http.StatusNoContent, "",
		)
		return client
	}
	alice := signIn("Alice Voter")
	bob := signIn("Bob Voter")

	redeemPage := getFrontendPage(t, alice, server.address, "/voting")
	redeemValues := frontendNamedValues(redeemPage.body, "command_id")
	wrongEvent := postFrontendForm(t, alice, server.address, "/voting", url.Values{
		"csrf_token": {csrfToken(redeemPage)},
		"command_id": {redeemValues.Get("command_id")},
		"event_id":   {"2"},
		"voting_key": {firstKey},
	})
	if wrongEvent.status != http.StatusUnprocessableEntity ||
		!strings.Contains(wrongEvent.body, unavailableMessage) {
		t.Fatalf("wrong-Event Voting Key = %d %q", wrongEvent.status, wrongEvent.body)
	}
	assertAccessibleFormErrors(t, wrongEvent, map[string]string{
		"voting-event": unavailableMessage,
		"voting-key":   unavailableMessage,
	})
	if !strings.Contains(wrongEvent.body, `value="2"`) ||
		strings.Contains(wrongEvent.body, firstKey) {
		t.Fatalf("wrong-Event form values = %q", wrongEvent.body)
	}
	redeemValues = frontendNamedValues(wrongEvent.body, "command_id")
	successValues := url.Values{
		"csrf_token": {csrfToken(wrongEvent)},
		"command_id": {redeemValues.Get("command_id")},
		"event_id":   {"1"},
		"voting_key": {firstKey},
	}
	redeemed := postFrontendForm(t, alice, server.address, "/voting", successValues)
	if redeemed.status != http.StatusOK ||
		!strings.Contains(redeemed.body, "Voting Eligibility granted.") {
		t.Fatalf("Voting Key redemption = %d %q", redeemed.status, redeemed.body)
	}
	if replay := postFrontendForm(
		t, alice, server.address, "/voting", successValues,
	); replay.status != http.StatusOK ||
		!strings.Contains(replay.body, "Voting Eligibility granted.") {
		t.Fatalf("Voting Key redemption replay = %d %q", replay.status, replay.body)
	}

	bobPage := getFrontendPage(t, bob, server.address, "/voting")
	bobValues := frontendNamedValues(bobPage.body, "command_id")
	reused := postFrontendForm(t, bob, server.address, "/voting", url.Values{
		"csrf_token": {csrfToken(bobPage)},
		"command_id": {bobValues.Get("command_id")},
		"event_id":   {"1"},
		"voting_key": {firstKey},
	})
	if reused.status != wrongEvent.status ||
		!strings.Contains(reused.body, unavailableMessage) {
		t.Fatalf("transferred Voting Key = %d %q", reused.status, reused.body)
	}

	revokeForm := regexp.MustCompile(
		`(?s)name="command_id" value="([^"]+-revoke-2)".*?name="key_id" value="2"`,
	).FindStringSubmatch(issued.body)
	if len(revokeForm) != 2 {
		t.Fatalf("Voting Key revoke form missing: %q", issued.body)
	}
	revoked := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {csrfToken(issued)},
		"command_id": {revokeForm[1]},
		"action":     {"revoke"},
		"key_id":     {"2"},
	})
	if revoked.status != http.StatusOK {
		t.Fatalf("revoke Voting Key = %d %q", revoked.status, revoked.body)
	}
	bobValues = frontendNamedValues(reused.body, "command_id")
	revokedRedemption := postFrontendForm(t, bob, server.address, "/voting", url.Values{
		"csrf_token": {csrfToken(reused)},
		"command_id": {bobValues.Get("command_id")},
		"event_id":   {"1"},
		"voting_key": {secondKey},
	})
	if revokedRedemption.status != wrongEvent.status ||
		!strings.Contains(revokedRedemption.body, unavailableMessage) {
		t.Fatalf("revoked Voting Key = %d %q", revokedRedemption.status, revokedRedemption.body)
	}

	failed := revokedRedemption
	for attempt := range 6 {
		values := frontendNamedValues(failed.body, "command_id")
		failed = postFrontendForm(t, bob, server.address, "/voting", url.Values{
			"csrf_token": {csrfToken(failed)},
			"command_id": {values.Get("command_id")},
			"event_id":   {"1"},
			"voting_key": {"NOT-A-KEY"},
		})
		if attempt < 5 && failed.status != http.StatusUnprocessableEntity {
			t.Fatalf("malformed Voting Key attempt %d = %d %q", attempt+1, failed.status, failed.body)
		}
	}
	if failed.status != http.StatusTooManyRequests {
		t.Fatalf("Voting Key abuse response = %d %q", failed.status, failed.body)
	}

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	database, err = sql.Open("sqlite", filepath.Join(dataDir, "beamers.db"))
	if err != nil {
		t.Fatalf("reopen Voting database: %v", err)
	}
	var eligibilityCount int
	if err = database.QueryRowContext(
		t.Context(),
		"SELECT count(*) FROM voting_eligibilities WHERE event_id = 1 AND account_id = 2",
	).Scan(&eligibilityCount); err != nil {
		t.Fatalf("read durable Voting Eligibility: %v", err)
	}
	if closeErr := database.Close(); closeErr != nil {
		t.Fatalf("close restarted Voting database: %v", closeErr)
	}
	if eligibilityCount != 1 {
		t.Fatalf("durable Voting Eligibility count = %d, want 1", eligibilityCount)
	}
	server = startBeamersWithPublicListener(t, bin, dataDir)
	if page = getFrontendPage(t, alice, server.address, "/voting"); page.status != http.StatusOK {
		t.Fatalf("restarted Voting page = %d %q", page.status, page.body)
	}
	server.stop(t)
}

func TestLiveCompetitionBallotUpdatesAndSurvivesRestart(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	settingsPath := "/backstage/events/1/settings"
	settings := getFrontendPage(t, administrator, server.address, settingsPath)
	configuredEvent := postFrontendForm(t, administrator, server.address, settingsPath, url.Values{
		"csrf_token":                        {requireFrontendCSRF(t, settings)},
		"command_id":                        {"publish-live-ballot-event"},
		"expected_event_revision":           {"2"},
		"event_name":                        {"BeamConf 2099"},
		"public":                            {"true"},
		"public_slug":                       {"beamconf-2099"},
		"planned_start_date":                {"2099-08-21"},
		"planned_end_date":                  {"2099-08-23"},
		"timezone":                          {"Europe/Berlin"},
		"event_locale":                      {"en-GB"},
		"content_language":                  {"en-GB"},
		"event_day_boundary":                {"06:00"},
		"entry_default_disposition":         {"Pending"},
		"submission_eligibility":            {"AllAccounts"},
		"voting_method":                     {"Range1To5"},
		"self_vote_policy":                  {"Allowed"},
		"target_adjustment_presets_seconds": {"-300,300,600"},
	})
	if configuredEvent.status != http.StatusSeeOther {
		t.Fatalf("publish live Ballot Event = %d %q", configuredEvent.status, configuredEvent.body)
	}
	entriesPath := "/backstage/events/1/competitions/" +
		strconv.FormatInt(competitionID, 10) + "/entries"
	page := getFrontendPage(t, administrator, server.address, entriesPath)
	created := postFrontendForm(t, administrator, server.address, entriesPath, url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"action":     {"create-entry"},
		"command_id": {"create-live-ballot-entry"},
		"entry_name": {"Live Ballot Entry"},
	})
	if created.status != http.StatusSeeOther {
		t.Fatalf("create Ballot Entry = %d %q", created.status, created.body)
	}
	page = getFrontendPage(t, administrator, server.address, entriesPath)
	configured := postFrontendForm(t, administrator, server.address, entriesPath, url.Values{
		"csrf_token":                  {requireFrontendCSRF(t, page)},
		"action":                      {"configure-readiness"},
		"command_id":                  {"configure-live-ballot-readiness"},
		"expected_readiness_revision": {"0"},
	})
	if configured.status != http.StatusSeeOther {
		t.Fatalf("configure Ballot Competition readiness = %d %q", configured.status, configured.body)
	}

	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts",
		map[string]string{
			"name": "Vera Voter", "password": "voter correct horse battery staple",
			"command_id": "create-live-voter",
		},
		http.StatusCreated,
		"{\"id\":2,\"name\":\"Vera Voter\",\"administrator\":false}\n",
	)
	keysPath := "/backstage/events/1/voting-keys"
	keysPage := getFrontendPage(t, administrator, server.address, keysPath)
	issued := postFrontendForm(t, administrator, server.address, keysPath, url.Values{
		"csrf_token": {requireFrontendCSRF(t, keysPage)},
		"command_id": {frontendNamedValues(keysPage.body, "command_id").Get("command_id")},
		"action":     {"issue"},
		"count":      {"1"},
		"expires_at": {time.Now().Add(24 * time.Hour).Format("2006-01-02T15:04")},
	})
	keyMatch := votingKeyOutput.FindStringSubmatch(issued.body)
	if issued.status != http.StatusOK || len(keyMatch) != 2 {
		t.Fatalf("issue live Voting Key = %d %q", issued.status, issued.body)
	}
	voter := authenticatedClient(t)
	voter.CheckRedirect = administrator.CheckRedirect
	assertJSONRequest(
		t, voter, server.address, "/auth/sign-in",
		map[string]string{
			"name": "Vera Voter", "password": "voter correct horse battery staple",
		},
		http.StatusNoContent, "",
	)
	votingPage := getFrontendPage(t, voter, server.address, "/voting")
	redeemed := postFrontendForm(t, voter, server.address, "/voting", url.Values{
		"csrf_token": {requireFrontendCSRF(t, votingPage)},
		"command_id": {frontendNamedValues(votingPage.body, "command_id").Get("command_id")},
		"event_id":   {"1"},
		"voting_key": {keyMatch[1]},
	})
	if redeemed.status != http.StatusOK ||
		!strings.Contains(redeemed.body, "Voting Eligibility granted.") {
		t.Fatalf("redeem live Voting Key = %d %q", redeemed.status, redeemed.body)
	}

	operationsPath := "/backstage/events/1/operations"
	operationsPage := getFrontendPage(t, administrator, server.address, operationsPath)
	started := postFrontendForm(t, administrator, server.address, operationsPath, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, operationsPage)},
		"action":                       {"start-session"},
		"command_id":                   {"start-live-ballot-competition"},
		"session_id":                   {strconv.FormatInt(competitionID, 10)},
		"expected_live_state_revision": {"0"},
	})
	if started.status != http.StatusSeeOther {
		t.Fatalf("start live Ballot Competition = %d %q", started.status, started.body)
	}
	votingControlPath := "/backstage/events/1/competitions/" +
		strconv.FormatInt(competitionID, 10) + "/voting"
	controlPage := getFrontendPage(t, administrator, server.address, votingControlPath)
	opened := postFrontendForm(t, administrator, server.address, votingControlPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, controlPage)},
		"command_id":        {"open-live-ballot"},
		"action":            {"open"},
		"expected_revision": {"0"},
	})
	if opened.status != http.StatusSeeOther {
		t.Fatalf("open live Voting Window = %d %q", opened.status, opened.body)
	}
	ineligibleCompetition := getFrontendPage(
		t,
		administrator,
		server.address,
		"/events/beamconf-2099/competitions/"+strconv.FormatInt(competitionID, 10),
	)
	if !strings.Contains(
		ineligibleCompetition.body,
		"Voting is unavailable to this Account.",
	) {
		t.Fatalf(
			"ineligible Competition Voting state = %d %q",
			ineligibleCompetition.status,
			ineligibleCompetition.body,
		)
	}

	page = getFrontendPage(t, administrator, server.address, entriesPath)
	claimed := postFrontendForm(t, administrator, server.address, entriesPath, url.Values{
		"csrf_token":                {requireFrontendCSRF(t, page)},
		"action":                    {"claim-control"},
		"command_id":                {"claim-live-ballot-control"},
		"expected_control_revision": {"0"},
	})
	if claimed.status != http.StatusSeeOther {
		t.Fatalf("claim live Ballot Program control = %d %q", claimed.status, claimed.body)
	}
	programClient := programv1connect.NewProgramControlServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	current, err := programClient.GetProgramChannel(t.Context(), connect.NewRequest(
		&programv1.GetProgramChannelRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil {
		t.Fatalf("read live Ballot Program Channel: %v", err)
	}
	channel := current.Msg.GetChannel()
	for _, commandID := range []string{"take-live-ballot-upcoming", "take-live-ballot-starting"} {
		taken, takeErr := programClient.Take(t.Context(), connect.NewRequest(
			&programv1.TakeRequest{
				EventId: 1, SessionId: competitionID, CommandId: commandID,
				ExpectedLiveStateRevision:    channel.GetLiveStateRevision(),
				ExpectedControlStateRevision: channel.GetControlStateRevision(),
				Preview:                      channel.GetPreview(),
			},
		))
		if takeErr != nil {
			t.Fatalf("advance live Ballot Program: %v", takeErr)
		}
		channel = taken.Msg.GetChannel()
	}
	competitionPath := "/events/beamconf-2099/competitions/" +
		strconv.FormatInt(competitionID, 10)
	competitionPage := getFrontendPage(t, voter, server.address, competitionPath)
	ballotPath := frontendLinkPath(t, competitionPage, "Vote")
	if want := "/voting/" + strconv.FormatInt(competitionID, 10) +
		"?event_id=1"; ballotPath != want {
		t.Fatalf("Competition Vote path = %q, want %q", ballotPath, want)
	}
	if !strings.Contains(
		competitionPage.body,
		"Voting is open. 0 of 0 presented Entries scored.",
	) {
		t.Fatalf("Competition Voting state = %d %q", competitionPage.status, competitionPage.body)
	}
	participationPage := getFrontendPage(t, voter, server.address, "/my-participation")
	if participationPath := frontendLinkPath(
		t,
		participationPage,
		"Vote",
	); participationPath != ballotPath {
		t.Fatalf("My Participation Vote path = %q, want %q", participationPath, ballotPath)
	}
	beforePresentation := getFrontendPage(t, voter, server.address, ballotPath)
	if beforePresentation.status != http.StatusOK ||
		!strings.Contains(beforePresentation.body, "No Entries have been presented yet.") {
		t.Fatalf("pre-presentation Ballot = %d %q", beforePresentation.status, beforePresentation.body)
	}
	streamPath := frontendSSEPath(t, beforePresentation.body)
	streamContext, cancelStream := context.WithCancel(t.Context())
	defer cancelStream()
	streamRequest, err := http.NewRequestWithContext(
		streamContext,
		http.MethodGet,
		"http://"+server.address+streamPath,
		http.NoBody,
	)
	if err != nil {
		t.Fatalf("create Ballot stream request: %v", err)
	}
	streamResponse, err := voter.Do(streamRequest)
	if err != nil {
		t.Fatalf("open Ballot stream: %v", err)
	}
	defer func() { _ = streamResponse.Body.Close() }()
	if streamResponse.StatusCode != http.StatusOK ||
		streamResponse.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Ballot stream = %d %q", streamResponse.StatusCode, streamResponse.Header)
	}
	notifications := make(chan error, 1)
	go func() {
		notifications <- readBallotInvalidation(bufio.NewReader(streamResponse.Body))
	}()
	order, err := competitionv1connect.NewCompetitionServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	).PreviewEntryOrder(t.Context(), connect.NewRequest(
		&competitionv1.PreviewEntryOrderRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil {
		t.Fatalf("preview live Ballot Entry Order: %v", err)
	}
	taken, err := programClient.Take(t.Context(), connect.NewRequest(
		&programv1.TakeRequest{
			EventId: 1, SessionId: competitionID, CommandId: "take-live-ballot-entry",
			ExpectedLiveStateRevision:    channel.GetLiveStateRevision(),
			ExpectedControlStateRevision: channel.GetControlStateRevision(),
			Preview:                      channel.GetPreview(),
			ExpectedEntryOrderRevision:   order.Msg.GetEntryOrder().GetRevision(),
			EntryOrderFingerprint:        order.Msg.GetFingerprint(),
		},
	))
	if err != nil {
		t.Fatalf("present live Ballot Entry: %v", err)
	}
	select {
	case notificationErr := <-notifications:
		if notificationErr != nil {
			t.Fatalf("read Ballot invalidation: %v", notificationErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ballot received no presentation invalidation")
	}
	cancelStream()
	if closeErr := streamResponse.Body.Close(); closeErr != nil {
		t.Fatalf("close Ballot stream: %v", closeErr)
	}

	ballot := getFrontendPage(t, voter, server.address, ballotPath)
	if ballot.status != http.StatusOK || !strings.Contains(ballot.body, "Live Ballot Entry") {
		t.Fatalf("presented Entry Ballot = %d %q", ballot.status, ballot.body)
	}
	voteEntryID := strconv.FormatInt(
		taken.Msg.GetChannel().GetProgramOutput().GetEntryId(),
		10,
	)
	invalidVote := postFrontendForm(t, voter, server.address, ballotPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, ballot)},
		"command_id":        {"cast-invalid-live-ballot-vote"},
		"event_id":          {"1"},
		"entry_id":          {voteEntryID},
		"expected_revision": {frontendNamedValues(ballot.body, "expected_revision").Get("expected_revision")},
		"value":             {"9"},
	})
	if invalidVote.status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid live Vote = %d %q", invalidVote.status, invalidVote.body)
	}
	assertAccessibleFormErrors(t, invalidVote, map[string]string{
		"vote-" + voteEntryID: "Check the Vote and try again.",
	})
	competitionPage = getFrontendPage(t, voter, server.address, competitionPath)
	if !strings.Contains(
		competitionPage.body,
		"Voting is open. 0 of 1 presented Entries scored.",
	) {
		t.Fatalf("presented Competition Voting state = %d %q", competitionPage.status, competitionPage.body)
	}
	values := frontendNamedValues(ballot.body, "expected_revision")
	voted := postFrontendForm(t, voter, server.address, ballotPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, ballot)},
		"command_id":        {"cast-live-ballot-vote"},
		"event_id":          {"1"},
		"entry_id":          {strconv.FormatInt(taken.Msg.GetChannel().GetProgramOutput().GetEntryId(), 10)},
		"expected_revision": {values.Get("expected_revision")},
		"value":             {"5"},
	})
	if voted.status != http.StatusSeeOther {
		t.Fatalf("cast live Vote = %d %q", voted.status, voted.body)
	}
	ballot = getFrontendPage(t, voter, server.address, ballotPath)
	editedVote := postFrontendForm(t, voter, server.address, ballotPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, ballot)},
		"command_id":        {"edit-live-ballot-vote"},
		"event_id":          {"1"},
		"entry_id":          {strconv.FormatInt(taken.Msg.GetChannel().GetProgramOutput().GetEntryId(), 10)},
		"expected_revision": {frontendNamedValues(ballot.body, "expected_revision").Get("expected_revision")},
		"value":             {"4"},
	})
	if editedVote.status != http.StatusSeeOther {
		t.Fatalf("edit live Vote = %d %q", editedVote.status, editedVote.body)
	}
	competitionPage = getFrontendPage(t, voter, server.address, competitionPath)
	if !strings.Contains(
		competitionPage.body,
		"Voting is open. 1 of 1 presented Entries scored.",
	) {
		t.Fatalf("scored Competition Voting state = %d %q", competitionPage.status, competitionPage.body)
	}
	controlPage = getFrontendPage(t, administrator, server.address, votingControlPath)
	if !strings.Contains(controlPage.body, "Participating: 1.") ||
		strings.Contains(controlPage.body, "Your score") ||
		strings.Contains(controlPage.body, `name="value"`) {
		t.Fatalf("Crew Voting projection leaked or missed participation: %q", controlPage.body)
	}
	entryID := strconv.FormatInt(
		taken.Msg.GetChannel().GetProgramOutput().GetEntryId(),
		10,
	)
	resultsPath := "/backstage/events/1/results"
	resultsPage := getFrontendPage(t, administrator, server.address, resultsPath)
	savedBeforeTally := postFrontendForm(
		t,
		administrator,
		server.address,
		resultsPath,
		url.Values{
			"csrf_token":             {requireFrontendCSRF(t, resultsPage)},
			"action":                 {"save-results-draft"},
			"command_id":             {"save-pre-tally-results"},
			"competition_session_id": {strconv.FormatInt(competitionID, 10)},
			"expected_revision":      {"0"},
			"disposition":            {"Publish"},
			"score_type":             {"None"},
			"score_visibility":       {"Public"},
			"score_precision":        {"0"},
			"score_requirement":      {"Optional"},
			"score_interpretation":   {"Informational"},
			"standing_entry_id":      {entryID},
			"standing":               {"Placed"},
			"placement":              {"1"},
			"display_order":          {"1"},
			"score":                  {""},
		},
	)
	if savedBeforeTally.status != http.StatusSeeOther {
		t.Fatalf(
			"save pre-Tally Results = %d %q",
			savedBeforeTally.status,
			savedBeforeTally.body,
		)
	}
	resultsPage = getFrontendPage(t, administrator, server.address, resultsPath)
	readyBeforeTally := postFrontendForm(
		t,
		administrator,
		server.address,
		resultsPath,
		url.Values{
			"csrf_token":             {requireFrontendCSRF(t, resultsPage)},
			"action":                 {"mark-results-ready"},
			"command_id":             {"ready-pre-tally-results"},
			"competition_session_id": {strconv.FormatInt(competitionID, 10)},
			"expected_revision":      {"1"},
		},
	)
	if readyBeforeTally.status != http.StatusSeeOther {
		t.Fatalf(
			"ready pre-Tally Results = %d %q",
			readyBeforeTally.status,
			readyBeforeTally.body,
		)
	}
	closeValues := url.Values{
		"csrf_token":        {requireFrontendCSRF(t, controlPage)},
		"command_id":        {"close-live-ballot"},
		"action":            {"close"},
		"expected_revision": {frontendNamedValues(controlPage.body, "expected_revision").Get("expected_revision")},
	}
	closed := postFrontendForm(
		t,
		administrator,
		server.address,
		votingControlPath,
		closeValues,
	)
	if closed.status != http.StatusSeeOther {
		t.Fatalf("close live Voting Window = %d %q", closed.status, closed.body)
	}
	replayedClose := postFrontendForm(
		t,
		administrator,
		server.address,
		votingControlPath,
		closeValues,
	)
	if replayedClose.status != http.StatusSeeOther {
		t.Fatalf(
			"replay close live Voting Window = %d %q",
			replayedClose.status,
			replayedClose.body,
		)
	}
	conflictingCloseValues := closeValues
	conflictingCloseValues.Set("expected_revision", "0")
	conflictingClose := postFrontendForm(
		t,
		administrator,
		server.address,
		votingControlPath,
		conflictingCloseValues,
	)
	if conflictingClose.status != http.StatusConflict {
		t.Fatalf(
			"conflicting close live Voting Window = %d %q",
			conflictingClose.status,
			conflictingClose.body,
		)
	}
	resultsPage = getFrontendPage(t, administrator, server.address, resultsPath)
	if resultsPage.status != http.StatusOK ||
		!strings.Contains(resultsPage.body, "Draft revision <code>2</code>") ||
		!strings.Contains(resultsPage.body, `data-tone="draft">Draft</span>`) ||
		!strings.Contains(resultsPage.body, "Voting Tally: 1") ||
		!strings.Contains(resultsPage.body, "4 total from 1 Ballots") ||
		strings.Contains(resultsPage.body, "Release standalone Results") {
		t.Fatalf("seeded Tally Results = %d %q", resultsPage.status, resultsPage.body)
	}
	overrideValues := url.Values{
		"csrf_token":             {requireFrontendCSRF(t, resultsPage)},
		"action":                 {"save-results-draft"},
		"command_id":             {"override-tally-results"},
		"competition_session_id": {strconv.FormatInt(competitionID, 10)},
		"expected_revision":      {"2"},
		"disposition":            {"Publish"},
		"score_type":             {"None"},
		"score_visibility":       {"Public"},
		"score_precision":        {"0"},
		"score_requirement":      {"Optional"},
		"score_interpretation":   {"Informational"},
		"standing_entry_id":      {entryID},
		"standing":               {"Unplaced"},
		"placement":              {""},
		"display_order":          {"1"},
		"score":                  {""},
	}
	rejectedOverride := postFrontendForm(
		t,
		administrator,
		server.address,
		resultsPath,
		overrideValues,
	)
	if rejectedOverride.status != http.StatusUnprocessableEntity {
		t.Fatalf(
			"reasonless Tally override = %d %q",
			rejectedOverride.status,
			rejectedOverride.body,
		)
	}
	overrideValues.Set("tally_override_reason", "Producer resolved the tied review.")
	savedOverride := postFrontendForm(
		t,
		administrator,
		server.address,
		resultsPath,
		overrideValues,
	)
	if savedOverride.status != http.StatusSeeOther {
		t.Fatalf(
			"save Tally override = %d %q",
			savedOverride.status,
			savedOverride.body,
		)
	}
	competitionPage = getFrontendPage(t, voter, server.address, competitionPath)
	if got := frontendLinkPath(t, competitionPage, "View Ballot"); got != ballotPath ||
		!strings.Contains(competitionPage.body, "Voting is closed.") {
		t.Fatalf(
			"closed Competition Ballot = %d path %q %q",
			competitionPage.status,
			got,
			competitionPage.body,
		)
	}
	readOnly := getFrontendPage(t, voter, server.address, ballotPath)
	if !strings.Contains(readOnly.body, "Voting is closed.") ||
		!strings.Contains(readOnly.body, "Your score: 4") {
		t.Fatalf("closed private Ballot = %d %q", readOnly.status, readOnly.body)
	}

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	server = startBeamersWithPublicListener(t, bin, dataDir)
	recoveryContext, cancelRecovery := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelRecovery()
	recoveryRequest, err := http.NewRequestWithContext(
		recoveryContext, http.MethodGet, "http://"+server.address+streamPath, http.NoBody,
	)
	if err != nil {
		t.Fatalf("create restarted Ballot stream request: %v", err)
	}
	recoveryResponse, err := voter.Do(recoveryRequest)
	if err != nil {
		t.Fatalf("open restarted Ballot stream: %v", err)
	}
	if err = readBallotInvalidation(bufio.NewReader(recoveryResponse.Body)); err != nil {
		t.Fatalf("read restarted Ballot invalidation: %v", err)
	}
	if closeErr := recoveryResponse.Body.Close(); closeErr != nil {
		t.Fatalf("close restarted Ballot stream: %v", closeErr)
	}
	restarted := getFrontendPage(t, voter, server.address, ballotPath)
	if restarted.status != http.StatusOK ||
		!strings.Contains(restarted.body, "Voting is closed.") ||
		!strings.Contains(restarted.body, "Your score: 4") {
		t.Fatalf("restarted private Ballot = %d %q", restarted.status, restarted.body)
	}
	restartedResults := getFrontendPage(t, administrator, server.address, resultsPath)
	if restartedResults.status != http.StatusOK ||
		!strings.Contains(restartedResults.body, "Draft revision <code>3</code>") ||
		!strings.Contains(restartedResults.body, "Voting Tally: 1") ||
		!strings.Contains(
			restartedResults.body,
			"Producer resolved the tied review.",
		) ||
		strings.Contains(restartedResults.body, "Release standalone Results") {
		t.Fatalf(
			"restarted Tally Results = %d %q",
			restartedResults.status,
			restartedResults.body,
		)
	}
	server.stop(t)
	database, err := sql.Open("sqlite", filepath.Join(dataDir, "beamers.db"))
	if err != nil {
		t.Fatalf("open Tally database: %v", err)
	}
	defer func() { _ = database.Close() }()
	var tallyCount, publicationCount int
	var tallyEntries, overrideReason string
	if err = database.QueryRowContext(
		t.Context(),
		"SELECT count(*), entries FROM voting_tallies WHERE competition_session_id = ?",
		competitionID,
	).Scan(&tallyCount, &tallyEntries); err != nil {
		t.Fatalf("read durable Voting Tally: %v", err)
	}
	if err = database.QueryRowContext(
		t.Context(),
		"SELECT count(*) FROM results_publications WHERE scope_session_id = ?",
		competitionID,
	).Scan(&publicationCount); err != nil {
		t.Fatalf("count isolated Results publications: %v", err)
	}
	if err = database.QueryRowContext(
		t.Context(),
		"SELECT reason FROM audit_entries "+
			"WHERE action = 'SaveCompetitionResultsDraft' AND reason <> '' "+
			"ORDER BY id DESC LIMIT 1",
	).Scan(&overrideReason); err != nil {
		t.Fatalf("read Tally override Audit Entry: %v", err)
	}
	if tallyCount != 1 ||
		strings.Contains(tallyEntries, "account") ||
		publicationCount != 0 ||
		overrideReason != "Producer resolved the tied review." {
		t.Fatalf(
			"Tally persistence = count %d entries %q publications %d audit %q",
			tallyCount,
			tallyEntries,
			publicationCount,
			overrideReason,
		)
	}
}

func TestBackstageOperatesBackupsAndDiagnostics(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	page := getFrontendPage(
		t,
		administrator,
		server.address,
		"/backstage/installation",
	)
	if page.status != http.StatusOK {
		t.Fatalf("Backstage installation = %d %q", page.status, page.body)
	}
	for _, want := range []string{
		"Installation health",
		"Readiness",
		"Capacity",
		"Storage",
		"Display stream",
		"Program stream",
		"Replication",
		"Telemetry",
		"Sanitized Backup",
		"Full-Fidelity Backup",
		"Prepare Restore",
	} {
		if !strings.Contains(page.body, want) {
			t.Errorf("Backstage installation lacks %q", want)
		}
	}
	if strings.Contains(page.body, server.dataDir) ||
		strings.Contains(page.body, "correct horse battery staple") {
		t.Fatalf("Backstage installation leaked a secret or host path: %q", page.body)
	}

	assertJSONRequest(
		t,
		administrator,
		server.address,
		"/admin/accounts",
		map[string]string{
			"name":       "Ordinary Crew",
			"password":   "ordinary correct horse battery staple",
			"command_id": "create-installation-observer",
		},
		http.StatusCreated,
		"{\"id\":2,\"name\":\"Ordinary Crew\",\"administrator\":false}\n",
	)
	crew := authenticatedClient(t)
	crew.CheckRedirect = administrator.CheckRedirect
	assertJSONRequest(
		t,
		crew,
		server.address,
		"/auth/sign-in",
		map[string]string{
			"name":     "Ordinary Crew",
			"password": "ordinary correct horse battery staple",
		},
		http.StatusNoContent,
		"",
	)
	if denied := getFrontendPage(
		t,
		crew,
		server.address,
		"/backstage/installation",
	); denied.status != http.StatusForbidden {
		t.Fatalf("non-Administrator installation = %d %q", denied.status, denied.body)
	}

	unconfirmed := postFrontendForm(
		t,
		administrator,
		server.address,
		"/backstage/installation",
		url.Values{
			"csrf_token": {requireFrontendCSRF(t, page)},
			"action":     {"backup-sanitized"},
		},
	)
	if unconfirmed.status != http.StatusUnprocessableEntity {
		t.Fatalf("unconfirmed Backup = %d %q", unconfirmed.status, unconfirmed.body)
	}
	assertAccessibleFormErrors(t, unconfirmed, map[string]string{
		"installation-backup-sanitized-confirm": "Confirm the Sanitized Backup",
	})
	sanitized := postFrontendForm(
		t,
		administrator,
		server.address,
		"/backstage/installation",
		url.Values{
			"csrf_token": {requireFrontendCSRF(t, page)},
			"action":     {"backup-sanitized"},
			"confirm":    {"true"},
		},
	)
	if sanitized.status != http.StatusOK ||
		sanitized.header.Get("Content-Type") != "application/zip" ||
		sanitized.header.Get("X-Beamers-Backup-Mode") != string(backup.Sanitized) {
		t.Fatalf(
			"Sanitized Backup = %d mode %q content type %q",
			sanitized.status,
			sanitized.header.Get("X-Beamers-Backup-Mode"),
			sanitized.header.Get("Content-Type"),
		)
	}
	archivePath := filepath.Join(t.TempDir(), "sanitized.zip")
	if err := os.WriteFile(archivePath, []byte(sanitized.body), 0o600); err != nil {
		t.Fatalf("write Sanitized Backup: %v", err)
	}
	manifest, err := backup.Verify(t.Context(), archivePath)
	if err != nil || manifest.Mode != backup.Sanitized {
		t.Fatalf("verify Sanitized Backup = %+v, %v", manifest, err)
	}

	failedReauthentication := postFrontendForm(
		t,
		administrator,
		server.address,
		"/backstage/installation",
		url.Values{
			"csrf_token":             {requireFrontendCSRF(t, page)},
			"action":                 {"backup-full-fidelity"},
			"password":               {"wrong password"},
			"acknowledge_protection": {"true"},
		},
	)
	if failedReauthentication.status != http.StatusUnauthorized {
		t.Fatalf(
			"Full-Fidelity Backup without reauthentication = %d %q",
			failedReauthentication.status,
			failedReauthentication.body,
		)
	}
	if strings.Contains(failedReauthentication.body, `value="wrong password"`) {
		t.Fatalf("failed Backup retained password: %q", failedReauthentication.body)
	}
	if !strings.Contains(
		failedReauthentication.body,
		`name="acknowledge_protection" value="true" required checked`,
	) {
		t.Fatalf(
			"failed Backup dropped protection acknowledgment: %q",
			failedReauthentication.body,
		)
	}
	assertAccessibleFormErrors(t, failedReauthentication, map[string]string{
		"installation-backup-full-fidelity-password": "current password",
	})
	fullFidelity := postFrontendForm(
		t,
		administrator,
		server.address,
		"/backstage/installation",
		url.Values{
			"csrf_token":             {requireFrontendCSRF(t, failedReauthentication)},
			"action":                 {"backup-full-fidelity"},
			"password":               {"correct horse battery staple"},
			"acknowledge_protection": {"true"},
		},
	)
	if fullFidelity.status != http.StatusOK ||
		fullFidelity.header.Get("X-Beamers-Backup-Mode") != string(backup.FullFidelity) {
		t.Fatalf(
			"Full-Fidelity Backup = %d mode %q body %q",
			fullFidelity.status,
			fullFidelity.header.Get("X-Beamers-Backup-Mode"),
			fullFidelity.body,
		)
	}
	fullFidelityPath := filepath.Join(t.TempDir(), "full-fidelity.zip")
	if err = os.WriteFile(fullFidelityPath, []byte(fullFidelity.body), 0o600); err != nil {
		t.Fatalf("write Full-Fidelity Backup: %v", err)
	}
	manifest, err = backup.Verify(t.Context(), fullFidelityPath)
	if err != nil || manifest.Mode != backup.FullFidelity {
		t.Fatalf("verify Full-Fidelity Backup = %+v, %v", manifest, err)
	}

	missingRestore := postFrontendMultipart(
		t,
		administrator,
		server.address,
		"/backstage/installation",
		map[string]string{
			"csrf_token": requireFrontendCSRF(t, page),
			"action":     "prepare-restore",
		},
		"",
		"",
		nil,
	)
	if missingRestore.status != http.StatusUnprocessableEntity {
		t.Fatalf("missing Restore Backup ZIP = %d %q", missingRestore.status, missingRestore.body)
	}
	assertAccessibleFormErrors(t, missingRestore, map[string]string{
		"installation-restore-backup": "Choose a Backup ZIP",
	})

	prepared := postFrontendMultipart(
		t,
		administrator,
		server.address,
		"/backstage/installation",
		map[string]string{
			"csrf_token": requireFrontendCSRF(t, page),
			"action":     "prepare-restore",
		},
		"backup",
		"sanitized.zip",
		[]byte(sanitized.body),
	)
	if prepared.status != http.StatusSeeOther ||
		prepared.header.Get("Location") != "/backstage/installation?prepared=true" {
		t.Fatalf("prepare Restore = %d %q", prepared.status, prepared.body)
	}
	preparedPage := getFrontendPage(
		t,
		administrator,
		server.address,
		"/backstage/installation",
	)
	for _, want := range []string{"Prepared Restore", "Validation:", "Passed", "Sanitized"} {
		if !strings.Contains(preparedPage.body, want) {
			t.Errorf("prepared Restore page lacks %q", want)
		}
	}
	if strings.Contains(preparedPage.body, "Apply Restore") ||
		strings.Contains(preparedPage.body, server.dataDir) {
		t.Fatalf("prepared Restore exposed replacement or host paths: %q", preparedPage.body)
	}
	removedRestore := requestJSON(
		t.Context(),
		administrator,
		server.address,
		"/admin/restores/apply",
		map[string]any{
			"password":                "correct horse battery staple",
			"acknowledge_replacement": true,
		},
	)
	if removedRestore.status != http.StatusNotFound ||
		removedRestore.header.Get("Content-Type") != "text/plain; charset=utf-8" ||
		removedRestore.body != "404 page not found\n" {
		t.Fatalf(
			"removed Restore API = %d %q %q",
			removedRestore.status,
			removedRestore.header.Get("Content-Type"),
			removedRestore.body,
		)
	}

	uploadReader, uploadWriter := io.Pipe()
	uploadForm := multipart.NewWriter(uploadWriter)
	uploadRequest, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"http://"+server.address+"/backstage/installation",
		uploadReader,
	)
	if err != nil {
		t.Fatalf("create blocked Restore upload: %v", err)
	}
	uploadRequest.Header.Set("Content-Type", uploadForm.FormDataContentType())
	uploadRequest.Header.Set("Origin", "http://"+server.address)
	uploadResult := make(chan frontendHTTPResult, 1)
	go func() {
		response, requestErr := administrator.Do(uploadRequest)
		uploadResult <- readFrontendHTTPResult(response, requestErr)
	}()
	uploadReady := make(chan error, 1)
	releaseUpload := make(chan struct{})
	uploadWriteResult := make(chan error, 1)
	preparedCSRF := requireFrontendCSRF(t, preparedPage)
	go func() {
		var writeErr error
		defer func() {
			closeErr := uploadWriter.CloseWithError(writeErr)
			uploadWriteResult <- errors.Join(writeErr, closeErr)
		}()
		for name, value := range map[string]string{
			"csrf_token": preparedCSRF,
			"action":     "prepare-restore",
		} {
			if writeErr = uploadForm.WriteField(name, value); writeErr != nil {
				uploadReady <- writeErr
				return
			}
		}
		var file io.Writer
		file, writeErr = uploadForm.CreateFormFile("backup", "blocked.zip")
		if writeErr == nil {
			_, writeErr = file.Write([]byte("blocked"))
		}
		uploadReady <- writeErr
		if writeErr != nil {
			return
		}
		select {
		case <-releaseUpload:
		case <-uploadRequest.Context().Done():
			writeErr = context.Cause(uploadRequest.Context())
			return
		}
		writeErr = uploadForm.Close()
	}()
	if err = <-uploadReady; err != nil {
		close(releaseUpload)
		t.Fatalf("start blocked Restore upload: %v", err)
	}

	cancelValues := url.Values{
		"csrf_token":               {preparedCSRF},
		"action":                   {"cancel-restore"},
		"password":                 {"correct horse battery staple"},
		"acknowledge_cancellation": {"true"},
	}
	cancelRequest, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"http://"+server.address+"/backstage/installation",
		strings.NewReader(cancelValues.Encode()),
	)
	if err != nil {
		close(releaseUpload)
		t.Fatalf("create concurrent Restore cancellation: %v", err)
	}
	cancelRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	cancelRequest.Header.Set("Origin", "http://"+server.address)
	cancelResult := make(chan frontendHTTPResult, 1)
	go func() {
		response, requestErr := administrator.Do(cancelRequest)
		cancelResult <- readFrontendHTTPResult(response, requestErr)
	}()

	var maintenancePage frontendResponse
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		maintenancePage = getFrontendPage(
			t,
			administrator,
			server.address,
			"/backstage/installation",
		)
		if maintenancePage.status == http.StatusServiceUnavailable {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if maintenancePage.status != http.StatusServiceUnavailable ||
		maintenancePage.header.Get("X-Beamers-Maintenance") != "restore" ||
		!strings.Contains(maintenancePage.body, "Maintenance state:") {
		close(releaseUpload)
		t.Fatalf(
			"Backstage Restore maintenance = %d, headers %v, body %q",
			maintenancePage.status,
			maintenancePage.header,
			maintenancePage.body,
		)
	}
	rejectedMutation := postFrontendForm(
		t,
		administrator,
		server.address,
		"/backstage/installation",
		url.Values{
			"csrf_token": {requireFrontendCSRF(t, preparedPage)},
			"action":     {"backup-sanitized"},
			"confirm":    {"true"},
		},
	)
	if rejectedMutation.status != http.StatusServiceUnavailable ||
		rejectedMutation.body != "maintenance in progress\n" {
		close(releaseUpload)
		t.Fatalf(
			"browser mutation during Restore maintenance = %d %q",
			rejectedMutation.status,
			rejectedMutation.body,
		)
	}
	close(releaseUpload)
	if err = <-uploadWriteResult; err != nil {
		t.Fatalf("finish blocked Restore upload: %v", err)
	}
	blockedUpload := <-uploadResult
	if blockedUpload.err != nil {
		t.Fatalf("blocked Restore upload: %v", blockedUpload.err)
	}
	cancellation := <-cancelResult
	if cancellation.err != nil {
		t.Fatalf("concurrent Restore cancellation: %v", cancellation.err)
	}
	if cancellation.page.status != http.StatusSeeOther {
		t.Fatalf(
			"concurrent Restore cancellation = %d %q",
			cancellation.page.status,
			cancellation.page.body,
		)
	}

	afterMaintenance := getFrontendPage(
		t,
		administrator,
		server.address,
		"/backstage/installation",
	)
	prepared = postFrontendMultipart(
		t,
		administrator,
		server.address,
		"/backstage/installation",
		map[string]string{
			"csrf_token": requireFrontendCSRF(t, afterMaintenance),
			"action":     "prepare-restore",
		},
		"backup",
		"sanitized.zip",
		[]byte(sanitized.body),
	)
	if prepared.status != http.StatusSeeOther {
		t.Fatalf("reprepare Restore after maintenance = %d %q", prepared.status, prepared.body)
	}

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	server = startBeamersWithPublicListener(t, bin, dataDir)
	restartedPage := getFrontendPage(
		t,
		administrator,
		server.address,
		"/backstage/installation",
	)
	if restartedPage.status != http.StatusOK ||
		!strings.Contains(restartedPage.body, "Prepared Restore") ||
		!strings.Contains(restartedPage.body, "Passed") {
		t.Fatalf(
			"prepared Restore after restart = %d %q",
			restartedPage.status,
			restartedPage.body,
		)
	}
	failedCancellation := postFrontendForm(
		t,
		administrator,
		server.address,
		"/backstage/installation",
		url.Values{
			"csrf_token":               {requireFrontendCSRF(t, restartedPage)},
			"action":                   {"cancel-restore"},
			"password":                 {"wrong password"},
			"acknowledge_cancellation": {"true"},
		},
	)
	if failedCancellation.status != http.StatusUnauthorized ||
		strings.Contains(failedCancellation.body, `value="wrong password"`) ||
		!strings.Contains(
			failedCancellation.body,
			`name="acknowledge_cancellation" value="true" required checked`,
		) {
		t.Fatalf(
			"failed Restore cancellation = %d %q",
			failedCancellation.status,
			failedCancellation.body,
		)
	}
	assertAccessibleFormErrors(t, failedCancellation, map[string]string{
		"installation-cancel-restore-password": "current password",
	})
	canceled := postFrontendForm(
		t,
		administrator,
		server.address,
		"/backstage/installation",
		url.Values{
			"csrf_token":               {requireFrontendCSRF(t, failedCancellation)},
			"action":                   {"cancel-restore"},
			"password":                 {"correct horse battery staple"},
			"acknowledge_cancellation": {"true"},
		},
	)
	if canceled.status != http.StatusSeeOther ||
		canceled.header.Get("Location") != "/backstage/installation?canceled=true" {
		t.Fatalf("cancel prepared Restore = %d %q", canceled.status, canceled.body)
	}
	afterCancellation := getFrontendPage(
		t,
		administrator,
		server.address,
		"/backstage/installation",
	)
	if !strings.Contains(afterCancellation.body, "No Restore is prepared.") {
		t.Fatalf("Restore remained prepared after cancellation: %q", afterCancellation.body)
	}
	server.stop(t)
}

func TestBrowserAdministersAccountsAndEventGrants(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	assertJSONRequest(
		t, administrator, server.address, "/admin/events",
		validEventInput(), http.StatusCreated,
		"{\"id\":1,\"name\":\"Revision 2026\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	secondEvent := validEventInput()
	secondEvent["name"] = "Revision 2027"
	secondEvent["command_id"] = "create-administration-event-2"
	assertJSONRequest(
		t, administrator, server.address, "/admin/events",
		secondEvent, http.StatusCreated,
		"{\"id\":2,\"name\":\"Revision 2027\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)

	backstage := getFrontendPage(t, administrator, server.address, "/backstage")
	if !strings.Contains(
		frontendBackstageNavigation(t, backstage),
		`href="/backstage/administration"`,
	) {
		t.Fatalf("Backstage lacks administration route: %q", backstage.body)
	}
	const path = "/backstage/administration"
	page := getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{
		"Accounts, Event Grants, and activation",
		"Ada Admin",
		"Revision 2026",
		`name="action" value="create-account"`,
		`name="action" value="grant"`,
	} {
		if page.status != http.StatusOK || !strings.Contains(page.body, want) {
			t.Fatalf("administration page lacks %q: %d %q", want, page.status, page.body)
		}
	}

	invalidAccount := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":     {requireFrontendCSRF(t, page)},
		"action":         {"create-account"},
		"command_id":     {"browser-invalid-administration-account"},
		"account_handle": {"opal"},
		"display_name":   {"Retain Opal Operator"},
		"password":       {"tiny-secret"},
	})
	if invalidAccount.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalidAccount.body, `value="opal"`) ||
		!strings.Contains(invalidAccount.body, `value="Retain Opal Operator"`) ||
		strings.Contains(invalidAccount.body, `value="tiny-secret"`) {
		t.Fatalf("invalid browser Account creation = %d %q", invalidAccount.status, invalidAccount.body)
	}
	assertAccessibleFormErrors(t, invalidAccount, map[string]string{
		"administration-account-password": "12 to 1024 characters",
	})

	const password = "operator administration correct horse battery staple"
	created := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":     {requireFrontendCSRF(t, invalidAccount)},
		"action":         {"create-account"},
		"command_id":     {"browser-create-administration-account"},
		"account_handle": {"opal"},
		"display_name":   {"Opal Operator"},
		"password":       {password},
	})
	if created.status != http.StatusSeeOther || created.header.Get("Location") != path {
		t.Fatalf("browser Account creation = %d %q", created.status, created.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Opal Operator") {
		t.Fatalf("created Account absent: %d %q", page.status, page.body)
	}
	invalidGrant := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":         {requireFrontendCSRF(t, page)},
		"action":             {"grant"},
		"command_id":         {"browser-invalid-administration-grant"},
		"account_id":         {"2"},
		"event_id":           {"2"},
		"role":               {"Spectator"},
		"lane_ids":           {"7, 9"},
		"display_group_keys": {"retain-stage"},
		"capability":         {"ViewResults"},
	})
	if invalidGrant.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalidGrant.body, `value="2" selected`) ||
		!strings.Contains(invalidGrant.body, `value="7, 9"`) ||
		!strings.Contains(invalidGrant.body, `value="retain-stage"`) ||
		!strings.Contains(invalidGrant.body, `value="ViewResults" checked`) {
		t.Fatalf("invalid browser Event Grant = %d %q", invalidGrant.status, invalidGrant.body)
	}
	assertAccessibleFormErrors(t, invalidGrant, map[string]string{
		"administration-grant-role": "valid Event role",
	})
	invalidCapabilities := postFrontendForm(
		t,
		administrator,
		server.address,
		path,
		url.Values{
			"csrf_token": {requireFrontendCSRF(t, invalidGrant)},
			"action":     {"grant"},
			"command_id": {"browser-invalid-administration-capabilities"},
			"account_id": {"2"},
			"event_id":   {"1"},
			"role":       {"Observer"},
			"capability": {"EmergencyAlert"},
		},
	)
	if invalidCapabilities.status != http.StatusUnprocessableEntity {
		t.Fatalf(
			"invalid browser Event Grant capabilities = %d %q",
			invalidCapabilities.status,
			invalidCapabilities.body,
		)
	}
	assertAccessibleFormErrors(t, invalidCapabilities, map[string]string{
		"administration-grant-capabilities": "Observer may receive only ViewResults",
	})
	granted := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":         {requireFrontendCSRF(t, invalidGrant)},
		"action":             {"grant"},
		"command_id":         {"browser-grant-administration-account"},
		"account_id":         {"2"},
		"event_id":           {"1"},
		"role":               {"Operator"},
		"display_group_keys": {"stage, stream"},
		"capability":         {"EmergencyAlert"},
	})
	if granted.status != http.StatusSeeOther || granted.header.Get("Location") != path {
		t.Fatalf("browser Event Grant = %d %q", granted.status, granted.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{
		"Opal Operator",
		"Revision 2026",
		"Operator",
		"stage, stream",
		"EmergencyAlert",
	} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("Event Grant view lacks %q: %q", want, page.body)
		}
	}
	duplicateGrant := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"action":     {"grant"},
		"command_id": {"browser-duplicate-administration-grant"},
		"account_id": {"2"},
		"event_id":   {"1"},
		"role":       {"Observer"},
	})
	if duplicateGrant.status != http.StatusConflict ||
		!strings.Contains(duplicateGrant.body, `role="alert"`) ||
		!strings.Contains(duplicateGrant.body, `id="error-summary"`) ||
		!strings.Contains(duplicateGrant.body, `value="2" selected`) ||
		!strings.Contains(duplicateGrant.body, `<option selected>Observer</option>`) {
		t.Fatalf("duplicate browser Event Grant = %d %q", duplicateGrant.status, duplicateGrant.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	selfGranted := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"action":     {"grant"},
		"command_id": {"browser-self-grant"},
		"account_id": {"1"},
		"event_id":   {"2"},
		"role":       {"Observer"},
	})
	if selfGranted.status != http.StatusSeeOther {
		t.Fatalf("browser self-grant = %d %q", selfGranted.status, selfGranted.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "<td data-numeric>#2</td><td data-numeric>#1</td><td>Observer</td>") {
		t.Fatalf("browser self-grant absent: %q", page.body)
	}

	operator := authenticatedClient(t)
	operator.CheckRedirect = administrator.CheckRedirect
	signInPage := getFrontendPage(t, operator, server.address, "/sign-in")
	signIn := postFrontendForm(t, operator, server.address, "/sign-in", url.Values{
		"csrf_token": {requireFrontendCSRF(t, signInPage)},
		"handle":     {"opal"},
		"password":   {password},
	})
	if signIn.status != http.StatusSeeOther {
		t.Fatalf("granted Account sign-in = %d %q", signIn.status, signIn.body)
	}
	operatorBackstage := getFrontendPage(t, operator, server.address, "/backstage")
	operatorNavigation := frontendBackstageNavigation(t, operatorBackstage)
	if !strings.Contains(operatorNavigation, "Revision 2026") ||
		!strings.Contains(operatorNavigation, "Emergency Alerts") ||
		strings.Contains(operatorNavigation, "Revision 2027") {
		t.Fatalf("Event Grant crossed Event boundary: %q", operatorNavigation)
	}
	if forbidden := getFrontendPage(
		t, operator, server.address, path,
	); forbidden.status != http.StatusForbidden {
		t.Fatalf("Operator administration = %d, want 403", forbidden.status)
	}

	dataDir := server.dataDir
	bin := server.bin
	server.stop(t)
	server = startBeamersWithPublicListener(t, bin, dataDir)
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Opal Operator") ||
		!strings.Contains(page.body, "EmergencyAlert") {
		t.Fatalf("restarted administration state = %d %q", page.status, page.body)
	}
	if !strings.Contains(page.body, `name="action" value="disable-account"`) {
		t.Fatalf("administration page lacks Disable Account: %q", page.body)
	}
	disabled := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"action":     {"disable-account"},
		"command_id": {"browser-disable-administration-account"},
		"account_id": {"2"},
		"reason":     {"crew_departed"},
	})
	if disabled.status != http.StatusSeeOther {
		t.Fatalf("browser Account disablement = %d %q", disabled.status, disabled.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{
		"CreateAccount",
		"CreateEventGrant",
		"DisableAccount",
		"crew_departed",
	} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("browser Audit view lacks %q: %q", want, page.body)
		}
	}
	if strings.Contains(page.body, password) || strings.Contains(page.body, "Credential") {
		t.Fatalf("browser Audit exposed authentication material: %q", page.body)
	}
	if root := getFrontendPage(
		t, operator, server.address, "/",
	); root.status != http.StatusOK ||
		!strings.Contains(root.body, "Sign in") ||
		strings.Contains(root.body, "Opal Operator") {
		t.Fatalf("disabled Account session = %d %q", root.status, root.body)
	}
	if public := getFrontendPage(
		t, authenticatedClient(t), server.publicAddress, path,
	); public.status != http.StatusNotFound {
		t.Fatalf("public-listener administration = %d, want 404", public.status)
	}
	server.stop(t)
}

func TestBrowserPreflightsAndActivatesEvent(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	prepareActiveSchedule(t, administrator, server)
	secondEvent := validEventInput()
	secondEvent["name"] = "Blocked Event"
	secondEvent["command_id"] = "create-blocked-browser-event"
	assertJSONRequest(
		t, administrator, server.address, "/admin/events",
		secondEvent, http.StatusCreated,
		"{\"id\":2,\"name\":\"Blocked Event\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	const path = "/backstage/administration"
	page := getFrontendPage(t, administrator, server.address, path)
	blocked := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"action":     {"preflight"},
		"event_id":   {"2"},
	})
	if blocked.status != http.StatusOK ||
		!strings.Contains(blocked.body, "published_rundown_missing") ||
		strings.Contains(blocked.body, `name="action" value="activate"`) {
		t.Fatalf("blocked browser Activation Preflight = %d %q", blocked.status, blocked.body)
	}

	page = getFrontendPage(t, administrator, server.address, path)
	preflight := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"action":     {"preflight"},
		"event_id":   {"1"},
	})
	if preflight.status != http.StatusOK ||
		!strings.Contains(preflight.body, "Activation Preflight") ||
		!strings.Contains(preflight.body, `name="action" value="activate"`) {
		t.Fatalf("browser Activation Preflight = %d %q", preflight.status, preflight.body)
	}
	confirmation := frontendNamedValues(
		preflight.body,
		"event_id",
		"event_revision",
		"published_revision",
		"activation_generation",
		"fingerprint",
		"command_id",
	)
	confirmation.Set("csrf_token", requireFrontendCSRF(t, preflight))
	confirmation.Set("action", "activate")
	invalidConfirmation := maps.Clone(confirmation)
	invalidConfirmation.Set("command_id", "")
	invalidActivation := postFrontendForm(
		t,
		administrator,
		server.address,
		path,
		invalidConfirmation,
	)
	if invalidActivation.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalidActivation.body, `role="alert"`) ||
		!strings.Contains(invalidActivation.body, `id="error-summary"`) ||
		!strings.Contains(invalidActivation.body, `name="action" value="activate"`) {
		t.Fatalf(
			"invalid browser Event activation = %d %q",
			invalidActivation.status,
			invalidActivation.body,
		)
	}
	assertAccessibleFormErrors(t, invalidActivation, map[string]string{
		"administration-activation-1-confirmation": "valid command identity",
	})
	correctedConfirmation := frontendActivationFormValues(
		t,
		invalidActivation.body,
		"csrf_token",
		"event_id",
		"event_revision",
		"published_revision",
		"activation_generation",
		"fingerprint",
		"command_id",
	)
	correctedConfirmation.Set("action", "activate")
	activated := postFrontendForm(
		t,
		administrator,
		server.address,
		path,
		correctedConfirmation,
	)
	if activated.status != http.StatusSeeOther {
		t.Fatalf("browser Event activation = %d %q", activated.status, activated.body)
	}
	staleActivation := postFrontendForm(
		t,
		administrator,
		server.address,
		path,
		confirmation,
	)
	if staleActivation.status != http.StatusConflict {
		t.Fatalf(
			"stale browser Event activation = %d %q",
			staleActivation.status,
			staleActivation.body,
		)
	}
	assertAccessibleFormErrors(t, staleActivation, map[string]string{
		"administration-activation-1-confirmation": "Preflight is stale",
	})
	refreshedConfirmation := frontendActivationFormValues(
		t,
		staleActivation.body,
		"csrf_token",
		"event_id",
		"event_revision",
		"published_revision",
		"activation_generation",
		"fingerprint",
		"command_id",
	)
	refreshedConfirmation.Set("action", "activate")
	refreshedActivation := postFrontendForm(
		t,
		administrator,
		server.address,
		path,
		refreshedConfirmation,
	)
	if refreshedActivation.status != http.StatusSeeOther {
		t.Fatalf(
			"refreshed browser Event activation = %d %q",
			refreshedActivation.status,
			refreshedActivation.body,
		)
	}

	dataDir := server.dataDir
	bin := server.bin
	server.stop(t)
	server = startBeamersWithPublicListener(t, bin, dataDir)
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Active Event #1") ||
		!strings.Contains(page.body, "generation 3") {
		t.Fatalf("restarted Active Event = %d %q", page.status, page.body)
	}
	server.stop(t)
}

func TestBrowserOperatesSessionDurably(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	path := "/backstage/events/1/operations"
	operator := provisionOperator(t, administrator, server)
	observer := provisionObserver(t, administrator, server)
	observerPage := getFrontendPage(t, observer, server.address, path)
	if observerPage.status != http.StatusForbidden {
		t.Fatalf("Observer Session operations = %d, want 403", observerPage.status)
	}
	operator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	page := getFrontendPage(t, operator, server.address, path)
	for _, want := range []string{
		"Sessions and Displays",
		"Opening Keynote",
		"Scheduled",
		`name="action" value="start-session"`,
		`name="expected_live_state_revision" value="0"`,
	} {
		if page.status != http.StatusOK || !strings.Contains(page.body, want) {
			t.Fatalf("operations page lacks %q: %d %q", want, page.status, page.body)
		}
	}
	started := postFrontendForm(t, operator, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"start-session"},
		"command_id":                   {"browser-start-keynote"},
		"session_id":                   {strconv.FormatInt(sessionID, 10)},
		"expected_live_state_revision": {"0"},
	})
	if started.status != http.StatusSeeOther || started.header.Get("Location") != path {
		t.Fatalf("browser Start Session = %d %q", started.status, started.body)
	}
	page = getFrontendPage(t, operator, server.address, path)
	if !strings.Contains(page.body, "Live") ||
		!strings.Contains(page.body, `name="action" value="end-session"`) ||
		!strings.Contains(page.body, `name="expected_live_state_revision" value="1"`) {
		t.Fatalf("started browser Session = %d %q", page.status, page.body)
	}
	stale := postFrontendForm(t, operator, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"start-session"},
		"command_id":                   {"browser-stale-start"},
		"session_id":                   {strconv.FormatInt(sessionID, 10)},
		"expected_live_state_revision": {"0"},
	})
	if stale.status != http.StatusConflict ||
		!strings.Contains(stale.body, `role="alert"`) ||
		!strings.Contains(stale.body, "Session changed") {
		t.Fatalf("stale browser Session command = %d %q", stale.status, stale.body)
	}
	assertAccessibleFormErrors(t, stale, nil)

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	server = startBeamersWithPublicListener(t, bin, dataDir)
	page = getFrontendPage(t, operator, server.address, path)
	if page.status != http.StatusOK ||
		!strings.Contains(page.body, "Opening Keynote") ||
		!strings.Contains(page.body, "Live") {
		t.Fatalf("restarted browser Session = %d %q", page.status, page.body)
	}
	ended := postFrontendForm(t, operator, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"end-session"},
		"command_id":                   {"browser-end-keynote"},
		"session_id":                   {strconv.FormatInt(sessionID, 10)},
		"expected_live_state_revision": {"1"},
	})
	if ended.status != http.StatusSeeOther {
		t.Fatalf("browser End Session = %d %q", ended.status, ended.body)
	}
	page = getFrontendPage(t, operator, server.address, path)
	if !strings.Contains(page.body, `data-tone="ended">Ended</span>`) ||
		!strings.Contains(page.body, "revision <code>2</code>") {
		t.Fatalf("ended browser Session = %d %q", page.status, page.body)
	}
	server.stop(t)
}

func TestBrowserPreviewsAdjustsCancelsAndReinstatesSession(t *testing.T) {
	producer, server := startAuthenticatedAdministratorWithPublicListener(t)
	sessionID := prepareActiveSchedule(t, producer, server)
	producer.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	path := "/backstage/events/1/operations"

	page := getFrontendPage(t, producer, server.address, path)
	started := postFrontendForm(t, producer, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"start-session"},
		"command_id":                   {"browser-start-before-adjustment"},
		"session_id":                   {strconv.FormatInt(sessionID, 10)},
		"expected_live_state_revision": {"0"},
	})
	if started.status != http.StatusSeeOther {
		t.Fatalf("start before browser adjustment = %d %q", started.status, started.body)
	}
	page = getFrontendPage(t, producer, server.address, path)
	invalidAdjustment := postFrontendForm(t, producer, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"preview-adjust-target"},
		"session_id":                   {strconv.FormatInt(sessionID, 10)},
		"expected_live_state_revision": {"1"},
		"adjustment":                   {"not-a-duration"},
	})
	if invalidAdjustment.status != http.StatusBadRequest ||
		!strings.Contains(invalidAdjustment.body, `value="not-a-duration"`) {
		t.Fatalf(
			"invalid target adjustment = %d %q",
			invalidAdjustment.status,
			invalidAdjustment.body,
		)
	}
	assertAccessibleFormErrors(t, invalidAdjustment, map[string]string{
		"preview-adjust-target-" + strconv.FormatInt(sessionID, 10) + "-adjustment": "Enter a duration",
	})

	preview := postFrontendForm(t, producer, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"preview-adjust-target"},
		"session_id":                   {strconv.FormatInt(sessionID, 10)},
		"expected_live_state_revision": {"1"},
		"adjustment":                   {"5m"},
	})
	for _, want := range []string{
		"Adjust Target Preview",
		"Hard Boundary",
		`data-timezone="Europe/Berlin"`,
		`name="action" value="adjust-target"`,
		`name="preview_fingerprint"`,
	} {
		if preview.status != http.StatusOK || !strings.Contains(preview.body, want) {
			t.Fatalf("Adjust Target Preview lacks %q: %d %q", want, preview.status, preview.body)
		}
	}
	adjustment := frontendNamedValues(
		preview.body,
		"session_id",
		"expected_live_state_revision",
		"adjustment",
		"preview_fingerprint",
		"command_id",
	)
	adjustment.Set("csrf_token", requireFrontendCSRF(t, preview))
	adjustment.Set("action", "adjust-target")
	unconfirmedAdjustment := postFrontendForm(
		t,
		producer,
		server.address,
		path,
		adjustment,
	)
	if unconfirmedAdjustment.status != http.StatusConflict ||
		!strings.Contains(unconfirmedAdjustment.body, "Adjust Target Preview") ||
		regexp.MustCompile(
			`id="adjust-target-`+strconv.FormatInt(sessionID, 10)+
				`-confirmed"[^>]+checked`,
		).MatchString(unconfirmedAdjustment.body) {
		t.Fatalf(
			"unconfirmed Adjust Target = %d %q",
			unconfirmedAdjustment.status,
			unconfirmedAdjustment.body,
		)
	}
	assertAccessibleFormErrors(t, unconfirmedAdjustment, map[string]string{
		"adjust-target-" + strconv.FormatInt(sessionID, 10) + "-confirmed": "Confirm Adjust Target",
	})

	staleAdjustment := frontendNamedValues(
		unconfirmedAdjustment.body,
		"session_id",
		"expected_live_state_revision",
		"adjustment",
		"preview_fingerprint",
		"command_id",
	)
	staleAdjustment.Set("csrf_token", requireFrontendCSRF(t, unconfirmedAdjustment))
	staleAdjustment.Set("action", "adjust-target")
	staleAdjustment.Set("preview_fingerprint", "stale-target-preview")
	staleAdjustment.Set("confirmed", "true")
	staleAdjustment.Set("hard_boundary_confirmed", "true")
	staleTarget := postFrontendForm(t, producer, server.address, path, staleAdjustment)
	if staleTarget.status != http.StatusConflict ||
		!strings.Contains(staleTarget.body, "Adjust Target Preview") ||
		regexp.MustCompile(
			`id="adjust-target-`+strconv.FormatInt(sessionID, 10)+
				`-confirmed"[^>]+checked`,
		).MatchString(staleTarget.body) {
		t.Fatalf("stale Adjust Target = %d %q", staleTarget.status, staleTarget.body)
	}
	assertAccessibleFormErrors(t, staleTarget, map[string]string{
		"adjust-target-" + strconv.FormatInt(sessionID, 10) + "-confirmed": "review and confirm",
	})

	hardBoundaryAdjustment := frontendNamedValues(
		staleTarget.body,
		"session_id",
		"expected_live_state_revision",
		"adjustment",
		"preview_fingerprint",
		"command_id",
	)
	hardBoundaryAdjustment.Set("csrf_token", requireFrontendCSRF(t, staleTarget))
	hardBoundaryAdjustment.Set("action", "adjust-target")
	hardBoundaryAdjustment.Set("confirmed", "true")
	missingHardBoundary := postFrontendForm(
		t,
		producer,
		server.address,
		path,
		hardBoundaryAdjustment,
	)
	if missingHardBoundary.status != http.StatusConflict ||
		!strings.Contains(missingHardBoundary.body, "Adjust Target Preview") ||
		regexp.MustCompile(
			`id="adjust-target-`+strconv.FormatInt(sessionID, 10)+
				`-hard-boundary-confirmed"[^>]+checked`,
		).MatchString(missingHardBoundary.body) {
		t.Fatalf(
			"unconfirmed Hard Boundary adjustment = %d %q",
			missingHardBoundary.status,
			missingHardBoundary.body,
		)
	}
	assertAccessibleFormErrors(t, missingHardBoundary, map[string]string{
		"adjust-target-" + strconv.FormatInt(sessionID, 10) +
			"-hard-boundary-confirmed": "Confirm Hard Boundary movement",
	})

	adjustment = frontendNamedValues(
		missingHardBoundary.body,
		"session_id",
		"expected_live_state_revision",
		"adjustment",
		"preview_fingerprint",
		"command_id",
	)
	adjustment.Set("csrf_token", requireFrontendCSRF(t, missingHardBoundary))
	adjustment.Set("action", "adjust-target")
	adjustment.Set("confirmed", "true")
	adjustment.Set("hard_boundary_confirmed", "true")
	adjusted := postFrontendForm(t, producer, server.address, path, adjustment)
	if adjusted.status != http.StatusSeeOther {
		t.Fatalf("browser Adjust Target = %d %q", adjusted.status, adjusted.body)
	}

	page = getFrontendPage(t, producer, server.address, path)
	unconfirmedCancellation := postFrontendForm(t, producer, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"cancel-session"},
		"command_id":                   {"browser-unconfirmed-cancellation"},
		"session_id":                   {strconv.FormatInt(sessionID, 10)},
		"expected_live_state_revision": {"2"},
		"public_cancellation_message":  {"Retain this public message."},
		"crew_notes":                   {"Retain these Crew notes."},
	})
	if unconfirmedCancellation.status != http.StatusUnprocessableEntity ||
		!strings.Contains(
			unconfirmedCancellation.body,
			`value="Retain this public message."`,
		) ||
		!strings.Contains(
			unconfirmedCancellation.body,
			">Retain these Crew notes.</textarea>",
		) ||
		regexp.MustCompile(
			`id="cancel-session-`+strconv.FormatInt(sessionID, 10)+
				`-confirmed"[^>]+checked`,
		).MatchString(unconfirmedCancellation.body) {
		t.Fatalf(
			"unconfirmed cancellation = %d %q",
			unconfirmedCancellation.status,
			unconfirmedCancellation.body,
		)
	}
	assertAccessibleFormErrors(t, unconfirmedCancellation, map[string]string{
		"cancel-session-" + strconv.FormatInt(sessionID, 10) + "-confirmed": "Confirm cancellation",
	})

	canceled := postFrontendForm(t, producer, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"cancel-session"},
		"command_id":                   {"browser-cancel-after-adjustment"},
		"session_id":                   {strconv.FormatInt(sessionID, 10)},
		"expected_live_state_revision": {"2"},
		"confirmed":                    {"true"},
		"public_cancellation_message":  {"Speaker delayed."},
		"crew_notes":                   {"Move to the next open placement."},
	})
	if canceled.status != http.StatusSeeOther {
		t.Fatalf("browser Cancel Session = %d %q", canceled.status, canceled.body)
	}
	page = getFrontendPage(t, producer, server.address, path)
	if !strings.Contains(page.body, `data-tone="canceled">Canceled</span>`) ||
		!strings.Contains(page.body, "revision <code>3</code>") ||
		!strings.Contains(page.body, `name="action" value="preview-reinstate"`) {
		t.Fatalf("canceled browser Session = %d %q", page.status, page.body)
	}
	invalidLaneIDs := postFrontendForm(t, producer, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"preview-reinstate"},
		"session_id":                   {strconv.FormatInt(sessionID, 10)},
		"expected_live_state_revision": {"3"},
		"forecast_start":               {"2099-08-21T11:30"},
		"lane_ids":                     {"not-lane-ids"},
		"location_ids":                 {"1"},
	})
	if invalidLaneIDs.status != http.StatusBadRequest ||
		!strings.Contains(invalidLaneIDs.body, `value="not-lane-ids"`) {
		t.Fatalf("invalid Reinstate Lane IDs = %d %q", invalidLaneIDs.status, invalidLaneIDs.body)
	}
	assertAccessibleFormErrors(t, invalidLaneIDs, map[string]string{
		"preview-reinstate-" + strconv.FormatInt(sessionID, 10) + "-lane-ids": "Enter Lane IDs",
	})

	invalidLocationIDs := postFrontendForm(t, producer, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, invalidLaneIDs)},
		"action":                       {"preview-reinstate"},
		"session_id":                   {strconv.FormatInt(sessionID, 10)},
		"expected_live_state_revision": {"3"},
		"forecast_start":               {"2099-08-21T11:30"},
		"lane_ids":                     {"1"},
		"location_ids":                 {"not-location-ids"},
	})
	if invalidLocationIDs.status != http.StatusBadRequest ||
		!strings.Contains(invalidLocationIDs.body, `value="not-location-ids"`) {
		t.Fatalf(
			"invalid Reinstate Location IDs = %d %q",
			invalidLocationIDs.status,
			invalidLocationIDs.body,
		)
	}
	assertAccessibleFormErrors(t, invalidLocationIDs, map[string]string{
		"preview-reinstate-" + strconv.FormatInt(sessionID, 10) + "-location-ids": "Enter Location IDs",
	})

	reinstatePreview := postFrontendForm(t, producer, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, invalidLocationIDs)},
		"action":                       {"preview-reinstate"},
		"session_id":                   {strconv.FormatInt(sessionID, 10)},
		"expected_live_state_revision": {"3"},
		"forecast_start":               {"2099-08-21T11:30"},
		"lane_ids":                     {"1"},
		"location_ids":                 {"1"},
	})
	for _, want := range []string{
		"Reinstate Session Preview",
		"Hard Boundary",
		`name="action" value="reinstate-session"`,
		`name="preview_fingerprint"`,
	} {
		if reinstatePreview.status != http.StatusOK ||
			!strings.Contains(reinstatePreview.body, want) {
			t.Fatalf(
				"Reinstate Session Preview lacks %q: %d %q",
				want,
				reinstatePreview.status,
				reinstatePreview.body,
			)
		}
	}
	reinstatement := frontendNamedValues(
		reinstatePreview.body,
		"session_id",
		"expected_live_state_revision",
		"forecast_start",
		"lane_ids",
		"location_ids",
		"preview_fingerprint",
		"command_id",
	)
	reinstatement.Set("csrf_token", requireFrontendCSRF(t, reinstatePreview))
	reinstatement.Set("action", "reinstate-session")
	unconfirmedReinstatement := postFrontendForm(
		t,
		producer,
		server.address,
		path,
		reinstatement,
	)
	if unconfirmedReinstatement.status != http.StatusConflict ||
		!strings.Contains(unconfirmedReinstatement.body, "Reinstate Session Preview") ||
		regexp.MustCompile(
			`id="reinstate-session-`+strconv.FormatInt(sessionID, 10)+
				`-confirmed"[^>]+checked`,
		).MatchString(unconfirmedReinstatement.body) {
		t.Fatalf(
			"unconfirmed Reinstate Session = %d %q",
			unconfirmedReinstatement.status,
			unconfirmedReinstatement.body,
		)
	}
	assertAccessibleFormErrors(t, unconfirmedReinstatement, map[string]string{
		"reinstate-session-" + strconv.FormatInt(sessionID, 10) + "-confirmed": "Confirm Reinstate",
	})

	staleReinstatement := frontendNamedValues(
		unconfirmedReinstatement.body,
		"session_id",
		"expected_live_state_revision",
		"forecast_start",
		"lane_ids",
		"location_ids",
		"preview_fingerprint",
		"command_id",
	)
	staleReinstatement.Set("csrf_token", requireFrontendCSRF(t, unconfirmedReinstatement))
	staleReinstatement.Set("action", "reinstate-session")
	staleReinstatement.Set("preview_fingerprint", "stale-reinstate-preview")
	staleReinstatement.Set("confirmed", "true")
	staleReinstatement.Set("hard_boundary_confirmed", "true")
	staleReinstate := postFrontendForm(t, producer, server.address, path, staleReinstatement)
	if staleReinstate.status != http.StatusConflict ||
		!strings.Contains(staleReinstate.body, "Reinstate Session Preview") ||
		regexp.MustCompile(
			`id="reinstate-session-`+strconv.FormatInt(sessionID, 10)+
				`-confirmed"[^>]+checked`,
		).MatchString(staleReinstate.body) {
		t.Fatalf("stale Reinstate Session = %d %q", staleReinstate.status, staleReinstate.body)
	}
	assertAccessibleFormErrors(t, staleReinstate, map[string]string{
		"reinstate-session-" + strconv.FormatInt(sessionID, 10) +
			"-confirmed": "review and confirm",
	})

	hardBoundaryReinstatement := frontendNamedValues(
		staleReinstate.body,
		"session_id",
		"expected_live_state_revision",
		"forecast_start",
		"lane_ids",
		"location_ids",
		"preview_fingerprint",
		"command_id",
	)
	hardBoundaryReinstatement.Set("csrf_token", requireFrontendCSRF(t, staleReinstate))
	hardBoundaryReinstatement.Set("action", "reinstate-session")
	hardBoundaryReinstatement.Set("confirmed", "true")
	missingReinstateHardBoundary := postFrontendForm(
		t,
		producer,
		server.address,
		path,
		hardBoundaryReinstatement,
	)
	if missingReinstateHardBoundary.status != http.StatusConflict ||
		!strings.Contains(missingReinstateHardBoundary.body, "Reinstate Session Preview") ||
		regexp.MustCompile(
			`id="reinstate-session-`+strconv.FormatInt(sessionID, 10)+
				`-hard-boundary-confirmed"[^>]+checked`,
		).MatchString(missingReinstateHardBoundary.body) {
		t.Fatalf(
			"unconfirmed Reinstate Hard Boundary = %d %q",
			missingReinstateHardBoundary.status,
			missingReinstateHardBoundary.body,
		)
	}
	assertAccessibleFormErrors(t, missingReinstateHardBoundary, map[string]string{
		"reinstate-session-" + strconv.FormatInt(sessionID, 10) +
			"-hard-boundary-confirmed": "Confirm Hard Boundary movement",
	})

	reinstatement = frontendNamedValues(
		missingReinstateHardBoundary.body,
		"session_id",
		"expected_live_state_revision",
		"forecast_start",
		"lane_ids",
		"location_ids",
		"preview_fingerprint",
		"command_id",
	)
	reinstatement.Set("csrf_token", requireFrontendCSRF(t, missingReinstateHardBoundary))
	reinstatement.Set("action", "reinstate-session")
	reinstatement.Set("confirmed", "true")
	reinstatement.Set("hard_boundary_confirmed", "true")
	reinstated := postFrontendForm(t, producer, server.address, path, reinstatement)
	if reinstated.status != http.StatusSeeOther {
		t.Fatalf("browser Reinstate Session = %d %q", reinstated.status, reinstated.body)
	}
	page = getFrontendPage(t, producer, server.address, path)
	if !strings.Contains(page.body, `data-tone="neutral">Scheduled</span>`) ||
		!strings.Contains(page.body, "revision <code>4</code>") {
		t.Fatalf("reinstated browser Session = %d %q", page.status, page.body)
	}
	server.stop(t)
}

func TestBrowserAdministersDisplaysAndRecovery(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	prepareActiveSchedule(t, administrator, server)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	path := "/backstage/events/1/operations"
	firstCode, _ := prepareBrowserEnrollment(t, server)
	secondCode, _ := prepareBrowserEnrollment(t, server)

	page := getFrontendPage(t, administrator, server.address, path)
	invalidEnrollment := postFrontendForm(
		t,
		administrator,
		server.address,
		path,
		url.Values{
			"csrf_token": {requireFrontendCSRF(t, page)},
			"action":     {"enroll-display"},
			"command_id": {"browser-invalid-display-name"},
			"code":       {firstCode},
			"name":       {""},
		},
	)
	if invalidEnrollment.status != http.StatusBadRequest ||
		strings.Contains(invalidEnrollment.body, `value="`+firstCode+`"`) {
		t.Fatalf(
			"invalid browser Display Enrollment = %d %q",
			invalidEnrollment.status,
			invalidEnrollment.body,
		)
	}
	assertAccessibleFormErrors(t, invalidEnrollment, map[string]string{
		"enroll-display-name": "Enter a Display name",
	})
	page = invalidEnrollment
	invalidRecovery := postFrontendForm(
		t,
		administrator,
		server.address,
		path,
		url.Values{
			"csrf_token": {requireFrontendCSRF(t, page)},
			"action":     {"enroll-display"},
			"command_id": {"browser-invalid-display-recovery"},
			"code":       {firstCode},
			"name":       {"Retain Recovery Display"},
			"display_id": {"not-a-number"},
		},
	)
	if invalidRecovery.status != http.StatusBadRequest ||
		strings.Contains(invalidRecovery.body, `value="`+firstCode+`"`) ||
		!strings.Contains(invalidRecovery.body, `value="Retain Recovery Display"`) ||
		!strings.Contains(invalidRecovery.body, `value="not-a-number"`) {
		t.Fatalf(
			"invalid browser Display recovery = %d %q",
			invalidRecovery.status,
			invalidRecovery.body,
		)
	}
	assertAccessibleFormErrors(t, invalidRecovery, map[string]string{
		"enroll-display-display-id": "Enter a positive Display ID",
	})
	page = invalidRecovery
	enroll := func(code, name, commandID string, displayID int) {
		t.Helper()
		values := url.Values{
			"csrf_token": {requireFrontendCSRF(t, page)},
			"action":     {"enroll-display"},
			"command_id": {commandID},
			"code":       {code},
			"name":       {name},
		}
		if displayID > 0 {
			values.Set("display_id", strconv.Itoa(displayID))
		}
		response := postFrontendForm(t, administrator, server.address, path, values)
		if response.status != http.StatusSeeOther {
			t.Fatalf("browser Display Enrollment = %d %q", response.status, response.body)
		}
		page = getFrontendPage(t, administrator, server.address, path)
	}
	enroll(firstCode, "Main Screen", "browser-enroll-main-display", 0)
	enroll(secondCode, "Side Screen", "browser-enroll-side-display", 0)
	for _, want := range []string{
		"Main Screen",
		"Side Screen",
		"offline",
		`name="action" value="assign-display"`,
	} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("Display operations page lacks %q: %q", want, page.body)
		}
	}
	invalidAssignment := postFrontendForm(
		t,
		administrator,
		server.address,
		path,
		url.Values{
			"csrf_token":         {requireFrontendCSRF(t, page)},
			"action":             {"assign-display"},
			"command_id":         {"browser-invalid-display-assignment"},
			"display_id":         {"1"},
			"location_id":        {"1"},
			"view_key":           {"invalid-view"},
			"display_group_keys": {"retain-stage"},
		},
	)
	if invalidAssignment.status != http.StatusBadRequest ||
		!strings.Contains(invalidAssignment.body, `value="retain-stage"`) {
		t.Fatalf(
			"invalid browser Display Assignment = %d %q",
			invalidAssignment.status,
			invalidAssignment.body,
		)
	}
	assertAccessibleFormErrors(t, invalidAssignment, map[string]string{
		"assign-display-1-view-key": "Choose a valid Display view",
	})
	page = invalidAssignment
	assign := func(displayID int, groups, commandID string) {
		t.Helper()
		response := postFrontendForm(t, administrator, server.address, path, url.Values{
			"csrf_token":         {requireFrontendCSRF(t, page)},
			"action":             {"assign-display"},
			"command_id":         {commandID},
			"display_id":         {strconv.Itoa(displayID)},
			"location_id":        {"1"},
			"view_key":           {"event-overview"},
			"display_group_keys": {groups},
		})
		if response.status != http.StatusSeeOther {
			t.Fatalf("browser Display Assignment = %d %q", response.status, response.body)
		}
		page = getFrontendPage(t, administrator, server.address, path)
	}
	assign(1, "stage", "browser-assign-main-display")
	assign(2, "stage, stream", "browser-assign-side-display")
	for _, want := range []string{
		"Main Hall",
		"event-overview",
		"stage, stream",
		"Applied Event #0",
	} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("assigned Display view lacks %q: %q", want, page.body)
		}
	}

	operator := provisionOperator(t, administrator, server)
	operatorPage := getFrontendPage(t, operator, server.address, path)
	if operatorPage.status != http.StatusOK ||
		!strings.Contains(operatorPage.body, "Main Screen") ||
		strings.Contains(operatorPage.body, `name="action" value="enroll-display"`) ||
		strings.Contains(operatorPage.body, `name="action" value="assign-display"`) {
		t.Fatalf("Display authority crossed Account authority: %d %q", operatorPage.status, operatorPage.body)
	}
	forbidden := postFrontendForm(t, operator, server.address, path, url.Values{
		"csrf_token":  {requireFrontendCSRF(t, operatorPage)},
		"action":      {"assign-display"},
		"command_id":  {"browser-forbidden-display-assignment"},
		"display_id":  {"1"},
		"location_id": {"1"},
		"view_key":    {"stage-timer"},
	})
	if forbidden.status != http.StatusForbidden {
		t.Fatalf("Operator Display Assignment = %d, want 403", forbidden.status)
	}

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	server = startBeamersWithPublicListener(t, bin, dataDir)
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Main Screen") ||
		!strings.Contains(page.body, "stage, stream") {
		t.Fatalf("restarted Display administration = %d %q", page.status, page.body)
	}
	recoveryCode, recoveryCredential := prepareBrowserEnrollment(t, server)
	recovery := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"action":     {"enroll-display"},
		"command_id": {"browser-recover-main-display"},
		"code":       {recoveryCode},
		"display_id": {"1"},
	})
	if recovery.status != http.StatusConflict ||
		!strings.Contains(recovery.body, "active credential") ||
		!strings.Contains(recovery.body, "Existing Display ID for recovery") ||
		!strings.Contains(recovery.body, `value="1"`) ||
		strings.Contains(recovery.body, `value="`+recoveryCode+`"`) {
		t.Fatalf("unsafe Display recovery = %d %q", recovery.status, recovery.body)
	}
	assertAccessibleFormErrors(t, recovery, nil)
	page = getFrontendPage(t, administrator, server.address, path)
	if count := strings.Count(page.body, `data-display-id="`); count != 2 {
		t.Fatalf("Display recovery produced %d identities, want 2: %q", count, page.body)
	}
	if !strings.Contains(page.body, "Main Screen") {
		t.Fatalf("Display recovery lost identity name: %q", page.body)
	}

	dataDir, bin = server.dataDir, server.bin
	server.stop(t)
	database, err := sql.Open("sqlite", filepath.Join(dataDir, "beamers.db"))
	if err != nil {
		t.Fatalf("open restored database: %v", err)
	}
	if _, err = database.ExecContext(
		t.Context(),
		"DELETE FROM display_credentials WHERE display_id = 1",
	); err != nil {
		_ = database.Close()
		t.Fatalf("strip restored Display credential: %v", err)
	}
	if _, err = database.ExecContext(
		t.Context(),
		"DELETE FROM event_grants WHERE account_id = 1 AND event_id = 1",
	); err != nil {
		_ = database.Close()
		t.Fatalf("strip Administrator Event Grant: %v", err)
	}
	if err = database.Close(); err != nil {
		t.Fatalf("close restored database: %v", err)
	}
	server = startBeamersWithPublicListener(t, bin, dataDir)
	page = getFrontendPage(t, administrator, server.address, path)
	if page.status != http.StatusOK ||
		!strings.Contains(page.body, `name="action" value="enroll-display"`) ||
		strings.Contains(page.body, "Opening Keynote") {
		t.Fatalf("Administrator Display authority requires Event Grant: %d %q", page.status, page.body)
	}
	recovered := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"action":     {"enroll-display"},
		"command_id": {"browser-recover-restored-main-display"},
		"code":       {recoveryCode},
		"display_id": {"1"},
	})
	if recovered.status != http.StatusSeeOther {
		t.Fatalf("browser Display recovery = %d %q", recovered.status, recovered.body)
	}
	displayClient := authenticatedClient(t)
	displayURL, err := url.Parse("http://" + server.address + "/display")
	if err != nil {
		t.Fatalf("parse recovered Display URL: %v", err)
	}
	displayClient.Jar.SetCookies(displayURL, []*http.Cookie{
		{Name: "beamers_display", Value: recoveryCredential, Path: "/display"},
		{
			Name: "beamers_display", Value: recoveryCredential,
			Path: "/beamers.display.v1.DisplayService",
		},
	})
	_ = readDisplaySnapshot(t, displayClient, server.address)
	page = getFrontendPage(t, administrator, server.address, path)
	if count := strings.Count(page.body, `data-display-id="`); count != 2 ||
		!strings.Contains(page.body, "Main Screen") {
		t.Fatalf("recovered Display identity was not preserved: %q", page.body)
	}
	server.stop(t)
}

func TestBrowserControlsProgramOutputAndOverrides(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	code, _ := prepareBrowserEnrollment(t, server)
	operationsPath := "/backstage/events/1/operations"
	operations := getFrontendPage(t, administrator, server.address, operationsPath)
	enrolled := postFrontendForm(t, administrator, server.address, operationsPath, url.Values{
		"csrf_token": {requireFrontendCSRF(t, operations)},
		"action":     {"enroll-display"},
		"command_id": {"browser-control-enroll-display"},
		"code":       {code},
		"name":       {"Stage Screen"},
	})
	if enrolled.status != http.StatusSeeOther {
		t.Fatalf("enroll control Display = %d %q", enrolled.status, enrolled.body)
	}
	operations = getFrontendPage(t, administrator, server.address, operationsPath)
	assigned := postFrontendForm(t, administrator, server.address, operationsPath, url.Values{
		"csrf_token":         {requireFrontendCSRF(t, operations)},
		"action":             {"assign-display"},
		"command_id":         {"browser-control-assign-display"},
		"display_id":         {"1"},
		"location_id":        {"1"},
		"view_key":           {"stage-timer"},
		"display_group_keys": {"stage"},
	})
	if assigned.status != http.StatusSeeOther {
		t.Fatalf("assign control Display = %d %q", assigned.status, assigned.body)
	}

	path := "/backstage/events/1/control"
	page := getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{
		"Program Output and Overrides",
		`href="/crew/program/` + strconv.FormatInt(sessionID, 10) + `?event_id=1"`,
		"Previous, Current, Next, Preview, and committed Program Output",
		"Stage Message",
		"Technical Difficulties",
		"Urgent Notice",
		"Emergency Alert",
		"Overlay or Replace",
	} {
		if page.status != http.StatusOK || !strings.Contains(page.body, want) {
			t.Fatalf("control page lacks %q: %d %q", want, page.status, page.body)
		}
	}
	buildVersion := frontendNamedValues(page.body, "build_version").Get("build_version")
	staleControl := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":       {requireFrontendCSRF(t, page)},
		"build_version":    {"obsolete-build"},
		"action":           {"preview-stage-message"},
		"text":             {"Stale control"},
		"target_group_key": {"stage"},
		"duration_seconds": {"300"},
		"emphasis":         {"Normal"},
	})
	if staleControl.status != http.StatusConflict ||
		!strings.Contains(staleControl.body, "reload required") ||
		!strings.Contains(staleControl.body, "Stale control") ||
		staleControl.header.Get("X-Beamers-Build") != buildVersion {
		t.Fatalf("stale Backstage control = %d %q", staleControl.status, staleControl.body)
	}
	assertAccessibleFormErrors(t, staleControl, nil)
	invalidPreview := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":       {requireFrontendCSRF(t, page)},
		"build_version":    {buildVersion},
		"action":           {"preview-stage-message"},
		"text":             {"Retain this message"},
		"duration_seconds": {"300"},
		"emphasis":         {"Normal"},
	})
	for _, want := range []string{
		`role="alert"`,
		`aria-invalid="true"`,
		`aria-describedby="stage-target-group-error"`,
		"Enter a Display Group.",
		"Retain this message",
	} {
		if invalidPreview.status != http.StatusUnprocessableEntity ||
			!strings.Contains(invalidPreview.body, want) {
			t.Fatalf(
				"invalid Override preview lacks %q: %d %q",
				want,
				invalidPreview.status,
				invalidPreview.body,
			)
		}
	}
	assertAccessibleFormErrors(t, invalidPreview, map[string]string{
		"stage-target-group": "Enter a Display Group.",
	})
	invalidTarget := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":       {requireFrontendCSRF(t, page)},
		"build_version":    {buildVersion},
		"action":           {"preview-urgent-notice"},
		"text":             {"Retain the target warning"},
		"target_type":      {"Event"},
		"target_id":        {"7"},
		"target_key":       {"old-group"},
		"presentation":     {"Overlay"},
		"duration_seconds": {"300"},
	})
	for _, want := range []string{
		`aria-describedby="preview-urgent-notice-target-id-error"`,
		`aria-describedby="preview-urgent-notice-target-key-error"`,
		"Leave the numeric ID empty for this target.",
		"Leave the Display Group key empty for this target.",
		"Retain the target warning",
	} {
		if invalidTarget.status != http.StatusUnprocessableEntity ||
			!strings.Contains(invalidTarget.body, want) {
			t.Fatalf("invalid Override target lacks %q: %d %q",
				want, invalidTarget.status, invalidTarget.body)
		}
	}
	assertAccessibleFormErrors(t, invalidTarget, map[string]string{
		"preview-urgent-notice-target-id":  "Leave the numeric ID empty",
		"preview-urgent-notice-target-key": "Leave the Display Group key empty",
	})
	invalidNumber := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":       {requireFrontendCSRF(t, page)},
		"build_version":    {buildVersion},
		"action":           {"preview-urgent-notice"},
		"text":             {"Retain malformed numbers"},
		"target_type":      {"Location"},
		"target_id":        {"not-a-number"},
		"presentation":     {"Overlay"},
		"duration_seconds": {"also-not-a-number"},
	})
	if invalidNumber.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalidNumber.body, `value="not-a-number"`) ||
		!strings.Contains(invalidNumber.body, `value="also-not-a-number"`) {
		t.Fatalf(
			"invalid Override numbers = %d %q",
			invalidNumber.status,
			invalidNumber.body,
		)
	}

	previewAndActivate := func(
		action string,
		label string,
		values url.Values,
		wantPresentation string,
	) url.Values {
		t.Helper()
		page = getFrontendPage(t, administrator, server.address, path)
		values.Set("csrf_token", requireFrontendCSRF(t, page))
		values.Set(
			"build_version",
			frontendNamedValues(page.body, "build_version").Get("build_version"),
		)
		values.Set("action", "preview-"+action)
		preview := postFrontendForm(t, administrator, server.address, path, values)
		for _, want := range []string{
			"Confirm " + label,
			"Resolved Displays",
			"Stage Screen",
			wantPresentation,
			`name="action" value="activate-` + action + `"`,
		} {
			if preview.status != http.StatusOK || !strings.Contains(preview.body, want) {
				t.Fatalf("%s preview lacks %q: %d %q", label, want, preview.status, preview.body)
			}
		}
		confirmation := frontendNamedValues(
			preview.body,
			"command_id",
			"build_version",
			"text",
			"target_group_key",
			"target_type",
			"target_id",
			"target_key",
			"presentation",
			"duration_seconds",
			"until_cleared",
			"emphasis",
			"preview_fingerprint",
		)
		confirmation.Set("csrf_token", requireFrontendCSRF(t, preview))
		confirmation.Set("action", "activate-"+action)
		confirmation.Set("confirmed", "true")
		activated := postFrontendForm(
			t, administrator, server.address, path, confirmation,
		)
		if activated.status != http.StatusSeeOther ||
			activated.header.Get("Location") != path {
			t.Fatalf("activate %s = %d %q", label, activated.status, activated.body)
		}
		return confirmation
	}

	stageConfirmation := previewAndActivate(
		"stage-message",
		"Stage Message",
		url.Values{
			"text":             {"Two minutes remaining"},
			"target_group_key": {"stage"},
			"duration_seconds": {"300"},
			"until_cleared":    {"true"},
			"emphasis":         {"Attention"},
		},
		"Overlay",
	)
	if retried := postFrontendForm(
		t, administrator, server.address, path, stageConfirmation,
	); retried.status != http.StatusSeeOther {
		t.Fatalf("exact Stage Message retry = %d %q", retried.status, retried.body)
	}
	staleActivation := maps.Clone(stageConfirmation)
	staleActivation.Set("build_version", "obsolete-build")
	staleActivationResponse := postFrontendForm(
		t, administrator, server.address, path, staleActivation,
	)
	if staleActivationResponse.status != http.StatusConflict ||
		!strings.Contains(staleActivationResponse.body, "Confirm Stage Message") ||
		!strings.Contains(staleActivationResponse.body, "Two minutes remaining") {
		t.Fatalf(
			"stale Stage Message activation = %d %q",
			staleActivationResponse.status,
			staleActivationResponse.body,
		)
	}
	assertAccessibleFormErrors(t, staleActivationResponse, map[string]string{
		"override-confirmed": "review and confirm this refreshed preview",
	})
	unconfirmed := maps.Clone(stageConfirmation)
	unconfirmed.Del("confirmed")
	if rejected := postFrontendForm(
		t, administrator, server.address, path, unconfirmed,
	); rejected.status != http.StatusUnprocessableEntity ||
		!strings.Contains(rejected.body, `role="alert"`) {
		t.Fatalf("unconfirmed Stage Message = %d %q", rejected.status, rejected.body)
	} else {
		assertAccessibleFormErrors(t, rejected, map[string]string{
			"override-confirmed": "Confirm Stage Message",
		})
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if count := strings.Count(page.body, "<h3>StageMessage</h3>"); count != 1 {
		t.Fatalf("exact Stage Message retry created %d active Overrides: %q", count, page.body)
	}
	clearStage := frontendNamedValues(
		page.body,
		"override_id",
		"expected_revision",
		"command_id",
		"build_version",
	)
	clearStage.Set("csrf_token", requireFrontendCSRF(t, page))
	clearStage.Set("action", "clear")
	unconfirmedClear := postFrontendForm(
		t, administrator, server.address, path, clearStage,
	)
	if unconfirmedClear.status != http.StatusUnprocessableEntity {
		t.Fatalf(
			"unconfirmed Stage Message clear = %d %q",
			unconfirmedClear.status,
			unconfirmedClear.body,
		)
	}
	assertAccessibleFormErrors(t, unconfirmedClear, map[string]string{
		"override-clear-" + clearStage.Get("override_id") + "-confirmed": "Confirm this action",
	})

	previewAndActivate(
		"technical-difficulties",
		"Technical Difficulties",
		url.Values{
			"text":             {"Please stand by"},
			"target_group_key": {"stage"},
			"duration_seconds": {"300"},
			"until_cleared":    {"true"},
		},
		"Replace",
	)
	previewAndActivate(
		"urgent-notice",
		"Urgent Notice",
		url.Values{
			"text":             {"Venue closes in ten minutes"},
			"target_type":      {"Event"},
			"target_id":        {"0"},
			"presentation":     {"Overlay"},
			"duration_seconds": {"300"},
		},
		"Overlay",
	)
	emergencyPath := "/crew/events/1/emergency-alerts/confirmation"
	invalidEmergency := getFrontendPage(
		t,
		administrator,
		server.address,
		emergencyPath+"?"+url.Values{
			"text":        {"Retain invalid Emergency Alert"},
			"target_type": {"Location"},
			"target_id":   {"not-a-number"},
		}.Encode(),
	)
	if invalidEmergency.status != http.StatusBadRequest ||
		!strings.Contains(invalidEmergency.body, "Retain invalid Emergency Alert") ||
		!strings.Contains(invalidEmergency.body, `value="not-a-number"`) {
		t.Fatalf(
			"invalid Emergency Alert preview = %d %q",
			invalidEmergency.status,
			invalidEmergency.body,
		)
	}
	assertAccessibleFormErrors(t, invalidEmergency, map[string]string{
		"emergency-target-id": "positive numeric target ID",
	})
	emergencyPreview := getFrontendPage(
		t,
		administrator,
		server.address,
		emergencyPath+"?"+url.Values{
			"text":        {"Evacuate using marked exits"},
			"target_type": {"Event"},
			"target_id":   {"0"},
		}.Encode(),
	)
	for _, want := range []string{
		"Confirm Emergency Alert",
		"Stage Screen",
		"Evacuate using marked exits",
		`name="preview_fingerprint"`,
		`href="/assets/frontend.css"`,
	} {
		if emergencyPreview.status != http.StatusOK ||
			!strings.Contains(emergencyPreview.body, want) {
			t.Fatalf(
				"Emergency Alert preview lacks %q: %d %q",
				want,
				emergencyPreview.status,
				emergencyPreview.body,
			)
		}
	}
	emergencyConfirmation := frontendNamedValues(
		emergencyPreview.body,
		"target_type",
		"target_id",
		"target_key",
		"text",
		"preview_fingerprint",
		"command_id",
		"build_version",
	)
	emergencyConfirmation.Set("confirmation_method", "Keyboard")
	staleEmergency := maps.Clone(emergencyConfirmation)
	staleEmergency.Set("build_version", "obsolete-build")
	staleEmergencyResponse := postFrontendForm(
		t, administrator, server.address, emergencyPath, staleEmergency,
	)
	if staleEmergencyResponse.status != http.StatusConflict ||
		staleEmergencyResponse.header.Get("X-Beamers-Build") != buildVersion ||
		!strings.Contains(staleEmergencyResponse.body, "Evacuate using marked exits") ||
		!strings.Contains(staleEmergencyResponse.body, `name="confirmation_method" value="Keyboard"`) {
		t.Fatalf(
			"stale Emergency Alert = %d %q",
			staleEmergencyResponse.status,
			staleEmergencyResponse.body,
		)
	}
	assertAccessibleFormErrors(t, staleEmergencyResponse, map[string]string{
		"emergency-alert-confirmation": "review this refreshed Emergency Alert",
	})
	emergencyConfirmation = frontendNamedValues(
		staleEmergencyResponse.body,
		"target_type",
		"target_id",
		"target_key",
		"text",
		"preview_fingerprint",
		"command_id",
		"build_version",
	)
	emergencyConfirmation.Set("confirmation_method", "Keyboard")
	emergency := postFrontendForm(
		t, administrator, server.address, emergencyPath, emergencyConfirmation,
	)
	if emergency.status != http.StatusSeeOther ||
		emergency.header.Get("Location") != path {
		t.Fatalf("activate Emergency Alert = %d %q", emergency.status, emergency.body)
	}

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	server = startBeamersWithPublicListener(t, bin, dataDir)
	page = getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{
		"Two minutes remaining",
		"Please stand by",
		"Venue closes in ten minutes",
		"Evacuate using marked exits",
		"Review and clear Emergency Alert",
	} {
		if page.status != http.StatusOK || !strings.Contains(page.body, want) {
			t.Fatalf("recovered control page lacks %q: %d %q", want, page.status, page.body)
		}
	}
	clearPath := regexp.MustCompile(
		`/crew/events/1/overrides/\d+/clear-confirmation`,
	).FindString(page.body)
	if clearPath == "" {
		t.Fatalf("Emergency Alert clear confirmation link missing: %q", page.body)
	}
	clearPreview := getFrontendPage(t, administrator, server.address, clearPath)
	if !strings.Contains(clearPreview.body, `href="/assets/frontend.css"`) {
		t.Fatalf("Emergency clear confirmation lacks Frontend styles: %q", clearPreview.body)
	}
	clearConfirmation := frontendNamedValues(
		clearPreview.body,
		"expected_revision",
		"command_id",
		"build_version",
	)
	clearConfirmation.Set("confirmation_method", "Keyboard")
	staleClear := maps.Clone(clearConfirmation)
	staleClear.Set("build_version", "obsolete-build")
	staleClearResponse := postFrontendForm(
		t, administrator, server.address, clearPath, staleClear,
	)
	if staleClearResponse.status != http.StatusConflict ||
		staleClearResponse.header.Get("X-Beamers-Build") != buildVersion ||
		!strings.Contains(staleClearResponse.body, "Evacuate using marked exits") ||
		!strings.Contains(staleClearResponse.body, `name="confirmation_method" value="Keyboard"`) {
		t.Fatalf(
			"stale Emergency clear = %d %q",
			staleClearResponse.status,
			staleClearResponse.body,
		)
	}
	assertAccessibleFormErrors(t, staleClearResponse, map[string]string{
		"emergency-clear-confirmation": "review this refreshed Emergency Alert clear",
	})
	clearConfirmation = frontendNamedValues(
		staleClearResponse.body,
		"expected_revision",
		"command_id",
		"build_version",
	)
	clearConfirmation.Set("confirmation_method", "Keyboard")
	cleared := postFrontendForm(
		t, administrator, server.address, clearPath, clearConfirmation,
	)
	if clearPreview.status != http.StatusOK ||
		cleared.status != http.StatusSeeOther ||
		cleared.header.Get("Location") != path {
		t.Fatalf(
			"clear Emergency Alert = preview %d, clear %d %q",
			clearPreview.status,
			cleared.status,
			cleared.body,
		)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if strings.Contains(page.body, "Evacuate using marked exits") {
		t.Fatalf("cleared Emergency Alert remained active: %q", page.body)
	}
	prizegivingID := prepareBrowserPrizegiving(t, administrator, server)
	programClient := programv1connect.NewProgramControlServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	current, err := programClient.GetProgramChannel(
		t.Context(),
		connect.NewRequest(&programv1.GetProgramChannelRequest{
			EventId: 1, SessionId: prizegivingID,
		}),
	)
	if err != nil {
		t.Fatalf("load browser Prizegiving Program Channel: %v", err)
	}
	claimed, err := programClient.ChangeControl(
		t.Context(),
		connect.NewRequest(&programv1.ChangeControlRequest{
			EventId: 1, SessionId: prizegivingID,
			Action:                       programv1.ControlAction_CONTROL_ACTION_CLAIM,
			CommandId:                    "claim-browser-prizegiving-check",
			ExpectedControlStateRevision: current.Msg.GetChannel().GetControlStateRevision(),
		}),
	)
	if err != nil {
		t.Fatalf("claim browser Prizegiving Program Channel: %v", err)
	}
	taken, err := programClient.Take(
		t.Context(),
		connect.NewRequest(&programv1.TakeRequest{
			EventId: 1, SessionId: prizegivingID,
			CommandId:                    "take-browser-prizegiving-check",
			ExpectedLiveStateRevision:    claimed.Msg.GetChannel().GetLiveStateRevision(),
			ExpectedControlStateRevision: claimed.Msg.GetChannel().GetControlStateRevision(),
			Preview:                      claimed.Msg.GetChannel().GetPreview(),
		}),
	)
	if err != nil || taken.Msg.GetChannel().GetProgramOutput().GetResult() == nil {
		t.Fatalf("take browser Prizegiving Result: %+v, %v", taken, err)
	}
	beginBrowserReveal(t, administrator, server, prizegivingID)
	page = getFrontendPage(t, administrator, server.address, path)
	if prizegivingID <= 0 ||
		!strings.Contains(
			page.body,
			`/crew/program/`+strconv.FormatInt(prizegivingID, 10)+`?event_id=1`,
		) {
		t.Fatalf("browser Prizegiving Program Channel missing: %d %q", prizegivingID, page.body)
	}
	if public := getFrontendPage(
		t, authenticatedClient(t), server.publicAddress, path,
	); public.status != http.StatusNotFound {
		t.Fatalf("public-listener control page = %d, want 404", public.status)
	}
	server.stop(t)
}

func TestBrowserPlansAndPublishesEvent(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	const path = "/backstage/events/1/planning"
	newEvent := getFrontendPage(t, administrator, server.address, "/backstage/events/new")
	if !regexp.MustCompile(`name="grant_self" value="true" checked`).MatchString(newEvent.body) {
		t.Fatalf("first Event Producer self-grant is not checked: %q", newEvent.body)
	}
	createdEvent := postFrontendForm(
		t,
		administrator,
		server.address,
		"/backstage/events/new",
		url.Values{
			"csrf_token":                        {requireFrontendCSRF(t, newEvent)},
			"command_id":                        {frontendNamedValues(newEvent.body, "command_id").Get("command_id")},
			"grant_command_id":                  {frontendNamedValues(newEvent.body, "grant_command_id").Get("grant_command_id")},
			"event_name":                        {"Revision 2026"},
			"planned_start_date":                {"2026-08-21"},
			"planned_end_date":                  {"2026-08-23"},
			"timezone":                          {"Europe/Berlin"},
			"event_locale":                      {"de-DE"},
			"content_language":                  {"en-GB"},
			"event_day_boundary":                {"06:00"},
			"entry_default_disposition":         {"Pending"},
			"target_adjustment_presets_seconds": {"-300,300,600"},
			"grant_self":                        {"true"},
		},
	)
	if createdEvent.status != http.StatusSeeOther ||
		createdEvent.header.Get("Location") != path {
		t.Fatalf("browser Event creation = %d %q", createdEvent.status, createdEvent.body)
	}
	laterEvent := getFrontendPage(t, administrator, server.address, "/backstage/events/new")
	if regexp.MustCompile(`name="grant_self" value="true" checked`).MatchString(laterEvent.body) {
		t.Fatalf("later Event Producer self-grant remains checked: %q", laterEvent.body)
	}
	administration := getFrontendPage(
		t,
		administrator,
		server.address,
		"/backstage/administration",
	)
	if !strings.Contains(administration.body, "CreateEventGrant") ||
		!strings.Contains(administration.body, "EventGrant #1:1") {
		t.Fatalf("first Event self-grant Audit Entry = %d %q", administration.status, administration.body)
	}
	page := getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{
		"Plan and publish",
		`name="location_name"`,
		`name="csv_data"`,
		`name="icalendar_data"`,
		"<dt>Draft revision</dt><dd>0</dd>",
		"<dt>Published revision</dt><dd>0</dd>",
	} {
		if page.status != http.StatusOK || !strings.Contains(page.body, want) {
			t.Fatalf("planning page lacks %q: %d %q", want, page.status, page.body)
		}
	}

	settingsPath := "/backstage/events/1/settings"
	settingsPage := getFrontendPage(t, administrator, server.address, settingsPath)
	configured := postFrontendForm(t, administrator, server.address, settingsPath, url.Values{
		"csrf_token":                        {requireFrontendCSRF(t, settingsPage)},
		"command_id":                        {"browser-update-event"},
		"expected_event_revision":           {"1"},
		"event_name":                        {"Revision Browser"},
		"planned_start_date":                {"2026-08-21"},
		"planned_end_date":                  {"2026-08-23"},
		"timezone":                          {"Europe/Berlin"},
		"event_locale":                      {"de-DE"},
		"content_language":                  {"en-GB"},
		"event_day_boundary":                {"06:00"},
		"entry_default_disposition":         {"Included"},
		"submission_eligibility":            {"AllAccounts"},
		"voting_method":                     {"Range1To5"},
		"self_vote_policy":                  {"Allowed"},
		"target_adjustment_presets_seconds": {"-300,300,600"},
	})
	if configured.status != http.StatusSeeOther || configured.header.Get("Location") != settingsPath {
		t.Fatalf("configure Event = %d %q", configured.status, configured.body)
	}

	page = getFrontendPage(t, administrator, server.address, path)
	invalidDraft := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":              {requireFrontendCSRF(t, page)},
		"action":                  {"draft"},
		"command_id":              {"browser-invalid-rundown"},
		"expected_draft_revision": {"0"},
		"location_name":           {"Safe Hall"},
		"lane_name":               {"Safe Stage"},
		"track_name":              {"Safe Track"},
		"session_title":           {strings.Repeat("x", 201)},
		"session_type":            {"Ceremony"},
		"audience_visibility":     {"Public"},
		"planned_start":           {"2026-08-21T10:00"},
		"planned_end":             {"2026-08-21T10:30"},
		"timing_policy":           {"FixedEnd"},
		"minimum_duration":        {"15m"},
		"start_boundary":          {"Hard"},
		"end_boundary":            {"Soft"},
	})
	if invalidDraft.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalidDraft.body, `name="location_name" value="Safe Hall"`) {
		t.Fatalf("invalid Draft structure = %d %q", invalidDraft.status, invalidDraft.body)
	}
	assertAccessibleFormErrors(t, invalidDraft, map[string]string{
		"draft-session-title": "must be 1 to 200 characters without control characters",
	})

	created := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":              {requireFrontendCSRF(t, invalidDraft)},
		"action":                  {"draft"},
		"command_id":              {"browser-create-rundown"},
		"expected_draft_revision": {"0"},
		"location_name":           {"Hall A"},
		"lane_name":               {"Main Stage"},
		"track_name":              {"Demos"},
		"session_title":           {"Opening Ceremony"},
		"session_type":            {"Ceremony"},
		"audience_visibility":     {"Public"},
		"planned_start":           {"2026-08-21T10:00"},
		"planned_end":             {"2026-08-21T10:30"},
		"timing_policy":           {"FixedEnd"},
		"minimum_duration":        {"15m"},
		"start_boundary":          {"Hard"},
		"end_boundary":            {"Soft"},
	})
	if created.status != http.StatusSeeOther {
		t.Fatalf("create Draft structure = %d %q", created.status, created.body)
	}
	if anonymous := getFrontendPage(
		t, authenticatedClient(t), server.publicAddress, path,
	); anonymous.status != http.StatusNotFound {
		t.Fatalf("public-listener planning = %d, want 404", anonymous.status)
	}

	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "<dt>Draft revision</dt><dd>1</dd>") ||
		!strings.Contains(page.body, "Opening Ceremony") {
		t.Fatalf("reviewable Draft = %d %q", page.status, page.body)
	}
	invalidSessionTime := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":              {requireFrontendCSRF(t, page)},
		"action":                  {"draft"},
		"command_id":              {"browser-invalid-session-time"},
		"expected_draft_revision": {"1"},
		"session_id":              {"1"},
		"base_session": {html.UnescapeString(
			frontendNamedValues(page.body, "base_session").Get("base_session"),
		)},
		"session_title":       {"Opening Ceremony"},
		"session_type":        {"Ceremony"},
		"audience_visibility": {"Public"},
		"planned_start":       {"not-a-time"},
		"planned_end":         {"2026-08-21T10:30"},
		"timing_policy":       {"FixedEnd"},
		"minimum_duration":    {"15m"},
		"start_boundary":      {"Hard"},
		"end_boundary":        {"Soft"},
	})
	if invalidSessionTime.status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid materialized Session time = %d %q",
			invalidSessionTime.status, invalidSessionTime.body)
	}
	assertAccessibleFormErrors(t, invalidSessionTime, nil)
	sessionStartFieldID := "session-1-planned-start"
	sessionStartZoneID := sessionStartFieldID + "-zone"
	wantDescribedBy := `aria-describedby="` + sessionStartZoneID + " " + sessionStartFieldID + `-error"`
	if !strings.Contains(invalidSessionTime.body, `id="`+sessionStartFieldID+`-error"`) ||
		!strings.Contains(invalidSessionTime.body, wantDescribedBy) {
		t.Fatalf(
			"invalid materialized Session time lacks merged aria-describedby %q: %q",
			wantDescribedBy, invalidSessionTime.body,
		)
	}
	if got := strings.Count(
		invalidSessionTime.body,
		`id="`+sessionStartFieldID+`"`,
	); got != 1 {
		t.Fatalf("invalid materialized Session time has %d %q inputs, want 1",
			got, sessionStartFieldID)
	}
	stale := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":              {requireFrontendCSRF(t, page)},
		"action":                  {"draft"},
		"command_id":              {"browser-stale-rundown"},
		"expected_draft_revision": {"0"},
		"location_name":           {"Stale Hall"},
	})
	if stale.status != http.StatusConflict ||
		!strings.Contains(stale.body, `role="alert"`) ||
		!strings.Contains(stale.body, "Draft changed") ||
		!strings.Contains(stale.body, `name="location_name" value="Stale Hall"`) {
		t.Fatalf("stale Draft response = %d %q", stale.status, stale.body)
	}
	assertAccessibleFormErrors(t, stale, nil)

	preview := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"action":     {"publish-preview"},
		"change_id":  {"4"},
	})
	if preview.status != http.StatusOK ||
		!strings.Contains(preview.body, "Confirm publish") ||
		!strings.Contains(preview.body, "<dt>Draft revision</dt><dd>1</dd>") ||
		!strings.Contains(preview.body, "<dt>Published revision</dt><dd>0</dd>") ||
		!strings.Contains(preview.body, "Automatically included dependency") ||
		!strings.Contains(preview.body, "Affected Lanes: Lane #1") ||
		!strings.Contains(preview.body, "Affected Displays: none currently assigned") {
		t.Fatalf("Publish Preview = %d %q", preview.status, preview.body)
	}
	staleConfirmation := frontendNamedValues(preview.body,
		"draft_revision", "published_revision", "fingerprint", "change_id",
	)
	staleConfirmation.Set("csrf_token", requireFrontendCSRF(t, preview))
	staleConfirmation.Set("action", "publish")
	staleConfirmation.Set("command_id", "browser-stale-publish")
	staleConfirmation.Set("publish_note", "Safe publish note")
	deferred := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":              {requireFrontendCSRF(t, page)},
		"action":                  {"draft"},
		"command_id":              {"browser-deferred-track"},
		"expected_draft_revision": {"1"},
		"track_name":              {"Deferred Track"},
	})
	if deferred.status != http.StatusSeeOther {
		t.Fatalf("create deferred Draft work = %d %q", deferred.status, deferred.body)
	}
	stalePublish := postFrontendForm(
		t, administrator, server.address, path, staleConfirmation,
	)
	if stalePublish.status != http.StatusConflict ||
		!strings.Contains(stalePublish.body, "Publish Preview is stale") ||
		!strings.Contains(stalePublish.body, "Confirm publish") ||
		!strings.Contains(stalePublish.body, "Safe publish note") {
		t.Fatalf("stale Publish = %d %q", stalePublish.status, stalePublish.body)
	}
	assertAccessibleFormErrors(t, stalePublish, nil)
	confirmation := frontendNamedValues(
		stalePublish.body,
		"draft_revision", "published_revision", "fingerprint", "change_id",
	)
	confirmation.Set("csrf_token", requireFrontendCSRF(t, stalePublish))
	confirmation.Set("action", "publish")
	confirmation.Set("command_id", "browser-publish-rundown")
	published := postFrontendForm(t, administrator, server.address, path, confirmation)
	if published.status != http.StatusSeeOther {
		t.Fatalf("Publish = %d %q", published.status, published.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "<dt>Published revision</dt><dd>1</dd>") ||
		!strings.Contains(page.body, "Opening Ceremony") ||
		!strings.Contains(page.body, "Deferred Track") {
		t.Fatalf("Published planning page = %d %q", page.status, page.body)
	}
	edited := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":              {requireFrontendCSRF(t, page)},
		"action":                  {"draft"},
		"command_id":              {"browser-edit-location"},
		"expected_draft_revision": {"3"},
		"location_id":             {"1"},
		"location_name":           {"Hall Alpha"},
		"base_location_name":      {"Hall A"},
	})
	if edited.status != http.StatusSeeOther {
		t.Fatalf("edit materialized Draft = %d %q", edited.status, edited.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "<dt>Draft revision</dt><dd>4</dd>") ||
		!strings.Contains(page.body, `value="Hall Alpha"`) {
		t.Fatalf("edited materialized Draft = %d %q", page.status, page.body)
	}
	publicSchedule := getFrontendPage(
		t, authenticatedClient(t), server.publicAddress, "/events/beamconf-2099/schedule",
	)
	for _, private := range []string{"Opening Ceremony", "Deferred Track", "Hall Alpha"} {
		if strings.Contains(publicSchedule.body, private) {
			t.Fatalf("public Schedule disclosed %q: %q", private, publicSchedule.body)
		}
	}

	const csvMappings = "kind=record_type\nkey=external_key\ntitle=title\nstart=planned_start\nend=planned_end\nlane=lane\nlocation=location"
	const csvData = "kind,key,title,start,end,lane,location\nSession,browser-session,Imported Session,2026-08-21T11:00:00+02:00,2026-08-21T11:30:00+02:00,Main Stage,Hall Alpha\n"
	invalidCSV := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":   {requireFrontendCSRF(t, page)},
		"action":       {"csv-preview"},
		"csv_mappings": {"not-a-mapping"},
		"csv_data":     {csvData},
	})
	if invalidCSV.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalidCSV.body, csvData) {
		t.Fatalf("invalid CSV preview = %d %q", invalidCSV.status, invalidCSV.body)
	}
	assertAccessibleFormErrors(t, invalidCSV, map[string]string{
		"csv-mappings": "must use one source=target mapping per line",
	})
	csvPreview := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":   {requireFrontendCSRF(t, page)},
		"action":       {"csv-preview"},
		"csv_mappings": {csvMappings},
		"csv_data":     {csvData},
	})
	if csvPreview.status != http.StatusOK ||
		!strings.Contains(csvPreview.body, "Imported Session") ||
		!strings.Contains(csvPreview.body, "<details open><summary>CSV import") ||
		!strings.Contains(csvPreview.body, "Confirm CSV import") {
		t.Fatalf("CSV preview = %d %q", csvPreview.status, csvPreview.body)
	}
	csvConfirmation := frontendNamedValues(
		csvPreview.body,
		"expected_draft_revision",
		"fingerprint",
	)
	csvConfirmation["proposal_id"] = frontendCheckboxValues(csvPreview.body, "proposal_id")
	csvConfirmation.Set("csrf_token", requireFrontendCSRF(t, csvPreview))
	csvConfirmation.Set("action", "csv-import")
	csvConfirmation.Set("command_id", "browser-import-csv")
	csvConfirmation.Set("csv_mappings", csvMappings)
	csvConfirmation.Set("csv_data", csvData)
	invalidCSVConfirmation := csvConfirmation
	invalidCSVConfirmation.Del("proposal_id")
	invalidCSVConfirmation.Set("command_id", "browser-empty-import-csv")
	emptyCSV := postFrontendForm(
		t, administrator, server.address, path, invalidCSVConfirmation,
	)
	if emptyCSV.status != http.StatusUnprocessableEntity ||
		!strings.Contains(emptyCSV.body, csvData) {
		t.Fatalf("empty CSV selection = %d %q", emptyCSV.status, emptyCSV.body)
	}
	assertAccessibleFormErrors(t, emptyCSV, map[string]string{
		"csv-proposal-ids": "select at least one proposal",
	})
	csvConfirmation = frontendNamedValues(
		emptyCSV.body,
		"expected_draft_revision",
		"fingerprint",
	)
	csvConfirmation["proposal_id"] = frontendCheckboxOptions(emptyCSV.body, "proposal_id")
	csvConfirmation.Set("csrf_token", requireFrontendCSRF(t, emptyCSV))
	csvConfirmation.Set("action", "csv-import")
	csvConfirmation.Set("command_id", "browser-import-csv")
	csvConfirmation.Set("csv_mappings", csvMappings)
	csvConfirmation.Set("csv_data", csvData)
	if imported := postFrontendForm(
		t, administrator, server.address, path, csvConfirmation,
	); imported.status != http.StatusSeeOther {
		t.Fatalf("CSV import = %d %q", imported.status, imported.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "<dt>Draft revision</dt><dd>5</dd>") ||
		!strings.Contains(page.body, "Imported Session") {
		t.Fatalf("CSV Draft = %d %q", page.status, page.body)
	}

	const icalendarData = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:browser-ical\r\nDTSTART:20260821T120000Z\r\nDTEND:20260821T123000Z\r\nSUMMARY:iCalendar Session\r\nLOCATION:Main Stage\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	icalendarPreview := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":     {requireFrontendCSRF(t, page)},
		"action":         {"icalendar-preview"},
		"icalendar_data": {icalendarData},
	})
	if icalendarPreview.status != http.StatusOK ||
		!strings.Contains(icalendarPreview.body, "iCalendar Session") ||
		!strings.Contains(icalendarPreview.body, "<details open><summary>iCalendar import") ||
		!strings.Contains(icalendarPreview.body, "Confirm iCalendar import") {
		t.Fatalf("iCalendar preview = %d %q", icalendarPreview.status, icalendarPreview.body)
	}
	icalendarConfirmation := frontendNamedValues(
		icalendarPreview.body,
		"expected_draft_revision",
		"fingerprint",
	)
	icalendarConfirmation["proposal_id"] = frontendCheckboxValues(
		icalendarPreview.body,
		"proposal_id",
	)
	icalendarConfirmation.Set("csrf_token", requireFrontendCSRF(t, icalendarPreview))
	icalendarConfirmation.Set("action", "icalendar-import")
	icalendarConfirmation.Set("command_id", "browser-import-icalendar")
	icalendarConfirmation.Set("icalendar_data", icalendarData)
	invalidICalendarConfirmation := icalendarConfirmation
	invalidICalendarConfirmation.Del("proposal_id")
	invalidICalendarConfirmation.Set("command_id", "browser-empty-import-icalendar")
	emptyICalendar := postFrontendForm(
		t, administrator, server.address, path, invalidICalendarConfirmation,
	)
	if emptyICalendar.status != http.StatusUnprocessableEntity ||
		!strings.Contains(emptyICalendar.body, icalendarData) {
		t.Fatalf("empty iCalendar selection = %d %q", emptyICalendar.status, emptyICalendar.body)
	}
	assertAccessibleFormErrors(t, emptyICalendar, map[string]string{
		"icalendar-proposal-ids": "select at least one proposal",
	})
	icalendarConfirmation = frontendNamedValues(
		emptyICalendar.body,
		"expected_draft_revision",
		"fingerprint",
	)
	icalendarConfirmation["proposal_id"] = frontendCheckboxOptions(
		emptyICalendar.body,
		"proposal_id",
	)
	icalendarConfirmation.Set("csrf_token", requireFrontendCSRF(t, emptyICalendar))
	icalendarConfirmation.Set("action", "icalendar-import")
	icalendarConfirmation.Set("command_id", "browser-import-icalendar")
	icalendarConfirmation.Set("icalendar_data", icalendarData)
	if imported := postFrontendForm(
		t, administrator, server.address, path, icalendarConfirmation,
	); imported.status != http.StatusSeeOther {
		t.Fatalf("iCalendar import = %d %q", imported.status, imported.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "<dt>Draft revision</dt><dd>6</dd>") ||
		!strings.Contains(page.body, "iCalendar Session") {
		t.Fatalf("iCalendar Draft = %d %q", page.status, page.body)
	}
	server.stop(t)
}

func TestBrowserManagesCompetitionEntries(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	path := "/backstage/events/1/competitions/" +
		strconv.FormatInt(competitionID, 10) + "/entries"

	page := getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{
		"Competition Entries and Attachments",
		`<html lang="en-GB" data-locale="en-GB">`,
		`href="/backstage/events/1/sessions"`,
		"Submission Deadline",
		"2099-08-21 13:30 CEST",
		`src="/assets/event-time.js"`,
		"Start preflight",
		`name="entry_name"`,
	} {
		if page.status != http.StatusOK || !strings.Contains(page.body, want) {
			t.Fatalf("Competition Entries page lacks %q: %d %q", want, page.status, page.body)
		}
	}
	planning := getFrontendPage(
		t,
		administrator,
		server.address,
		"/backstage/events/1/planning",
	)
	if !strings.Contains(planning.body, `href="`+path+`"`) ||
		!strings.Contains(planning.body, "Manage Demo Competition") {
		t.Fatalf("published Competition lacks Entries route: %d %q", planning.status, planning.body)
	}
	unscopedOperator := provisionOperatorWithLanes(t, administrator, server, nil)
	if denied := getFrontendPage(
		t, unscopedOperator, server.address, path,
	); denied.status != http.StatusNotFound {
		t.Fatalf("unscoped Operator Competition Entries = %d %q", denied.status, denied.body)
	}
	if denied := postFrontendForm(
		t,
		unscopedOperator,
		server.address,
		path,
		url.Values{
			"action":            {"close-reopen-window"},
			"window_id":         {"1"},
			"expected_revision": {"1"},
			"confirm_close":     {"true"},
		},
	); denied.status != http.StatusNotFound {
		t.Fatalf("unscoped Operator Reopen Window update = %d %q", denied.status, denied.body)
	}

	invalidEntryDetails := strings.Repeat("d", 10001)
	invalidEntry := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":     {requireFrontendCSRF(t, page)},
		"action":         {"create-entry"},
		"command_id":     {"browser-invalid-entry"},
		"entry_name":     {""},
		"public_details": {invalidEntryDetails},
		"crew_notes":     {"Safe Crew note"},
	})
	if invalidEntry.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalidEntry.body, invalidEntryDetails) {
		t.Fatalf("invalid Entry creation = %d %q", invalidEntry.status, invalidEntry.body)
	}
	assertAccessibleFormErrors(t, invalidEntry, map[string]string{
		"create-entry-entry-name":     "Enter an Entry name.",
		"create-entry-public-details": "Enter no more than 10000 characters.",
	})

	created := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":     {requireFrontendCSRF(t, invalidEntry)},
		"action":         {"create-entry"},
		"command_id":     {"browser-create-entry"},
		"entry_name":     {"Project Aurora"},
		"public_details": {"A public abstract"},
		"crew_notes":     {"Crew-only staging note"},
	})
	if created.status != http.StatusSeeOther || created.header.Get("Location") != path {
		t.Fatalf("browser Entry creation = %d %q", created.status, created.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{
		"Project Aurora",
		"A public abstract",
		"Crew-only staging note",
		`name="action" value="review-entry"`,
		`name="action" value="change-disposition"`,
	} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("created Entry page lacks %q: %q", want, page.body)
		}
	}
	if !strings.Contains(page.body, "missing_file_delivery") ||
		!strings.Contains(page.body, `role="alert"`) {
		t.Fatalf("accessible required-file preflight = %d %q", page.status, page.body)
	}
	if strings.Contains(page.body, "sha256/") {
		t.Fatalf("Backstage exposed Attachment storage path: %q", page.body)
	}

	configured := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                  {requireFrontendCSRF(t, page)},
		"action":                      {"configure-readiness"},
		"command_id":                  {"browser-configure-readiness"},
		"expected_readiness_revision": {"0"},
		"require_entry_review":        {"true"},
	})
	if configured.status != http.StatusSeeOther {
		t.Fatalf("configure Competition readiness = %d %q", configured.status, configured.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "unresolved_entry_review") ||
		!strings.Contains(page.body, `role="alert"`) {
		t.Fatalf("accessible review preflight = %d %q", page.status, page.body)
	}

	reviewed := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"review-entry"},
		"command_id":        {"browser-review-entry"},
		"entry_id":          {"1"},
		"expected_revision": {"1"},
	})
	if reviewed.status != http.StatusSeeOther {
		t.Fatalf("review Entry = %d %q", reviewed.status, reviewed.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if strings.Contains(page.body, "Review Outdated") {
		t.Fatalf("reviewed Entry projection = %d %q", page.status, page.body)
	}

	updated := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"update-entry"},
		"command_id":        {"browser-update-entry"},
		"entry_id":          {"1"},
		"expected_revision": {"2"},
		"entry_name":        {"Project Aurora Revised"},
		"public_details":    {"A revised public abstract"},
		"crew_notes":        {"Revised Crew-only note"},
	})
	if updated.status != http.StatusSeeOther {
		t.Fatalf("edit Entry = %d %q", updated.status, updated.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Project Aurora Revised") ||
		!strings.Contains(page.body, "Review Outdated") {
		t.Fatalf("Entry edit did not invalidate review = %d %q", page.status, page.body)
	}
	staleEntry := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"update-entry"},
		"command_id":        {"browser-stale-entry"},
		"entry_id":          {"1"},
		"expected_revision": {"2"},
		"entry_name":        {"Safe stale Entry"},
		"public_details":    {"Safe stale public details"},
		"crew_notes":        {"Safe stale Crew notes"},
	})
	if staleEntry.status != http.StatusConflict ||
		!strings.Contains(staleEntry.body, `value="Safe stale Entry"`) {
		t.Fatalf("stale Entry update = %d %q", staleEntry.status, staleEntry.body)
	}
	assertAccessibleFormErrors(t, staleEntry, nil)

	rejected := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"change-disposition"},
		"command_id":        {"browser-reject-entry"},
		"entry_id":          {"1"},
		"expected_revision": {"3"},
		"disposition":       {"Rejected"},
	})
	if rejected.status != http.StatusSeeOther {
		t.Fatalf("reject Entry = %d %q", rejected.status, rejected.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, `data-tone="danger">Rejected</span>`) {
		t.Fatalf("rejected Entry projection = %d %q", page.status, page.body)
	}

	included := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"change-disposition"},
		"command_id":        {"browser-include-entry"},
		"entry_id":          {"1"},
		"expected_revision": {"4"},
		"disposition":       {"Included"},
	})
	if included.status != http.StatusSeeOther {
		t.Fatalf("include Entry = %d %q", included.status, included.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	second := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":     {requireFrontendCSRF(t, page)},
		"action":         {"create-entry"},
		"command_id":     {"browser-create-second-entry"},
		"entry_name":     {"Project Borealis"},
		"public_details": {"Second public abstract"},
	})
	if second.status != http.StatusSeeOther {
		t.Fatalf("create second Entry = %d %q", second.status, second.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	orderRevision := frontendNamedValues(page.body, "expected_order_revision").
		Get("expected_order_revision")
	reordered := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":              {requireFrontendCSRF(t, page)},
		"action":                  {"configure-order"},
		"command_id":              {"browser-reorder-entries"},
		"expected_order_revision": {orderRevision},
		"order_policy":            {"ManualOrder"},
		"manual_entry_ids":        {"2,1"},
	})
	if reordered.status != http.StatusSeeOther {
		t.Fatalf("reorder Entries = %d %q", reordered.status, reordered.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !regexp.MustCompile(`Canonical order:\s+2,\s+1`).MatchString(page.body) {
		t.Fatalf("manual Entry order projection = %d %q", page.status, page.body)
	}
	reviewedBeforeUpload := postFrontendForm(
		t,
		administrator,
		server.address,
		path,
		url.Values{
			"csrf_token":        {requireFrontendCSRF(t, page)},
			"action":            {"review-entry"},
			"command_id":        {"browser-review-before-upload"},
			"entry_id":          {"1"},
			"expected_revision": {frontendEntryRevision(t, page.body, 1)},
		},
	)
	if reviewedBeforeUpload.status != http.StatusSeeOther {
		t.Fatalf(
			"review Entry before Attachment replacement = %d %q",
			reviewedBeforeUpload.status,
			reviewedBeforeUpload.body,
		)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, `</span><span>revision <code>`) {
		t.Fatalf("review before Attachment replacement = %d %q", page.status, page.body)
	}

	uploadPath := path + "/upload"
	invalidUpload := requestMultipart(
		t.Context(),
		administrator,
		server.address,
		uploadPath,
		map[string]string{
			"csrf_token": requireFrontendCSRF(t, page),
			"command_id": "browser-invalid-upload",
			"entry_id":   "1",
			"name":       "",
			"crew_only":  "true",
		},
		"safe.txt",
		"text/plain",
		[]byte("must not persist"),
	)
	if invalidUpload.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalidUpload.body, `name="crew_only" value="true" checked`) {
		t.Fatalf("invalid browser Attachment upload = %d %q", invalidUpload.status, invalidUpload.body)
	}
	assertAccessibleFormErrors(t, frontendResponse{
		status: invalidUpload.status, header: invalidUpload.header, body: invalidUpload.body,
	}, map[string]string{
		"upload-entry-1-name": "Enter an Attachment name.",
	})

	firstUpload := requestMultipart(
		t.Context(),
		administrator,
		server.address,
		uploadPath,
		map[string]string{
			"csrf_token": requireFrontendCSRF(t, page),
			"command_id": "browser-upload-v1",
			"entry_id":   "1",
			"name":       "slides",
		},
		"slides-v1.txt",
		"text/plain",
		[]byte("first immutable version"),
	)
	if firstUpload.status != http.StatusSeeOther {
		t.Fatalf("first browser Attachment upload = %d %q", firstUpload.status, firstUpload.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Review Outdated") {
		t.Fatalf("Attachment replacement did not invalidate Entry review: %q", page.body)
	}
	foreignVersion := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"version-release-hold"},
		"command_id":        {"browser-foreign-version"},
		"version_id":        {"999"},
		"expected_revision": {"0"},
		"hold":              {"true"},
	})
	if foreignVersion.status != http.StatusNotFound {
		t.Fatalf(
			"foreign Attachment Version target = %d %q",
			foreignVersion.status,
			foreignVersion.body,
		)
	}
	downloaded := getFrontendPage(
		t,
		administrator,
		server.address,
		"/crew/events/1/attachment-versions/1",
	)
	if downloaded.status != http.StatusOK ||
		downloaded.body != "first immutable version" {
		t.Fatalf("verified immutable Attachment read = %d %q", downloaded.status, downloaded.body)
	}
	secondUpload := requestMultipart(
		t.Context(),
		administrator,
		server.address,
		uploadPath,
		map[string]string{
			"csrf_token": requireFrontendCSRF(t, page),
			"command_id": "browser-upload-v2",
			"entry_id":   "1",
			"name":       "slides",
		},
		"slides-v2.txt",
		"text/plain",
		[]byte("second immutable version"),
	)
	if secondUpload.status != http.StatusSeeOther {
		t.Fatalf("replacement browser Attachment upload = %d %q", secondUpload.status, secondUpload.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{
		"slides-v1.txt",
		"slides-v2.txt",
		"Version 1",
		"Version 2",
		"SHA-256",
		"Release Eligibility: Public",
	} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("immutable Attachment history lacks %q: %q", want, page.body)
		}
	}
	if strings.Contains(page.body, "sha256/") {
		t.Fatalf("Attachment history exposed storage path: %q", page.body)
	}

	ready := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"attachment-readiness"},
		"command_id":        {"browser-ready-v2"},
		"entry_id":          {"1"},
		"version_id":        {"2"},
		"expected_revision": {"1"},
		"final":             {"true"},
		"primary":           {"true"},
	})
	if ready.status != http.StatusSeeOther {
		t.Fatalf("set Final Primary Attachment = %d %q", ready.status, ready.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, `data-tone="success">Final</span>`) ||
		!strings.Contains(page.body, `data-tone="success">Primary</span>`) {
		t.Fatalf("Attachment readiness projection = %d %q", page.status, page.body)
	}

	held := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"version-release-hold"},
		"command_id":        {"browser-hold-v2"},
		"version_id":        {"2"},
		"expected_revision": {"0"},
		"hold":              {"true"},
	})
	if held.status != http.StatusSeeOther {
		t.Fatalf("hold Attachment Version release = %d %q", held.status, held.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, `data-tone="warning">Release Held</span>`) {
		t.Fatalf("Attachment release hold projection = %d %q", page.status, page.body)
	}

	policy := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"competition-release-policy"},
		"command_id":        {"browser-release-policy"},
		"expected_revision": {"0"},
		"override":          {"true"},
		"release_policy":    {"OnEnded"},
	})
	if policy.status != http.StatusSeeOther {
		t.Fatalf("configure Competition Attachment release = %d %q", policy.status, policy.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Competition override: On Ended") {
		t.Fatalf("Competition Attachment release projection = %d %q", page.status, page.body)
	}

	expiresAt := time.Now().UTC().Add(3 * time.Hour).Format("2006-01-02T15:04")
	invalidReopen := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"action":     {"create-reopen-window"},
		"command_id": {"browser-invalid-reopen-entry"},
		"entry_id":   {"1"},
		"reason":     {""},
		"expires_at": {"not-a-date"},
	})
	if invalidReopen.status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid Reopen Window = %d %q", invalidReopen.status, invalidReopen.body)
	}
	assertAccessibleFormErrors(t, invalidReopen, map[string]string{
		"create-reopen-window-1-reason":     "is required",
		"create-reopen-window-1-expires-at": "must be a valid local date and time",
	})
	reopened := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {requireFrontendCSRF(t, invalidReopen)},
		"action":     {"create-reopen-window"},
		"command_id": {"browser-reopen-entry"},
		"entry_id":   {"1"},
		"reason":     {"Late corrected slides"},
		"expires_at": {expiresAt},
	})
	if reopened.status != http.StatusSeeOther {
		t.Fatalf("create Reopen Window = %d %q", reopened.status, reopened.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Late corrected slides") ||
		!strings.Contains(page.body, "Active Reopen Window") ||
		!strings.Contains(page.body, `name="action" value="extend-reopen-window"`) ||
		!strings.Contains(page.body, `name="action" value="close-reopen-window"`) ||
		!strings.Contains(page.body, `name="confirm_close" value="true"`) {
		t.Fatalf("bounded Reopen Window projection = %d %q", page.status, page.body)
	}
	extendedExpiry := time.Now().UTC().Add(4 * time.Hour).Format("2006-01-02T15:04")
	extended := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"extend-reopen-window"},
		"command_id":        {"browser-extend-entry-window"},
		"entry_id":          {"1"},
		"window_id":         {"1"},
		"expected_revision": {"1"},
		"expires_at":        {extendedExpiry},
	})
	if extended.status != http.StatusSeeOther {
		t.Fatalf("extend Reopen Window = %d %q", extended.status, extended.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	staleExpiry := time.Now().UTC().Add(5 * time.Hour).Format("2006-01-02T15:04")
	stale := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"extend-reopen-window"},
		"command_id":        {"browser-stale-entry-window"},
		"entry_id":          {"1"},
		"window_id":         {"1"},
		"expected_revision": {"1"},
		"expires_at":        {staleExpiry},
	})
	if stale.status != http.StatusConflict ||
		!strings.Contains(stale.body, "Attachment state changed. Reload and try again.") ||
		!strings.Contains(stale.body, `value="`+staleExpiry+`"`) {
		t.Fatalf("stale Reopen Window extension = %d %q", stale.status, stale.body)
	}
	assertAccessibleFormErrors(t, stale, nil)
	page = getFrontendPage(t, administrator, server.address, path)
	invalid := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"extend-reopen-window"},
		"command_id":        {"browser-invalid-entry-window"},
		"entry_id":          {"1"},
		"window_id":         {"1"},
		"expected_revision": {"2"},
		"expires_at":        {expiresAt},
	})
	if invalid.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalid.body, "Choose an expiry later than the current expiry.") {
		t.Fatalf("invalid Reopen Window extension = %d %q", invalid.status, invalid.body)
	}
	assertAccessibleFormErrors(t, invalid, map[string]string{
		"extend-reopen-window-1-1-expires-at": "Choose an expiry later than the current expiry.",
	})
	if !strings.Contains(invalid.body, "<details open>") {
		t.Fatalf(
			"invalid Reopen Window extension leaves its section collapsed: %q",
			invalid.body,
		)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	unbounded := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"extend-reopen-window"},
		"command_id":        {"browser-unbounded-entry-window"},
		"entry_id":          {"1"},
		"window_id":         {"1"},
		"expected_revision": {"2"},
		"expires_at":        {time.Now().UTC().Add(8 * 24 * time.Hour).Format("2006-01-02T15:04")},
	})
	if unbounded.status != http.StatusUnprocessableEntity ||
		!strings.Contains(unbounded.body, "Choose a future expiry within 7 days.") {
		t.Fatalf("unbounded Reopen Window extension = %d %q", unbounded.status, unbounded.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	otherWindow := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"action":     {"create-reopen-window"},
		"command_id": {"browser-reopen-other-entry"},
		"entry_id":   {"2"},
		"reason":     {"Other Entry correction"},
		"expires_at": {time.Now().UTC().Add(3 * time.Hour).Format("2006-01-02T15:04")},
	})
	if otherWindow.status != http.StatusSeeOther {
		t.Fatalf("create other Entry Reopen Window = %d %q", otherWindow.status, otherWindow.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	forged := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"close-reopen-window"},
		"command_id":        {"browser-close-forged-owner"},
		"entry_id":          {"1"},
		"window_id":         {"2"},
		"expected_revision": {"1"},
		"confirm_close":     {"true"},
	})
	if forged.status != http.StatusNotFound {
		t.Fatalf("cross-owner Reopen Window closure = %d %q", forged.status, forged.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if strings.Count(page.body, "Active Reopen Window") != 2 {
		t.Fatalf("cross-owner closure changed Reopen Window = %d %q", page.status, page.body)
	}
	closedOther := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"close-reopen-window"},
		"command_id":        {"browser-close-other-entry"},
		"entry_id":          {"2"},
		"window_id":         {"2"},
		"expected_revision": {"1"},
		"confirm_close":     {"true"},
	})
	if closedOther.status != http.StatusSeeOther {
		t.Fatalf("close other Entry Reopen Window = %d %q", closedOther.status, closedOther.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	unconfirmed := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"close-reopen-window"},
		"command_id":        {"browser-unconfirmed-entry-window"},
		"entry_id":          {"1"},
		"window_id":         {"1"},
		"expected_revision": {"2"},
	})
	if unconfirmed.status != http.StatusUnprocessableEntity {
		t.Fatalf("unconfirmed Reopen Window closure = %d %q", unconfirmed.status, unconfirmed.body)
	}
	assertAccessibleFormErrors(t, unconfirmed, map[string]string{
		"close-reopen-window-1-1-confirm-close": "must be checked",
	})
	page = getFrontendPage(t, administrator, server.address, path)
	closedWindow := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"close-reopen-window"},
		"command_id":        {"browser-close-entry-window"},
		"entry_id":          {"1"},
		"window_id":         {"1"},
		"expected_revision": {"2"},
		"confirm_close":     {"true"},
	})
	if closedWindow.status != http.StatusSeeOther {
		t.Fatalf("close Reopen Window = %d %q", closedWindow.status, closedWindow.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "<details>") ||
		!strings.Contains(page.body, "Reopen Window history") ||
		!strings.Contains(page.body, "Late corrected slides") ||
		strings.Contains(page.body, "Active Reopen Window") {
		t.Fatalf("closed Reopen Window history = %d %q", page.status, page.body)
	}
	historicalUpdate := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"extend-reopen-window"},
		"command_id":        {"browser-extend-closed-entry-window"},
		"entry_id":          {"1"},
		"window_id":         {"1"},
		"expected_revision": {"3"},
		"expires_at":        {time.Now().UTC().Add(5 * time.Hour).Format("2006-01-02T15:04")},
	})
	if historicalUpdate.status != http.StatusUnprocessableEntity ||
		!strings.Contains(historicalUpdate.body, "Only active Reopen Windows can be changed.") {
		t.Fatalf(
			"closed Reopen Window update = %d %q",
			historicalUpdate.status,
			historicalUpdate.body,
		)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	reopenedAgain := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"action":     {"create-reopen-window"},
		"command_id": {"browser-reopen-entry-again"},
		"entry_id":   {"1"},
		"reason":     {"Final corrected slides"},
		"expires_at": {time.Now().UTC().Add(3 * time.Hour).Format("2006-01-02T15:04")},
	})
	if reopenedAgain.status != http.StatusSeeOther {
		t.Fatalf("create replacement Reopen Window = %d %q", reopenedAgain.status, reopenedAgain.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	failure := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"record-technical-failure"},
		"command_id":        {"browser-record-failure"},
		"entry_id":          {"1"},
		"expected_revision": {frontendEntryRevision(t, page.body, 1)},
		"crew_reason":       {"Projector lost signal"},
	})
	if failure.status != http.StatusSeeOther {
		t.Fatalf("record browser Technical Failure = %d %q", failure.status, failure.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	heldEntry := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"entry-release-hold"},
		"command_id":        {"browser-hold-entry"},
		"entry_id":          {"1"},
		"expected_revision": {frontendEntryRevision(t, page.body, 1)},
		"hold":              {"true"},
		"crew_reason":       {"Awaiting organizer approval"},
	})
	if heldEntry.status != http.StatusSeeOther {
		t.Fatalf("hold Entry release = %d %q", heldEntry.status, heldEntry.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Projector lost signal") ||
		!strings.Contains(page.body, `data-tone="warning">Release Held</span>`) {
		t.Fatalf("independent Entry exception state = %d %q", page.status, page.body)
	}
	setCompetitionSubmissionDeadline(
		t,
		administrator,
		server,
		competitionID,
		time.Now().UTC().Add(-time.Minute),
	)
	page = getFrontendPage(t, administrator, server.address, path)
	closed := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"action":     {"create-entry"},
		"command_id": {"browser-after-deadline"},
		"entry_name": {"Too late"},
	})
	if closed.status != http.StatusGone ||
		!strings.Contains(closed.body, "fixed Submission Deadline") {
		t.Fatalf("fixed browser Submission Deadline = %d %q", closed.status, closed.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	reopenedUpload := requestMultipart(
		t.Context(),
		administrator,
		server.address,
		uploadPath,
		map[string]string{
			"csrf_token": requireFrontendCSRF(t, page),
			"command_id": "browser-upload-reopened",
			"entry_id":   "1",
			"name":       "slides",
		},
		"slides-v3.txt",
		"text/plain",
		[]byte("upload through bounded reopen window"),
	)
	if reopenedUpload.status != http.StatusSeeOther {
		t.Fatalf("browser upload in Reopen Window = %d %q", reopenedUpload.status, reopenedUpload.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "slides-v3.txt") ||
		!strings.Contains(page.body, "Version 3") {
		t.Fatalf("reopened immutable Attachment Version = %d %q", page.status, page.body)
	}
	crewOnlyUpload := requestMultipart(
		t.Context(),
		administrator,
		server.address,
		uploadPath,
		map[string]string{
			"csrf_token": requireFrontendCSRF(t, page),
			"command_id": "browser-upload-crew-only",
			"entry_id":   "1",
			"name":       "organizer notes",
			"crew_only":  "true",
		},
		"organizer.txt",
		"text/plain",
		[]byte("crew-only file"),
	)
	if crewOnlyUpload.status != http.StatusSeeOther {
		t.Fatalf("Crew Only Attachment upload = %d %q", crewOnlyUpload.status, crewOnlyUpload.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Release Eligibility: Crew Only") {
		t.Fatalf("immutable Release Eligibility projection = %d %q", page.status, page.body)
	}
	if public := getFrontendPage(
		t, authenticatedClient(t), server.publicAddress, path,
	); public.status != http.StatusNotFound {
		t.Fatalf("public-listener Entries = %d, want 404", public.status)
	}
	server.stop(t)
}

func exercisePresentationSubmissionFlow(
	t *testing.T,
	administrator, alex, blair *http.Client,
	server *runningServer,
	presentationID int64,
) {
	t.Helper()
	submissionsPath := "/my-participation"
	producerPath := "/backstage/events/1/presentations/" +
		strconv.FormatInt(presentationID, 10) + "/submission"
	uploadPath := "/submissions/1/presentations/" +
		strconv.FormatInt(presentationID, 10) + "/upload"

	alexPage := getFrontendPage(t, alex, server.address, submissionsPath)
	if strings.Contains(alexPage.body, "Create Presentation") ||
		strings.Contains(alexPage.body, "Propose Presentation") {
		t.Fatalf("self-service Presentation proposal exposed: %q", alexPage.body)
	}
	producerPage := getFrontendPage(t, administrator, server.address, producerPath)
	if producerPage.status != http.StatusOK ||
		!strings.Contains(producerPage.body, "Crew Managed") {
		t.Fatalf("Crew Managed Presentation = %d %q", producerPage.status, producerPage.body)
	}
	assigned := postFrontendForm(t, administrator, server.address, producerPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, producerPage)},
		"action":            {"assign-submitter"},
		"command_id":        {"assign-presentation-alex"},
		"expected_revision": {frontendNamedValues(producerPage.body, "expected_revision").Get("expected_revision")},
		"account_id":        {"2"},
	})
	if assigned.status != http.StatusSeeOther {
		t.Fatalf("assign Presentation Submitter = %d %q", assigned.status, assigned.body)
	}
	staleAssignment := postFrontendForm(t, administrator, server.address, producerPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, producerPage)},
		"action":            {"assign-submitter"},
		"command_id":        {"stale-presentation-assignment"},
		"expected_revision": {frontendNamedValues(producerPage.body, "expected_revision").Get("expected_revision")},
		"account_id":        {"3"},
	})
	if staleAssignment.status != http.StatusConflict ||
		!strings.Contains(staleAssignment.body, `value="3" selected`) {
		t.Fatalf("stale Presentation assignment = %d %q", staleAssignment.status, staleAssignment.body)
	}
	assertAccessibleFormErrors(t, staleAssignment, nil)

	presentationPath := "/events/beamconf-2099/schedule/sessions/" +
		strconv.FormatInt(presentationID, 10)
	presentationPage := getFrontendPage(
		t,
		alex,
		server.address,
		presentationPath,
	)
	submissionsPath = frontendLinkPath(t, presentationPage, "Manage Presentation")
	if want := "/my-participation#presentation-" +
		strconv.FormatInt(presentationID, 10) + "-manage"; submissionsPath != want {
		t.Fatalf("Presentation maintenance path = %q, want %q", submissionsPath, want)
	}
	uploadParticipationPath := frontendLinkPath(
		t,
		presentationPage,
		"Upload Presentation File",
	)
	if want := "/my-participation#presentation-" +
		strconv.FormatInt(presentationID, 10) +
		"-upload"; uploadParticipationPath != want {
		t.Fatalf("Presentation upload path = %q, want %q", uploadParticipationPath, want)
	}
	alexPage = getFrontendPage(t, alex, server.address, submissionsPath)
	if !strings.Contains(alexPage.body, "<h2>Presentations</h2>") ||
		!strings.Contains(alexPage.body, "Opening Keynote") {
		t.Fatalf("assigned Presentation listing = %d %q", alexPage.status, alexPage.body)
	}
	if got := frontendLinkPath(t, alexPage, "View Presentation"); got != presentationPath {
		t.Fatalf("Presentation context path = %q, want %q", got, presentationPath)
	}
	invalidSpeaker := strings.Repeat("s", 201)
	invalidPresentationDetails := strings.Repeat("d", 10001)
	invalidPresentation := postFrontendForm(t, alex, server.address, submissionsPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, alexPage)},
		"action":            {"update-presentation"},
		"command_id":        {"alex-invalid-presentation"},
		"event_id":          {"1"},
		"session_id":        {strconv.FormatInt(presentationID, 10)},
		"expected_revision": {frontendPresentationRevision(t, alexPage.body, presentationID)},
		"speaker":           {invalidSpeaker},
		"public_details":    {invalidPresentationDetails},
	})
	if invalidPresentation.status != http.StatusUnprocessableEntity {
		t.Fatalf(
			"invalid assigned Presentation = %d %q",
			invalidPresentation.status,
			invalidPresentation.body,
		)
	}
	prefix := "presentation-" + strconv.FormatInt(presentationID, 10)
	assertAccessibleFormErrors(t, invalidPresentation, map[string]string{
		prefix + "-speaker": "Enter no more than 200 visible characters.",
		prefix + "-details": "Enter no more than 10000 characters.",
	})
	if !strings.Contains(invalidPresentation.body, invalidSpeaker) ||
		!strings.Contains(invalidPresentation.body, invalidPresentationDetails) {
		t.Fatalf("invalid Presentation values = %q", invalidPresentation.body)
	}
	updated := postFrontendForm(t, alex, server.address, submissionsPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, invalidPresentation)},
		"action":            {"update-presentation"},
		"command_id":        {"alex-update-presentation"},
		"event_id":          {"1"},
		"session_id":        {strconv.FormatInt(presentationID, 10)},
		"expected_revision": {frontendPresentationRevision(t, alexPage.body, presentationID)},
		"speaker":           {"Alex Public Speaker"},
		"public_details":    {"Alex approved public details"},
	})
	if updated.status != http.StatusSeeOther {
		t.Fatalf("update assigned Presentation = %d %q", updated.status, updated.body)
	}
	for version, body := range [][]byte{
		[]byte("first immutable slides"),
		[]byte("second immutable slides"),
	} {
		alexPage = getFrontendPage(t, alex, server.address, submissionsPath)
		uploaded := requestMultipart(
			t.Context(),
			alex,
			server.address,
			uploadPath,
			map[string]string{
				"csrf_token": requireFrontendCSRF(t, alexPage),
				"command_id": "alex-presentation-upload-" + strconv.Itoa(version+1),
				"name":       "slides",
			},
			"slides-v"+strconv.Itoa(version+1)+".pdf",
			"application/pdf",
			body,
		)
		if uploaded.status != http.StatusSeeOther {
			t.Fatalf(
				"Presentation upload Version %d = %d %q",
				version+1,
				uploaded.status,
				uploaded.body,
			)
		}
	}
	alexPage = getFrontendPage(t, alex, server.address, submissionsPath)
	for _, want := range []string{
		"2 immutable Attachment Versions",
		"slides-v1.pdf",
		"Version 1",
		"slides-v2.pdf",
		"Version 2",
	} {
		if !strings.Contains(alexPage.body, want) {
			t.Fatalf("Presentation version history lacks %q: %q", want, alexPage.body)
		}
	}

	producerPage = getFrontendPage(t, administrator, server.address, producerPath)
	replaced := postFrontendForm(t, administrator, server.address, producerPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, producerPage)},
		"action":            {"assign-submitter"},
		"command_id":        {"replace-presentation-with-blair"},
		"expected_revision": {frontendNamedValues(producerPage.body, "expected_revision").Get("expected_revision")},
		"account_id":        {"3"},
	})
	if replaced.status != http.StatusSeeOther {
		t.Fatalf("replace Presentation Submitter = %d %q", replaced.status, replaced.body)
	}
	alexPage = getFrontendPage(t, alex, server.address, submissionsPath)
	if strings.Contains(alexPage.body, "Opening Keynote") ||
		strings.Contains(alexPage.body, "slides-v2.pdf") {
		t.Fatalf("former Presentation Submitter retained access: %q", alexPage.body)
	}
	presentationPage = getFrontendPage(t, alex, server.address, presentationPath)
	if !strings.Contains(presentationPage.body, "Presentation maintenance is unavailable.") ||
		strings.Contains(presentationPage.body, "Blair Submitter") {
		t.Fatalf(
			"neutral unavailable Presentation state = %d %q",
			presentationPage.status,
			presentationPage.body,
		)
	}
	formerUpload := requestMultipart(
		t.Context(),
		alex,
		server.address,
		uploadPath,
		map[string]string{
			"csrf_token": requireFrontendCSRF(t, alexPage),
			"command_id": "former-submitter-upload",
			"name":       "must-not-parse",
		},
		"private.pdf",
		"application/pdf",
		[]byte("must remain private"),
	)
	if formerUpload.status != http.StatusNotFound {
		t.Fatalf("former Submitter upload = %d %q", formerUpload.status, formerUpload.body)
	}
	blairPage := getFrontendPage(t, blair, server.address, submissionsPath)
	for _, want := range []string{
		"Opening Keynote",
		"Alex Public Speaker",
		"Alex approved public details",
		"slides-v1.pdf",
		"slides-v2.pdf",
	} {
		if !strings.Contains(blairPage.body, want) {
			t.Fatalf("replacement Presentation projection lacks %q: %q", want, blairPage.body)
		}
	}

	rundownClient := rundownv1connect.NewRundownServiceClient(
		administrator,
		"http://"+server.address,
		connect.WithProtoJSON(),
	)
	current, err := rundownClient.GetCrewRundown(
		t.Context(),
		connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}),
	)
	if err != nil {
		t.Fatalf("load Rundown before Presentation deadline: %v", err)
	}
	setPresentationUploadDeadline(
		t,
		rundownClient,
		current.Msg.GetDraftRevision(),
		presentationID,
		time.Now().UTC().Add(-time.Minute),
	)
	blairPage = getFrontendPage(t, blair, server.address, submissionsPath)
	closed := postFrontendForm(t, blair, server.address, submissionsPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, blairPage)},
		"action":            {"update-presentation"},
		"command_id":        {"blair-update-closed-presentation"},
		"event_id":          {"1"},
		"session_id":        {strconv.FormatInt(presentationID, 10)},
		"expected_revision": {frontendPresentationRevision(t, blairPage.body, presentationID)},
		"speaker":           {"Too Late"},
	})
	if closed.status != http.StatusGone {
		t.Fatalf("Presentation fixed Upload Deadline = %d %q", closed.status, closed.body)
	}
	closedUpload := requestMultipart(
		t.Context(),
		blair,
		server.address,
		uploadPath,
		map[string]string{
			"csrf_token": requireFrontendCSRF(t, blairPage),
			"command_id": "blair-presentation-upload-closed",
			"name":       "closed",
		},
		"closed.pdf",
		"application/pdf",
		[]byte("must not persist"),
	)
	if closedUpload.status != http.StatusGone {
		t.Fatalf("closed Presentation upload = %d %q", closedUpload.status, closedUpload.body)
	}

	producerPage = getFrontendPage(t, administrator, server.address, producerPath)
	invalidReopen := postFrontendForm(t, administrator, server.address, producerPath, url.Values{
		"csrf_token": {requireFrontendCSRF(t, producerPage)},
		"action":     {"create-reopen-window"},
		"command_id": {"invalid-reopen-presentation-submission"},
		"reason":     {""},
		"expires_at": {"not-a-date"},
	})
	if invalidReopen.status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid Presentation Reopen Window = %d %q", invalidReopen.status, invalidReopen.body)
	}
	assertAccessibleFormErrors(t, invalidReopen, map[string]string{
		"create-reopen-window-reason":     "is required",
		"create-reopen-window-expires-at": "must be a valid local date and time",
	})
	reopened := postFrontendForm(t, administrator, server.address, producerPath, url.Values{
		"csrf_token": {requireFrontendCSRF(t, invalidReopen)},
		"action":     {"create-reopen-window"},
		"command_id": {"reopen-presentation-submission"},
		"reason":     {"Submitter correction"},
		"expires_at": {time.Now().UTC().Add(3 * time.Hour).Format("2006-01-02T15:04")},
	})
	if reopened.status != http.StatusSeeOther {
		t.Fatalf("reopen Presentation submission = %d %q", reopened.status, reopened.body)
	}
	producerPage = getFrontendPage(t, administrator, server.address, producerPath)
	if !strings.Contains(producerPage.body, "Submitter correction") ||
		!strings.Contains(producerPage.body, "Active Reopen Window") ||
		!strings.Contains(producerPage.body, `name="action" value="extend-reopen-window"`) ||
		!strings.Contains(producerPage.body, `name="action" value="close-reopen-window"`) {
		t.Fatalf("Presentation Reopen Window projection = %d %q", producerPage.status, producerPage.body)
	}
	extended := postFrontendForm(t, administrator, server.address, producerPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, producerPage)},
		"action":            {"extend-reopen-window"},
		"command_id":        {"extend-presentation-submission"},
		"window_id":         {"1"},
		"expected_revision": {"1"},
		"expires_at":        {time.Now().UTC().Add(4 * time.Hour).Format("2006-01-02T15:04")},
	})
	if extended.status != http.StatusSeeOther {
		t.Fatalf("extend Presentation Reopen Window = %d %q", extended.status, extended.body)
	}
	producerPage = getFrontendPage(t, administrator, server.address, producerPath)
	invalid := postFrontendForm(t, administrator, server.address, producerPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, producerPage)},
		"action":            {"extend-reopen-window"},
		"command_id":        {"invalid-presentation-extension"},
		"window_id":         {"1"},
		"expected_revision": {"2"},
		"expires_at":        {time.Now().UTC().Add(3 * time.Hour).Format("2006-01-02T15:04")},
	})
	if invalid.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalid.body, "Choose an expiry later than the current expiry.") {
		t.Fatalf("invalid Presentation Reopen Window extension = %d %q", invalid.status, invalid.body)
	}
	assertAccessibleFormErrors(t, invalid, map[string]string{
		"extend-reopen-window-" + strconv.FormatInt(presentationID, 10) + "-1-expires-at": "Choose an expiry later than the current expiry.",
	})
	producerPage = getFrontendPage(t, administrator, server.address, producerPath)
	closedWindow := postFrontendForm(t, administrator, server.address, producerPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, producerPage)},
		"action":            {"close-reopen-window"},
		"command_id":        {"close-presentation-submission"},
		"window_id":         {"1"},
		"expected_revision": {"2"},
		"confirm_close":     {"true"},
	})
	if closedWindow.status != http.StatusSeeOther {
		t.Fatalf("close Presentation Reopen Window = %d %q", closedWindow.status, closedWindow.body)
	}
	blairPage = getFrontendPage(t, blair, server.address, submissionsPath)
	closedAgain := requestMultipart(
		t.Context(),
		blair,
		server.address,
		uploadPath,
		map[string]string{
			"csrf_token": requireFrontendCSRF(t, blairPage),
			"command_id": "blair-presentation-upload-closed-again",
			"name":       "closed-again",
		},
		"closed-again.pdf",
		"application/pdf",
		[]byte("must not persist"),
	)
	if closedAgain.status != http.StatusGone {
		t.Fatalf("early-closed Presentation upload = %d %q", closedAgain.status, closedAgain.body)
	}
	producerPage = getFrontendPage(t, administrator, server.address, producerPath)
	if !strings.Contains(producerPage.body, "Reopen Window history") ||
		!strings.Contains(producerPage.body, "Submitter correction") {
		t.Fatalf("Presentation Reopen Window history = %d %q", producerPage.status, producerPage.body)
	}
	reopened = postFrontendForm(t, administrator, server.address, producerPath, url.Values{
		"csrf_token": {requireFrontendCSRF(t, producerPage)},
		"action":     {"create-reopen-window"},
		"command_id": {"reopen-presentation-submission-again"},
		"reason":     {"Final Submitter correction"},
		"expires_at": {time.Now().UTC().Add(3 * time.Hour).Format("2006-01-02T15:04")},
	})
	if reopened.status != http.StatusSeeOther {
		t.Fatalf("reopen Presentation submission again = %d %q", reopened.status, reopened.body)
	}
	blairPage = getFrontendPage(t, blair, server.address, submissionsPath)
	reopenedUpdate := postFrontendForm(t, blair, server.address, submissionsPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, blairPage)},
		"action":            {"update-presentation"},
		"command_id":        {"blair-update-reopened-presentation"},
		"event_id":          {"1"},
		"session_id":        {strconv.FormatInt(presentationID, 10)},
		"expected_revision": {frontendPresentationRevision(t, blairPage.body, presentationID)},
		"speaker":           {"Blair Final Speaker"},
		"public_details":    {"Blair final approved details"},
	})
	if reopenedUpdate.status != http.StatusSeeOther {
		t.Fatalf("update Presentation in Reopen Window = %d %q", reopenedUpdate.status, reopenedUpdate.body)
	}
	blairPage = getFrontendPage(t, blair, server.address, submissionsPath)
	reopenedUpload := requestMultipart(
		t.Context(),
		blair,
		server.address,
		uploadPath,
		map[string]string{
			"csrf_token": requireFrontendCSRF(t, blairPage),
			"command_id": "blair-presentation-upload-reopened",
			"name":       "slides",
		},
		"slides-v3.pdf",
		"application/pdf",
		[]byte("third immutable slides"),
	)
	if reopenedUpload.status != http.StatusSeeOther {
		t.Fatalf("reopened Presentation upload = %d %q", reopenedUpload.status, reopenedUpload.body)
	}
	blairPage = getFrontendPage(t, blair, server.address, submissionsPath)
	if !strings.Contains(blairPage.body, "slides-v3.pdf") ||
		!strings.Contains(blairPage.body, "Version 3") {
		t.Fatalf("reopened Presentation Version = %d %q", blairPage.status, blairPage.body)
	}

	audit := get(t, administrator, server.address, "/admin/audit")
	auditBody, err := io.ReadAll(audit.Body)
	_ = audit.Body.Close()
	if err != nil ||
		bytes.Count(auditBody, []byte(`"action":"AssignPresentationSubmitter"`)) != 3 ||
		!bytes.Contains(auditBody, []byte(`"reason":"stale_presentation_submission"`)) ||
		!bytes.Contains(auditBody, []byte(`"note":"Submitter Account #3"`)) {
		t.Fatalf("Presentation assignment Audit evidence = %s (%v)", auditBody, err)
	}
	publicSchedule := getFrontendPage(
		t,
		authenticatedClient(t),
		server.publicAddress,
		"/events/beamconf-2099/schedule",
	)
	if publicSchedule.status != http.StatusOK ||
		!strings.Contains(publicSchedule.body, "Blair Final Speaker") ||
		!strings.Contains(publicSchedule.body, "Blair final approved details") {
		t.Fatalf("approved public Presentation projection = %d %q", publicSchedule.status, publicSchedule.body)
	}
}

func TestAccountSubmissionsHonorPolicyOwnershipAndReopenWindows(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	presentationID := prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	entriesPath := "/backstage/events/1/competitions/" +
		strconv.FormatInt(competitionID, 10) + "/entries"

	settingsPath := "/backstage/events/1/settings"
	settings := getFrontendPage(t, administrator, server.address, settingsPath)
	eventRevision := frontendNamedValues(settings.body, "expected_event_revision").
		Get("expected_event_revision")
	published := postFrontendForm(t, administrator, server.address, settingsPath, url.Values{
		"csrf_token":                        {requireFrontendCSRF(t, settings)},
		"command_id":                        {"publish-submission-event"},
		"expected_event_revision":           {eventRevision},
		"event_name":                        {"BeamConf 2099"},
		"public":                            {"true"},
		"planned_start_date":                {"2099-08-21"},
		"planned_end_date":                  {"2099-08-23"},
		"timezone":                          {"Europe/Berlin"},
		"event_locale":                      {"en-GB"},
		"content_language":                  {"en-GB"},
		"event_day_boundary":                {"06:00"},
		"entry_default_disposition":         {"Pending"},
		"submission_eligibility":            {"AllAccounts"},
		"voting_method":                     {"Range1To5"},
		"self_vote_policy":                  {"Allowed"},
		"target_adjustment_presets_seconds": {"-300,300,600"},
	})
	if published.status != http.StatusSeeOther {
		t.Fatalf("publish submission Event = %d %q", published.status, published.body)
	}

	const password = "submission correct horse battery staple"
	for id, name := range []string{"Alex Submitter", "Blair Submitter"} {
		assertJSONRequest(
			t,
			administrator,
			server.address,
			"/admin/accounts",
			map[string]string{
				"name": name, "password": password,
				"command_id": "create-submitter-" + strconv.Itoa(id+2),
			},
			http.StatusCreated,
			"{\"id\":"+strconv.Itoa(id+2)+",\"name\":\""+name+"\",\"administrator\":false}\n",
		)
	}
	signIn := func(name string) *http.Client {
		t.Helper()
		client := authenticatedClient(t)
		client.CheckRedirect = administrator.CheckRedirect
		assertJSONRequest(
			t,
			client,
			server.address,
			"/auth/sign-in",
			map[string]string{"name": name, "password": password},
			http.StatusNoContent,
			"",
		)
		return client
	}
	alex := signIn("Alex Submitter")
	blair := signIn("Blair Submitter")
	exercisePresentationSubmissionFlow(
		t,
		administrator,
		alex,
		blair,
		server,
		presentationID,
	)

	competitionPath := "/events/beamconf-2099/competitions/" +
		strconv.FormatInt(competitionID, 10)
	competitionPage := getFrontendPage(t, alex, server.address, competitionPath)
	submissionsPath := frontendLinkPath(t, competitionPage, "Submit")
	if want := "/my-participation#competition-" +
		strconv.FormatInt(competitionID, 10); submissionsPath != want {
		t.Fatalf("Competition submission path = %q, want %q", submissionsPath, want)
	}
	alexPage := getFrontendPage(t, alex, server.address, submissionsPath)
	if alexPage.status != http.StatusOK ||
		!strings.Contains(alexPage.body, "<h1>My Participation</h1>") ||
		!strings.Contains(alexPage.body, `href="/my-participation">My Participation</a>`) ||
		strings.Contains(alexPage.body, `>Submissions</a>`) ||
		strings.Contains(alexPage.body, `>Voting</a>`) ||
		!strings.Contains(alexPage.body, `href="/voting">Redeem a Voting Key</a>`) ||
		!strings.Contains(alexPage.body, "<h2>Entries</h2>") ||
		!strings.Contains(alexPage.body, "Demo Competition") {
		t.Fatalf("Account submission listing = %d %q", alexPage.status, alexPage.body)
	}
	if got := frontendLinkPath(t, alexPage, "View Competition"); got != competitionPath {
		t.Fatalf("Competition context path = %q, want %q", got, competitionPath)
	}
	invalidDetails := strings.Repeat("x", 10001)
	invalid := postFrontendForm(t, alex, server.address, submissionsPath, url.Values{
		"csrf_token":     {requireFrontendCSRF(t, alexPage)},
		"action":         {"create"},
		"command_id":     {"account-create-invalid"},
		"event_id":       {"1"},
		"session_id":     {strconv.FormatInt(competitionID, 10)},
		"entry_name":     {""},
		"public_details": {invalidDetails},
	})
	if invalid.status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid Account submission = %d %q", invalid.status, invalid.body)
	}
	assertAccessibleFormErrors(t, invalid, map[string]string{
		"entry-create-" + strconv.FormatInt(competitionID, 10) + "-name":    "Enter an Entry name.",
		"entry-create-" + strconv.FormatInt(competitionID, 10) + "-details": "Enter no more than 10000 characters.",
	})
	if !strings.Contains(invalid.body, invalidDetails) {
		t.Fatalf("invalid Account submission values = %q", invalid.body)
	}
	created := postFrontendForm(t, alex, server.address, submissionsPath, url.Values{
		"csrf_token":     {requireFrontendCSRF(t, alexPage)},
		"action":         {"create"},
		"command_id":     {"account-create-first"},
		"event_id":       {"1"},
		"session_id":     {strconv.FormatInt(competitionID, 10)},
		"entry_name":     {"Alex Public Credit"},
		"public_details": {"First public abstract"},
	})
	if created.status != http.StatusSeeOther {
		t.Fatalf("All Accounts submission = %d %q", created.status, created.body)
	}
	competitionPage = getFrontendPage(t, alex, server.address, competitionPath)
	if got := frontendLinkPath(
		t,
		competitionPage,
		"Manage My Entry",
	); got != submissionsPath {
		t.Fatalf("managed Entry path = %q, want %q", got, submissionsPath)
	}

	entriesPage := getFrontendPage(t, administrator, server.address, entriesPath)
	policyRevision := frontendNamedValues(
		entriesPage.body,
		"expected_submission_eligibility_revision",
	).Get("expected_submission_eligibility_revision")
	tightened := postFrontendForm(t, administrator, server.address, entriesPath, url.Values{
		"csrf_token": {requireFrontendCSRF(t, entriesPage)},
		"action":     {"configure-submission-eligibility"},
		"command_id": {"require-voting-eligibility"},
		"expected_submission_eligibility_revision": {policyRevision},
		"override":               {"true"},
		"submission_eligibility": {"VotingEligibleAccounts"},
	})
	if tightened.status != http.StatusSeeOther {
		t.Fatalf("tighten Submission Eligibility = %d %q", tightened.status, tightened.body)
	}
	unavailable := getFrontendPage(t, blair, server.address, competitionPath)
	if !strings.Contains(unavailable.body, "Entry submission is unavailable.") ||
		!strings.Contains(unavailable.body, "Voting has not opened.") ||
		strings.Contains(unavailable.body, "VotingEligibleAccounts") {
		t.Fatalf("neutral unavailable submission state = %d %q", unavailable.status, unavailable.body)
	}

	alexPage = getFrontendPage(t, alex, server.address, submissionsPath)
	if strings.Contains(alexPage.body, "VotingEligibleAccounts") {
		t.Fatalf("My Participation exposed Submission policy: %q", alexPage.body)
	}
	updated := postFrontendForm(t, alex, server.address, submissionsPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, alexPage)},
		"action":            {"update"},
		"command_id":        {"account-update-after-tightening"},
		"event_id":          {"1"},
		"session_id":        {strconv.FormatInt(competitionID, 10)},
		"entry_id":          {"1"},
		"expected_revision": {frontendEntryRevision(t, alexPage.body, 1)},
		"entry_name":        {"Alex Revised Credit"},
		"public_details":    {"Revised public abstract"},
	})
	if updated.status != http.StatusSeeOther {
		t.Fatalf("existing submission after tightening = %d %q", updated.status, updated.body)
	}
	alexPage = getFrontendPage(t, alex, server.address, submissionsPath)
	ineligibleForm := url.Values{
		"csrf_token": {requireFrontendCSRF(t, alexPage)},
		"action":     {"create"},
		"command_id": {"account-create-ineligible"},
		"event_id":   {"1"}, "session_id": {strconv.FormatInt(competitionID, 10)},
		"entry_name": {"Blocked Entry"},
	}
	ineligible := postFrontendForm(t, alex, server.address, submissionsPath, ineligibleForm)
	if ineligible.status != http.StatusForbidden {
		t.Fatalf("Voting Eligible submission = %d %q", ineligible.status, ineligible.body)
	}
	replayedIneligible := postFrontendForm(
		t,
		alex,
		server.address,
		submissionsPath,
		ineligibleForm,
	)
	if replayedIneligible.status != http.StatusForbidden {
		t.Fatalf(
			"replayed Voting Eligible submission = %d %q",
			replayedIneligible.status,
			replayedIneligible.body,
		)
	}
	audit := get(t, administrator, server.address, "/admin/audit")
	auditBody, err := io.ReadAll(audit.Body)
	_ = audit.Body.Close()
	if err != nil ||
		bytes.Count(auditBody, []byte(`"action":"CreateSubmittedCompetitionEntry"`)) != 2 ||
		!bytes.Contains(auditBody, []byte(`"reason":"submission_ineligible"`)) {
		t.Fatalf("Account submission rejection Audit evidence = %s (%v)", auditBody, err)
	}

	entriesPage = getFrontendPage(t, administrator, server.address, entriesPath)
	policyRevision = frontendNamedValues(
		entriesPage.body,
		"expected_submission_eligibility_revision",
	).Get("expected_submission_eligibility_revision")
	opened := postFrontendForm(t, administrator, server.address, entriesPath, url.Values{
		"csrf_token": {requireFrontendCSRF(t, entriesPage)},
		"action":     {"configure-submission-eligibility"},
		"command_id": {"allow-all-accounts"},
		"expected_submission_eligibility_revision": {policyRevision},
		"override":               {"true"},
		"submission_eligibility": {"AllAccounts"},
	})
	if opened.status != http.StatusSeeOther {
		t.Fatalf("open Submission Eligibility = %d %q", opened.status, opened.body)
	}
	alexPage = getFrontendPage(t, alex, server.address, submissionsPath)
	second := postFrontendForm(t, alex, server.address, submissionsPath, url.Values{
		"csrf_token": {requireFrontendCSRF(t, alexPage)},
		"action":     {"create"},
		"command_id": {"account-create-second"},
		"event_id":   {"1"}, "session_id": {strconv.FormatInt(competitionID, 10)},
		"entry_name": {"Alex Second Entry"},
	})
	if second.status != http.StatusSeeOther {
		t.Fatalf("Competition All Accounts override = %d %q", second.status, second.body)
	}

	entriesPage = getFrontendPage(t, administrator, server.address, entriesPath)
	crewManaged := postFrontendForm(t, administrator, server.address, entriesPath, url.Values{
		"csrf_token": {requireFrontendCSRF(t, entriesPage)},
		"action":     {"create-entry"},
		"command_id": {"create-crew-managed-submission"},
		"entry_name": {"Crew Assigned Credit"},
	})
	if crewManaged.status != http.StatusSeeOther {
		t.Fatalf("create Crew Managed Entry = %d %q", crewManaged.status, crewManaged.body)
	}
	entriesPage = getFrontendPage(t, administrator, server.address, entriesPath)
	assigned := postFrontendForm(t, administrator, server.address, entriesPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, entriesPage)},
		"action":            {"assign-submitter"},
		"command_id":        {"assign-crew-managed-submission"},
		"entry_id":          {"3"},
		"expected_revision": {frontendEntryRevision(t, entriesPage.body, 3)},
		"account_id":        {"3"},
	})
	if assigned.status != http.StatusSeeOther {
		t.Fatalf("assign Crew Managed Entry = %d %q", assigned.status, assigned.body)
	}

	entriesPage = getFrontendPage(t, administrator, server.address, entriesPath)
	reviewed := postFrontendForm(t, administrator, server.address, entriesPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, entriesPage)},
		"action":            {"review-entry"},
		"command_id":        {"review-account-submission"},
		"entry_id":          {"1"},
		"expected_revision": {frontendEntryRevision(t, entriesPage.body, 1)},
	})
	if reviewed.status != http.StatusSeeOther {
		t.Fatalf("review Account submission = %d %q", reviewed.status, reviewed.body)
	}
	alexPage = getFrontendPage(t, alex, server.address, submissionsPath)
	invalidated := postFrontendForm(t, alex, server.address, submissionsPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, alexPage)},
		"action":            {"update"},
		"command_id":        {"account-invalidate-review"},
		"event_id":          {"1"},
		"session_id":        {strconv.FormatInt(competitionID, 10)},
		"entry_id":          {"1"},
		"expected_revision": {frontendEntryRevision(t, alexPage.body, 1)},
		"entry_name":        {"Alex Final Credit"},
	})
	if invalidated.status != http.StatusSeeOther {
		t.Fatalf("Account review invalidation = %d %q", invalidated.status, invalidated.body)
	}
	entriesPage = getFrontendPage(t, administrator, server.address, entriesPath)
	if !strings.Contains(entriesPage.body, "Review Outdated") {
		t.Fatalf("Account update retained stale review: %q", entriesPage.body)
	}

	setCompetitionSubmissionDeadline(
		t,
		administrator,
		server,
		competitionID,
		time.Now().UTC().Add(-time.Minute),
	)
	alexPage = getFrontendPage(t, alex, server.address, submissionsPath)
	closed := postFrontendForm(t, alex, server.address, submissionsPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, alexPage)},
		"action":            {"update"},
		"command_id":        {"account-update-after-deadline"},
		"event_id":          {"1"},
		"session_id":        {strconv.FormatInt(competitionID, 10)},
		"entry_id":          {"1"},
		"expected_revision": {frontendEntryRevision(t, alexPage.body, 1)},
		"entry_name":        {"Too Late"},
	})
	if closed.status != http.StatusGone {
		t.Fatalf("Account fixed Submission Deadline = %d %q", closed.status, closed.body)
	}
	for attempt := range 2 {
		closedUpload := requestMultipart(
			t.Context(),
			alex,
			server.address,
			"/submissions/1/entries/1/upload",
			map[string]string{
				"csrf_token": requireFrontendCSRF(t, alexPage),
				"command_id": "account-upload-closed",
				"name":       "closed",
			},
			"closed.zip",
			"application/zip",
			[]byte("must not persist"),
		)
		if closedUpload.status != http.StatusGone {
			t.Fatalf(
				"closed Account upload attempt %d = %d %q",
				attempt+1,
				closedUpload.status,
				closedUpload.body,
			)
		}
	}
	audit = get(t, administrator, server.address, "/admin/audit")
	auditBody, err = io.ReadAll(audit.Body)
	_ = audit.Body.Close()
	if err != nil ||
		bytes.Count(
			auditBody,
			[]byte(
				`"action":"UploadAttachment","target_type":"Entry",`+
					`"target_id":"1","outcome":"Rejected","reason":"upload_closed"`,
			),
		) != 1 {
		t.Fatalf("Account upload rejection Audit evidence = %s (%v)", auditBody, err)
	}

	entriesPage = getFrontendPage(t, administrator, server.address, entriesPath)
	reopened := postFrontendForm(t, administrator, server.address, entriesPath, url.Values{
		"csrf_token": {requireFrontendCSRF(t, entriesPage)},
		"action":     {"create-reopen-window"},
		"command_id": {"reopen-account-submission"},
		"entry_id":   {"1"},
		"reason":     {"Submitter correction"},
		"expires_at": {time.Now().UTC().Add(3 * time.Hour).Format("2006-01-02T15:04")},
	})
	if reopened.status != http.StatusSeeOther {
		t.Fatalf("reopen Account submission = %d %q", reopened.status, reopened.body)
	}
	alexPage = getFrontendPage(t, alex, server.address, submissionsPath)
	reopenedUpdate := postFrontendForm(t, alex, server.address, submissionsPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, alexPage)},
		"action":            {"update"},
		"command_id":        {"account-update-reopened"},
		"event_id":          {"1"},
		"session_id":        {strconv.FormatInt(competitionID, 10)},
		"entry_id":          {"1"},
		"expected_revision": {frontendEntryRevision(t, alexPage.body, 1)},
		"entry_name":        {"Alex Reopened Credit"},
	})
	if reopenedUpdate.status != http.StatusSeeOther {
		t.Fatalf("update in Reopen Window = %d %q", reopenedUpdate.status, reopenedUpdate.body)
	}
	entriesPage = getFrontendPage(t, administrator, server.address, entriesPath)
	reviewed = postFrontendForm(t, administrator, server.address, entriesPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, entriesPage)},
		"action":            {"review-entry"},
		"command_id":        {"review-before-account-upload"},
		"entry_id":          {"1"},
		"expected_revision": {frontendEntryRevision(t, entriesPage.body, 1)},
	})
	if reviewed.status != http.StatusSeeOther {
		t.Fatalf("review before Account upload = %d %q", reviewed.status, reviewed.body)
	}

	content := []byte("immutable Account submission")
	alexPage = getFrontendPage(t, alex, server.address, submissionsPath)
	invalidFilename := strings.Repeat("f", 256)
	invalidUpload := requestMultipart(
		t.Context(),
		alex,
		server.address,
		"/submissions/1/entries/1/upload",
		map[string]string{
			"csrf_token": requireFrontendCSRF(t, alexPage),
			"command_id": "account-upload-invalid",
			"name":       "",
		},
		invalidFilename,
		"application/zip",
		content,
	)
	if invalidUpload.status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid Account upload = %d %q", invalidUpload.status, invalidUpload.body)
	}
	assertAccessibleFormErrors(t, frontendResponse{
		status: invalidUpload.status,
		header: invalidUpload.header,
		body:   invalidUpload.body,
	}, map[string]string{
		"entry-1-upload-name": "Enter an Attachment name.",
		"entry-1-upload-file": "Choose a file with a valid name.",
	})
	if strings.Contains(invalidUpload.body, invalidFilename) ||
		strings.Contains(invalidUpload.body, string(content)) {
		t.Fatalf("invalid Account upload retained file secret: %q", invalidUpload.body)
	}
	uploaded := requestMultipart(
		t.Context(),
		alex,
		server.address,
		"/submissions/1/entries/1/upload",
		map[string]string{
			"csrf_token": requireFrontendCSRF(t, alexPage),
			"command_id": "account-upload-reopened",
			"name":       "archive",
		},
		"entry.zip",
		"application/zip",
		content,
	)
	if uploaded.status != http.StatusSeeOther {
		t.Fatalf("Account upload in Reopen Window = %d %q", uploaded.status, uploaded.body)
	}
	digest := sha256.Sum256(content)
	hash := hex.EncodeToString(digest[:])
	alexPage = getFrontendPage(t, alex, server.address, submissionsPath)
	if !strings.Contains(alexPage.body, "entry.zip") ||
		!strings.Contains(alexPage.body, hash) {
		t.Fatalf("Account upload integrity metadata missing: %q", alexPage.body)
	}
	entriesPage = getFrontendPage(t, administrator, server.address, entriesPath)
	if !strings.Contains(entriesPage.body, "Review Outdated") {
		t.Fatalf("Account upload retained stale review: %q", entriesPage.body)
	}
	closedWindow := postFrontendForm(t, administrator, server.address, entriesPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, entriesPage)},
		"action":            {"close-reopen-window"},
		"command_id":        {"close-account-submission"},
		"entry_id":          {"1"},
		"window_id":         {"3"},
		"expected_revision": {"1"},
		"confirm_close":     {"true"},
	})
	if closedWindow.status != http.StatusSeeOther {
		t.Fatalf("close Account Reopen Window = %d %q", closedWindow.status, closedWindow.body)
	}
	server.stop(t)
	server = startBeamersWithPublicListener(t, server.bin, server.dataDir)
	alexPage = getFrontendPage(t, alex, server.address, submissionsPath)
	closedAfterRestart := postFrontendForm(t, alex, server.address, submissionsPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, alexPage)},
		"action":            {"update"},
		"command_id":        {"account-update-after-reopen-close"},
		"event_id":          {"1"},
		"session_id":        {strconv.FormatInt(competitionID, 10)},
		"entry_id":          {"1"},
		"expected_revision": {frontendEntryRevision(t, alexPage.body, 1)},
		"entry_name":        {"Too Late Again"},
	})
	if closedAfterRestart.status != http.StatusGone {
		t.Fatalf(
			"closed Account Reopen Window after restart = %d %q",
			closedAfterRestart.status,
			closedAfterRestart.body,
		)
	}
	blairPage := getFrontendPage(t, blair, server.address, submissionsPath)
	if !strings.Contains(blairPage.body, "Crew Assigned Credit") ||
		strings.Contains(blairPage.body, "Alex Reopened Credit") ||
		strings.Contains(blairPage.body, "entry.zip") ||
		strings.Contains(blairPage.body, hash) {
		t.Fatalf("Account submission privacy/assignment = %d %q", blairPage.status, blairPage.body)
	}
	server.stop(t)
}

func TestBrowserStagesAndReviewsCompetitionResults(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	competitionClient := competitionv1connect.NewCompetitionServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	createEntry := func(commandID, name string) int64 {
		t.Helper()
		created, err := competitionClient.CreateEntry(
			t.Context(),
			connect.NewRequest(&competitionv1.CreateEntryRequest{
				EventId: 1, SessionId: competitionID,
				CommandId: commandID, Name: name,
			}),
		)
		if err != nil {
			t.Fatalf("create Results Entry: %v", err)
		}
		return created.Msg.GetEntry().GetId()
	}
	firstID := createEntry("browser-results-entry-first", "First Result")
	secondID := createEntry("browser-results-entry-second", "Second Result")
	path := "/backstage/events/1/results"

	page := getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{
		"Results and Prizegiving",
		"Demo Competition",
		"First Result",
		"Second Result",
		`name="action" value="save-results-draft"`,
	} {
		if page.status != http.StatusOK || !strings.Contains(page.body, want) {
			t.Fatalf("Results page lacks %q: %d %q", want, page.status, page.body)
		}
	}
	invalidDraft := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":             {requireFrontendCSRF(t, page)},
		"action":                 {"save-results-draft"},
		"command_id":             {"browser-invalid-results"},
		"competition_session_id": {strconv.FormatInt(competitionID, 10)},
		"expected_revision":      {"0"},
		"disposition":            {"NoPublicResults"},
		"no_public_reason":       {"Retain this Crew Reason."},
		"tally_override_reason":  {"Retain this tally reason."},
		"public_explanation":     {"Retain this Results explanation."},
		"score_type":             {"Duration"},
		"score_visibility":       {"CrewOnly"},
		"score_unit":             {"seconds"},
		"score_precision":        {"-1"},
		"score_requirement":      {"Optional"},
		"score_interpretation":   {"LowerWins"},
		"standing_entry_id": {
			strconv.FormatInt(firstID, 10),
			strconv.FormatInt(secondID, 10),
		},
		"standing":      {"Unplaced", "Placed"},
		"placement":     {"", "2"},
		"display_order": {"2", "1"},
		"score":         {"1m2s", "59s"},
	})
	if invalidDraft.status != http.StatusUnprocessableEntity ||
		!strings.Contains(
			invalidDraft.body,
			">Retain this Results explanation.</textarea>",
		) ||
		!strings.Contains(invalidDraft.body, ">Retain this Crew Reason.</textarea>") ||
		!strings.Contains(invalidDraft.body, `<option value="NoPublicResults" selected>No Public Results</option>`) ||
		!strings.Contains(invalidDraft.body, `<option value="Duration" selected>Duration</option>`) ||
		!strings.Contains(invalidDraft.body, `<option value="CrewOnly" selected>Crew Only</option>`) ||
		!strings.Contains(invalidDraft.body, `name="score_unit" value="seconds"`) ||
		!strings.Contains(invalidDraft.body, `<option value="Optional" selected>Optional</option>`) ||
		!strings.Contains(invalidDraft.body, `<option value="LowerWins" selected>Lower Wins</option>`) ||
		!strings.Contains(invalidDraft.body, `name="score" value="1m2s"`) ||
		!strings.Contains(invalidDraft.body, `name="score" value="59s"`) {
		t.Fatalf("invalid Results Draft = %d %q", invalidDraft.status, invalidDraft.body)
	}
	assertAccessibleFormErrors(t, invalidDraft, map[string]string{
		"results-" + strconv.FormatInt(competitionID, 10) + "-score-precision": "nonnegative integer",
	})
	invalidPlacement := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":             {requireFrontendCSRF(t, invalidDraft)},
		"action":                 {"save-results-draft"},
		"command_id":             {"browser-invalid-results-placement"},
		"competition_session_id": {strconv.FormatInt(competitionID, 10)},
		"expected_revision":      {"0"},
		"disposition":            {"Publish"},
		"score_type":             {"Decimal"},
		"score_visibility":       {"Public"},
		"score_unit":             {"points"},
		"score_precision":        {"1"},
		"score_requirement":      {"Required"},
		"score_interpretation":   {"HigherWins"},
		"standing_entry_id": {
			strconv.FormatInt(firstID, 10),
			strconv.FormatInt(secondID, 10),
		},
		"standing":      {"Placed", "Placed"},
		"placement":     {"not-a-number", "2"},
		"display_order": {"1", "2"},
		"score":         {"9.5", "8.0"},
	})
	if invalidPlacement.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalidPlacement.body, `name="placement" value="not-a-number"`) ||
		!strings.Contains(invalidPlacement.body, `name="score" value="9.5"`) ||
		!strings.Contains(invalidPlacement.body, `name="score" value="8.0"`) {
		t.Fatalf(
			"invalid Results placement = %d %q",
			invalidPlacement.status,
			invalidPlacement.body,
		)
	}
	assertAccessibleFormErrors(t, invalidPlacement, map[string]string{
		"results-" + strconv.FormatInt(competitionID, 10) + "-placement-" +
			strconv.FormatInt(firstID, 10): "positive integer",
	})
	invalidRanking := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":             {requireFrontendCSRF(t, invalidPlacement)},
		"action":                 {"save-results-draft"},
		"command_id":             {"browser-invalid-results-ranking"},
		"competition_session_id": {strconv.FormatInt(competitionID, 10)},
		"expected_revision":      {"0"},
		"disposition":            {"Publish"},
		"score_type":             {"Decimal"},
		"score_visibility":       {"Public"},
		"score_unit":             {"points"},
		"score_precision":        {"1"},
		"score_requirement":      {"Required"},
		"score_interpretation":   {"HigherWins"},
		"standing_entry_id": {
			strconv.FormatInt(firstID, 10),
			strconv.FormatInt(secondID, 10),
		},
		"standing":      {"Placed", "Placed"},
		"placement":     {"2", "1"},
		"display_order": {"1", "2"},
		"score":         {"9.5", "8.0"},
	})
	if invalidRanking.status != http.StatusUnprocessableEntity {
		t.Fatalf(
			"invalid Results ranking = %d %q",
			invalidRanking.status,
			invalidRanking.body,
		)
	}
	assertAccessibleFormErrors(t, invalidRanking, map[string]string{
		"results-" + strconv.FormatInt(competitionID, 10) +
			"-result-standings": "competition ranking",
	})

	save := func(commandID, expectedRevision string, placements []string) frontendResponse {
		t.Helper()
		return postFrontendForm(t, administrator, server.address, path, url.Values{
			"csrf_token":             {requireFrontendCSRF(t, page)},
			"action":                 {"save-results-draft"},
			"command_id":             {commandID},
			"competition_session_id": {strconv.FormatInt(competitionID, 10)},
			"expected_revision":      {expectedRevision},
			"disposition":            {"Publish"},
			"score_type":             {"Decimal"},
			"score_visibility":       {"Public"},
			"score_unit":             {"points"},
			"score_precision":        {"1"},
			"score_requirement":      {"Required"},
			"score_interpretation":   {"HigherWins"},
			"public_explanation":     {"Retain this Results explanation."},
			"standing_entry_id": {
				strconv.FormatInt(firstID, 10),
				strconv.FormatInt(secondID, 10),
			},
			"standing":      {"Placed", "Placed"},
			"placement":     placements,
			"display_order": {"1", "2"},
			"score":         {"9.5", "8.0"},
		})
	}
	if saved := save("browser-save-results", "0", []string{"1", "2"}); saved.status != http.StatusSeeOther || saved.header.Get("Location") != path {
		t.Fatalf("save browser Results = %d %q", saved.status, saved.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Draft revision <code>1</code>") ||
		!strings.Contains(page.body, `data-tone="draft">Draft</span>`) {
		t.Fatalf("saved browser Results missing revision state: %d %q", page.status, page.body)
	}
	staleDraft := save("browser-stale-results", "0", []string{"1", "2"})
	if staleDraft.status != http.StatusConflict ||
		!strings.Contains(staleDraft.body, "Results changed") ||
		!strings.Contains(
			staleDraft.body,
			">Retain this Results explanation.</textarea>",
		) {
		t.Fatalf("stale Results Draft = %d %q", staleDraft.status, staleDraft.body)
	}
	assertAccessibleFormErrors(t, staleDraft, nil)
	page = getFrontendPage(t, administrator, server.address, path)

	ready := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":             {requireFrontendCSRF(t, page)},
		"action":                 {"mark-results-ready"},
		"command_id":             {"browser-ready-results"},
		"competition_session_id": {strconv.FormatInt(competitionID, 10)},
		"expected_revision":      {"1"},
	})
	if ready.status != http.StatusSeeOther {
		t.Fatalf("mark browser Results Ready = %d %q", ready.status, ready.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Draft revision <code>1</code>") ||
		!strings.Contains(page.body, `data-tone="success">Ready</span>`) {
		t.Fatalf("reviewed browser Results missing Ready state: %d %q", page.status, page.body)
	}

	if tied := save("browser-tie-results", "1", []string{"1", "1"}); tied.status != http.StatusSeeOther {
		t.Fatalf("save tied browser Results = %d %q", tied.status, tied.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Draft revision <code>2</code>") ||
		!strings.Contains(page.body, `data-tone="draft">Draft</span>`) {
		t.Fatalf("changed browser Results did not clear Ready: %d %q", page.status, page.body)
	}

	invalidAwards := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                {requireFrontendCSRF(t, page)},
		"action":                    {"save-competition-awards"},
		"command_id":                {"browser-invalid-results-awards"},
		"competition_session_id":    {strconv.FormatInt(competitionID, 10)},
		"expected_revision":         {"2"},
		"award_key":                 {"retained-key"},
		"award_name":                {"Retained Competition Award"},
		"award_recipient_entry_ids": {strconv.FormatInt(firstID, 10)},
		"award_recipient_names":     {"Retained recipient"},
		"award_promoted":            {"true"},
		"award_display_order":       {"not-a-number"},
	})
	if invalidAwards.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalidAwards.body, `name="award_key" value="retained-key"`) ||
		!strings.Contains(
			invalidAwards.body,
			`name="award_name" value="Retained Competition Award"`,
		) ||
		!strings.Contains(
			invalidAwards.body,
			">Retained recipient</textarea>",
		) ||
		!strings.Contains(
			invalidAwards.body,
			`name="award_display_order" value="not-a-number"`,
		) {
		t.Fatalf("invalid Competition Awards = %d %q", invalidAwards.status, invalidAwards.body)
	}
	assertAccessibleFormErrors(t, invalidAwards, map[string]string{
		"competition-awards-" + strconv.FormatInt(competitionID, 10) +
			"-award-display-order-0": "positive integer",
	})

	awarded := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                {requireFrontendCSRF(t, invalidAwards)},
		"action":                    {"save-competition-awards"},
		"command_id":                {"browser-save-results-awards"},
		"competition_session_id":    {strconv.FormatInt(competitionID, 10)},
		"expected_revision":         {"2"},
		"award_key":                 {"audience-choice"},
		"award_name":                {"Audience Choice"},
		"award_recipient_entry_ids": {strconv.FormatInt(secondID, 10)},
		"award_recipient_names":     {""},
		"award_promoted":            {"true"},
		"award_display_order":       {"1"},
	})
	if awarded.status != http.StatusSeeOther {
		t.Fatalf("save browser Competition Awards = %d %q", awarded.status, awarded.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{"Draft revision <code>3</code>", "Audience Choice", "audience-choice"} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("Competition Award page lacks %q: %d %q", want, page.status, page.body)
		}
	}
	staleAwards := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                {requireFrontendCSRF(t, page)},
		"action":                    {"save-competition-awards"},
		"command_id":                {"browser-stale-results-awards"},
		"competition_session_id":    {strconv.FormatInt(competitionID, 10)},
		"expected_revision":         {"2"},
		"award_key":                 {"stale-key"},
		"award_name":                {"Stale Competition Award"},
		"award_recipient_entry_ids": {strconv.FormatInt(firstID, 10)},
		"award_recipient_names":     {"Stale recipient"},
		"award_promoted":            {"false"},
		"award_display_order":       {"1"},
	})
	if staleAwards.status != http.StatusConflict ||
		!strings.Contains(staleAwards.body, `name="award_key" value="stale-key"`) ||
		!strings.Contains(
			staleAwards.body,
			`name="award_name" value="Stale Competition Award"`,
		) ||
		!strings.Contains(staleAwards.body, ">Stale recipient</textarea>") {
		t.Fatalf("stale Competition Awards = %d %q", staleAwards.status, staleAwards.body)
	}
	assertAccessibleFormErrors(t, staleAwards, nil)
	page = getFrontendPage(t, administrator, server.address, path)

	ready = postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":             {requireFrontendCSRF(t, page)},
		"action":                 {"mark-results-ready"},
		"command_id":             {"browser-ready-awarded-results"},
		"competition_session_id": {strconv.FormatInt(competitionID, 10)},
		"expected_revision":      {"3"},
	})
	if ready.status != http.StatusSeeOther {
		t.Fatalf("mark awarded browser Results Ready = %d %q", ready.status, ready.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	ceremonyID := frontendNamedValues(page.body, "ceremony_session_id").
		Get("ceremony_session_id")
	if ceremonyID == "" || !strings.Contains(page.body, "Designate Prizegiving") {
		t.Fatalf("browser Prizegiving designation unavailable: %d %q", page.status, page.body)
	}
	designated := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":          {requireFrontendCSRF(t, page)},
		"action":              {"designate-prizegiving"},
		"command_id":          {"browser-designate-prizegiving"},
		"ceremony_session_id": {ceremonyID},
	})
	if designated.status != http.StatusSeeOther {
		t.Fatalf("designate browser Prizegiving = %d %q", designated.status, designated.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Plan revision <code>0</code>") ||
		!strings.Contains(page.body, `name="action" value="save-prizegiving-plan"`) {
		t.Fatalf("designated browser Prizegiving missing plan: %d %q", page.status, page.body)
	}

	template := "{{.Event.Name}} Results\n{{range .Items}}{{with .Competition}}{{.Title}}{{end}}{{end}}"
	invalidPlan := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                     {requireFrontendCSRF(t, page)},
		"action":                         {"save-prizegiving-plan"},
		"command_id":                     {"browser-invalid-prizegiving-plan"},
		"ceremony_session_id":            {ceremonyID},
		"expected_revision":              {"0"},
		"plan_competition_session_id":    {strconv.FormatInt(competitionID, 10)},
		"release_policy":                 {"AllAtCue"},
		"results_text_template_revision": {"0"},
		"results_text_template":          {"Retain this Prizegiving template."},
	})
	if invalidPlan.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalidPlan.body, ">Retain this Prizegiving template.</textarea>") ||
		!strings.Contains(invalidPlan.body, `<option value="AllAtCue" selected>All At Cue</option>`) ||
		!regexp.MustCompile(
			`name="plan_competition_session_id" value="`+
				strconv.FormatInt(competitionID, 10)+`" checked`,
		).MatchString(invalidPlan.body) {
		t.Fatalf("invalid browser Prizegiving plan = %d %q", invalidPlan.status, invalidPlan.body)
	}
	assertAccessibleFormErrors(t, invalidPlan, map[string]string{
		"prizegiving-" + ceremonyID + "-results-text-template-revision": "positive integer",
	})

	savedPlan := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                     {requireFrontendCSRF(t, invalidPlan)},
		"action":                         {"save-prizegiving-plan"},
		"command_id":                     {"browser-save-prizegiving-plan"},
		"ceremony_session_id":            {ceremonyID},
		"expected_revision":              {"0"},
		"plan_competition_session_id":    {strconv.FormatInt(competitionID, 10)},
		"release_policy":                 {"ProgressiveOnReveal"},
		"results_text_template_revision": {"1"},
		"results_text_template":          {template},
	})
	if savedPlan.status != http.StatusSeeOther {
		t.Fatalf("save browser Prizegiving plan = %d %q", savedPlan.status, savedPlan.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{
		"Plan revision <code>1</code>",
		"CompetitionResults",
		"CompetitionAward",
		`name="reveal_method"`,
	} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("browser Prizegiving plan lacks %q: %d %q", want, page.status, page.body)
		}
	}
	invalidEditedPlan := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                     {requireFrontendCSRF(t, page)},
		"action":                         {"save-prizegiving-plan"},
		"command_id":                     {"browser-invalid-edited-prizegiving-plan"},
		"ceremony_session_id":            {ceremonyID},
		"expected_revision":              {"1"},
		"plan_competition_session_id":    {strconv.FormatInt(competitionID, 10)},
		"release_policy":                 {"AtCeremonyEnd"},
		"results_text_template_revision": {"2"},
		"results_text_template":          {"Retain this edited Prizegiving template."},
		"item_kind":                      {"CompetitionResults", "CompetitionAward"},
		"item_competition_session_id":    {strconv.FormatInt(competitionID, 10), strconv.FormatInt(competitionID, 10)},
		"item_award_key":                 {"", "audience-choice"},
		"sequence_display_order":         {"not-a-number", "4"},
		"reveal_method":                  {"AnimatedScoreBars", "SequentialPodium"},
		"publication_display_order":      {"2", "1"},
	})
	if invalidEditedPlan.status != http.StatusUnprocessableEntity ||
		!strings.Contains(
			invalidEditedPlan.body,
			`name="sequence_display_order" value="not-a-number"`,
		) ||
		!strings.Contains(
			invalidEditedPlan.body,
			`name="sequence_display_order" value="4"`,
		) ||
		!strings.Contains(invalidEditedPlan.body, `<option value="AnimatedScoreBars" selected>Animated Score Bars</option>`) ||
		!strings.Contains(invalidEditedPlan.body, `<option value="SequentialPodium" selected>Sequential Podium</option>`) ||
		!strings.Contains(
			invalidEditedPlan.body,
			`name="publication_display_order" value="2"`,
		) ||
		!strings.Contains(
			invalidEditedPlan.body,
			`name="publication_display_order" value="1"`,
		) {
		t.Fatalf(
			"invalid edited Prizegiving plan = %d %q",
			invalidEditedPlan.status,
			invalidEditedPlan.body,
		)
	}
	assertAccessibleFormErrors(t, invalidEditedPlan, map[string]string{
		"prizegiving-" + ceremonyID + "-sequence-display-order-0": "positive integer",
	})

	editedPlan := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                     {requireFrontendCSRF(t, page)},
		"action":                         {"save-prizegiving-plan"},
		"command_id":                     {"browser-edit-prizegiving-plan"},
		"ceremony_session_id":            {ceremonyID},
		"expected_revision":              {"1"},
		"plan_competition_session_id":    {strconv.FormatInt(competitionID, 10)},
		"release_policy":                 {"ProgressiveOnReveal"},
		"results_text_template_revision": {"1"},
		"results_text_template":          {template},
		"item_kind":                      {"CompetitionResults", "CompetitionAward"},
		"item_competition_session_id":    {strconv.FormatInt(competitionID, 10), strconv.FormatInt(competitionID, 10)},
		"item_award_key":                 {"", "audience-choice"},
		"sequence_display_order":         {"1", "2"},
		"reveal_method":                  {"SequentialPodium", "StaticResult"},
		"publication_display_order":      {"1", "2"},
	})
	if editedPlan.status != http.StatusSeeOther {
		t.Fatalf("edit browser Prizegiving plan = %d %q", editedPlan.status, editedPlan.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Plan revision <code>2</code>") ||
		!regexp.MustCompile(`<option value="SequentialPodium" selected>Sequential Podium</option>`).MatchString(page.body) {
		t.Fatalf("edited browser Prizegiving sequence missing: %d %q", page.status, page.body)
	}

	preflight := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":          {requireFrontendCSRF(t, page)},
		"action":              {"run-prizegiving-preflight"},
		"command_id":          {"browser-lock-prizegiving"},
		"ceremony_session_id": {ceremonyID},
		"expected_revision":   {"2"},
	})
	if preflight.status != http.StatusSeeOther {
		t.Fatalf("browser Prizegiving Preflight = %d %q", preflight.status, preflight.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{
		`data-tone="warning">Locked</span>`,
		"Preview locked Results",
		"Rehearse locked Results",
		"Open Prizegiving Program Control",
	} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("locked browser Prizegiving lacks %q: %d %q", want, page.status, page.body)
		}
	}
	preview := getFrontendPage(
		t,
		administrator,
		server.address,
		path+"?ceremony_id="+ceremonyID+"&preview=Preview",
	)
	rehearsal := getFrontendPage(
		t,
		administrator,
		server.address,
		path+"?ceremony_id="+ceremonyID+"&preview=Rehearsal",
	)
	for label, response := range map[string]frontendResponse{
		"Preview": preview, "Rehearsal": rehearsal,
	} {
		if response.status != http.StatusOK ||
			!strings.Contains(response.body, label) ||
			!strings.Contains(response.body, "PREVIEW — NOT PROGRAM OUTPUT") {
			t.Fatalf("%s browser Results = %d %q", label, response.status, response.body)
		}
	}
	publicPath := "/results/events/1/prizegiving/" + ceremonyID
	if public := getFrontendPage(
		t, authenticatedClient(t), server.publicAddress, publicPath,
	); public.status != http.StatusNotFound {
		t.Fatalf("Preview or rehearsal published Results: %d %q", public.status, public.body)
	}

	ceremonyIDValue, err := strconv.ParseInt(ceremonyID, 10, 64)
	if err != nil {
		t.Fatalf("parse browser Prizegiving ID: %v", err)
	}
	operationsPath := "/backstage/events/1/operations"
	operationsPage := getFrontendPage(t, administrator, server.address, operationsPath)
	started := postFrontendForm(t, administrator, server.address, operationsPath, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, operationsPage)},
		"action":                       {"start-session"},
		"command_id":                   {"browser-start-prizegiving"},
		"session_id":                   {ceremonyID},
		"expected_live_state_revision": {"0"},
	})
	if started.status != http.StatusSeeOther {
		t.Fatalf("start browser Prizegiving = %d %q", started.status, started.body)
	}
	programClient := programv1connect.NewProgramControlServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	channel, err := programClient.GetProgramChannel(
		t.Context(),
		connect.NewRequest(&programv1.GetProgramChannelRequest{
			EventId: 1, SessionId: ceremonyIDValue,
		}),
	)
	if err != nil {
		t.Fatalf("load browser Prizegiving Program Channel: %v", err)
	}
	claimed, err := programClient.ChangeControl(
		t.Context(),
		connect.NewRequest(&programv1.ChangeControlRequest{
			EventId: 1, SessionId: ceremonyIDValue,
			Action:                       programv1.ControlAction_CONTROL_ACTION_CLAIM,
			CommandId:                    "claim-results-browser-prizegiving",
			ExpectedControlStateRevision: channel.Msg.GetChannel().GetControlStateRevision(),
		}),
	)
	if err != nil {
		t.Fatalf("claim browser Prizegiving control: %v", err)
	}
	taken, err := programClient.Take(
		t.Context(),
		connect.NewRequest(&programv1.TakeRequest{
			EventId: 1, SessionId: ceremonyIDValue,
			CommandId:                    "take-first-browser-result",
			ExpectedLiveStateRevision:    claimed.Msg.GetChannel().GetLiveStateRevision(),
			ExpectedControlStateRevision: claimed.Msg.GetChannel().GetControlStateRevision(),
			Preview:                      claimed.Msg.GetChannel().GetPreview(),
		}),
	)
	if err != nil {
		t.Fatalf("take first browser Result from %+v: %v", claimed.Msg.GetChannel(), err)
	}
	firstRevealed, err := programClient.ActOnResult(
		t.Context(),
		connect.NewRequest(&programv1.ActOnResultRequest{
			EventId: 1, SessionId: ceremonyIDValue,
			CommandId:                    "reveal-first-browser-result",
			Action:                       programv1.ResultAction_RESULT_ACTION_SKIP_TO_FINAL,
			Item:                         taken.Msg.GetChannel().GetProgramOutput(),
			ExpectedProgramRevision:      taken.Msg.GetChannel().GetLiveStateRevision(),
			ExpectedControlStateRevision: taken.Msg.GetChannel().GetControlStateRevision(),
		}),
	)
	if err != nil {
		t.Fatalf("reveal first browser Result: %v", err)
	}
	partial := getFrontendPage(
		t,
		authenticatedClient(t),
		server.publicAddress,
		publicPath+"/revisions/1/results.json",
	)
	if partial.status != http.StatusOK ||
		!strings.Contains(partial.body, `"status": "Partial"`) ||
		!strings.Contains(partial.body, "Demo Competition") {
		t.Fatalf("progressive partial browser Results = %d %q", partial.status, partial.body)
	}
	secondTaken, err := programClient.Take(
		t.Context(),
		connect.NewRequest(&programv1.TakeRequest{
			EventId: 1, SessionId: ceremonyIDValue,
			CommandId:                    "take-second-browser-result",
			ExpectedLiveStateRevision:    firstRevealed.Msg.GetChannel().GetLiveStateRevision(),
			ExpectedControlStateRevision: firstRevealed.Msg.GetChannel().GetControlStateRevision(),
			Preview:                      firstRevealed.Msg.GetChannel().GetPreview(),
		}),
	)
	if err != nil {
		t.Fatalf("take second browser Result: %v", err)
	}
	if _, err = programClient.ActOnResult(
		t.Context(),
		connect.NewRequest(&programv1.ActOnResultRequest{
			EventId: 1, SessionId: ceremonyIDValue,
			CommandId:                    "reveal-second-browser-result",
			Action:                       programv1.ResultAction_RESULT_ACTION_SKIP_TO_FINAL,
			Item:                         secondTaken.Msg.GetChannel().GetProgramOutput(),
			ExpectedProgramRevision:      secondTaken.Msg.GetChannel().GetLiveStateRevision(),
			ExpectedControlStateRevision: secondTaken.Msg.GetChannel().GetControlStateRevision(),
		}),
	); err != nil {
		t.Fatalf("reveal second browser Result: %v", err)
	}
	completeReveal := getFrontendPage(
		t,
		authenticatedClient(t),
		server.publicAddress,
		publicPath+"/revisions/2/results.json",
	)
	if completeReveal.status != http.StatusOK ||
		!strings.Contains(completeReveal.body, `"status": "Partial"`) ||
		!strings.Contains(completeReveal.body, "Audience Choice") {
		t.Fatalf(
			"complete progressive reveal browser Results = %d %q",
			completeReveal.status,
			completeReveal.body,
		)
	}
	operationsPage = getFrontendPage(t, administrator, server.address, operationsPath)
	ended := postFrontendForm(t, administrator, server.address, operationsPath, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, operationsPage)},
		"action":                       {"end-session"},
		"command_id":                   {"browser-end-prizegiving"},
		"session_id":                   {ceremonyID},
		"expected_live_state_revision": {"1"},
	})
	if ended.status != http.StatusSeeOther {
		t.Fatalf("end browser Prizegiving = %d %q", ended.status, ended.body)
	}
	final := getFrontendPage(
		t,
		authenticatedClient(t),
		server.publicAddress,
		publicPath+"/revisions/3/results.json",
	)
	if final.status != http.StatusOK ||
		!strings.Contains(final.body, `"status": "Final"`) ||
		!strings.Contains(final.body, "Audience Choice") {
		t.Fatalf("final browser Prizegiving Results = %d %q", final.status, final.body)
	}
}

func TestBrowserPublishesAndCorrectsStandaloneResults(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	competitionClient := competitionv1connect.NewCompetitionServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	created, err := competitionClient.CreateEntry(
		t.Context(),
		connect.NewRequest(&competitionv1.CreateEntryRequest{
			EventId: 1, SessionId: competitionID,
			CommandId: "browser-standalone-entry", Name: "Standalone Winner",
		}),
	)
	if err != nil {
		t.Fatalf("create standalone Results Entry: %v", err)
	}
	path := "/backstage/events/1/results"
	page := getFrontendPage(t, administrator, server.address, path)
	saved := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":             {requireFrontendCSRF(t, page)},
		"action":                 {"save-results-draft"},
		"command_id":             {"browser-save-standalone-results"},
		"competition_session_id": {strconv.FormatInt(competitionID, 10)},
		"expected_revision":      {"0"},
		"disposition":            {"Publish"},
		"score_type":             {"None"},
		"score_visibility":       {"Public"},
		"score_precision":        {"0"},
		"score_requirement":      {"Optional"},
		"score_interpretation":   {"Informational"},
		"standing_entry_id":      {strconv.FormatInt(created.Msg.GetEntry().GetId(), 10)},
		"standing":               {"Placed"},
		"placement":              {"1"},
		"display_order":          {"1"},
		"score":                  {""},
	})
	if saved.status != http.StatusSeeOther {
		t.Fatalf("save standalone browser Results = %d %q", saved.status, saved.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	ready := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":             {requireFrontendCSRF(t, page)},
		"action":                 {"mark-results-ready"},
		"command_id":             {"browser-ready-standalone-results"},
		"competition_session_id": {strconv.FormatInt(competitionID, 10)},
		"expected_revision":      {"1"},
	})
	if ready.status != http.StatusSeeOther {
		t.Fatalf("mark standalone browser Results Ready = %d %q", ready.status, ready.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Release standalone Results") {
		t.Fatalf("standalone browser release unavailable: %d %q", page.status, page.body)
	}
	released := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":             {requireFrontendCSRF(t, page)},
		"action":                 {"release-standalone-results"},
		"command_id":             {"browser-release-standalone-results"},
		"competition_session_id": {strconv.FormatInt(competitionID, 10)},
	})
	if released.status != http.StatusSeeOther {
		t.Fatalf("release standalone browser Results = %d %q", released.status, released.body)
	}

	scopePath := "/results/events/1/standalone/" + strconv.FormatInt(competitionID, 10)
	htmlPath := "/events/beamconf-2099/results"
	html := getFrontendPage(t, authenticatedClient(t), server.publicAddress, htmlPath)
	text := getFrontendPage(t, authenticatedClient(t), server.publicAddress, scopePath+"/results.txt")
	jsonRevisionOne := getFrontendPage(
		t,
		authenticatedClient(t),
		server.publicAddress,
		scopePath+"/revisions/1/results.json",
	)
	for label, response := range map[string]frontendResponse{
		"HTML": html, "text": text, "JSON": jsonRevisionOne,
	} {
		if response.status != http.StatusOK ||
			!strings.Contains(response.body, "Standalone Winner") {
			t.Fatalf("standalone %s Results = %d %q", label, response.status, response.body)
		}
	}
	if text.header.Get("ETag") != jsonRevisionOne.header.Get("ETag") {
		t.Fatalf(
			"standalone machine Results ETags = text %q JSON %q",
			text.header.Get("ETag"),
			jsonRevisionOne.header.Get("ETag"),
		)
	}

	server.stop(t)
	server = startBeamersWithPublicListener(t, server.bin, server.dataDir)
	htmlAfterRestart := getFrontendPage(
		t, authenticatedClient(t), server.publicAddress, htmlPath,
	)
	if htmlAfterRestart.status != http.StatusOK ||
		htmlAfterRestart.body != html.body ||
		htmlAfterRestart.header.Get("ETag") != html.header.Get("ETag") {
		t.Fatalf(
			"standalone Results after restart = %d %q %q",
			htmlAfterRestart.status,
			htmlAfterRestart.header.Get("ETag"),
			htmlAfterRestart.body,
		)
	}

	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, `name="action" value="save-results-correction"`) {
		t.Fatalf("browser Results Correction unavailable: %d %q", page.status, page.body)
	}
	correctedJSON := strings.Replace(
		jsonRevisionOne.body,
		"Demo Competition",
		"Corrected Demo Competition",
		1,
	)
	malformed := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"save-results-correction"},
		"command_id":                   {"browser-malformed-correction"},
		"correction_scope":             {"Standalone"},
		"correction_scope_session_id":  {strconv.FormatInt(competitionID, 10)},
		"expected_correction_revision": {"0"},
		"base_publication_revision":    {"1"},
		"corrected_results_json":       {`{"items":`},
		"crew_reason":                  {"Retain this Correction Crew Reason."},
		"public_note":                  {"Retain this Correction public note."},
	})
	if malformed.status != http.StatusUnprocessableEntity ||
		!strings.Contains(malformed.body, `>{&#34;items&#34;:</textarea>`) ||
		!strings.Contains(
			malformed.body,
			">Retain this Correction Crew Reason.</textarea>",
		) ||
		!strings.Contains(
			malformed.body,
			">Retain this Correction public note.</textarea>",
		) {
		t.Fatalf("malformed browser Results Correction = %d %q", malformed.status, malformed.body)
	}
	assertAccessibleFormErrors(t, malformed, map[string]string{
		"results-correction-" + strconv.FormatInt(competitionID, 10) +
			"-corrected-results-json": "valid Results JSON",
	})
	page = malformed
	reasonless := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"save-results-correction"},
		"command_id":                   {"browser-reasonless-correction"},
		"correction_scope":             {"Standalone"},
		"correction_scope_session_id":  {strconv.FormatInt(competitionID, 10)},
		"expected_correction_revision": {"0"},
		"base_publication_revision":    {"1"},
		"corrected_results_json":       {correctedJSON},
	})
	if reasonless.status != http.StatusUnprocessableEntity {
		t.Fatalf("reasonless browser Results Correction = %d %q", reasonless.status, reasonless.body)
	}
	if !strings.Contains(reasonless.body, "Corrected Demo Competition") {
		t.Fatalf("reasonless Results Correction lost safe JSON: %q", reasonless.body)
	}
	assertAccessibleFormErrors(t, reasonless, map[string]string{
		"results-correction-" + strconv.FormatInt(competitionID, 10) + "-crew-reason": "Crew Reason",
	})
	page = reasonless
	correction := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"save-results-correction"},
		"command_id":                   {"browser-save-correction"},
		"correction_scope":             {"Standalone"},
		"correction_scope_session_id":  {strconv.FormatInt(competitionID, 10)},
		"expected_correction_revision": {"0"},
		"base_publication_revision":    {"1"},
		"corrected_results_json":       {correctedJSON},
		"crew_reason":                  {"The published Competition title was incomplete."},
		"public_note":                  {"Competition title corrected."},
	})
	if correction.status != http.StatusSeeOther {
		t.Fatalf("save browser Results Correction = %d %q", correction.status, correction.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Correction revision <code>1</code>") ||
		!strings.Contains(page.body, "Draft") {
		t.Fatalf("saved browser Results Correction unavailable: %d %q", page.status, page.body)
	}
	reviewed := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"review-results-correction"},
		"command_id":                   {"browser-review-correction"},
		"correction_scope":             {"Standalone"},
		"correction_scope_session_id":  {strconv.FormatInt(competitionID, 10)},
		"expected_correction_revision": {"1"},
	})
	if reviewed.status != http.StatusSeeOther {
		t.Fatalf("review browser Results Correction = %d %q", reviewed.status, reviewed.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	published := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"publish-results-correction"},
		"command_id":                   {"browser-publish-correction"},
		"correction_scope":             {"Standalone"},
		"correction_scope_session_id":  {strconv.FormatInt(competitionID, 10)},
		"expected_correction_revision": {"2"},
	})
	if published.status != http.StatusSeeOther {
		t.Fatalf("publish browser Results Correction = %d %q", published.status, published.body)
	}
	corrected := getFrontendPage(t, authenticatedClient(t), server.publicAddress, htmlPath)
	prior := getFrontendPage(
		t,
		authenticatedClient(t),
		server.publicAddress,
		scopePath+"/revisions/1/results.json",
	)
	if corrected.status != http.StatusOK ||
		!strings.Contains(corrected.body, "Corrected Demo Competition") ||
		!strings.Contains(corrected.body, "Competition title corrected.") ||
		prior.body != jsonRevisionOne.body {
		t.Fatalf(
			"corrected or prior browser Results = corrected %d %q, prior %d %q",
			corrected.status, corrected.body, prior.status, prior.body,
		)
	}

	page = getFrontendPage(t, administrator, server.address, path)
	invalidEventAwards := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"save-event-awards"},
		"command_id":        {"browser-invalid-event-awards"},
		"expected_revision": {"0"},
		"event_award_key":   {"retained-event-key"},
		"event_award_name":  {"Retained Event Award"},
		"event_award_recipient_entry_ids": {
			strconv.FormatInt(created.Msg.GetEntry().GetId(), 10),
		},
		"event_award_recipient_names": {"Retained Event recipient"},
		"event_award_path":            {"Standalone"},
		"event_award_display_order":   {"not-a-number"},
	})
	if invalidEventAwards.status != http.StatusUnprocessableEntity ||
		!strings.Contains(
			invalidEventAwards.body,
			`name="event_award_key" value="retained-event-key"`,
		) ||
		!strings.Contains(
			invalidEventAwards.body,
			`name="event_award_name" value="Retained Event Award"`,
		) ||
		!strings.Contains(
			invalidEventAwards.body,
			">Retained Event recipient</textarea>",
		) ||
		!strings.Contains(
			invalidEventAwards.body,
			`name="event_award_display_order" value="not-a-number"`,
		) {
		t.Fatalf(
			"invalid browser Event Awards = %d %q",
			invalidEventAwards.status,
			invalidEventAwards.body,
		)
	}
	assertAccessibleFormErrors(t, invalidEventAwards, map[string]string{
		"event-awards-event-award-display-order-0": "positive integer",
	})
	eventAwards := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                      {requireFrontendCSRF(t, invalidEventAwards)},
		"action":                          {"save-event-awards"},
		"command_id":                      {"browser-save-event-awards"},
		"expected_revision":               {"0"},
		"event_award_key":                 {"community"},
		"event_award_name":                {"Community Award"},
		"event_award_recipient_entry_ids": {""},
		"event_award_recipient_names":     {"Community Hero"},
		"event_award_path":                {"Standalone"},
		"event_award_display_order":       {"1"},
	})
	if eventAwards.status != http.StatusSeeOther {
		t.Fatalf("save browser Event Awards = %d %q", eventAwards.status, eventAwards.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{
		"Community Award",
		"Standalone path revision <code>1</code>",
		`data-tone="draft">Draft</span>`,
	} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("browser Event Awards lack %q: %d %q", want, page.status, page.body)
		}
	}
	staleEventAwards := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"save-event-awards"},
		"command_id":        {"browser-stale-event-awards"},
		"expected_revision": {"0"},
		"event_award_key":   {"stale-event-key"},
		"event_award_name":  {"Stale Event Award"},
		"event_award_recipient_entry_ids": {
			strconv.FormatInt(created.Msg.GetEntry().GetId(), 10),
		},
		"event_award_recipient_names": {"Stale Event recipient"},
		"event_award_path":            {"Standalone"},
		"event_award_display_order":   {"1"},
	})
	if staleEventAwards.status != http.StatusConflict ||
		!strings.Contains(
			staleEventAwards.body,
			`name="event_award_key" value="stale-event-key"`,
		) ||
		!strings.Contains(
			staleEventAwards.body,
			`name="event_award_name" value="Stale Event Award"`,
		) ||
		!strings.Contains(
			staleEventAwards.body,
			">Stale Event recipient</textarea>",
		) {
		t.Fatalf(
			"stale browser Event Awards = %d %q",
			staleEventAwards.status,
			staleEventAwards.body,
		)
	}
	assertAccessibleFormErrors(t, staleEventAwards, nil)
	page = getFrontendPage(t, administrator, server.address, path)
	eventAwardsReady := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":            {requireFrontendCSRF(t, page)},
		"action":                {"mark-event-awards-ready"},
		"command_id":            {"browser-ready-event-awards"},
		"expected_revision":     {"1"},
		"event_award_path_kind": {"Standalone"},
		"event_award_path_prizegiving_session_id": {"0"},
		"expected_path_revision":                  {"1"},
	})
	if eventAwardsReady.status != http.StatusSeeOther {
		t.Fatalf("mark browser Event Awards Ready = %d %q", eventAwardsReady.status, eventAwardsReady.body)
	}
	eventAwardsPreflight := getFrontendPage(
		t,
		administrator,
		server.address,
		path+"?event_awards_preflight=true",
	)
	if eventAwardsPreflight.status != http.StatusOK ||
		!strings.Contains(
			eventAwardsPreflight.body,
			"Standalone Event Awards Preflight passed without changing release state.",
		) {
		t.Fatalf(
			"standalone Event Awards Preflight = %d %q",
			eventAwardsPreflight.status,
			eventAwardsPreflight.body,
		)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	eventAwardsReleased := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":             {requireFrontendCSRF(t, page)},
		"action":                 {"release-standalone-event-awards"},
		"command_id":             {"browser-release-event-awards"},
		"expected_revision":      {"1"},
		"expected_path_revision": {"1"},
	})
	if eventAwardsReleased.status != http.StatusSeeOther {
		t.Fatalf("release browser Event Awards = %d %q", eventAwardsReleased.status, eventAwardsReleased.body)
	}
	eventAwardsPath := "/results/events/1/event-awards"
	for label, response := range map[string]frontendResponse{
		"HTML": getFrontendPage(
			t, authenticatedClient(t), server.publicAddress, "/events/beamconf-2099/results",
		),
		"text": getFrontendPage(
			t, authenticatedClient(t), server.publicAddress, eventAwardsPath+"/results.txt",
		),
		"JSON": getFrontendPage(
			t,
			authenticatedClient(t),
			server.publicAddress,
			eventAwardsPath+"/revisions/1/results.json",
		),
	} {
		if response.status != http.StatusOK ||
			!strings.Contains(response.body, "Community Award") ||
			!strings.Contains(response.body, "Community Hero") {
			t.Fatalf("public Event Awards %s = %d %q", label, response.status, response.body)
		}
	}
}

func TestBrowserDefersAndResolvesCompetitionEntries(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	path := "/backstage/events/1/competitions/" +
		strconv.FormatInt(competitionID, 10) + "/entries"

	names := []string{"Aurora", "Beacon", "Comet"}
	for index, name := range names {
		page := getFrontendPage(t, administrator, server.address, path)
		created := postFrontendForm(t, administrator, server.address, path, url.Values{
			"csrf_token": {requireFrontendCSRF(t, page)},
			"action":     {"create-entry"},
			"command_id": {"browser-live-entry-" + strconv.Itoa(index)},
			"entry_name": {name},
			"crew_notes": {"private " + name},
		})
		if created.status != http.StatusSeeOther {
			t.Fatalf("create live Entry %q = %d %q", name, created.status, created.body)
		}
	}
	page := getFrontendPage(t, administrator, server.address, path)
	if configured := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                  {requireFrontendCSRF(t, page)},
		"action":                      {"configure-readiness"},
		"command_id":                  {"browser-live-readiness"},
		"expected_readiness_revision": {"0"},
	}); configured.status != http.StatusSeeOther {
		t.Fatalf("disable live file delivery = %d %q", configured.status, configured.body)
	}

	operationsPath := "/backstage/events/1/operations"
	operationsPage := getFrontendPage(t, administrator, server.address, operationsPath)
	started := postFrontendForm(t, administrator, server.address, operationsPath, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, operationsPage)},
		"action":                       {"start-session"},
		"command_id":                   {"start-browser-competition"},
		"session_id":                   {strconv.FormatInt(competitionID, 10)},
		"expected_live_state_revision": {"0"},
	})
	if started.status != http.StatusSeeOther {
		t.Fatalf("start browser Competition = %d %q", started.status, started.body)
	}
	operator := provisionOperator(t, administrator, server)
	operator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	backstage := getFrontendPage(
		t,
		operator,
		server.address,
		"/backstage/events/1/sessions",
	)
	if backstage.status != http.StatusOK ||
		!strings.Contains(backstage.body, `href="`+path+`"`) ||
		!strings.Contains(backstage.body, "Competition Entries and Attachments") {
		t.Fatalf("Operator Competition Entry navigation = %d %q", backstage.status, backstage.body)
	}
	page = getFrontendPage(t, operator, server.address, path)
	if page.status != http.StatusOK ||
		!strings.Contains(page.body, `name="action" value="record-technical-failure"`) ||
		strings.Contains(page.body, `name="action" value="create-entry"`) ||
		strings.Contains(page.body, `name="action" value="resolve-entry"`) {
		t.Fatalf("Operator Competition Entry controls = %d %q", page.status, page.body)
	}
	claimed := postFrontendForm(t, operator, server.address, path, url.Values{
		"csrf_token":                {requireFrontendCSRF(t, page)},
		"action":                    {"claim-control"},
		"command_id":                {"browser-claim-control"},
		"expected_control_revision": {"0"},
	})
	if claimed.status != http.StatusSeeOther {
		t.Fatalf("claim browser Program Control = %d %q", claimed.status, claimed.body)
	}

	programClient := programv1connect.NewProgramControlServiceClient(
		operator, "http://"+server.address, connect.WithProtoJSON(),
	)
	competitionClient := competitionv1connect.NewCompetitionServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	current, err := programClient.GetProgramChannel(t.Context(), connect.NewRequest(
		&programv1.GetProgramChannelRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil {
		t.Fatalf("read claimed Program Channel: %v", err)
	}
	channel := current.Msg.GetChannel()
	for _, commandID := range []string{"take-browser-upcoming", "take-browser-starting"} {
		taken, takeErr := programClient.Take(t.Context(), connect.NewRequest(
			&programv1.TakeRequest{
				EventId: 1, SessionId: competitionID, CommandId: commandID,
				ExpectedLiveStateRevision:    channel.GetLiveStateRevision(),
				ExpectedControlStateRevision: channel.GetControlStateRevision(),
				Preview:                      channel.GetPreview(),
			},
		))
		if takeErr != nil {
			t.Fatalf("advance browser Competition: %v", takeErr)
		}
		channel = taken.Msg.GetChannel()
	}

	firstDeferredID := channel.GetNext().GetEntryId()
	page = getFrontendPage(t, operator, server.address, path)
	deferred := postFrontendForm(t, operator, server.address, path, url.Values{
		"csrf_token":                {requireFrontendCSRF(t, page)},
		"action":                    {"defer-entry"},
		"command_id":                {"browser-defer-first"},
		"entry_id":                  {strconv.FormatInt(firstDeferredID, 10)},
		"expected_revision":         {frontendEntryRevision(t, page.body, int(firstDeferredID))},
		"expected_program_revision": {strconv.FormatInt(channel.GetLiveStateRevision(), 10)},
		"expected_control_revision": {strconv.FormatInt(channel.GetControlStateRevision(), 10)},
	})
	if deferred.status != http.StatusSeeOther {
		t.Fatalf("defer first browser Entry = %d %q", deferred.status, deferred.body)
	}
	current, err = programClient.GetProgramChannel(t.Context(), connect.NewRequest(
		&programv1.GetProgramChannelRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil {
		t.Fatalf("read deferred Program Channel: %v", err)
	}
	channel = current.Msg.GetChannel()
	presentedID := channel.GetNext().GetEntryId()
	order, err := competitionClient.PreviewEntryOrder(t.Context(), connect.NewRequest(
		&competitionv1.PreviewEntryOrderRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil {
		t.Fatalf("preview browser Entry Order: %v", err)
	}
	taken, err := programClient.Take(t.Context(), connect.NewRequest(
		&programv1.TakeRequest{
			EventId: 1, SessionId: competitionID, CommandId: "take-browser-presented",
			ExpectedLiveStateRevision:    channel.GetLiveStateRevision(),
			ExpectedControlStateRevision: channel.GetControlStateRevision(),
			Preview:                      channel.GetPreview(),
			ExpectedEntryOrderRevision:   order.Msg.GetEntryOrder().GetRevision(),
			EntryOrderFingerprint:        order.Msg.GetFingerprint(),
		},
	))
	if err != nil {
		t.Fatalf("present browser Entry: %v", err)
	}
	channel = taken.Msg.GetChannel()
	secondDeferredID := channel.GetNext().GetEntryId()
	page = getFrontendPage(t, operator, server.address, path)
	failed := postFrontendForm(t, operator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"record-technical-failure"},
		"command_id":        {"browser-live-failure"},
		"entry_id":          {strconv.FormatInt(secondDeferredID, 10)},
		"expected_revision": {frontendEntryRevision(t, page.body, int(secondDeferredID))},
		"crew_reason":       {"Encoder unavailable"},
	})
	if failed.status != http.StatusSeeOther {
		t.Fatalf("record live Technical Failure = %d %q", failed.status, failed.body)
	}
	page = getFrontendPage(t, operator, server.address, path)
	deferred = postFrontendForm(t, operator, server.address, path, url.Values{
		"csrf_token":                {requireFrontendCSRF(t, page)},
		"action":                    {"defer-entry"},
		"command_id":                {"browser-defer-second"},
		"entry_id":                  {strconv.FormatInt(secondDeferredID, 10)},
		"expected_revision":         {frontendEntryRevision(t, page.body, int(secondDeferredID))},
		"expected_program_revision": {strconv.FormatInt(channel.GetLiveStateRevision(), 10)},
		"expected_control_revision": {strconv.FormatInt(channel.GetControlStateRevision(), 10)},
	})
	if deferred.status != http.StatusSeeOther {
		t.Fatalf("defer second browser Entry = %d %q", deferred.status, deferred.body)
	}

	operationsPage = getFrontendPage(t, operator, server.address, operationsPath)
	endPreview := postFrontendForm(t, operator, server.address, operationsPath, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, operationsPage)},
		"action":                       {"preview-end-session"},
		"session_id":                   {strconv.FormatInt(competitionID, 10)},
		"expected_live_state_revision": {"1"},
	})
	for _, want := range []string{
		"End Competition Preview",
		"Deferred Entries will become Not Presented",
		names[int(firstDeferredID)-1],
		names[int(secondDeferredID)-1],
		`name="action" value="end-session"`,
		`name="deferred_entries_fingerprint"`,
	} {
		if endPreview.status != http.StatusOK || !strings.Contains(endPreview.body, want) {
			t.Fatalf("browser Competition End Preview lacks %q: %d %q", want, endPreview.status, endPreview.body)
		}
	}
	endFormStart := strings.Index(endPreview.body, `name="action" value="end-session"`)
	endFormEnd := strings.Index(endPreview.body[endFormStart:], "</form>")
	end := frontendNamedValues(
		endPreview.body[endFormStart:endFormStart+endFormEnd],
		"session_id",
		"expected_live_state_revision",
		"command_id",
		"deferred_entries_fingerprint",
	)
	end.Set("csrf_token", requireFrontendCSRF(t, endPreview))
	end.Set("action", "end-session")
	unconfirmedEnd := postFrontendForm(t, operator, server.address, operationsPath, end)
	if unconfirmedEnd.status != http.StatusConflict ||
		!strings.Contains(unconfirmedEnd.body, "End Competition Preview") {
		t.Fatalf(
			"unconfirmed browser Competition End = %d %q",
			unconfirmedEnd.status,
			unconfirmedEnd.body,
		)
	}
	endConfirmationID := "end-session-" + strconv.FormatInt(competitionID, 10) +
		"-confirmed-deferred-entries"
	if regexp.MustCompile(`id="` + endConfirmationID + `"[^>]+checked`).MatchString(
		unconfirmedEnd.body,
	) {
		t.Fatalf("unconfirmed browser Competition End remained checked: %q", unconfirmedEnd.body)
	}
	assertAccessibleFormErrors(t, unconfirmedEnd, map[string]string{
		endConfirmationID: "Confirm deferred Entries.",
	})

	endFormStart = strings.Index(unconfirmedEnd.body, `name="action" value="end-session"`)
	endFormEnd = strings.Index(unconfirmedEnd.body[endFormStart:], "</form>")
	end = frontendNamedValues(
		unconfirmedEnd.body[endFormStart:endFormStart+endFormEnd],
		"session_id",
		"expected_live_state_revision",
		"command_id",
		"deferred_entries_fingerprint",
	)
	end.Set("csrf_token", requireFrontendCSRF(t, unconfirmedEnd))
	end.Set("action", "end-session")
	end.Set("deferred_entries_fingerprint", "stale-end-preview")
	end.Set("confirmed_deferred_entries", "true")
	staleEnd := postFrontendForm(t, operator, server.address, operationsPath, end)
	if staleEnd.status != http.StatusConflict ||
		!strings.Contains(staleEnd.body, "End Competition Preview") {
		t.Fatalf("stale browser Competition End = %d %q", staleEnd.status, staleEnd.body)
	}
	if regexp.MustCompile(`id="` + endConfirmationID + `"[^>]+checked`).MatchString(staleEnd.body) {
		t.Fatalf("stale browser Competition End remained checked: %q", staleEnd.body)
	}
	assertAccessibleFormErrors(t, staleEnd, map[string]string{
		endConfirmationID: "review and confirm",
	})

	endFormStart = strings.Index(staleEnd.body, `name="action" value="end-session"`)
	endFormEnd = strings.Index(staleEnd.body[endFormStart:], "</form>")
	end = frontendNamedValues(
		staleEnd.body[endFormStart:endFormStart+endFormEnd],
		"session_id",
		"expected_live_state_revision",
		"command_id",
		"deferred_entries_fingerprint",
	)
	end.Set("csrf_token", requireFrontendCSRF(t, staleEnd))
	end.Set("action", "end-session")
	end.Set("confirmed_deferred_entries", "true")
	ended := postFrontendForm(t, operator, server.address, operationsPath, end)
	if ended.status != http.StatusSeeOther {
		t.Fatalf("end browser Competition = %d %q", ended.status, ended.body)
	}

	page = getFrontendPage(t, administrator, server.address, path)
	invalidPublicMessage := strings.Repeat("p", 10001)
	invalidResolution := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                      {requireFrontendCSRF(t, page)},
		"action":                          {"resolve-entry"},
		"command_id":                      {"browser-invalid-resolution"},
		"entry_id":                        {strconv.FormatInt(firstDeferredID, 10)},
		"expected_revision":               {frontendEntryRevision(t, page.body, int(firstDeferredID))},
		"result_disposition":              {"Withheld"},
		"crew_reason":                     {"Organizer decision"},
		"public_disqualification_message": {invalidPublicMessage},
	})
	if invalidResolution.status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid Entry resolution = %d %q", invalidResolution.status, invalidResolution.body)
	}
	assertAccessibleFormErrors(t, invalidResolution, map[string]string{
		"resolve-entry-" + strconv.FormatInt(firstDeferredID, 10) +
			"-public-disqualification-message": "Enter no more than 10000 characters.",
	})
	resolved := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":         {requireFrontendCSRF(t, invalidResolution)},
		"action":             {"resolve-entry"},
		"command_id":         {"browser-resolve-corrected"},
		"entry_id":           {strconv.FormatInt(firstDeferredID, 10)},
		"expected_revision":  {frontendEntryRevision(t, invalidResolution.body, int(firstDeferredID))},
		"result_disposition": {"Withheld"},
		"crew_reason":        {"Organizer decision"},
	})
	if resolved.status != http.StatusSeeOther {
		t.Fatalf("corrected Entry resolution = %d %q", resolved.status, resolved.body)
	}

	resolutions := []struct {
		entryID     int64
		disposition string
		reason      string
		public      string
	}{
		{presentedID, "Disqualified", "Rules violation", "Disqualified after review"},
		{secondDeferredID, "Eligible", "Technical failure accepted", ""},
	}
	for index, resolution := range resolutions {
		page = getFrontendPage(t, administrator, server.address, path)
		result := postFrontendForm(t, administrator, server.address, path, url.Values{
			"csrf_token":                      {requireFrontendCSRF(t, page)},
			"action":                          {"resolve-entry"},
			"command_id":                      {"browser-resolve-" + strconv.Itoa(index)},
			"entry_id":                        {strconv.FormatInt(resolution.entryID, 10)},
			"expected_revision":               {frontendEntryRevision(t, page.body, int(resolution.entryID))},
			"result_disposition":              {resolution.disposition},
			"crew_reason":                     {resolution.reason},
			"public_disqualification_message": {resolution.public},
		})
		if result.status != http.StatusSeeOther {
			t.Fatalf(
				"resolve browser Entry %d as %s = %d %q",
				resolution.entryID, resolution.disposition, result.status, result.body,
			)
		}
	}
	page = getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{"Result: Withheld", "Result: Disqualified", "Result: Eligible"} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("resolved browser Entries lack %q: %q", want, page.body)
		}
	}
	public := getFrontendPage(
		t,
		authenticatedClient(t),
		server.publicAddress,
		"/events/beamconf-2099/schedule/sessions/"+strconv.FormatInt(competitionID, 10),
	)
	withheldName := names[firstDeferredID-1]
	if strings.Contains(public.body, withheldName) ||
		strings.Contains(public.body, "Organizer decision") ||
		strings.Contains(public.body, "Encoder unavailable") ||
		strings.Contains(public.body, "private ") ||
		!strings.Contains(public.body, "Disqualified after review") {
		t.Fatalf("public Competition resolution projection = %d %q", public.status, public.body)
	}
	server.stop(t)
}

type frontendResponse struct {
	status int
	header http.Header
	body   string
}

type frontendHTTPResult struct {
	page frontendResponse
	err  error
}

func readFrontendHTTPResult(
	response *http.Response,
	requestErr error,
) frontendHTTPResult {
	if requestErr != nil {
		return frontendHTTPResult{err: requestErr}
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	return frontendHTTPResult{
		page: frontendResponse{
			status: response.StatusCode,
			header: response.Header,
			body:   string(body),
		},
		err: errors.Join(readErr, closeErr),
	}
}

func frontendEntryRevision(t *testing.T, body string, entryID int) string {
	t.Helper()
	expression := regexp.MustCompile(
		`name="entry_id" value="` + strconv.Itoa(entryID) +
			`">\s*<input type="hidden" name="expected_revision" value="([0-9]+)"`,
	)
	match := expression.FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("Entry #%d revision not found in %q", entryID, body)
	}
	return match[1]
}

func frontendPresentationRevision(t *testing.T, body string, sessionID int64) string {
	t.Helper()
	expression := regexp.MustCompile(
		`(?s)name="action" value="update-presentation">.*?` +
			`name="session_id" value="` + strconv.FormatInt(sessionID, 10) +
			`">.*?name="expected_revision" value="([0-9]+)"`,
	)
	match := expression.FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("Presentation #%d revision not found in %q", sessionID, body)
	}
	return match[1]
}

func frontendNamedValues(body string, names ...string) url.Values {
	values := make(url.Values)
	for _, name := range names {
		expression := regexp.MustCompile(
			`type="hidden" name="` + regexp.QuoteMeta(name) + `" value="([^"]*)"`,
		)
		for _, match := range expression.FindAllStringSubmatch(body, -1) {
			values.Add(name, match[1])
		}
	}
	return values
}

func frontendActivationFormValues(
	t *testing.T,
	body string,
	names ...string,
) url.Values {
	t.Helper()
	for _, form := range regexp.MustCompile(`(?s)<form\b.*?</form>`).FindAllString(body, -1) {
		if strings.Contains(form, `name="action" value="activate"`) {
			return frontendNamedValues(form, names...)
		}
	}
	t.Fatalf("page has no activation form: %q", body)
	return nil
}

func frontendCheckboxValues(body, name string) []string {
	expression := regexp.MustCompile(
		`type="checkbox" name="` + regexp.QuoteMeta(name) + `" value="([^"]*)" checked`,
	)
	var values []string
	for _, match := range expression.FindAllStringSubmatch(body, -1) {
		values = append(values, match[1])
	}
	return values
}

func frontendCheckboxOptions(body, name string) []string {
	expression := regexp.MustCompile(
		`type="checkbox"\s+name="` + regexp.QuoteMeta(name) + `"\s+value="([^"]*)"`,
	)
	var values []string
	for _, match := range expression.FindAllStringSubmatch(body, -1) {
		values = append(values, match[1])
	}
	return values
}

func getFrontendPage(
	t *testing.T,
	client *http.Client,
	address string,
	path string,
) frontendResponse {
	t.Helper()
	return getFrontendPageAccept(t, client, address, path, "text/html")
}

func getFrontendPageAccept(
	t *testing.T,
	client *http.Client,
	address string,
	path string,
	accept string,
) frontendResponse {
	t.Helper()
	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"http://"+address+path,
		http.NoBody,
	)
	if err != nil {
		t.Fatalf("create GET %s: %v", path, err)
	}
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	return doFrontendRequest(t, client, request)
}

func postFrontendForm(
	t *testing.T,
	client *http.Client,
	address string,
	path string,
	values url.Values,
) frontendResponse {
	t.Helper()
	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"http://"+address+path,
		strings.NewReader(values.Encode()),
	)
	if err != nil {
		t.Fatalf("create POST %s: %v", path, err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "text/html")
	request.Header.Set("Origin", "http://"+address)
	return doFrontendRequest(t, client, request)
}

func postFrontendMultipart(
	t *testing.T,
	client *http.Client,
	address string,
	path string,
	fields map[string]string,
	fileField string,
	filename string,
	content []byte,
) frontendResponse {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatalf("write multipart field %s: %v", name, err)
		}
	}
	if fileField != "" {
		file, err := writer.CreateFormFile(fileField, filename)
		if err != nil {
			t.Fatalf("create multipart file: %v", err)
		}
		if _, err = file.Write(content); err != nil {
			t.Fatalf("write multipart file: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart body: %v", err)
	}
	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"http://"+address+path,
		&body,
	)
	if err != nil {
		t.Fatalf("create multipart POST %s: %v", path, err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Accept", "text/html")
	request.Header.Set("Origin", "http://"+address)
	return doFrontendRequest(t, client, request)
}

func doFrontendRequest(
	t *testing.T,
	client *http.Client,
	request *http.Request,
) frontendResponse {
	t.Helper()
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", request.Method, request.URL, err)
	}
	body, readErr := io.ReadAll(response.Body)
	if err = errors.Join(readErr, response.Body.Close()); err != nil {
		t.Fatalf("read %s %s: %v", request.Method, request.URL, err)
	}
	return frontendResponse{
		status: response.StatusCode,
		header: response.Header.Clone(),
		body:   string(body),
	}
}

func assertFrontendRecovery(
	t *testing.T,
	response frontendResponse,
	status int,
	heading string,
) {
	t.Helper()
	if response.status != status ||
		response.header.Get("Content-Type") != "text/html; charset=utf-8" ||
		response.header.Get("Cache-Control") != "no-store" ||
		response.header.Get("Location") != "" {
		t.Fatalf(
			"browser recovery = %d Content-Type %q Cache-Control %q Location %q",
			response.status,
			response.header.Get("Content-Type"),
			response.header.Get("Cache-Control"),
			response.header.Get("Location"),
		)
	}
	for _, want := range []string{
		`<html lang="en">`,
		`class="skip-link" href="#main-content"`,
		`<main id="main-content" tabindex="-1">`,
		"<h1>" + heading + "</h1>",
		`<a href="/">Return to Events</a>`,
	} {
		if !strings.Contains(response.body, want) {
			t.Errorf("browser recovery lacks %q: %q", want, response.body)
		}
	}
	for _, name := range []string{
		"Content-Security-Policy",
		"Permissions-Policy",
		"Referrer-Policy",
		"X-Content-Type-Options",
		"X-Frame-Options",
	} {
		if response.header.Get(name) == "" {
			t.Errorf("browser recovery lacks %s", name)
		}
	}
}

func assertAccessibleFormErrors(
	t *testing.T,
	response frontendResponse,
	expected map[string]string,
) {
	t.Helper()
	for _, want := range []string{
		`id="error-summary"`,
		`role="alert"`,
		`tabindex="-1"`,
		`autofocus`,
	} {
		if !strings.Contains(response.body, want) {
			t.Errorf("form error response lacks %q: %q", want, response.body)
		}
	}
	for fieldID, message := range expected {
		for _, want := range []string{
			`href="#` + fieldID + `"`,
			`id="` + fieldID + `-error"`,
			`aria-describedby="` + fieldID + `-error"`,
			message,
		} {
			if !strings.Contains(response.body, want) {
				t.Errorf("form error response lacks %q: %q", want, response.body)
			}
		}
	}
	if got := strings.Count(response.body, `aria-invalid="true"`); got < len(expected) {
		t.Errorf("form error response has %d invalid fields, want at least %d", got, len(expected))
	}
}

func requireFrontendCSRF(t *testing.T, response frontendResponse) string {
	t.Helper()
	match := frontendCSRFInput.FindStringSubmatch(response.body)
	if len(match) != 2 {
		t.Fatalf("CSRF page = %d %q", response.status, response.body)
	}
	return match[1]
}

func frontendBackstageNavigation(t *testing.T, response frontendResponse) string {
	t.Helper()
	const start = `<nav class="backstage-links"`
	startAt := strings.Index(response.body, start)
	if response.status != http.StatusOK || startAt < 0 {
		t.Fatalf("Backstage navigation page = %d %q", response.status, response.body)
	}
	endAt := strings.Index(response.body[startAt:], "</nav>")
	if endAt < 0 {
		t.Fatalf("Backstage navigation is unclosed: %q", response.body)
	}
	return response.body[startAt : startAt+endAt]
}

func assertFrontendPrimaryNavigation(
	t *testing.T,
	response frontendResponse,
	backstage bool,
) {
	t.Helper()
	const start = `<nav aria-label="Primary"`
	startAt := strings.Index(response.body, start)
	if response.status != http.StatusOK || startAt < 0 {
		t.Fatalf("primary navigation page = %d %q", response.status, response.body)
	}
	endAt := strings.Index(response.body[startAt:], "</nav>")
	if endAt < 0 {
		t.Fatalf("primary navigation is unclosed: %q", response.body)
	}
	navigation := response.body[startAt : startAt+endAt]
	for _, want := range []string{
		`href="/">Events</a>`,
		`href="/profile">Profile</a>`,
		`href="/my-participation">My Participation</a>`,
		`href="/my-schedule">My Schedule</a>`,
		`action="/sign-out"`,
		">Ada Admin</span>",
	} {
		if !strings.Contains(navigation, want) {
			t.Errorf("primary navigation lacks %q: %q", want, navigation)
		}
	}
	if got := strings.Contains(navigation, `href="/backstage">Backstage</a>`); got != backstage {
		t.Errorf("primary navigation Backstage = %t, want %t: %q", got, backstage, navigation)
	}
	if !regexp.MustCompile(`name="csrf_token" value="[^"]+"`).MatchString(navigation) {
		t.Errorf("primary navigation lacks a CSRF proof: %q", navigation)
	}
}

func assertFrontendSignedOutNavigation(t *testing.T, response frontendResponse) {
	t.Helper()
	const start = `<nav aria-label="Primary"`
	startAt := strings.Index(response.body, start)
	if response.status != http.StatusOK || startAt < 0 {
		t.Fatalf("signed-out navigation page = %d %q", response.status, response.body)
	}
	endAt := strings.Index(response.body[startAt:], "</nav>")
	if endAt < 0 {
		t.Fatalf("signed-out navigation is unclosed: %q", response.body)
	}
	navigation := response.body[startAt : startAt+endAt]
	for _, want := range []string{`href="/">Events</a>`, ">Sign in</a>"} {
		if !strings.Contains(navigation, want) {
			t.Errorf("signed-out navigation lacks %q: %q", want, navigation)
		}
	}
	for _, private := range []string{
		`href="/profile"`,
		`href="/my-participation"`,
		`href="/backstage"`,
		`action="/sign-out"`,
	} {
		if strings.Contains(navigation, private) {
			t.Errorf("signed-out navigation exposes %q: %q", private, navigation)
		}
	}
}

func assertFrontendEventShell(
	t *testing.T,
	response frontendResponse,
	activePath string,
	breadcrumbs ...string,
) {
	t.Helper()
	if response.status != http.StatusOK {
		t.Fatalf("Event shell page = %d %q", response.status, response.body)
	}
	eventNavigation := `<nav aria-label="` + breadcrumbs[1] + ` Event"`
	for _, want := range []string{
		`<details class="event-drawer">`,
		`<aside class="event-sidebar">`,
		`<nav aria-label="Breadcrumb"`,
		eventNavigation,
		"BeamConf 2099",
	} {
		if !strings.Contains(response.body, want) {
			t.Errorf("Event shell lacks %q: %q", want, response.body)
		}
	}
	if count := strings.Count(response.body, eventNavigation); count != 2 {
		t.Errorf("Event navigation copies = %d, want 2: %q", count, response.body)
	}
	active := regexp.MustCompile(
		`href="` + regexp.QuoteMeta(activePath) + `"[^>]*aria-current="page"`,
	)
	if !active.MatchString(response.body) {
		t.Errorf("Event shell has no active %q destination: %q", activePath, response.body)
	}
	breadcrumbAt := strings.Index(response.body, `<nav aria-label="Breadcrumb"`)
	if breadcrumbAt < 0 {
		return
	}
	breadcrumbEnd := strings.Index(response.body[breadcrumbAt:], "</nav>")
	if breadcrumbEnd < 0 {
		return
	}
	breadcrumb := response.body[breadcrumbAt : breadcrumbAt+breadcrumbEnd]
	for _, want := range breadcrumbs {
		if !strings.Contains(breadcrumb, want) {
			t.Errorf("Event breadcrumb lacks %q: %q", want, breadcrumb)
		}
	}
}

func frontendLinkPath(t *testing.T, response frontendResponse, label string) string {
	t.Helper()
	link := regexp.MustCompile(`href="([^"]+)">` + regexp.QuoteMeta(label) + `</a>`).
		FindStringSubmatch(response.body)
	if response.status != http.StatusOK || len(link) != 2 {
		t.Fatalf("%q link page = %d %q", label, response.status, response.body)
	}
	return strings.ReplaceAll(link[1], "&amp;", "&")
}

func frontendResponseCookie(
	t *testing.T,
	header http.Header,
	name string,
) *http.Cookie {
	t.Helper()
	response := &http.Response{Header: header}
	for _, cookie := range response.Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("response has no %s cookie: %v", name, header.Values("Set-Cookie"))
	return nil
}
