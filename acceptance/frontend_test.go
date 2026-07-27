package acceptance_test

import (
	"encoding/base64"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	competitionv1 "github.com/dotwaffle/beamers/gen/beamers/competition/v1"
	"github.com/dotwaffle/beamers/gen/beamers/competition/v1/competitionv1connect"
	programv1 "github.com/dotwaffle/beamers/gen/beamers/program/v1"
	"github.com/dotwaffle/beamers/gen/beamers/program/v1/programv1connect"
	sessionv1 "github.com/dotwaffle/beamers/gen/beamers/session/v1"
	"github.com/dotwaffle/beamers/gen/beamers/session/v1/sessionv1connect"
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
		"Event #1",
		"Producer",
		"Event #2",
		"Observer",
	} {
		if adminPage.status != http.StatusOK || !strings.Contains(adminNavigation, text) {
			t.Errorf("Administrator Backstage lacks %q: %d %q", text, adminPage.status, adminPage.body)
		}
	}
	producer := assertBackstage(
		"Pat Producer",
		[]string{"Plan and publish", "Competition Entries and Attachments", "Results and Prizegiving"},
		[]string{"Installation"},
	)
	assertBackstage(
		"Opal Operator",
		[]string{
			"Sessions and Displays",
			"Program Output and Overrides",
			"Emergency Alerts",
			"Results and Prizegiving",
		},
		[]string{"Plan and publish", "Competition Entries and Attachments", "Installation"},
	)
	assertBackstage(
		"Olive Observer",
		[]string{"Event overview"},
		[]string{"Sessions and Displays", "Results and Prizegiving", "Installation"},
	)
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

