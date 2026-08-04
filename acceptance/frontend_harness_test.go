package acceptance_test

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var frontendCSRFInput = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)
var recoveryCodeOutput = regexp.MustCompile(`data-recovery-code>([^<]+)</code>`)
var recoveryTokenOutput = regexp.MustCompile(`data-recovery-token>([^<]+)</code>`)

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

// frontendEntryArticle isolates one Entry's <article id="entry-N">...</article>
// fragment, so an assertion cannot pass on account of a different Entry's
// markup elsewhere on the same Competition Entries page.
func frontendEntryArticle(t *testing.T, body string, entryID int) string {
	t.Helper()
	start := strings.Index(body, `id="entry-`+strconv.Itoa(entryID)+`"`)
	if start == -1 {
		t.Fatalf("Entry #%d article not found in %q", entryID, body)
	}
	rest := body[start:]
	end := strings.Index(rest, `<article id="entry-`)
	if end == -1 {
		end = strings.Index(rest, "</section>")
	}
	if end == -1 {
		t.Fatalf("Entry #%d article has no closing boundary in %q", entryID, body)
	}
	return rest[:end]
}

// frontendSessionArticle isolates one Session's <article id="session-N">
// fragment on the Operations page, so a lifecycle badge and revision found
// independently cannot combine to pass an assertion for the wrong Session.
func frontendSessionArticle(t *testing.T, body string, sessionID int64) string {
	t.Helper()
	marker := `id="session-` + strconv.FormatInt(sessionID, 10) + `"`
	start := strings.Index(body, marker)
	if start == -1 {
		t.Fatalf("Session #%d article not found in %q", sessionID, body)
	}
	rest := body[start:]
	end := strings.Index(rest, `<article`)
	if end == -1 {
		end = strings.Index(rest, "</section>")
	}
	if end == -1 {
		t.Fatalf("Session #%d article has no closing boundary in %q", sessionID, body)
	}
	return rest[:end]
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
