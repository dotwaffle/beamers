package acceptance_test

import (
	"database/sql"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

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
		"command_id":   {frontendNamedValues(profile.body, "command_id").Get("command_id")},
		"display_name": {"Public Person"},
		"published":    {"true"},
	})
	if saved.status != http.StatusSeeOther {
		t.Fatalf("Profile update = %d %q", saved.status, saved.body)
	}
	auditEntries, auditBody := readAuditHistory(t, admin, server.address)
	profileAudited := false
	for _, entry := range auditEntries {
		if entry.Action != "UpdateAccountProfile" {
			continue
		}
		profileAudited = true
		if entry.ActorAccountID != 2 || entry.TargetType != "Account" ||
			entry.TargetID != "2" || entry.Outcome != "Succeeded" ||
			entry.ServerTime.IsZero() {
			t.Errorf("Profile update Audit Entry = %+v", entry)
		}
	}
	if !profileAudited {
		t.Fatalf("Profile update did not record an Audit Entry: %s", auditBody)
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
		"lane_ids":           {"7", "9"},
		"display_group_keys": {"retain-stage"},
		"capability":         {"ViewResults"},
	})
	if invalidGrant.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalidGrant.body, `value="2" selected`) ||
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

	assertJSONRequest(
		t, administrator, server.address, "/admin/events/1/grants",
		map[string]any{"account_id": 1, "role": "Producer", "command_id": "grant-lane-picker-producer"},
		http.StatusCreated, "{\"event_id\":1,\"account_id\":1,\"role\":\"Producer\"}\n",
	)
	_, laneID := addPlacementLane(t, administrator, server)
	page = getFrontendPage(t, administrator, server.address, path)
	if !regexp.MustCompile(
		`type="checkbox"\s+name="lane_ids"\s+value="`+strconv.FormatInt(laneID, 10)+`"`,
	).MatchString(page.body) || !strings.Contains(page.body, "Side Lane") {
		t.Fatalf("Event Grant Lane picker lacks Side Lane checkbox: %q", page.body)
	}
	laneScopedAccount := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":     {requireFrontendCSRF(t, page)},
		"action":         {"create-account"},
		"command_id":     {"browser-create-lane-scoped-account"},
		"account_handle": {"quinn"},
		"display_name":   {"Quinn Crew"},
		"password":       {"lane picker acceptance correct horse"},
	})
	if laneScopedAccount.status != http.StatusSeeOther {
		t.Fatalf("browser lane-scoped Account creation = %d %q", laneScopedAccount.status, laneScopedAccount.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	lanePicked := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"action":     {"grant"},
		"command_id": {"browser-grant-with-lane-picker"},
		"account_id": {"3"},
		"event_id":   {"1"},
		"role":       {"Operator"},
		"lane_ids":   {strconv.FormatInt(laneID, 10)},
	})
	if lanePicked.status != http.StatusSeeOther {
		t.Fatalf("browser Event Grant with Lane picker = %d %q", lanePicked.status, lanePicked.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Lanes "+strconv.FormatInt(laneID, 10)) {
		t.Fatalf("Event Grant Lane picker selection absent: %q", page.body)
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
	if !strings.Contains(page.body, `<th scope="row" id="administration-account-row-2">`) ||
		!strings.Contains(page.body, `aria-describedby="administration-account-row-2">Disable Account`) ||
		!strings.Contains(page.body, `aria-describedby="administration-account-row-2">Issue Recovery Token`) {
		t.Fatalf(
			"administration Accounts row lacks identity association: %q",
			page.body,
		)
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
