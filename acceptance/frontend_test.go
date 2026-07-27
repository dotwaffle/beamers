package acceptance_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"io"
	"maps"
	"mime/multipart"
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

	competitionv1 "github.com/dotwaffle/beamers/gen/beamers/competition/v1"
	"github.com/dotwaffle/beamers/gen/beamers/competition/v1/competitionv1connect"
	programv1 "github.com/dotwaffle/beamers/gen/beamers/program/v1"
	"github.com/dotwaffle/beamers/gen/beamers/program/v1/programv1connect"
	"github.com/dotwaffle/beamers/internal/backup"
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
		path := "/backstage/events/" + strconv.Itoa(eventID) + "/planning"
		page := getFrontendPage(t, administrator, server.address, path)
		values := frontendNamedValues(
			page.body,
			"command_id",
			"expected_event_revision",
		)
		values.Set("csrf_token", requireFrontendCSRF(t, page))
		values.Set("action", "event")
		values.Set("event_name", name)
		values.Set("public_slug", slug)
		values.Set("planned_start_date", start)
		values.Set("planned_end_date", end)
		values.Set("timezone", "Europe/Berlin")
		values.Set("event_locale", locale)
		values.Set("content_language", "en-GB")
		values.Set("event_day_boundary", "06:00")
		values.Set("entry_default_disposition", "Pending")
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
	setListed(1, "Revision 2099", "revision-2099", "2099-08-21", "2099-08-23", "en-GB", true)
	setListed(2, "Summer Showcase", "summer-private", "2026-08-21", "2026-08-23", "de-DE", false)
	setListed(2, "Summer Showcase", "summer-showcase", "2026-08-21", "2026-08-23", "de-DE", true)

	root := getFrontendPage(t, authenticatedClient(t), server.publicAddress, "/")
	for _, want := range []string{
		"Featured Event",
		`href="/events/revision-2099"`,
		"Revision 2099",
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
		"/events/revision-2099":   "Revision 2099",
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
		redirect := getFrontendPage(
			t,
			publicClient,
			server.publicAddress,
			"/events/"+alias,
		)
		if redirect.status != http.StatusFound ||
			redirect.header.Get("Location") != "/events/summer-final" {
			t.Fatalf(
				"Event Slug Alias %q = %d Location %q",
				alias,
				redirect.status,
				redirect.header.Get("Location"),
			)
		}
	}
	collision := submitEvent(
		1,
		"Revision 2099",
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
		"/backstage/events/3/planning",
		url.Values{
			"csrf_token": {requireFrontendCSRF(t, root)},
			"action":     {"event"},
			"public":     {"true"},
		},
	)
	if denied.status != http.StatusNotFound {
		t.Fatalf("Administrator published without Event Grant: %d %q", denied.status, denied.body)
	}

	setListed(1, "Revision 2099", "revision-2099", "2099-08-21", "2099-08-23", "en-GB", false)
	root = getFrontendPage(t, authenticatedClient(t), server.publicAddress, "/")
	if strings.Contains(root.body, "Revision 2099") ||
		!strings.Contains(root.body, "Summer Showcase") {
		t.Fatalf("Public Event Listing followed Active Event instead of Producer state: %q", root.body)
	}
	if hidden := getFrontendPage(
		t, authenticatedClient(t), server.publicAddress, "/events/revision-2099",
	); hidden.status != http.StatusNotFound {
		t.Fatalf("unlisted Active Event = %d %q", hidden.status, hidden.body)
	}

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	server = startBeamersWithPublicListener(t, bin, dataDir)
	root = getFrontendPage(t, authenticatedClient(t), server.publicAddress, "/")
	if !strings.Contains(root.body, `href="/events/summer-showcase"`) ||
		strings.Contains(root.body, "Revision 2099") {
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
	fullFidelity := postFrontendForm(
		t,
		administrator,
		server.address,
		"/backstage/installation",
		url.Values{
			"csrf_token":             {requireFrontendCSRF(t, page)},
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
	assertJSONRequest(
		t,
		administrator,
		server.address,
		"/admin/restores/apply",
		map[string]any{
			"password":                "correct horse battery staple",
			"acknowledge_replacement": true,
		},
		http.StatusNotFound,
		"404 page not found\n",
	)

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
	canceled := postFrontendForm(
		t,
		administrator,
		server.address,
		"/backstage/installation",
		url.Values{
			"csrf_token":               {requireFrontendCSRF(t, restartedPage)},
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

	const password = "operator administration correct horse battery staple"
	created := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":     {requireFrontendCSRF(t, page)},
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
	granted := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":         {requireFrontendCSRF(t, page)},
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
		!strings.Contains(duplicateGrant.body, `role="alert"`) {
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
	if !strings.Contains(page.body, "Event #2, Account #1: Observer") {
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
	if !strings.Contains(operatorNavigation, "Event #1") ||
		!strings.Contains(operatorNavigation, "Emergency Alerts") ||
		strings.Contains(operatorNavigation, "Event #2") {
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
		!strings.Contains(invalidActivation.body, `role="alert"`) {
		t.Fatalf(
			"invalid browser Event activation = %d %q",
			invalidActivation.status,
			invalidActivation.body,
		)
	}
	activated := postFrontendForm(t, administrator, server.address, path, confirmation)
	if activated.status != http.StatusSeeOther {
		t.Fatalf("browser Event activation = %d %q", activated.status, activated.body)
	}

	dataDir := server.dataDir
	bin := server.bin
	server.stop(t)
	server = startBeamersWithPublicListener(t, bin, dataDir)
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Active Event #1") ||
		!strings.Contains(page.body, "generation 2") {
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
	if !strings.Contains(page.body, "Ended · revision 2") {
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
	adjustment.Set("confirmed", "true")
	adjustment.Set("hard_boundary_confirmed", "true")
	adjusted := postFrontendForm(t, producer, server.address, path, adjustment)
	if adjusted.status != http.StatusSeeOther {
		t.Fatalf("browser Adjust Target = %d %q", adjusted.status, adjusted.body)
	}

	page = getFrontendPage(t, producer, server.address, path)
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
	if !strings.Contains(page.body, "Canceled · revision 3") ||
		!strings.Contains(page.body, `name="action" value="preview-reinstate"`) {
		t.Fatalf("canceled browser Session = %d %q", page.status, page.body)
	}
	reinstatePreview := postFrontendForm(t, producer, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
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
	reinstatement.Set("confirmed", "true")
	reinstatement.Set("hard_boundary_confirmed", "true")
	reinstated := postFrontendForm(t, producer, server.address, path, reinstatement)
	if reinstated.status != http.StatusSeeOther {
		t.Fatalf("browser Reinstate Session = %d %q", reinstated.status, reinstated.body)
	}
	page = getFrontendPage(t, producer, server.address, path)
	if !strings.Contains(page.body, "Scheduled · revision 4") {
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
		!strings.Contains(recovery.body, "Existing Display ID for recovery") {
		t.Fatalf("unsafe Display recovery = %d %q", recovery.status, recovery.body)
	}
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
		staleControl.header.Get("X-Beamers-Build") != buildVersion {
		t.Fatalf("stale Backstage control = %d %q", staleControl.status, staleControl.body)
	}
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
	unconfirmed := maps.Clone(stageConfirmation)
	unconfirmed.Del("confirmed")
	if rejected := postFrontendForm(
		t, administrator, server.address, path, unconfirmed,
	); rejected.status != http.StatusUnprocessableEntity ||
		!strings.Contains(rejected.body, `role="alert"`) {
		t.Fatalf("unconfirmed Stage Message = %d %q", rejected.status, rejected.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if count := strings.Count(page.body, "<h3>StageMessage</h3>"); count != 1 {
		t.Fatalf("exact Stage Message retry created %d active Overrides: %q", count, page.body)
	}

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
	if stale := postFrontendForm(
		t, administrator, server.address, emergencyPath, staleEmergency,
	); stale.status != http.StatusConflict ||
		stale.header.Get("X-Beamers-Build") != buildVersion {
		t.Fatalf("stale Emergency Alert = %d %q", stale.status, stale.body)
	}
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
	if stale := postFrontendForm(
		t, administrator, server.address, clearPath, staleClear,
	); stale.status != http.StatusConflict ||
		stale.header.Get("X-Beamers-Build") != buildVersion {
		t.Fatalf("stale Emergency clear = %d %q", stale.status, stale.body)
	}
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
	if !strings.Contains(page.body, "Draft revision 1") ||
		!strings.Contains(page.body, "Ready: false") {
		t.Fatalf("saved browser Results missing revision state: %d %q", page.status, page.body)
	}

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
	if !strings.Contains(page.body, "Draft revision 1") ||
		!strings.Contains(page.body, "Ready: true") {
		t.Fatalf("reviewed browser Results missing Ready state: %d %q", page.status, page.body)
	}

	if tied := save("browser-tie-results", "1", []string{"1", "1"}); tied.status != http.StatusSeeOther {
		t.Fatalf("save tied browser Results = %d %q", tied.status, tied.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Draft revision 2") ||
		!strings.Contains(page.body, "Ready: false") {
		t.Fatalf("changed browser Results did not clear Ready: %d %q", page.status, page.body)
	}

	awarded := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                {requireFrontendCSRF(t, page)},
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
	for _, want := range []string{"Draft revision 3", "Audience Choice", "audience-choice"} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("Competition Award page lacks %q: %d %q", want, page.status, page.body)
		}
	}

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
	if !strings.Contains(page.body, "Plan revision 0") ||
		!strings.Contains(page.body, `name="action" value="save-prizegiving-plan"`) {
		t.Fatalf("designated browser Prizegiving missing plan: %d %q", page.status, page.body)
	}

	template := "{{.Event.Name}} Results\n{{range .Items}}{{with .Competition}}{{.Title}}{{end}}{{end}}"
	savedPlan := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                     {requireFrontendCSRF(t, page)},
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
		"Plan revision 1",
		"CompetitionResults",
		"CompetitionAward",
		`name="reveal_method"`,
	} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("browser Prizegiving plan lacks %q: %d %q", want, page.status, page.body)
		}
	}

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
	if !strings.Contains(page.body, "Plan revision 2") ||
		!regexp.MustCompile(`<option selected>SequentialPodium</option>`).MatchString(page.body) {
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
		"Locked: true",
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
	html := getFrontendPage(t, authenticatedClient(t), server.publicAddress, scopePath)
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
		if response.header.Get("ETag") != html.header.Get("ETag") {
			t.Fatalf("standalone %s ETag = %q, want %q", label, response.header.Get("ETag"), html.header.Get("ETag"))
		}
	}

	server.stop(t)
	server = startBeamersWithPublicListener(t, server.bin, server.dataDir)
	htmlAfterRestart := getFrontendPage(
		t, authenticatedClient(t), server.publicAddress, scopePath,
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
	page = getFrontendPage(t, administrator, server.address, path)
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
	if !strings.Contains(page.body, "Correction revision 1") ||
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
	corrected := getFrontendPage(t, authenticatedClient(t), server.publicAddress, scopePath)
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
	eventAwards := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                      {requireFrontendCSRF(t, page)},
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
	for _, want := range []string{"Community Award", "Standalone path revision 1", "Ready: false"} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("browser Event Awards lack %q: %d %q", want, page.status, page.body)
		}
	}
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
			t, authenticatedClient(t), server.publicAddress, eventAwardsPath,
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
	end.Set("confirmed_deferred_entries", "true")
	ended := postFrontendForm(t, operator, server.address, operationsPath, end)
	if ended.status != http.StatusSeeOther {
		t.Fatalf("end browser Competition = %d %q", ended.status, ended.body)
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
	file, err := writer.CreateFormFile(fileField, filename)
	if err != nil {
		t.Fatalf("create multipart file: %v", err)
	}
	if _, err = file.Write(content); err != nil {
		t.Fatalf("write multipart file: %v", err)
	}
	if err = writer.Close(); err != nil {
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