func TestBrowserPlansAndPublishesEvent(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	const path = "/backstage/events/1/planning"
	newEvent := getFrontendPage(t, administrator, server.address, "/backstage/events/new")
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
	page := getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{
		"Plan and publish",
		`name="event_name"`,
		`name="location_name"`,
		`name="csv_data"`,
		`name="icalendar_data"`,
		"Draft revision 0",
		"Published revision 0",
	} {
		if page.status != http.StatusOK || !strings.Contains(page.body, want) {
			t.Fatalf("planning page lacks %q: %d %q", want, page.status, page.body)
		}
	}

	configured := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                        {requireFrontendCSRF(t, page)},
		"action":                            {"event"},
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
		"target_adjustment_presets_seconds": {"-300,300,600"},
	})
	if configured.status != http.StatusSeeOther || configured.header.Get("Location") != path {
		t.Fatalf("configure Event = %d %q", configured.status, configured.body)
	}

	page = getFrontendPage(t, administrator, server.address, path)
	created := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":              {requireFrontendCSRF(t, page)},
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
	if !strings.Contains(page.body, "Draft revision 1") ||
		!strings.Contains(page.body, "Opening Ceremony") {
		t.Fatalf("reviewable Draft = %d %q", page.status, page.body)
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
		!strings.Contains(stale.body, "Draft changed") {
		t.Fatalf("stale Draft response = %d %q", stale.status, stale.body)
	}

	preview := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"action":     {"publish-preview"},
		"change_id":  {"4"},
	})
	if preview.status != http.StatusOK ||
		!strings.Contains(preview.body, "Confirm publish") ||
		!strings.Contains(preview.body, "Draft revision 1") ||
		!strings.Contains(preview.body, "Published revision 0") ||
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
		!strings.Contains(stalePublish.body, "Publish Preview is stale") {
		t.Fatalf("stale Publish = %d %q", stalePublish.status, stalePublish.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	preview = postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"action":     {"publish-preview"},
		"change_id":  {"4"},
	})
	confirmation := frontendNamedValues(
		preview.body,
		"draft_revision", "published_revision", "fingerprint", "change_id",
	)
	confirmation.Set("csrf_token", requireFrontendCSRF(t, preview))
	confirmation.Set("action", "publish")
	confirmation.Set("command_id", "browser-publish-rundown")
	published := postFrontendForm(t, administrator, server.address, path, confirmation)
	if published.status != http.StatusSeeOther {
		t.Fatalf("Publish = %d %q", published.status, published.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Published revision 1") ||
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
	if !strings.Contains(page.body, "Draft revision 4") ||
		!strings.Contains(page.body, `value="Hall Alpha"`) {
		t.Fatalf("edited materialized Draft = %d %q", page.status, page.body)
	}
	publicSchedule := getFrontendPage(
		t, authenticatedClient(t), server.publicAddress, "/schedule",
	)
	for _, private := range []string{"Opening Ceremony", "Deferred Track", "Hall Alpha"} {
		if strings.Contains(publicSchedule.body, private) {
			t.Fatalf("public Schedule disclosed %q: %q", private, publicSchedule.body)
		}
	}

	const csvMappings = "kind=record_type\nkey=external_key\ntitle=title\nstart=planned_start\nend=planned_end\nlane=lane\nlocation=location"
	const csvData = "kind,key,title,start,end,lane,location\nSession,browser-session,Imported Session,2026-08-21T11:00:00+02:00,2026-08-21T11:30:00+02:00,Main Stage,Hall Alpha\n"
	csvPreview := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":   {requireFrontendCSRF(t, page)},
		"action":       {"csv-preview"},
		"csv_mappings": {csvMappings},
		"csv_data":     {csvData},
	})
	if csvPreview.status != http.StatusOK ||
		!strings.Contains(csvPreview.body, "Imported Session") ||
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
	if imported := postFrontendForm(
		t, administrator, server.address, path, csvConfirmation,
	); imported.status != http.StatusSeeOther {
		t.Fatalf("CSV import = %d %q", imported.status, imported.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Draft revision 5") ||
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
	if imported := postFrontendForm(
		t, administrator, server.address, path, icalendarConfirmation,
	); imported.status != http.StatusSeeOther {
		t.Fatalf("iCalendar import = %d %q", imported.status, imported.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Draft revision 6") ||
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
		`href="/backstage/events/1/planning#competition-entries"`,
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

	created := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":     {requireFrontendCSRF(t, page)},
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
	if !strings.Contains(page.body, "review current: true") {
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
		!strings.Contains(page.body, "review current: false") {
		t.Fatalf("Entry edit did not invalidate review = %d %q", page.status, page.body)
	}

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
	if !strings.Contains(page.body, "Disposition: Rejected") {
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
	if !strings.Contains(page.body, "review current: true") {
		t.Fatalf("review before Attachment replacement = %d %q", page.status, page.body)
	}

	uploadPath := path + "/upload"
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
	if !strings.Contains(page.body, "review current: false") {
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
	if !strings.Contains(page.body, "Final: true") ||
		!strings.Contains(page.body, "Primary: true") {
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
	if !strings.Contains(page.body, "Release Hold: true") {
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
	if !strings.Contains(page.body, "Competition override: OnEnded") {
		t.Fatalf("Competition Attachment release projection = %d %q", page.status, page.body)
	}

	expiresAt := time.Now().UTC().Add(3 * time.Hour).Format("2006-01-02T15:04")
	reopened := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
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
		!strings.Contains(page.body, "Open until") {
		t.Fatalf("bounded Reopen Window projection = %d %q", page.status, page.body)
	}
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
		!strings.Contains(page.body, "Entry Release Hold: true") {
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
	if !strings.Contains(page.body, "Release Eligibility: CrewOnly") {
		t.Fatalf("immutable Release Eligibility projection = %d %q", page.status, page.body)
	}
	if public := getFrontendPage(
		t, authenticatedClient(t), server.publicAddress, path,
	); public.status != http.StatusNotFound {
		t.Fatalf("public-listener Entries = %d, want 404", public.status)
	}
	server.stop(t)
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

	sessionClient := sessionv1connect.NewSessionControlServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	if _, err := sessionClient.StartSession(t.Context(), connect.NewRequest(
		&sessionv1.StartSessionRequest{
			EventId: 1, SessionId: competitionID, CommandId: "start-browser-competition",
			ExpectedLiveStateRevision: proto.Int64(0),
		},
	)); err != nil {
		t.Fatalf("start browser Competition: %v", err)
	}
	operator := provisionOperator(t, administrator, server)
	operator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	backstage := getFrontendPage(t, operator, server.address, "/backstage")
	if backstage.status != http.StatusOK ||
		!strings.Contains(backstage.body, `href="`+path+`"`) ||
		!strings.Contains(backstage.body, "Demo Competition Entries and Attachments") {
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

	preflight, err := competitionClient.PreflightEnd(t.Context(), connect.NewRequest(
		&competitionv1.PreflightEndRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || !preflight.Msg.GetRequiresConfirmation() {
		t.Fatalf("browser Competition End preflight = %+v, %v", preflight, err)
	}
	if _, err = sessionClient.EndSession(t.Context(), connect.NewRequest(
		&sessionv1.EndSessionRequest{
			EventId: 1, SessionId: competitionID, CommandId: "end-browser-competition",
			ExpectedLiveStateRevision:  proto.Int64(1),
			ConfirmedDeferredEntries:   true,
			DeferredEntriesFingerprint: preflight.Msg.GetFingerprint(),
		},
	)); err != nil {
		t.Fatalf("end browser Competition: %v", err)
	}

	resolutions := []struct {
		entryID     int64
		disposition string
		reason      string
		public      string
	}{
		{firstDeferredID, "Withheld", "Organizer decision", ""},
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
		"/schedule/sessions/"+strconv.FormatInt(competitionID, 10),
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
