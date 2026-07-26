package acceptance_test

import (
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var frontendCSRFInput = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

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
	} {
		response := getFrontendPage(t, client, server.address, asset)
		if response.status != http.StatusOK || response.body == "" {
			t.Fatalf("GET %s = %d, %q", asset, response.status, response.body)
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

type frontendResponse struct {
	status int
	header http.Header
	body   string
}

func getFrontendPage(
	t *testing.T,
	client *http.Client,
	address string,
	path string,
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

func requireFrontendCSRF(t *testing.T, response frontendResponse) string {
	t.Helper()
	match := frontendCSRFInput.FindStringSubmatch(response.body)
	if response.status != http.StatusOK || len(match) != 2 {
		t.Fatalf("CSRF page = %d %q", response.status, response.body)
	}
	return match[1]
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
