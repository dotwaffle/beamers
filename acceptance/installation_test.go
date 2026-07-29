package acceptance_test

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	activationv1 "github.com/dotwaffle/beamers/gen/beamers/activation/v1"
	"github.com/dotwaffle/beamers/gen/beamers/activation/v1/activationv1connect"
	competitionv1 "github.com/dotwaffle/beamers/gen/beamers/competition/v1"
	"github.com/dotwaffle/beamers/gen/beamers/competition/v1/competitionv1connect"
	programv1 "github.com/dotwaffle/beamers/gen/beamers/program/v1"
	"github.com/dotwaffle/beamers/gen/beamers/program/v1/programv1connect"
	resultsv1 "github.com/dotwaffle/beamers/gen/beamers/results/v1"
	"github.com/dotwaffle/beamers/gen/beamers/results/v1/resultsv1connect"
	rundownv1 "github.com/dotwaffle/beamers/gen/beamers/rundown/v1"
	"github.com/dotwaffle/beamers/gen/beamers/rundown/v1/rundownv1connect"
	sessionv1 "github.com/dotwaffle/beamers/gen/beamers/session/v1"
	"github.com/dotwaffle/beamers/gen/beamers/session/v1/sessionv1connect"
	"github.com/dotwaffle/beamers/internal/store/storetest"
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

func TestAdministratorRevisesPreviewsActivatesAndRestoresInstallationTheme(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	guest := authenticatedClient(t)
	guest.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response := get(t, guest, server.address, "/backstage/themes")
	_ = readResponseBody(t, response)
	if response.StatusCode != http.StatusSeeOther ||
		response.Header.Get("Location") != "/sign-in" {
		t.Fatalf(
			"unauthenticated Theme administration = %d location %q",
			response.StatusCode,
			response.Header.Get("Location"),
		)
	}
	assertJSONRequest(
		t,
		administrator,
		server.address,
		"/admin/accounts",
		map[string]string{
			"name":       "Pat Producer",
			"password":   "producer correct horse battery staple",
			"command_id": "create-theme-observer",
		},
		http.StatusCreated,
		"{\"id\":2,\"name\":\"Pat Producer\",\"administrator\":false}\n",
	)
	observer := authenticatedClient(t)
	assertJSONRequest(
		t,
		observer,
		server.address,
		"/auth/sign-in",
		map[string]string{
			"name":     "Pat Producer",
			"password": "producer correct horse battery staple",
		},
		http.StatusNoContent,
		"",
	)
	assertFrontendRecovery(
		t,
		getFrontendPage(t, observer, server.address, "/backstage/themes"),
		http.StatusForbidden,
		"Access denied",
	)
	csrf := browserCSRFToken(t, administrator, server.address, "/backstage/themes")
	profileCSRF := browserCSRFToken(t, administrator, server.address, "/profile")
	_, _ = postBrowserForm(
		t,
		administrator,
		server.address,
		"/profile",
		url.Values{
			"csrf_token":   {profileCSRF},
			"display_name": {"Ada Admin"},
			"published":    {"true"},
		},
	)

	inaccessible := installationThemeForm(csrf, "create-draft", "create-inaccessible-theme")
	inaccessible.Set("text_color", "#777777")
	inaccessible.Set("surface_color", "#ffffff")
	inaccessible.Set("background_color", "#ffffff")
	_, body := postBrowserForm(
		t,
		administrator,
		server.address,
		"/backstage/themes",
		inaccessible,
	)
	for _, want := range []string{
		"Theme Revision #1",
		"4.5:1",
		`data-theme-preview="public"`,
		`data-theme-preview="account"`,
		`data-theme-preview="backstage"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("inaccessible Theme preview missing %q: %s", want, body)
		}
	}
	assertGETContains(t, administrator, server.address, "/assets/installation-theme.css", "#080b15")

	blocked := installationThemeForm(csrf, "activate", "activate-inaccessible-theme")
	blocked.Set("revision_id", "1")
	blocked.Set("expected_active_revision_id", "0")
	status, body := postBrowserForm(
		t,
		administrator,
		server.address,
		"/backstage/themes",
		blocked,
	)
	if status != http.StatusUnprocessableEntity ||
		!strings.Contains(body, "Theme activation is blocked") {
		t.Fatalf("blocked Theme activation = %d %q", status, body)
	}

	unknown := installationThemeForm(csrf, "create-draft", "create-arbitrary-theme")
	unknown.Set("raw_css", "body { display: none }")
	unknown.Set("raw_html", "<script>alert(1)</script>")
	unknown.Set("javascript", "alert(1)")
	unknown.Set("font_upload", "arbitrary.woff2")
	status, body = postBrowserForm(
		t,
		administrator,
		server.address,
		"/backstage/themes",
		unknown,
	)
	if status != http.StatusBadRequest ||
		!strings.Contains(body, "undocumented Theme control") {
		t.Fatalf("arbitrary Theme input = %d %q", status, body)
	}

	accessible := installationThemeForm(csrf, "create-draft", "create-accessible-theme")
	accessible.Set("background_color", "#101828")
	status, body = postBrowserForm(
		t,
		administrator,
		server.address,
		"/backstage/themes",
		accessible,
	)
	if !strings.Contains(body, "Theme Revision #2") ||
		!strings.Contains(body, "passes known activation checks") {
		t.Fatalf("accessible Theme preview = %d %q", status, body)
	}
	assertGETContains(t, administrator, server.address, "/assets/installation-theme.css", "#080b15")

	activate := installationThemeForm(csrf, "activate", "activate-accessible-theme")
	activate.Set("revision_id", "2")
	activate.Set("expected_active_revision_id", "0")
	_, _ = postBrowserForm(
		t,
		administrator,
		server.address,
		"/backstage/themes",
		activate,
	)
	assertGETContains(t, administrator, server.address, "/assets/installation-theme.css", "#101828")
	assertGETContains(t, administrator, server.address, "/", "/assets/installation-theme.css")
	assertGETContains(t, administrator, server.address, "/sign-in", "/assets/installation-theme.css")
	assertGETContains(
		t,
		administrator,
		server.address,
		"/people/ada%20admin",
		"/assets/installation-theme.css",
	)
	_, _ = postBrowserForm(
		t,
		administrator,
		server.address,
		"/effects",
		url.Values{
			"csrf_token":     {csrf},
			"reduce_effects": {"true"},
		},
	)
	assertGETContains(
		t,
		administrator,
		server.address,
		"/people/ada%20admin",
		`data-reduced-effects="true"`,
	)
	assertGETContains(t, administrator, server.address, "/people/ada%20admin", "Resume effects")
	for _, want := range []string{
		`body[data-reduced-effects="true"]`,
		"@media (prefers-reduced-motion: reduce)",
		"@media (forced-colors: active)",
	} {
		assertGETContains(
			t,
			administrator,
			server.address,
			"/assets/installation-theme.css",
			want,
		)
	}

	rollback := installationThemeForm(csrf, "activate", "rollback-built-in-theme")
	rollback.Set("revision_id", "0")
	rollback.Set("expected_active_revision_id", "2")
	_, _ = postBrowserForm(
		t,
		administrator,
		server.address,
		"/backstage/themes",
		rollback,
	)
	assertGETContains(t, administrator, server.address, "/assets/installation-theme.css", "#080b15")

	reactivate := installationThemeForm(csrf, "activate", "reactivate-accessible-theme")
	reactivate.Set("revision_id", "2")
	reactivate.Set("expected_active_revision_id", "0")
	_, _ = postBrowserForm(
		t,
		administrator,
		server.address,
		"/backstage/themes",
		reactivate,
	)
	assertGETContains(t, administrator, server.address, "/assets/installation-theme.css", "#101828")

	server.stop(t)
	restarted := startBeamers(t, server.bin, server.dataDir)
	assertGETContains(t, authenticatedClient(t), restarted.address, "/assets/installation-theme.css", "#101828")
	restarted.stop(t)

	backupPath := filepath.Join(t.TempDir(), "theme-backup.zip")
	runBeamers(t, server.bin, "backup", "--data-dir", server.dataDir, "--output", backupPath)
	restoredDataDir := filepath.Join(t.TempDir(), "restored")
	runBeamers(t, server.bin, "init", "--data-dir", restoredDataDir)
	var plan struct {
		JournalPath string `json:"journal_path"`
	}
	if err := json.Unmarshal([]byte(runBeamersOutput(
		t,
		server.bin,
		"restore", "preview",
		"--input", backupPath,
		"--data-dir", restoredDataDir,
	)), &plan); err != nil || plan.JournalPath == "" {
		t.Fatalf("decode Theme Restore plan = %+v, %v", plan, err)
	}
	runBeamers(
		t,
		server.bin,
		"restore", "apply",
		"--journal", plan.JournalPath,
		"--acknowledge-replacement",
		"--acknowledge-configuration-differences",
	)
	restored := startBeamers(t, server.bin, restoredDataDir)
	assertGETContains(t, authenticatedClient(t), restored.address, "/assets/installation-theme.css", "#101828")
	restored.stop(t)
}

func TestProducerActivatesInheritedEventThemeAcrossPublicSchedule(t *testing.T) {
	producer, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, producer, server)
	displayClient := enrollAndAssignDisplay(
		t, producer, server, "Theme Display", "location-signage",
	)
	csrf := browserCSRFToken(
		t,
		producer,
		server.address,
		"/backstage/events/1/theme",
	)
	assertGETContains(
		t,
		producer,
		server.address,
		"/backstage/events/1/theme",
		"Fully inherited Installation Theme",
	)

	draft := eventThemeForm(csrf, "create-draft", "create-event-theme")
	draft.Set("background_color", "#112233")
	draft.Set("location-signage_accent_color", "#ffdf6e")
	draft.Set("location-signage_motion", "still")
	status, body := postBrowserForm(
		t,
		producer,
		server.address,
		"/backstage/events/1/theme",
		draft,
	)
	if status != http.StatusOK ||
		!strings.Contains(body, "Event Theme Revision #1") ||
		!strings.Contains(body, "passes known inherited activation checks") ||
		!strings.Contains(body, `data-theme-variant="location-signage"`) ||
		!strings.Contains(body, "#ffdf6e") {
		t.Fatalf("Event Theme preview = %d %q", status, body)
	}
	assertGETContains(
		t,
		producer,
		server.address,
		"/assets/events/1/theme.css",
		"#080b15",
	)
	beforeActivation := readDisplaySnapshot(t, displayClient, server.address)
	beforePosition, err := strconv.ParseUint(beforeActivation.StreamPosition, 10, 64)
	if err != nil {
		t.Fatalf("parse pre-activation Display stream position: %v", err)
	}
	streamContext, cancelStream := context.WithTimeout(t.Context(), 5*time.Second)
	streamURL := fmt.Sprintf(
		"http://%s/display/events?stream_id=%s&after=%s",
		server.address,
		url.QueryEscape(beforeActivation.StreamID),
		url.QueryEscape(beforeActivation.StreamPosition),
	)
	streamRequest, err := http.NewRequestWithContext(
		streamContext, http.MethodGet, streamURL, http.NoBody,
	)
	if err != nil {
		t.Fatalf("create Event Theme Display stream request: %v", err)
	}
	streamResponse, err := displayClient.Do(streamRequest)
	if err != nil {
		t.Fatalf("open Event Theme Display stream: %v", err)
	}
	streamReader := bufio.NewReader(streamResponse.Body)
	if heartbeat, readErr := streamReader.ReadString('\n'); readErr != nil ||
		heartbeat != ": heartbeat\n" {
		t.Fatalf("Event Theme Display heartbeat = %q, %v", heartbeat, readErr)
	}
	if _, err = streamReader.ReadString('\n'); err != nil {
		t.Fatalf("finish Event Theme Display heartbeat: %v", err)
	}

	activate := eventThemeForm(csrf, "activate", "activate-event-theme")
	activate.Set("revision_id", "1")
	activate.Set("expected_active_revision_id", "0")
	_, _ = postBrowserForm(
		t,
		producer,
		server.address,
		"/backstage/events/1/theme",
		activate,
	)
	var invalidation strings.Builder
	for {
		line, readErr := streamReader.ReadString('\n')
		if readErr != nil {
			t.Fatalf("read Event Theme Display invalidation: %v", readErr)
		}
		if line == "\n" {
			break
		}
		invalidation.WriteString(line)
	}
	for _, want := range []string{
		fmt.Sprintf("id: %d\n", beforePosition+1),
		"event: invalidate\n",
		fmt.Sprintf(`"stream_position":%d`, beforePosition+1),
	} {
		if !strings.Contains(invalidation.String(), want) {
			t.Errorf("Event Theme Display invalidation missing %q: %s", want, invalidation.String())
		}
	}
	cancelStream()
	if err = streamResponse.Body.Close(); err != nil {
		t.Errorf("close Event Theme Display stream: %v", err)
	}
	assertGETContains(
		t,
		producer,
		server.address,
		"/assets/events/1/theme.css",
		"#112233",
	)
	assertGETContains(
		t,
		producer,
		server.address,
		"/events/revision-2099",
		"/assets/events/1/theme.css",
	)
	assertGETContains(
		t,
		producer,
		server.address,
		"/events/revision-2099/schedule",
		"/assets/events/1/theme.css",
	)
	assertGETContains(t, producer, server.address, "/events/revision-2099/schedule", "Pause effects")
	displayPage := readDisplayHTML(t, displayClient, server.address)
	for _, want := range []string{
		`display-layout-location-signage`,
		`display-transition-none`,
		`--display-signal:#ffdf6e`,
	} {
		if !strings.Contains(displayPage, want) {
			t.Errorf("active Event Theme Display missing %q: %s", want, displayPage)
		}
	}

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	server = startBeamers(t, bin, dataDir)
	assertGETContains(
		t,
		displayClient,
		server.address,
		"/display",
		"--display-signal:#ffdf6e",
	)
	csrf = browserCSRFToken(t, producer, server.address, "/backstage/events/1/theme")

	unknown := eventThemeForm(csrf, "create-draft", "create-arbitrary-event-theme")
	unknown.Set("raw_css", "body { display: none }")
	status, body = postBrowserForm(
		t,
		producer,
		server.address,
		"/backstage/events/1/theme",
		unknown,
	)
	if status != http.StatusBadRequest ||
		!strings.Contains(body, "undocumented Theme control") {
		t.Fatalf("arbitrary Event Theme input = %d %q", status, body)
	}

	inaccessible := eventThemeForm(csrf, "create-draft", "create-inaccessible-event-theme")
	inaccessible.Set("text_color", "#777777")
	inaccessible.Set("background_color", "#ffffff")
	inaccessible.Set("surface_color", "#ffffff")
	_, body = postBrowserForm(
		t,
		producer,
		server.address,
		"/backstage/events/1/theme",
		inaccessible,
	)
	if !strings.Contains(body, "Event Theme Revision #2") ||
		!strings.Contains(body, "4.5:1") {
		t.Fatalf("inaccessible Event Theme preview = %q", body)
	}
	blocked := eventThemeForm(csrf, "activate", "activate-inaccessible-event-theme")
	blocked.Set("revision_id", "2")
	blocked.Set("expected_active_revision_id", "1")
	status, body = postBrowserForm(
		t,
		producer,
		server.address,
		"/backstage/events/1/theme",
		blocked,
	)
	if status != http.StatusConflict ||
		!strings.Contains(body, "Activation is blocked") {
		t.Fatalf("blocked Event Theme activation = %d %q", status, body)
	}

	rollback := eventThemeForm(csrf, "activate", "rollback-event-theme")
	rollback.Set("revision_id", "0")
	rollback.Set("expected_active_revision_id", "1")
	_, _ = postBrowserForm(
		t,
		producer,
		server.address,
		"/backstage/events/1/theme",
		rollback,
	)
	assertGETContains(
		t,
		producer,
		server.address,
		"/assets/events/1/theme.css",
		"#080b15",
	)
	emergencyPreview := requestJSON(
		t.Context(),
		producer,
		server.address,
		"/crew/events/1/emergency-alerts/preview",
		map[string]any{
			"target": map[string]any{"type": "Event"},
			"text":   "Evacuate using marked exits",
		},
	)
	var preview struct {
		ConfirmationFingerprint string `json:"confirmation_fingerprint"`
	}
	if err := json.Unmarshal([]byte(emergencyPreview.body), &preview); err != nil ||
		emergencyPreview.status != http.StatusOK ||
		preview.ConfirmationFingerprint == "" {
		t.Fatalf(
			"preview themed Emergency Alert = %d %q, %v",
			emergencyPreview.status, emergencyPreview.body, err,
		)
	}
	emergency := requestJSON(
		t.Context(),
		producer,
		server.address,
		"/crew/events/1/emergency-alerts",
		map[string]any{
			"target":              map[string]any{"type": "Event"},
			"text":                "Evacuate using marked exits",
			"preview_fingerprint": preview.ConfirmationFingerprint,
			"confirmed":           true, "confirmation_method": "Keyboard",
			"command_id": "activate-themed-emergency",
		},
	)
	if emergency.status != http.StatusOK {
		t.Fatalf("activate themed Emergency Alert = %d %q", emergency.status, emergency.body)
	}
	displayPage = readDisplayHTML(t, displayClient, server.address)
	const emergencyClass = `class="display-view emergency-alert display-override-replace"`
	if !strings.Contains(displayPage, emergencyClass) ||
		strings.Contains(displayPage, emergencyClass+" style=") {
		t.Fatalf("Emergency Alert inherited Event Theme: %s", displayPage)
	}
	server.stop(t)
}

func TestUnenrolledDisplayPresentsEnrollmentCodeAndQR(t *testing.T) {
	_, server := startAuthenticatedAdministrator(t)
	displayClient := authenticatedClient(t)

	response := get(t, displayClient, server.address, "/display")
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Display Enrollment page: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /display = %d %q, want %d", response.StatusCode, body, http.StatusOK)
	}
	page := string(body)
	for _, want := range []string{"Enroll this Display", "Enrollment code:", "data:image/png;base64,"} {
		if !strings.Contains(page, want) {
			t.Errorf("Display Enrollment page does not contain %q; body: %s", want, page)
		}
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Display Enrollment Cache-Control = %q", response.Header.Get("Cache-Control"))
	}
	displayURL, err := url.Parse("http://" + server.address + "/display")
	if err != nil {
		t.Fatalf("parse Display URL: %v", err)
	}
	cookies := displayClient.Jar.Cookies(displayURL)
	if !slices.ContainsFunc(cookies, func(cookie *http.Cookie) bool {
		return cookie.Name == "beamers_display" && cookie.Value != ""
	}) {
		t.Errorf("Display Enrollment cookies = %+v, want Display credential candidate", cookies)
	}
	server.stop(t)
}

func TestAdministratorClaimsDisplayEnrollmentOnceWithoutGrantingCrewAuthority(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	displayClient := authenticatedClient(t)

	enrollment := get(t, displayClient, server.address, "/display")
	body, readErr := io.ReadAll(enrollment.Body)
	closeErr := enrollment.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Display Enrollment page: %v", err)
	}
	code := regexp.MustCompile(`[A-Z2-7]{4}-[A-Z2-7]{4}`).FindString(string(body))
	if code == "" {
		t.Fatalf("Display Enrollment page has no human-readable code: %s", body)
	}
	assertGETResponse(
		t, displayClient, server.address, "/auth/session", http.StatusUnauthorized,
		"authentication required\n",
	)
	claimPage := get(t, administrator, server.address, "/admin/displays/enroll?code="+url.QueryEscape(code))
	claimBody, readErr := io.ReadAll(claimPage.Body)
	closeErr = claimPage.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Display claim page: %v", err)
	}
	if claimPage.StatusCode != http.StatusOK || !strings.Contains(string(claimBody), code) {
		t.Fatalf("Display claim page = %d %q", claimPage.StatusCode, claimBody)
	}
	currentBuild := claimPage.Header.Get("X-Beamers-Build")
	if currentBuild == "" || !strings.Contains(string(claimBody), `name="build_version" value="`+currentBuild+`"`) {
		t.Fatalf("Display claim page does not identify build %q: %s", currentBuild, claimBody)
	}

	claim := url.Values{"code": {code}, "name": {"Stage Left"}, "command_id": {"claim-stage-left"}}
	stale := postForm(t, administrator, server.address, claim)
	staleBody, readErr := io.ReadAll(stale.Body)
	closeErr = stale.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read stale Display claim response: %v", err)
	}
	if stale.StatusCode != http.StatusConflict ||
		!strings.Contains(string(staleBody), `http-equiv="refresh"`) ||
		!strings.Contains(string(staleBody), "Beamers was updated") {
		t.Fatalf("stale Display claim = %d %q, want reload required", stale.StatusCode, staleBody)
	}
	reloaded := get(t, administrator, server.address, "/admin/displays/enroll?code="+url.QueryEscape(code))
	reloadedBody, readErr := io.ReadAll(reloaded.Body)
	closeErr = reloaded.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read reloaded Display claim page: %v", err)
	}
	if reloaded.StatusCode != http.StatusOK ||
		!strings.Contains(string(reloadedBody), `name="name" value="Stage Left"`) {
		t.Fatalf("reloaded Display claim did not preserve entered name: %d %q", reloaded.StatusCode, reloadedBody)
	}
	claim.Set("build_version", currentBuild)
	claimed := postForm(t, administrator, server.address, claim)
	claimedBody, readErr := io.ReadAll(claimed.Body)
	closeErr = claimed.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Display claim response: %v", err)
	}
	if claimed.StatusCode != http.StatusCreated || !strings.Contains(string(claimedBody), "Stage Left") {
		t.Fatalf("claim Display = %d %q", claimed.StatusCode, claimedBody)
	}
	reused := postForm(t, administrator, server.address, url.Values{
		"code": {code}, "name": {"Other Name"}, "command_id": {"reuse-stage-left-code"},
		"build_version": {currentBuild},
	})
	if reused.StatusCode != http.StatusConflict {
		t.Errorf("reused Display Enrollment code status = %d, want %d", reused.StatusCode, http.StatusConflict)
	}
	closeErr = reused.Body.Close()
	if closeErr != nil {
		t.Errorf("close reused Display Enrollment response: %v", closeErr)
	}

	standby := get(t, displayClient, server.address, "/display")
	standbyBody, readErr := io.ReadAll(standby.Body)
	closeErr = standby.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read enrolled Display: %v", err)
	}
	if standby.StatusCode != http.StatusOK || !strings.Contains(string(standbyBody), "Stage Left") ||
		!strings.Contains(string(standbyBody), "Standby") || strings.Contains(string(standbyBody), "Enrollment code:") {
		t.Errorf("enrolled Display = %d %q", standby.StatusCode, standbyBody)
	}
	assertGETResponse(
		t, displayClient, server.address, "/auth/session", http.StatusUnauthorized,
		"authentication required\n",
	)
	server.stop(t)
}

func TestDisplayListRequiresActiveEventCrewEvenWhenEmpty(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	assertJSONRequest(
		t, administrator, server.address, "/admin/events",
		map[string]string{
			"name": "Future Event", "planned_start_date": "2100-09-01",
			"planned_end_date": "2100-09-02", "timezone": "Europe/Berlin",
			"event_locale": "en-GB", "content_language": "en-GB",
			"event_day_boundary": "06:00", "command_id": "create-future-display-event",
		},
		http.StatusCreated,
		"{\"id\":2,\"name\":\"Future Event\",\"planned_start_date\":\"2100-09-01\",\"planned_end_date\":\"2100-09-02\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"en-GB\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts",
		map[string]string{
			"name": "Future Observer", "password": "observer correct horse battery staple",
			"command_id": "create-future-observer",
		},
		http.StatusCreated, "{\"id\":2,\"name\":\"Future Observer\",\"administrator\":false}\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/events/2/grants",
		map[string]any{"account_id": 2, "role": "Observer", "command_id": "grant-future-observer"},
		http.StatusCreated, "{\"event_id\":2,\"account_id\":2,\"role\":\"Observer\"}\n",
	)
	observer := authenticatedClient(t)
	assertJSONRequest(
		t, observer, server.address, "/auth/sign-in",
		map[string]string{
			"name": "Future Observer", "password": "observer correct horse battery staple",
		},
		http.StatusNoContent, "",
	)
	assertGETResponse(
		t, observer, server.address, "/admin/displays", http.StatusForbidden,
		"Active Event crew authority required\n",
	)
	server.stop(t)
}

func TestDisplayAssignmentIsDurableAndNeverInheritedAcrossActiveEvents(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	displayClient := authenticatedClient(t)
	enrollment := get(t, displayClient, server.address, "/display")
	body, readErr := io.ReadAll(enrollment.Body)
	closeErr := enrollment.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Display Enrollment page: %v", err)
	}
	code := regexp.MustCompile(`[A-Z2-7]{4}-[A-Z2-7]{4}`).FindString(string(body))
	claimed := postForm(t, administrator, server.address, url.Values{
		"code": {code}, "name": {"Lobby Display"}, "command_id": {"claim-lobby-display"},
		"build_version": {crewBuild(t, administrator, server.address)},
	})
	closeErr = claimed.Body.Close()
	if closeErr != nil {
		t.Errorf("close Display claim response: %v", closeErr)
	}
	if claimed.StatusCode != http.StatusCreated {
		t.Fatalf("claim Display status = %d", claimed.StatusCode)
	}
	assertGETResponse(
		t, administrator, server.address, "/admin/displays", http.StatusOK,
		"[{\"id\":1,\"name\":\"Lobby Display\",\"active_event_id\":1,\"standby\":true,\"event_name\":\"Revision 2099\",\"delivery_state\":\"offline\",\"applied_active_event_id\":0,\"applied_activation_generation\":0,\"applied_published_revision\":0,\"applied_standby\":true,\"clock_offset_milliseconds\":0,\"clock_uncertainty_milliseconds\":0}]\n",
	)
	operator := provisionOperator(t, administrator, server)
	assertGETResponse(
		t, operator, server.address, "/admin/displays", http.StatusOK,
		"[{\"id\":1,\"name\":\"Lobby Display\",\"active_event_id\":1,\"standby\":true,\"event_name\":\"Revision 2099\",\"delivery_state\":\"offline\",\"applied_active_event_id\":0,\"applied_activation_generation\":0,\"applied_published_revision\":0,\"applied_standby\":true,\"clock_offset_milliseconds\":0,\"clock_uncertainty_milliseconds\":0}]\n",
	)
	activationClient := activationv1connect.NewActivationServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	preflight, err := activationClient.Preflight(t.Context(), connect.NewRequest(&activationv1.PreflightRequest{EventId: 1}))
	if err != nil {
		t.Fatalf("Preflight Event with unassigned Display: %v", err)
	}
	if !slices.ContainsFunc(preflight.Msg.GetWarnings(), func(finding *activationv1.Finding) bool {
		return finding.GetCode() == "unassigned_display" && strings.Contains(finding.GetMessage(), "Lobby Display")
	}) {
		t.Errorf("Activation Preflight warnings = %+v, want unassigned Display", preflight.Msg.GetWarnings())
	}
	assignmentRequest := map[string]any{
		"event_id": 1, "location_id": 1, "view_key": "event-overview",
		"command_id": "assign-lobby-display",
	}
	assertJSONRequest(
		t, administrator, server.address, "/admin/displays/1/assign",
		assignmentRequest,
		http.StatusOK,
		"{\"display_id\":1,\"event_id\":1,\"location_id\":1,\"view_key\":\"event-overview\"}\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/displays/1/assign", assignmentRequest,
		http.StatusOK,
		"{\"display_id\":1,\"event_id\":1,\"location_id\":1,\"view_key\":\"event-overview\"}\n",
	)
	assignedPreflight, err := activationClient.Preflight(
		t.Context(), connect.NewRequest(&activationv1.PreflightRequest{EventId: 1}),
	)
	if err != nil {
		t.Fatalf("Preflight Event with assigned Display: %v", err)
	}
	assertJSONRequest(
		t, administrator, server.address, "/admin/displays/1/assign",
		map[string]any{
			"event_id": 1, "location_id": 1, "view_key": "location-signage",
			"command_id": "reassign-lobby-display",
		},
		http.StatusOK,
		"{\"display_id\":1,\"event_id\":1,\"location_id\":1,\"view_key\":\"location-signage\"}\n",
	)
	if _, activationErr := activationClient.Activate(t.Context(), connect.NewRequest(&activationv1.ActivateRequest{
		EventId: 1, CommandId: "reject-stale-display-routing",
		Confirmation: assignedPreflight.Msg.GetConfirmation(),
	})); connect.CodeOf(activationErr) != connect.CodeAborted {
		t.Errorf("activation after Display reassignment error = %v, want Aborted", activationErr)
	}
	assignmentRequest["command_id"] = "restore-lobby-display-assignment"
	assertJSONRequest(
		t, administrator, server.address, "/admin/displays/1/assign", assignmentRequest,
		http.StatusOK,
		"{\"display_id\":1,\"event_id\":1,\"location_id\":1,\"view_key\":\"event-overview\"}\n",
	)
	rundownClient := rundownv1connect.NewRundownServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	currentRundown, err := rundownClient.GetCrewRundown(
		t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}),
	)
	if err != nil {
		t.Fatalf("Get Rundown before Draft Location rename: %v", err)
	}
	if _, err := rundownClient.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "rename-display-location-draft-only",
		ExpectedDraftRevision: currentRundown.Msg.GetDraftRevision(),
		Locations: []*rundownv1.LocationDraft{{
			Id: 1, Name: "Unpublished Hall",
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"name"}},
		}, {Ref: "unpublished-location", Name: "Unpublished Location"}},
	})); err != nil {
		t.Fatalf("rename assigned Location in Draft: %v", err)
	}
	assertJSONRequest(
		t, administrator, server.address, "/admin/displays/1/assign",
		map[string]any{
			"event_id": 1, "location_id": 2, "view_key": "event-overview",
			"command_id": "reject-unpublished-display-location",
		},
		http.StatusUnprocessableEntity,
		"valid Event, Location, View, and command_id are required\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/displays/1/assign",
		map[string]any{
			"event_id": 1, "location_id": 1, "view_key": "unknown-view",
			"command_id": "reject-unknown-display-view",
		},
		http.StatusUnprocessableEntity,
		"valid Event, Location, View, and command_id are required\n",
	)
	assigned := get(t, displayClient, server.address, "/display")
	assignedBody, readErr := io.ReadAll(assigned.Body)
	closeErr = assigned.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read assigned Display: %v", err)
	}
	for _, want := range []string{"Lobby Display", "Revision 2099", "Main Hall", "event-overview"} {
		if !strings.Contains(string(assignedBody), want) {
			t.Errorf("assigned Display does not contain %q; body: %s", want, assignedBody)
		}
	}
	if strings.Contains(string(assignedBody), "Unpublished Hall") {
		t.Errorf("assigned Display leaked Draft Location name: %s", assignedBody)
	}
	if strings.Contains(string(assignedBody), "<h1>Standby</h1>") {
		t.Errorf("assigned Display remains on Standby: %s", assignedBody)
	}

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	restarted := startBeamers(t, bin, dataDir)
	persisted := get(t, displayClient, restarted.address, "/display")
	persistedBody, readErr := io.ReadAll(persisted.Body)
	closeErr = persisted.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Display after restart: %v", err)
	}
	if !strings.Contains(string(persistedBody), "event-overview") {
		t.Errorf("Display Assignment did not survive restart: %s", persistedBody)
	}

	prepareAndActivateSecondEvent(t, administrator, restarted)
	assertJSONRequest(
		t, administrator, restarted.address, "/admin/displays/1/assign",
		map[string]any{
			"event_id": 2, "location_id": 1, "view_key": "event-overview",
			"command_id": "reject-cross-event-location",
		},
		http.StatusUnprocessableEntity,
		"valid Event, Location, View, and command_id are required\n",
	)
	standby := get(t, displayClient, restarted.address, "/display")
	standbyBody, readErr := io.ReadAll(standby.Body)
	closeErr = standby.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Display after Active Event switch: %v", err)
	}
	if !strings.Contains(string(standbyBody), "<h1>Standby</h1>") ||
		!strings.Contains(string(standbyBody), "Revision 2100") || strings.Contains(string(standbyBody), "Main Hall") {
		t.Errorf("Display inherited prior Event Assignment: %s", standbyBody)
	}
	assertGETResponse(
		t, administrator, restarted.address, "/admin/displays", http.StatusOK,
		"[{\"id\":1,\"name\":\"Lobby Display\",\"active_event_id\":2,\"standby\":true,\"event_name\":\"Revision 2100\",\"delivery_state\":\"offline\",\"applied_active_event_id\":0,\"applied_activation_generation\":0,\"applied_published_revision\":0,\"applied_standby\":true,\"clock_offset_milliseconds\":0,\"clock_uncertainty_milliseconds\":0}]\n",
	)
	restarted.stop(t)
}

func TestDisplaySnapshotContainsOnlyAuthorizedPublicActiveEventState(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	displayClient := enrollAndAssignDisplay(t, administrator, server, "Overview Display", "event-overview")

	result := requestJSON(
		t.Context(),
		displayClient,
		server.address,
		"/beamers.display.v1.DisplayService/GetSnapshot",
		map[string]any{},
	)
	if result.err != nil {
		t.Fatalf("Get Display Snapshot: %v", result.err)
	}
	if result.status != http.StatusOK {
		t.Fatalf("Get Display Snapshot = %d %q, want %d", result.status, result.body, http.StatusOK)
	}
	for _, want := range []string{
		`"protocolVersion":"beamers.display.v1"`,
		`"displayId":"1"`,
		`"activeEventId":"1"`,
		`"activationGeneration":"1"`,
		`"publishedRevision":"1"`,
		`"eventTimezone":"Europe/Berlin"`,
		`"viewKey":"event-overview"`,
		`"title":"Opening Keynote"`,
	} {
		if !strings.Contains(result.body, want) {
			t.Errorf("Display Snapshot missing %s: %s", want, result.body)
		}
	}
	for _, private := range []string{"Private Soundcheck", "radio channel 4", "CrewOnly"} {
		if strings.Contains(result.body, private) {
			t.Errorf("Display Snapshot leaked %q: %s", private, result.body)
		}
	}
	var decoded struct {
		Snapshot struct {
			StreamID       string `json:"streamId"`
			StreamPosition string `json:"streamPosition"`
			SnapshotToken  string `json:"snapshotToken"`
		} `json:"snapshot"`
	}
	if err := json.Unmarshal([]byte(result.body), &decoded); err != nil {
		t.Fatalf("decode Display Snapshot: %v", err)
	}
	if decoded.Snapshot.StreamID == "" {
		t.Errorf("Display Snapshot missing stream ID: %s", result.body)
	}
	if decoded.Snapshot.SnapshotToken == "" {
		t.Errorf("Display Snapshot missing acknowledgment token: %s", result.body)
	}
	if _, err := strconv.ParseUint(decoded.Snapshot.StreamPosition, 10, 64); err != nil {
		t.Errorf("Display Snapshot stream position = %q: %v", decoded.Snapshot.StreamPosition, err)
	}
	server.stop(t)
}

func TestDisplaySSEStreamsRevisionedInvalidationsAfterSnapshot(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	displayClient := enrollAndAssignDisplay(t, administrator, server, "Streaming Display", "event-overview")

	snapshotResult := requestJSON(
		t.Context(),
		displayClient,
		server.address,
		"/beamers.display.v1.DisplayService/GetSnapshot",
		map[string]any{},
	)
	if snapshotResult.err != nil || snapshotResult.status != http.StatusOK {
		t.Fatalf(
			"Get Display Snapshot = %d %q, %v, want %d",
			snapshotResult.status,
			snapshotResult.body,
			snapshotResult.err,
			http.StatusOK,
		)
	}
	var snapshot struct {
		Snapshot struct {
			StreamID       string `json:"streamId"`
			StreamPosition string `json:"streamPosition"`
		} `json:"snapshot"`
	}
	if err := json.Unmarshal([]byte(snapshotResult.body), &snapshot); err != nil {
		t.Fatalf("decode Display Snapshot: %v", err)
	}
	snapshotPosition, err := strconv.ParseUint(snapshot.Snapshot.StreamPosition, 10, 64)
	if err != nil {
		t.Fatalf("parse Display Snapshot stream position: %v", err)
	}
	expectedPosition := snapshotPosition + 1

	streamContext, cancelStream := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelStream()
	streamURL := fmt.Sprintf(
		"http://%s/display/events?stream_id=%s&after=%s",
		server.address,
		url.QueryEscape(snapshot.Snapshot.StreamID),
		url.QueryEscape(snapshot.Snapshot.StreamPosition),
	)
	streamRequest, err := http.NewRequestWithContext(streamContext, http.MethodGet, streamURL, http.NoBody)
	if err != nil {
		t.Fatalf("create Display stream request: %v", err)
	}
	streamResponse, err := displayClient.Do(streamRequest)
	if err != nil {
		t.Fatalf("open Display stream: %v", err)
	}
	defer func() {
		_ = streamResponse.Body.Close()
	}()
	if streamResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(streamResponse.Body)
		t.Fatalf("open Display stream = %d %q, want %d", streamResponse.StatusCode, body, http.StatusOK)
	}
	if got := streamResponse.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Display stream Content-Type = %q, want text/event-stream", got)
	}
	reader := bufio.NewReader(streamResponse.Body)
	heartbeat, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read Display heartbeat: %v", err)
	}
	if heartbeat != ": heartbeat\n" {
		t.Errorf("Display heartbeat = %q, want %q", heartbeat, ": heartbeat\n")
	}
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("finish Display heartbeat: %v", err)
	}

	assertJSONRequest(
		t, administrator, server.address, "/admin/displays/1/assign",
		map[string]any{
			"event_id": 1, "location_id": 1, "view_key": "location-signage",
			"command_id": "reroute-streaming-display",
		},
		http.StatusOK,
		"{\"display_id\":1,\"event_id\":1,\"location_id\":1,\"view_key\":\"location-signage\"}\n",
	)

	var event strings.Builder
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			t.Fatalf("read Display invalidation: %v", readErr)
		}
		if line == "\n" {
			break
		}
		event.WriteString(line)
	}
	for _, want := range []string{
		fmt.Sprintf("id: %d\n", expectedPosition),
		"event: invalidate\n",
		`"protocol_version":"beamers.display.v1"`,
		`"asset_version":"`,
		fmt.Sprintf(`"stream_position":%d`, expectedPosition),
		`"active_event_id":1`,
		`"activation_generation":1`,
		`"published_revision":1`,
	} {
		if !strings.Contains(event.String(), want) {
			t.Errorf("Display invalidation missing %q: %s", want, event.String())
		}
	}

	sessionClient := sessionv1connect.NewSessionControlServiceClient(
		administrator,
		"http://"+server.address,
		connect.WithProtoJSON(),
	)
	if _, err := sessionClient.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "start-streaming-session",
		ExpectedLiveStateRevision: proto.Int64(0),
	})); err != nil {
		t.Fatalf("Start Session while Display subscribed: %v", err)
	}
	var liveEvent strings.Builder
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			t.Fatalf("read live Display invalidation: %v", readErr)
		}
		if line == "\n" {
			break
		}
		liveEvent.WriteString(line)
	}
	for _, want := range []string{
		fmt.Sprintf("id: %d\n", expectedPosition+1),
		fmt.Sprintf(`"stream_position":%d`, expectedPosition+1),
		`"activation_generation":1`,
		`"published_revision":1`,
	} {
		if !strings.Contains(liveEvent.String(), want) {
			t.Errorf("live Display invalidation missing %q: %s", want, liveEvent.String())
		}
	}
	cancelStream()
	if err := streamResponse.Body.Close(); err != nil {
		t.Errorf("close Display stream: %v", err)
	}
	server.stop(t)
}

func TestDisplaySSEUnknownPositionForcesResnapshot(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	displayClient := enrollAndAssignDisplay(t, administrator, server, "Gap Display", "event-overview")
	snapshot := readDisplaySnapshot(t, displayClient, server.address)
	position, err := strconv.ParseUint(snapshot.StreamPosition, 10, 64)
	if err != nil {
		t.Fatalf("parse Display stream position: %v", err)
	}

	streamContext, cancelStream := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelStream()
	streamURL := fmt.Sprintf(
		"http://%s/display/events?stream_id=%s&after=%d",
		server.address,
		url.QueryEscape(snapshot.StreamID),
		position+100,
	)
	request, err := http.NewRequestWithContext(streamContext, http.MethodGet, streamURL, http.NoBody)
	if err != nil {
		t.Fatalf("create unknown-position stream request: %v", err)
	}
	response, err := displayClient.Do(request)
	if err != nil {
		t.Fatalf("open unknown-position Display stream: %v", err)
	}
	reader := bufio.NewReader(response.Body)
	var event strings.Builder
	for strings.Count(event.String(), "\n\n") < 2 {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			t.Fatalf("read unknown-position Display stream: %v", readErr)
		}
		event.WriteString(line)
	}
	for _, want := range []string{
		": heartbeat\n\n",
		"event: invalidate\n",
		fmt.Sprintf(`"stream_position":%d`, position),
	} {
		if !strings.Contains(event.String(), want) {
			t.Errorf("unknown-position Display stream missing %q: %s", want, event.String())
		}
	}
	cancelStream()
	if closeErr := response.Body.Close(); closeErr != nil {
		t.Errorf("close unknown-position Display stream: %v", closeErr)
	}
	server.stop(t)
}

func TestDisplayAcknowledgesAppliedStateIndependentlyOfCommands(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	displayClient := enrollAndAssignDisplay(t, administrator, server, "Acknowledging Display", "event-overview")
	readApplied := func() displaySnapshotState {
		return readDisplaySnapshot(t, displayClient, server.address)
	}
	acknowledge := func(applied displaySnapshotState) jsonResponse {
		return requestDisplayAcknowledgment(
			t,
			displayClient,
			server.address,
			applied,
			displayHealth{},
		)
	}
	previouslyApplied := readApplied()

	assertJSONRequest(
		t, administrator, server.address, "/admin/displays/1/assign",
		map[string]any{
			"event_id": 1, "location_id": 1, "view_key": "location-signage",
			"command_id": "reroute-before-acknowledgment",
		},
		http.StatusOK,
		"{\"display_id\":1,\"event_id\":1,\"location_id\":1,\"view_key\":\"location-signage\"}\n",
	)
	delayed := acknowledge(previouslyApplied)
	if delayed.err != nil || delayed.status != http.StatusOK {
		t.Fatalf(
			"delayed Display acknowledgment = %d %q, %v, want %d",
			delayed.status,
			delayed.body,
			delayed.err,
			http.StatusOK,
		)
	}
	applied := readApplied()
	acknowledged := acknowledge(applied)
	if acknowledged.err != nil {
		t.Fatalf("Acknowledge Display state: %v", acknowledged.err)
	}
	if acknowledged.status != http.StatusOK {
		t.Fatalf("Acknowledge Display state = %d %q, want %d", acknowledged.status, acknowledged.body, http.StatusOK)
	}
	for _, want := range []string{
		`"displayId":"1"`,
		fmt.Sprintf(`"streamId":%q`, applied.StreamID),
		fmt.Sprintf(`"streamPosition":%q`, applied.StreamPosition),
		`"activeEventId":"1"`,
		`"activationGeneration":"1"`,
		`"publishedRevision":"1"`,
		`"appliedAt":`,
	} {
		if !strings.Contains(acknowledged.body, want) {
			t.Errorf("Display acknowledgment missing %s: %s", want, acknowledged.body)
		}
	}
	replayed := acknowledge(applied)
	if replayed.err != nil || replayed.status != http.StatusOK || replayed.body != acknowledged.body {
		t.Errorf(
			"idempotent Display acknowledgment = %d %q, %v, want %d %q",
			replayed.status,
			replayed.body,
			replayed.err,
			http.StatusOK,
			acknowledged.body,
		)
	}
	regressed := acknowledge(previouslyApplied)
	if regressed.err != nil {
		t.Fatalf("send regressed Display acknowledgment: %v", regressed.err)
	}
	if regressed.status != http.StatusBadRequest ||
		!strings.Contains(regressed.body, `"code":"failed_precondition"`) {
		t.Errorf(
			"regressed Display acknowledgment = %d %q, want failed_precondition",
			regressed.status,
			regressed.body,
		)
	}
	impossible := requestJSON(
		t.Context(),
		displayClient,
		server.address,
		"/beamers.display.v1.DisplayService/Acknowledge",
		map[string]any{
			"protocol_version":      applied.ProtocolVersion,
			"stream_id":             applied.StreamID,
			"stream_position":       applied.StreamPosition,
			"active_event_id":       "999",
			"activation_generation": applied.ActivationGeneration,
			"published_revision":    applied.PublishedRevision,
			"snapshot_token":        applied.SnapshotToken,
		},
	)
	if impossible.err != nil {
		t.Fatalf("send impossible Display acknowledgment: %v", impossible.err)
	}
	if impossible.status != http.StatusBadRequest ||
		!strings.Contains(impossible.body, `"code":"invalid_argument"`) {
		t.Errorf(
			"impossible Display acknowledgment = %d %q, want invalid_argument",
			impossible.status,
			impossible.body,
		)
	}
	server.stop(t)
}

func TestCrewSeeDisplayDeliveryHealthAndAppliedGeneration(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	displayClient := enrollAndAssignDisplay(t, administrator, server, "Health Display", "event-overview")

	assertDisplayListContains(
		t,
		administrator,
		server.address,
		`"delivery_state":"offline"`,
	)
	acknowledge := func(offset, uncertainty int64, unstable bool) {
		acknowledgeDisplaySnapshotWithHealth(
			t,
			displayClient,
			server.address,
			readDisplaySnapshot(t, displayClient, server.address),
			displayHealth{
				clockOffsetMilliseconds:      offset,
				clockUncertaintyMilliseconds: uncertainty,
				rendererUnstable:             unstable,
			},
		)
	}
	acknowledge(25, 10, false)
	for _, want := range []string{
		`"delivery_state":"applied"`,
		`"applied_active_event_id":1`,
		`"applied_activation_generation":1`,
		`"applied_published_revision":1`,
		`"applied_standby":false`,
		`"clock_offset_milliseconds":25`,
		`"clock_uncertainty_milliseconds":10`,
		`"applied_at":`,
	} {
		assertDisplayListContains(t, administrator, server.address, want)
	}

	assertJSONRequest(
		t, administrator, server.address, "/admin/displays/1/assign",
		map[string]any{
			"event_id": 1, "location_id": 1, "view_key": "location-signage",
			"command_id": "make-health-display-lag",
		},
		http.StatusOK,
		"{\"display_id\":1,\"event_id\":1,\"location_id\":1,\"view_key\":\"location-signage\"}\n",
	)
	assertDisplayListContains(
		t,
		administrator,
		server.address,
		`"delivery_state":"lagging"`,
	)

	acknowledge(300, 10, false)
	assertDisplayListContains(
		t,
		administrator,
		server.address,
		`"delivery_state":"excessively_skewed"`,
	)
	acknowledge(0, 10, true)
	assertDisplayListContains(
		t,
		administrator,
		server.address,
		`"delivery_state":"unstable"`,
	)
	server.stop(t)
}

func TestObsoleteCrewClientMutationRequiresReload(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	enrollAndAssignDisplay(t, administrator, server, "Build Display", "event-overview")

	list := get(t, administrator, server.address, "/admin/displays")
	currentBuild := list.Header.Get("X-Beamers-Build")
	if closeErr := list.Body.Close(); closeErr != nil {
		t.Errorf("close Display list response: %v", closeErr)
	}
	if currentBuild == "" {
		t.Fatal("crew response does not identify the server build")
	}
	liveness := get(t, authenticatedClient(t), server.address, "/livez")
	if got := liveness.Header.Get("X-Beamers-Build"); got != "" {
		t.Errorf("public liveness disclosed server build %q", got)
	}
	if closeErr := liveness.Body.Close(); closeErr != nil {
		t.Errorf("close liveness response: %v", closeErr)
	}
	body := bytes.NewBufferString(
		`{"event_id":1,"location_id":1,"view_key":"location-signage","command_id":"stale-crew-build"}`,
	)
	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"http://"+server.address+"/admin/displays/1/assign",
		body,
	)
	if err != nil {
		t.Fatalf("create stale crew mutation: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Beamers-Build", "obsolete-build")
	response, err := administrator.Do(request)
	if err != nil {
		t.Fatalf("send stale crew mutation: %v", err)
	}
	responseBody, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read stale crew mutation: %v", err)
	}
	if response.StatusCode != http.StatusConflict ||
		!strings.Contains(string(responseBody), `"code":"reload_required"`) ||
		response.Header.Get("X-Beamers-Build") != currentBuild {
		t.Errorf(
			"stale crew mutation = %d %q build %q, want reload-required build %q",
			response.StatusCode,
			responseBody,
			response.Header.Get("X-Beamers-Build"),
			currentBuild,
		)
	}
	assertDisplayListContains(t, administrator, server.address, `"view_key":"event-overview"`)
	server.stop(t)
}

func TestDisplayAppliedStateRecoversAfterRestartAndActiveEventChange(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	displayClient := enrollAndAssignDisplay(t, administrator, server, "Restart Display", "event-overview")

	acknowledgeDisplaySnapshot(t, displayClient, server.address, readDisplaySnapshot(t, displayClient, server.address))
	assertDisplayListContains(t, administrator, server.address, `"delivery_state":"applied"`)

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	restarted := startBeamers(t, bin, dataDir)
	assertDisplayListContains(t, administrator, restarted.address, `"delivery_state":"lagging"`)
	acknowledgeDisplaySnapshot(
		t,
		displayClient,
		restarted.address,
		readDisplaySnapshot(t, displayClient, restarted.address),
	)
	assertDisplayListContains(t, administrator, restarted.address, `"delivery_state":"applied"`)

	prepareAndActivateSecondEvent(t, administrator, restarted)
	standby := readDisplaySnapshot(t, displayClient, restarted.address)
	if !standby.Standby || standby.ActiveEventID != "2" || standby.ActivationGeneration != "2" {
		t.Fatalf("Display state after Active Event change = %+v, want Event 2 generation 2 Standby", standby)
	}
	if standby.Composition.Layout.Key != "standby" ||
		len(standby.Composition.Layout.Regions) != 2 ||
		standby.Composition.Layout.Regions[0].Name != "branding" ||
		!standby.Composition.Layout.Regions[0].Persistent {
		t.Errorf("Standby composition = %+v, want persistent branding and message Regions", standby.Composition)
	}
	acknowledgeDisplaySnapshot(t, displayClient, restarted.address, standby)
	for _, want := range []string{
		`"delivery_state":"applied"`,
		`"active_event_id":2`,
		`"standby":true`,
		`"applied_active_event_id":2`,
		`"applied_activation_generation":2`,
		`"applied_standby":true`,
	} {
		assertDisplayListContains(t, administrator, restarted.address, want)
	}
	restarted.stop(t)
}

func TestProducerConfiguresAccessibleBuiltInDisplayViews(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	displayClient := enrollAndAssignDisplay(t, administrator, server, "Themed Display", "event-overview")

	configureInput := map[string]any{
		"expected_event_revision": 2,
		"rotation_seconds":        30,
		"reduced_effects":         true,
		"theme": map[string]any{
			"branding":         "FOSDEM",
			"foreground_color": "#ffffff",
			"background_color": "#101828",
			"accent_color":     "#1d4ed8",
			"background":       "variable-media",
			"scrim_color":      "#000000",
			"scrim_opacity":    85,
			"font":             "sans",
			"transition":       "fade",
		},
		"command_id": "configure-display-views",
	}
	configured := requestJSONMethod(
		t.Context(),
		http.MethodPut,
		administrator,
		server.address,
		"/crew/events/1/display-configuration",
		configureInput,
	)
	if configured.err != nil || configured.status != http.StatusOK {
		t.Fatalf(
			"configure Display Views = %d %q, %v, want %d",
			configured.status,
			configured.body,
			configured.err,
			http.StatusOK,
		)
	}
	for _, want := range []string{
		`"event_id":1`,
		`"rotation_seconds":30`,
		`"reduced_effects":true`,
		`"branding":"FOSDEM"`,
		`"background":"variable-media"`,
		`"scrim_opacity":85`,
		`"timer_thresholds":[{"remaining_seconds":300,"emphasis":"attention"},{"remaining_seconds":60,"emphasis":"urgent"}]`,
	} {
		if !strings.Contains(configured.body, want) {
			t.Errorf("configured Display Views missing %s: %s", want, configured.body)
		}
	}
	replayed := requestJSONMethod(
		t.Context(),
		http.MethodPut,
		administrator,
		server.address,
		"/crew/events/1/display-configuration",
		configureInput,
	)
	if replayed.err != nil || replayed.status != http.StatusOK ||
		replayed.body != configured.body {
		t.Errorf(
			"replayed Display configuration = %d %q, %v, want %d %q",
			replayed.status,
			replayed.body,
			replayed.err,
			http.StatusOK,
			configured.body,
		)
	}
	staleInput := maps.Clone(configureInput)
	staleInput["command_id"] = "stale-display-configuration"
	stale := requestJSONMethod(
		t.Context(),
		http.MethodPut,
		administrator,
		server.address,
		"/crew/events/1/display-configuration",
		staleInput,
	)
	if stale.err != nil || stale.status != http.StatusConflict {
		t.Errorf(
			"stale Display configuration = %d %q, %v, want %d",
			stale.status,
			stale.body,
			stale.err,
			http.StatusConflict,
		)
	}
	assertJSONRequest(
		t,
		administrator,
		server.address,
		"/admin/accounts",
		map[string]string{
			"name":       "Olive Observer",
			"password":   "observer correct horse battery staple",
			"command_id": "create-display-observer",
		},
		http.StatusCreated,
		"{\"id\":2,\"name\":\"Olive Observer\",\"administrator\":false}\n",
	)
	assertJSONRequest(
		t,
		administrator,
		server.address,
		"/admin/events/1/grants",
		map[string]any{
			"account_id": 2,
			"role":       "Observer",
			"command_id": "grant-display-observer",
		},
		http.StatusCreated,
		"{\"event_id\":1,\"account_id\":2,\"role\":\"Observer\"}\n",
	)
	observer := authenticatedClient(t)
	assertJSONRequest(
		t,
		observer,
		server.address,
		"/auth/sign-in",
		map[string]string{
			"name":     "Olive Observer",
			"password": "observer correct horse battery staple",
		},
		http.StatusNoContent,
		"",
	)
	observerInput := maps.Clone(configureInput)
	observerInput["expected_event_revision"] = 2
	observerInput["command_id"] = "observer-display-configuration"
	observerResult := requestJSONMethod(
		t.Context(),
		http.MethodPut,
		observer,
		server.address,
		"/crew/events/1/display-configuration",
		observerInput,
	)
	if observerResult.err != nil || observerResult.status != http.StatusForbidden {
		t.Errorf(
			"Observer Display configuration = %d %q, %v, want %d",
			observerResult.status,
			observerResult.body,
			observerResult.err,
			http.StatusForbidden,
		)
	}

	entry := get(t, displayClient, server.address, "/display")
	entryBody, readErr := io.ReadAll(entry.Body)
	closeErr := entry.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read configured Display entry: %v", err)
	}
	for _, want := range []string{
		`class="display-view display-layout-event-overview`,
		`display-transition-none`,
		`--display-foreground:#f1f5ff`,
		`--display-background:#080b15`,
		`--display-accent:#141a2c`,
		`--display-signal:#62ebcb`,
		`data-region="header"`,
		`data-region="schedule"`,
		`data-region="clock"`,
	} {
		if !strings.Contains(string(entryBody), want) {
			t.Errorf("configured Display entry missing %q: %s", want, entryBody)
		}
	}

	snapshot := requestJSON(
		t.Context(),
		displayClient,
		server.address,
		"/beamers.display.v1.DisplayService/GetSnapshot",
		map[string]any{},
	)
	if snapshot.err != nil || snapshot.status != http.StatusOK {
		t.Fatalf("Get Display Snapshot = %d %q, %v", snapshot.status, snapshot.body, snapshot.err)
	}
	for _, want := range []string{
		`"composition":{`,
		`"layout":{"key":"event-overview"`,
		`"rotationSeconds":30`,
		`"theme":{"branding":"FOSDEM"`,
		`"transition":"none"`,
	} {
		if !strings.Contains(snapshot.body, want) {
			t.Errorf("Display Snapshot missing %s: %s", want, snapshot.body)
		}
	}
	var decodedSnapshot struct {
		Snapshot displaySnapshotState `json:"snapshot"`
	}
	if err := json.Unmarshal([]byte(snapshot.body), &decodedSnapshot); err != nil {
		t.Fatalf("decode configured Display Snapshot: %v", err)
	}
	layout := decodedSnapshot.Snapshot.Composition.Layout
	if layout.Key != "event-overview" || layout.RotationSeconds != 30 ||
		len(layout.Regions) != 3 ||
		layout.Regions[0].Name != "header" ||
		layout.Regions[0].Widget != "branding" ||
		!layout.Regions[0].Persistent ||
		layout.Regions[1].Name != "schedule" ||
		layout.Regions[1].Widget != "rotation" ||
		layout.Regions[1].Persistent ||
		layout.Regions[2].Name != "clock" ||
		layout.Regions[2].Widget != "clock" ||
		!layout.Regions[2].Persistent {
		t.Errorf("configured Display Layout = %+v", layout)
	}

	invalid := requestJSONMethod(
		t.Context(),
		http.MethodPut,
		administrator,
		server.address,
		"/crew/events/1/display-configuration",
		map[string]any{
			"expected_event_revision": 2,
			"rotation_seconds":        30,
			"theme": map[string]any{
				"foreground_color": "#777777",
				"background_color": "#ffffff",
				"accent_color":     "#aaaaaa",
				"background":       "solid",
				"scrim_color":      "#000000",
				"scrim_opacity":    85,
				"font":             "sans",
				"transition":       "fade",
			},
			"command_id": "reject-inaccessible-display-theme",
		},
	)
	if invalid.err != nil {
		t.Fatalf("send inaccessible Display Theme: %v", invalid.err)
	}
	if invalid.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalid.body, `"field":"theme.foreground_color"`) {
		t.Errorf(
			"inaccessible Display Theme = %d %q, want foreground contrast validation",
			invalid.status,
			invalid.body,
		)
	}
	server.stop(t)

	restarted := startBeamers(t, server.bin, server.dataDir)
	persisted := requestJSONMethod(
		t.Context(),
		http.MethodGet,
		administrator,
		restarted.address,
		"/crew/events/1/display-configuration",
		nil,
	)
	if persisted.err != nil || persisted.status != http.StatusOK ||
		!strings.Contains(persisted.body, `"branding":"FOSDEM"`) ||
		!strings.Contains(persisted.body, `"reduced_effects":true`) {
		t.Errorf(
			"persisted Display configuration = %d %q, %v",
			persisted.status,
			persisted.body,
			persisted.err,
		)
	}
	restartedSnapshot := requestJSON(
		t.Context(),
		displayClient,
		restarted.address,
		"/beamers.display.v1.DisplayService/GetSnapshot",
		map[string]any{},
	)
	if restartedSnapshot.err != nil || restartedSnapshot.status != http.StatusOK ||
		!strings.Contains(restartedSnapshot.body, `"branding":"FOSDEM"`) {
		t.Errorf(
			"restarted Display Snapshot = %d %q, %v",
			restartedSnapshot.status,
			restartedSnapshot.body,
			restartedSnapshot.err,
		)
	}
	restarted.stop(t)
}

func TestLocationSignageRendersPublicScheduleAndNeutralCrewOnlyOccupancy(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	displayClient := enrollAndAssignDisplay(t, administrator, server, "Signage Display", "location-signage")

	response := get(t, displayClient, server.address, "/display")
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Location Signage: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Location Signage = %d %q, want %d", response.StatusCode, body, http.StatusOK)
	}
	for _, want := range []string{
		"Location Signage", "Opening Keynote", "Forecast Start:", "Location unavailable until",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("Location Signage missing %q: %s", want, body)
		}
	}
	for _, private := range []string{"Private Soundcheck", "radio channel 4"} {
		if strings.Contains(string(body), private) {
			t.Errorf("Location Signage leaked %q: %s", private, body)
		}
	}
	snapshot := requestJSON(
		t.Context(),
		displayClient,
		server.address,
		"/beamers.display.v1.DisplayService/GetSnapshot",
		map[string]any{},
	)
	if snapshot.err != nil || snapshot.status != http.StatusOK {
		t.Fatalf("Get Location Signage Snapshot = %d %q, %v", snapshot.status, snapshot.body, snapshot.err)
	}
	if !strings.Contains(snapshot.body, `"availabilityMessage":"Location unavailable until `) {
		t.Errorf("Location Signage Snapshot missing neutral occupancy: %s", snapshot.body)
	}
	for _, private := range []string{`"id":"2"`, `"title":"Private Soundcheck"`, "radio channel 4"} {
		if strings.Contains(snapshot.body, private) {
			t.Errorf("Location Signage Snapshot leaked %q: %s", private, snapshot.body)
		}
	}
	server.stop(t)
}

func TestEventOverviewRendersCommittedPublicSchedule(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	displayClient := enrollAndAssignDisplay(t, administrator, server, "Overview Display", "event-overview")

	response := get(t, displayClient, server.address, "/display")
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Event Overview: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Event Overview = %d %q, want %d", response.StatusCode, body, http.StatusOK)
	}
	for _, want := range []string{
		"Event Overview", "Opening Keynote", "Forecast Start:", `src="/display/assets/`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("Event Overview missing %q: %s", want, body)
		}
	}
	for _, private := range []string{"Private Soundcheck", "radio channel 4", "Location unavailable"} {
		if strings.Contains(string(body), private) {
			t.Errorf("Event Overview leaked %q: %s", private, body)
		}
	}
	clientScript := get(t, displayClient, server.address, "/display/client.js")
	scriptBody, readErr := io.ReadAll(clientScript.Body)
	closeErr = clientScript.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Display client: %v", err)
	}
	if clientScript.StatusCode != http.StatusOK {
		t.Fatalf("Display client = %d %q, want %d", clientScript.StatusCode, scriptBody, http.StatusOK)
	}
	for _, want := range []string{
		"GetSnapshot",
		"renderSnapshot(snapshot, offset)",
		"Acknowledge",
		"new EventSource",
		"controlledReload",
		"sessionStorage",
		"rendererUnstable",
	} {
		if !strings.Contains(string(scriptBody), want) {
			t.Errorf("Display client missing %q: %s", want, scriptBody)
		}
	}
	server.stop(t)
}

func TestDisplayEntryUsesRecoverableContentAddressedAssets(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	displayClient := enrollAndAssignDisplay(t, administrator, server, "Recovering Display", "event-overview")

	response := get(t, displayClient, server.address, "/display")
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Display entry document: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Display entry document = %d %q, want %d", response.StatusCode, body, http.StatusOK)
	}
	page := string(body)
	for _, want := range []string{
		`role="status"`,
		`aria-live="polite"`,
		`data-connection="connecting"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("Display entry document missing %q: %s", want, page)
		}
	}
	assetMatch := regexp.MustCompile(`src="(/display/assets/([0-9a-f]{64})/client\.js)"`).FindStringSubmatch(page)
	if len(assetMatch) != 3 {
		t.Fatalf("Display entry document has no content-addressed client asset: %s", page)
	}

	asset := get(t, displayClient, server.address, assetMatch[1])
	assetBody, readErr := io.ReadAll(asset.Body)
	closeErr = asset.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read content-addressed Display client: %v", err)
	}
	if asset.StatusCode != http.StatusOK || len(assetBody) == 0 {
		t.Errorf("content-addressed Display client = %d %q, want non-empty %d", asset.StatusCode, assetBody, http.StatusOK)
	}
	if got := asset.Header.Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("content-addressed Display client Cache-Control = %q", got)
	}
	stale := get(t, displayClient, server.address, "/display/assets/"+strings.Repeat("0", 64)+"/client.js")
	if stale.StatusCode != http.StatusNotFound {
		t.Errorf("stale Display client asset = %d, want %d", stale.StatusCode, http.StatusNotFound)
	}
	if closeErr := stale.Body.Close(); closeErr != nil {
		t.Errorf("close stale Display client response: %v", closeErr)
	}
	server.stop(t)
}

func TestDisplaySnapshotIdentifiesItsCompatibleClientAsset(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	displayClient := enrollAndAssignDisplay(t, administrator, server, "Compatible Display", "event-overview")

	entry := get(t, displayClient, server.address, "/display")
	entryBody, readErr := io.ReadAll(entry.Body)
	closeErr := entry.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Display entry document: %v", err)
	}
	assetMatch := regexp.MustCompile(`/display/assets/([0-9a-f]{64})/client\.js`).FindStringSubmatch(string(entryBody))
	if len(assetMatch) != 2 {
		t.Fatalf("Display entry document has no asset version: %s", entryBody)
	}

	snapshot := requestJSON(
		t.Context(),
		displayClient,
		server.address,
		"/beamers.display.v1.DisplayService/GetSnapshot",
		map[string]any{},
	)
	if snapshot.err != nil || snapshot.status != http.StatusOK {
		t.Fatalf("Get Display Snapshot = %d %q, %v", snapshot.status, snapshot.body, snapshot.err)
	}
	for _, want := range []string{
		`"protocolVersion":"beamers.display.v1"`,
		fmt.Sprintf(`"assetVersion":%q`, assetMatch[1]),
	} {
		if !strings.Contains(snapshot.body, want) {
			t.Errorf("Display Snapshot missing %s: %s", want, snapshot.body)
		}
	}
	server.stop(t)
}

func TestAdministratorCreatesEventWithCoreConfiguration(t *testing.T) {
	client, server := startAuthenticatedAdministrator(t)

	result := requestJSON(
		t.Context(),
		client,
		server.address,
		"/admin/events",
		map[string]string{
			"name":               "Revision 2026",
			"planned_start_date": "2026-08-21",
			"planned_end_date":   "2026-08-23",
			"timezone":           "Europe/Berlin",
			"event_locale":       "de-DE",
			"content_language":   "en-GB",
			"event_day_boundary": "06:00",
			"command_id":         "create-event-1",
		},
	)
	if result.err != nil {
		t.Fatalf("create Event: %v", result.err)
	}
	if result.status != http.StatusCreated {
		t.Fatalf("create Event status = %d, want %d; body: %s", result.status, http.StatusCreated, result.body)
	}
	var created struct {
		ID               int    `json:"id"`
		Name             string `json:"name"`
		PlannedStartDate string `json:"planned_start_date"`
		PlannedEndDate   string `json:"planned_end_date"`
		Timezone         string `json:"timezone"`
		EventLocale      string `json:"event_locale"`
		ContentLanguage  string `json:"content_language"`
		EventDayBoundary string `json:"event_day_boundary"`
		Revision         int    `json:"revision"`
	}
	if err := json.Unmarshal([]byte(result.body), &created); err != nil {
		t.Fatalf("decode created Event: %v", err)
	}
	want := struct {
		ID               int    `json:"id"`
		Name             string `json:"name"`
		PlannedStartDate string `json:"planned_start_date"`
		PlannedEndDate   string `json:"planned_end_date"`
		Timezone         string `json:"timezone"`
		EventLocale      string `json:"event_locale"`
		ContentLanguage  string `json:"content_language"`
		EventDayBoundary string `json:"event_day_boundary"`
		Revision         int    `json:"revision"`
	}{
		ID: 1, Name: "Revision 2026",
		PlannedStartDate: "2026-08-21", PlannedEndDate: "2026-08-23",
		Timezone: "Europe/Berlin", EventLocale: "de-DE", ContentLanguage: "en-GB",
		EventDayBoundary: "06:00", Revision: 1,
	}
	if created != want {
		t.Errorf("created Event = %+v, want %+v", created, want)
	}
	server.stop(t)
}

func enrollAndAssignDisplay(
	t *testing.T,
	administrator *http.Client,
	server *runningServer,
	name string,
	viewKey string,
) *http.Client {
	t.Helper()
	displayClient := authenticatedClient(t)
	enrollment := get(t, displayClient, server.address, "/display")
	body, readErr := io.ReadAll(enrollment.Body)
	closeErr := enrollment.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Display Enrollment page: %v", err)
	}
	code := regexp.MustCompile(`[A-Z2-7]{4}-[A-Z2-7]{4}`).FindString(string(body))
	claimed := postForm(t, administrator, server.address, url.Values{
		"code": {code}, "name": {name}, "command_id": {"claim-snapshot-display"},
		"build_version": {crewBuild(t, administrator, server.address)},
	})
	if closeErr := claimed.Body.Close(); closeErr != nil {
		t.Errorf("close Display claim response: %v", closeErr)
	}
	if claimed.StatusCode != http.StatusCreated {
		t.Fatalf("claim Display = %d, want %d", claimed.StatusCode, http.StatusCreated)
	}
	assertJSONRequest(
		t, administrator, server.address, "/admin/displays/1/assign",
		map[string]any{
			"event_id": 1, "location_id": 1, "view_key": viewKey,
			"command_id": "assign-snapshot-display",
		},
		http.StatusOK,
		fmt.Sprintf(
			"{\"display_id\":1,\"event_id\":1,\"location_id\":1,\"view_key\":%q}\n",
			viewKey,
		),
	)
	return displayClient
}

func TestStageMessagesAndTechnicalDifficultiesOverrideCurrentDisplayView(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	displayClient := enrollAndAssignDisplay(
		t, administrator, server, "Stage Display", "stage-timer",
	)
	sessionClient := sessionv1connect.NewSessionControlServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	started, err := sessionClient.StartSession(t.Context(), connect.NewRequest(
		&sessionv1.StartSessionRequest{
			EventId: 1, SessionId: sessionID, CommandId: "start-override-session",
			ExpectedLiveStateRevision: proto.Int64(0),
		},
	))
	if err != nil {
		t.Fatalf("start Session beneath Overrides: %v", err)
	}
	configured := requestJSONMethod(
		t.Context(), http.MethodPatch, administrator, server.address,
		"/crew/events/1/stage-message-configuration",
		map[string]any{
			"default_duration_seconds": 10, "expected_revision": 0,
			"command_id": "configure-stage-message-presets",
			"presets": []map[string]any{{
				"key": "wrap", "text": "Please wrap up",
				"target_group_key": "crew", "duration_seconds": 15,
				"emphasis": "Attention",
			}},
		},
	)
	if configured.status != http.StatusOK {
		t.Fatalf("configure Stage Message preset = %d: %s", configured.status, configured.body)
	}
	preview := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/stage-messages/preview",
		map[string]any{"preset_key": "wrap", "until_cleared": true},
	)
	if preview.status != http.StatusOK ||
		!strings.Contains(preview.body, `"text":"Please wrap up"`) ||
		!strings.Contains(preview.body, `"displays":[{"id":1,"name":"Stage Display","view_key":"stage-timer"}]`) {
		t.Fatalf("preview Stage Message targets = %d: %s", preview.status, preview.body)
	}
	operator := provisionOperator(t, administrator, server)
	denied := requestJSON(
		t.Context(), operator, server.address, "/crew/events/1/stage-messages",
		map[string]any{
			"text": "unauthorized", "target_group_key": "crew",
			"until_cleared": true, "command_id": "deny-unscoped-stage-message",
		},
	)
	if denied.status != http.StatusForbidden {
		t.Fatalf("unscoped Operator Stage Message = %d: %s", denied.status, denied.body)
	}
	preset := requestJSON(
		t.Context(), administrator, server.address, "/crew/events/1/stage-messages",
		map[string]any{
			"preset_key": "wrap", "until_cleared": true,
			"command_id": "send-preset-stage-message",
		},
	)
	var presetOverride struct {
		ID       int `json:"id"`
		Revision int `json:"revision"`
	}
	if err = json.Unmarshal([]byte(preset.body), &presetOverride); err != nil ||
		preset.status != http.StatusOK || presetOverride.ID <= 0 {
		t.Fatalf("send preset Stage Message = %d: %s (%v)", preset.status, preset.body, err)
	}
	page := readDisplayHTML(t, displayClient, server.address)
	if !strings.Contains(page, `data-stage-message`) ||
		!strings.Contains(page, `data-emphasis="Attention"`) ||
		!strings.Contains(page, "Attention:") ||
		!strings.Contains(page, "Please wrap up") {
		t.Fatalf("preset Stage Message missing accessible emphasis: %s", page)
	}
	replacement := requestJSON(
		t.Context(), administrator, server.address, "/crew/events/1/stage-messages",
		map[string]any{
			"text": "Stop now", "target_group_key": "crew",
			"emphasis": "Urgent", "until_cleared": true,
			"command_id": "replace-stage-message",
		},
	)
	var replacementOverride struct {
		ID       int `json:"id"`
		Revision int `json:"revision"`
	}
	if err = json.Unmarshal([]byte(replacement.body), &replacementOverride); err != nil ||
		replacement.status != http.StatusOK || replacementOverride.ID <= presetOverride.ID {
		t.Fatalf("replace Stage Message = %d: %s (%v)", replacement.status, replacement.body, err)
	}
	page = readDisplayHTML(t, displayClient, server.address)
	if !strings.Contains(page, "Urgent:") || !strings.Contains(page, "Stop now") ||
		strings.Contains(page, "Please wrap up") {
		t.Fatalf("replacement Stage Message queued old content: %s", page)
	}

	reassignedPublic := requestJSON(
		t.Context(), administrator, server.address, "/admin/displays/1/assign",
		map[string]any{
			"event_id": 1, "location_id": 1, "view_key": "event-overview",
			"display_group_keys": []string{"crew"},
			"command_id":         "assign-override-display-public",
		},
	)
	if reassignedPublic.status != http.StatusOK {
		t.Fatalf("assign public Display to crew group = %d: %s", reassignedPublic.status, reassignedPublic.body)
	}
	page = readDisplayHTML(t, displayClient, server.address)
	if strings.Contains(page, "Stop now") || strings.Contains(page, `data-stage-message`) {
		t.Fatalf("Stage Message leaked to public Display: %s", page)
	}
	activeOverrides := requestJSONMethod(
		t.Context(), http.MethodGet, administrator, server.address,
		"/crew/events/1/overrides", nil,
	)
	if activeOverrides.status != http.StatusOK ||
		!strings.Contains(activeOverrides.body, fmt.Sprintf(`"id":%d`, replacementOverride.ID)) ||
		!strings.Contains(activeOverrides.body, `"displays":[]`) {
		t.Fatalf("active Override targets after membership change = %d: %s", activeOverrides.status, activeOverrides.body)
	}
	reassignedCrew := requestJSON(
		t.Context(), administrator, server.address, "/admin/displays/1/assign",
		map[string]any{
			"event_id": 1, "location_id": 1, "view_key": "stage-timer",
			"display_group_keys": []string{"crew"},
			"command_id":         "restore-override-display-crew",
		},
	)
	if reassignedCrew.status != http.StatusOK {
		t.Fatalf("restore crew Display Assignment = %d: %s", reassignedCrew.status, reassignedCrew.body)
	}
	if page = readDisplayHTML(t, displayClient, server.address); !strings.Contains(page, "Stop now") {
		t.Fatalf("live Display Group membership did not restore current Stage Message: %s", page)
	}
	activeOverrides = requestJSONMethod(
		t.Context(), http.MethodGet, administrator, server.address,
		"/crew/events/1/overrides", nil,
	)
	if activeOverrides.status != http.StatusOK ||
		!strings.Contains(activeOverrides.body, `"displays":[{"id":1,"name":"Stage Display","view_key":"stage-timer"}]`) {
		t.Fatalf("active Override targets after membership restore = %d: %s", activeOverrides.status, activeOverrides.body)
	}

	technical := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/technical-difficulties",
		map[string]any{
			"target_group_key": "crew", "until_cleared": true,
			"command_id": "activate-technical-difficulties",
		},
	)
	var technicalOverride struct {
		ID       int `json:"id"`
		Revision int `json:"revision"`
	}
	if err = json.Unmarshal([]byte(technical.body), &technicalOverride); err != nil ||
		technical.status != http.StatusOK || technicalOverride.ID <= 0 {
		t.Fatalf("activate Technical Difficulties = %d: %s (%v)", technical.status, technical.body, err)
	}
	page = readDisplayHTML(t, displayClient, server.address)
	if !strings.Contains(page, `data-override-kind="TechnicalDifficulties"`) ||
		!strings.Contains(page, "Technical Difficulties") ||
		!strings.Contains(page, "Stop now") ||
		strings.Contains(page, `<time data-stage-timer-clock`) {
		t.Fatalf("Technical Difficulties Replace with Stage Message Overlay = %s", page)
	}
	appliedOverrides := readDisplaySnapshot(t, displayClient, server.address)
	if appliedOverrides.StageMessage.ID != strconv.Itoa(replacementOverride.ID) ||
		appliedOverrides.TechnicalDifficulties.ID != strconv.Itoa(technicalOverride.ID) {
		t.Fatalf("Display Override Snapshot = %+v", appliedOverrides)
	}
	acknowledgeDisplaySnapshot(t, displayClient, server.address, appliedOverrides)
	assertDisplayListContains(
		t, administrator, server.address,
		fmt.Sprintf(`"applied_stage_message_id":%d`, replacementOverride.ID),
	)
	assertDisplayListContains(
		t, administrator, server.address,
		fmt.Sprintf(`"applied_technical_difficulties_id":%d`, technicalOverride.ID),
	)

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	server = startBeamers(t, bin, dataDir)
	sessionClient = sessionv1connect.NewSessionControlServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	page = readDisplayHTML(t, displayClient, server.address)
	if !strings.Contains(page, "Technical Difficulties") || !strings.Contains(page, "Stop now") {
		t.Fatalf("Overrides did not survive restart: %s", page)
	}
	clearedStage := requestJSON(
		t.Context(), administrator, server.address,
		fmt.Sprintf("/crew/events/1/overrides/%d/clear", replacementOverride.ID),
		map[string]any{
			"expected_revision": replacementOverride.Revision,
			"command_id":        "clear-stage-message",
		},
	)
	if clearedStage.status != http.StatusOK {
		t.Fatalf("clear Stage Message = %d: %s", clearedStage.status, clearedStage.body)
	}
	page = readDisplayHTML(t, displayClient, server.address)
	if strings.Contains(page, "Stop now") || strings.Contains(page, "Please wrap up") ||
		!strings.Contains(page, "Technical Difficulties") {
		t.Fatalf("clearing Stage Message revealed queued content or cleared Replace: %s", page)
	}
	clearedTechnical := requestJSON(
		t.Context(), administrator, server.address,
		fmt.Sprintf("/crew/events/1/overrides/%d/clear", technicalOverride.ID),
		map[string]any{
			"expected_revision": technicalOverride.Revision,
			"command_id":        "clear-technical-difficulties",
		},
	)
	if clearedTechnical.status != http.StatusOK {
		t.Fatalf("clear Technical Difficulties = %d: %s", clearedTechnical.status, clearedTechnical.body)
	}
	page = readDisplayHTML(t, displayClient, server.address)
	if strings.Contains(page, "Technical Difficulties") ||
		!strings.Contains(page, `<time data-stage-timer-clock`) {
		t.Fatalf("clearing Replace did not restore current Stage Timer: %s", page)
	}
	expiring := requestJSON(
		t.Context(), administrator, server.address, "/crew/events/1/stage-messages",
		map[string]any{
			"text": "One second", "target_group_key": "crew",
			"duration_seconds": 1, "emphasis": "Normal",
			"command_id": "send-expiring-stage-message",
		},
	)
	if expiring.status != http.StatusOK {
		t.Fatalf("send expiring Stage Message = %d: %s", expiring.status, expiring.body)
	}
	var expiringOverride struct {
		ID int `json:"id"`
	}
	if err = json.Unmarshal([]byte(expiring.body), &expiringOverride); err != nil ||
		expiringOverride.ID <= 0 {
		t.Fatalf("decode expiring Stage Message = %s: %v", expiring.body, err)
	}
	if page = readDisplayHTML(t, displayClient, server.address); !strings.Contains(page, "One second") {
		t.Fatalf("expiring Stage Message not initially visible: %s", page)
	}
	appliedExpiring := readDisplaySnapshot(t, displayClient, server.address)
	acknowledgeDisplaySnapshot(t, displayClient, server.address, appliedExpiring)
	if fixtureErr := storetest.SetDisplayOverrideExpiry(
		t.Context(), filepath.Join(server.dataDir, "beamers.db"), expiringOverride.ID, time.Unix(1, 0),
	); fixtureErr != nil {
		t.Fatalf("expire Stage Message fixture: %v", fixtureErr)
	}
	appliedExpired := readDisplaySnapshot(t, displayClient, server.address)
	if appliedExpired.StreamPosition != appliedExpiring.StreamPosition ||
		appliedExpired.StageMessage.ID != "" {
		t.Fatalf("expired same-cursor Display Snapshot = %+v", appliedExpired)
	}
	acknowledgeDisplaySnapshot(t, displayClient, server.address, appliedExpired)
	stale := requestDisplayAcknowledgment(
		t, displayClient, server.address, appliedExpiring, displayHealth{},
	)
	if stale.status != http.StatusBadRequest ||
		!strings.Contains(stale.body, `"code":"failed_precondition"`) {
		t.Fatalf("stale same-cursor Override acknowledgment = %d: %s", stale.status, stale.body)
	}
	if page = readDisplayHTML(t, displayClient, server.address); strings.Contains(page, "One second") {
		t.Fatalf("expired Stage Message remained visible: %s", page)
	}
	ended, err := sessionClient.EndSession(t.Context(), connect.NewRequest(
		&sessionv1.EndSessionRequest{
			EventId: 1, SessionId: sessionID, CommandId: "end-session-after-overrides",
			ExpectedLiveStateRevision: new(started.Msg.GetState().GetLiveStateRevision()),
		},
	))
	if err != nil || ended.Msg.GetState().GetLiveStateRevision() != 2 {
		t.Fatalf("Overrides mutated Session live state = %+v, %v", ended, err)
	}
	server.stop(t)
}

func TestUrgentNoticesAndEmergencyAlertsTargetCurrentDisplays(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	displayClient := enrollAndAssignDisplay(
		t, administrator, server, "Priority Display", "stage-timer",
	)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	assign := func(viewKey string, groups []string, commandID string) {
		t.Helper()
		result := requestJSON(
			t.Context(), administrator, server.address, "/admin/displays/1/assign",
			map[string]any{
				"event_id": 1, "location_id": 1, "view_key": viewKey,
				"display_group_keys": groups, "command_id": commandID,
			},
		)
		if result.status != http.StatusOK {
			t.Fatalf("assign Priority Display = %d: %s", result.status, result.body)
		}
	}
	previewTarget := func(target map[string]any, wantDisplay bool) {
		t.Helper()
		result := requestJSON(
			t.Context(), administrator, server.address,
			"/crew/events/1/urgent-notices/preview",
			map[string]any{
				"target": target, "text": "Scope preview",
				"presentation": "Overlay", "until_cleared": true,
			},
		)
		hasDisplay := strings.Contains(result.body, `"id":1,"name":"Priority Display"`)
		if result.status != http.StatusOK || hasDisplay != wantDisplay {
			t.Fatalf("preview target %v = %d: %s", target, result.status, result.body)
		}
	}

	previewTarget(map[string]any{"type": "Event"}, true)
	previewTarget(map[string]any{"type": "Crew"}, true)
	previewTarget(map[string]any{"type": "Public"}, false)
	previewTarget(map[string]any{"type": "Location", "id": 1}, true)
	previewTarget(map[string]any{"type": "Lane", "id": 1}, true)
	previewTarget(map[string]any{"type": "Display", "id": 1}, true)
	assign("stage-timer", []string{"ops"}, "assign-priority-custom-group")
	previewTarget(map[string]any{"type": "DisplayGroup", "key": "ops"}, true)
	assign("event-overview", nil, "assign-priority-public")
	previewTarget(map[string]any{"type": "Public"}, true)
	assign("competition-output", nil, "assign-priority-program")
	previewTarget(map[string]any{"type": "ProgramChannel", "id": competitionID}, true)
	assign("stage-timer", []string{"ops"}, "restore-priority-stage")

	technical := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/technical-difficulties",
		map[string]any{
			"target_group_key": "crew", "until_cleared": true,
			"command_id": "priority-technical",
		},
	)
	if technical.status != http.StatusOK {
		t.Fatalf("activate underlying Technical Difficulties = %d: %s", technical.status, technical.body)
	}
	stage := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/stage-messages",
		map[string]any{
			"text": "Stage underneath", "target_group_key": "crew",
			"until_cleared": true, "command_id": "priority-stage-message",
		},
	)
	if stage.status != http.StatusOK {
		t.Fatalf("activate underlying Stage Message = %d: %s", stage.status, stage.body)
	}
	overlay := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/urgent-notices",
		map[string]any{
			"target": map[string]any{"type": "DisplayGroup", "key": "ops"},
			"text":   "Urgent overlay", "presentation": "Overlay",
			"until_cleared": true, "command_id": "activate-urgent-overlay",
		},
	)
	var overlayOverride struct {
		ID       int `json:"id"`
		Revision int `json:"revision"`
	}
	if err := json.Unmarshal([]byte(overlay.body), &overlayOverride); err != nil ||
		overlay.status != http.StatusOK || overlayOverride.ID <= 0 {
		t.Fatalf("activate Urgent Overlay = %d: %s (%v)", overlay.status, overlay.body, err)
	}
	page := readDisplayHTML(t, displayClient, server.address)
	for _, want := range []string{"Technical Difficulties", "Stage underneath", "Urgent overlay"} {
		if !strings.Contains(page, want) {
			t.Fatalf("Urgent Overlay did not compose above lower content %q: %s", want, page)
		}
	}
	assign("stage-timer", nil, "leave-priority-custom-group")
	page = readDisplayHTML(t, displayClient, server.address)
	if strings.Contains(page, "Urgent overlay") {
		t.Fatalf("logical Urgent target did not re-resolve after leave: %s", page)
	}
	active := requestJSONMethod(
		t.Context(), http.MethodGet, administrator, server.address,
		"/crew/events/1/overrides", nil,
	)
	if active.status != http.StatusOK ||
		!strings.Contains(active.body, fmt.Sprintf(`"id":%d`, overlayOverride.ID)) ||
		!strings.Contains(active.body, `"displays":[]`) {
		t.Fatalf("active Urgent membership after leave = %d: %s", active.status, active.body)
	}

	individual := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/urgent-notices",
		map[string]any{
			"target": map[string]any{"type": "Display", "id": 1},
			"text":   "Fixed Display notice", "presentation": "Replace",
			"until_cleared": true, "command_id": "activate-individual-urgent",
		},
	)
	var individualOverride struct {
		ID       int `json:"id"`
		Revision int `json:"revision"`
	}
	if err := json.Unmarshal([]byte(individual.body), &individualOverride); err != nil ||
		individual.status != http.StatusOK || individualOverride.ID <= overlayOverride.ID {
		t.Fatalf("activate individual Urgent Replace = %d: %s (%v)", individual.status, individual.body, err)
	}
	rejectedOverrides := []struct {
		method string
		path   string
		body   map[string]any
		action string
	}{
		{
			method: http.MethodPatch,
			path:   "/crew/events/1/stage-message-configuration",
			body: map[string]any{
				"expected_revision": -1,
				"command_id":        "reject-invalid-stage-message-configuration",
			},
			action: "ConfigureStageMessages",
		},
		{
			path: "/crew/events/1/stage-messages",
			body: map[string]any{
				"text": "invalid", "target_group_key": "crew",
				"duration_seconds": -1, "command_id": "reject-invalid-stage-message",
			},
			action: "SendStageMessage",
		},
		{
			path: "/crew/events/1/technical-difficulties",
			body: map[string]any{
				"text": "invalid", "duration_seconds": 0,
				"command_id": "reject-technical",
			},
			action: "ActivateTechnicalDifficulties",
		},
		{
			path: "/crew/events/1/urgent-notices",
			body: map[string]any{
				"target": map[string]any{"type": "Event"}, "text": "invalid",
				"duration_seconds": -1, "command_id": "reject-invalid-urgent",
			},
			action: "ActivateUrgentNotice",
		},
		{
			path: fmt.Sprintf(
				"/crew/events/1/overrides/%d/clear",
				individualOverride.ID,
			),
			body: map[string]any{
				"expected_revision": 0, "command_id": "reject-invalid-clear",
			},
			action: "ClearDisplayOverride",
		},
	}
	for _, rejected := range rejectedOverrides {
		for range 2 {
			method := rejected.method
			if method == "" {
				method = http.MethodPost
			}
			response := requestJSONMethod(
				t.Context(), method, administrator, server.address,
				rejected.path, rejected.body,
			)
			if response.status != http.StatusUnprocessableEntity {
				t.Fatalf(
					"%s invalid input = %d: %s",
					rejected.action, response.status, response.body,
				)
			}
		}
	}
	assign("event-overview", []string{"other"}, "move-fixed-priority-display")
	page = readDisplayHTML(t, displayClient, server.address)
	if !strings.Contains(page, "Fixed Display notice") ||
		strings.Contains(page, "Technical Difficulties") ||
		strings.Contains(page, "Stage underneath") {
		t.Fatalf("individual Urgent Replace did not remain fixed or suppress lower content: %s", page)
	}

	emergencyPreview := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/emergency-alerts/preview",
		map[string]any{
			"target": map[string]any{"type": "Event"},
			"text":   "Evacuate using marked exits",
		},
	)
	var preview struct {
		ConfirmationFingerprint string `json:"confirmation_fingerprint"`
	}
	if err := json.Unmarshal([]byte(emergencyPreview.body), &preview); err != nil ||
		emergencyPreview.status != http.StatusOK || preview.ConfirmationFingerprint == "" {
		t.Fatalf("preview Emergency Alert = %d: %s (%v)", emergencyPreview.status, emergencyPreview.body, err)
	}
	confirmationPage := get(
		t, administrator, server.address,
		"/crew/events/1/emergency-alerts/confirmation?target_type=Event&text="+
			url.QueryEscape("Evacuate using marked exits"),
	)
	confirmationBody, readErr := io.ReadAll(confirmationPage.Body)
	closeErr := confirmationPage.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil ||
		confirmationPage.StatusCode != http.StatusOK ||
		!bytes.Contains(confirmationBody, []byte(`value="Keyboard"`)) ||
		!bytes.Contains(confirmationBody, []byte("Priority Display")) {
		t.Fatalf(
			"keyboard Emergency confirmation page = %d: %s (%v)",
			confirmationPage.StatusCode, confirmationBody, err,
		)
	}
	unconfirmedEmergency := map[string]any{
		"target": map[string]any{"type": "Event"},
		"text":   "Evacuate using marked exits", "preview_fingerprint": preview.ConfirmationFingerprint,
		"command_id": "reject-unconfirmed-emergency",
	}
	for range 2 {
		unconfirmed := requestJSON(
			t.Context(), administrator, server.address,
			"/crew/events/1/emergency-alerts", unconfirmedEmergency,
		)
		if unconfirmed.status != http.StatusUnprocessableEntity {
			t.Fatalf("unconfirmed Emergency Alert = %d: %s", unconfirmed.status, unconfirmed.body)
		}
	}
	for range 2 {
		missingFingerprint := requestJSON(
			t.Context(), administrator, server.address,
			"/crew/events/1/emergency-alerts",
			map[string]any{
				"target": map[string]any{"type": "Event"},
				"text":   "Evacuate using marked exits", "confirmed": true,
				"confirmation_method": "Keyboard",
				"command_id":          "reject-missing-emergency-fingerprint",
			},
		)
		if missingFingerprint.status != http.StatusUnprocessableEntity {
			t.Fatalf(
				"missing Emergency fingerprint = %d: %s",
				missingFingerprint.status, missingFingerprint.body,
			)
		}
	}
	rejectedEntries, _ := readAuditHistory(t, administrator, server.address)
	rejectedCounts := map[string]int{
		"ConfigureStageMessages":        1,
		"SendStageMessage":              1,
		"ActivateTechnicalDifficulties": 1,
		"ActivateUrgentNotice":          1,
		"ClearDisplayOverride":          1,
		"ActivateEmergencyAlert":        2,
	}
	for _, entry := range rejectedEntries {
		if entry.Outcome != "Rejected" {
			continue
		}
		if _, expected := rejectedCounts[entry.Action]; !expected {
			continue
		}
		if entry.Reason != "override_invalid_input" {
			t.Errorf("%s rejection reason = %q", entry.Action, entry.Reason)
		}
		rejectedCounts[entry.Action]--
	}
	for action, remaining := range rejectedCounts {
		if remaining != 0 {
			t.Errorf("%s rejected Audit Entry count remaining = %d", action, remaining)
		}
	}
	emergency := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/emergency-alerts",
		map[string]any{
			"target": map[string]any{"type": "Event"},
			"text":   "Evacuate using marked exits", "preview_fingerprint": preview.ConfirmationFingerprint,
			"confirmed": true, "confirmation_method": "Keyboard",
			"command_id": "activate-confirmed-emergency",
		},
	)
	var emergencyOverride struct {
		ID       int `json:"id"`
		Revision int `json:"revision"`
	}
	if err := json.Unmarshal([]byte(emergency.body), &emergencyOverride); err != nil ||
		emergency.status != http.StatusOK || emergencyOverride.ID <= individualOverride.ID {
		t.Fatalf("activate Emergency Alert = %d: %s (%v)", emergency.status, emergency.body, err)
	}
	page = readDisplayHTML(t, displayClient, server.address)
	if !strings.Contains(page, "Emergency Alert") ||
		!strings.Contains(page, "Evacuate using marked exits") ||
		strings.Contains(page, "Fixed Display notice") ||
		strings.Contains(page, "Technical Difficulties") {
		t.Fatalf("Emergency Alert did not suppress every lower priority: %s", page)
	}
	applied := readDisplaySnapshot(t, displayClient, server.address)
	if applied.EmergencyAlert.ID != strconv.Itoa(emergencyOverride.ID) {
		t.Fatalf("Emergency Alert Display Snapshot = %+v", applied)
	}
	acknowledgeDisplaySnapshot(t, displayClient, server.address, applied)
	assertDisplayListContains(
		t, administrator, server.address,
		fmt.Sprintf(`"applied_emergency_alert_id":%d`, emergencyOverride.ID),
	)

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	server = startBeamers(t, bin, dataDir)
	page = readDisplayHTML(t, displayClient, server.address)
	if !strings.Contains(page, "Evacuate using marked exits") {
		t.Fatalf("Emergency Alert did not survive restart: %s", page)
	}
	clearPath := fmt.Sprintf(
		"/crew/events/1/overrides/%d/clear", emergencyOverride.ID,
	)
	clearConfirmation := get(
		t, administrator, server.address,
		fmt.Sprintf(
			"/crew/events/1/overrides/%d/clear-confirmation",
			emergencyOverride.ID,
		),
	)
	clearConfirmationBody, readErr := io.ReadAll(clearConfirmation.Body)
	closeErr = clearConfirmation.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil ||
		clearConfirmation.StatusCode != http.StatusOK ||
		!bytes.Contains(clearConfirmationBody, []byte(`value="Keyboard"`)) ||
		!bytes.Contains(clearConfirmationBody, []byte("Clear Emergency Alert")) {
		t.Fatalf(
			"keyboard Emergency clear confirmation page = %d: %s (%v)",
			clearConfirmation.StatusCode, clearConfirmationBody, err,
		)
	}
	rejectedClear := requestJSON(
		t.Context(), administrator, server.address, clearPath,
		map[string]any{
			"expected_revision": emergencyOverride.Revision,
			"command_id":        "reject-unconfirmed-emergency-clear",
		},
	)
	if rejectedClear.status != http.StatusUnprocessableEntity {
		t.Fatalf("unconfirmed Emergency clear = %d: %s", rejectedClear.status, rejectedClear.body)
	}
	cleared := requestJSON(
		t.Context(), administrator, server.address, clearPath,
		map[string]any{
			"expected_revision": emergencyOverride.Revision,
			"confirmed":         true, "confirmation_method": "Keyboard",
			"command_id": "clear-confirmed-emergency",
		},
	)
	if cleared.status != http.StatusOK {
		t.Fatalf("confirmed Emergency clear = %d: %s", cleared.status, cleared.body)
	}
	page = readDisplayHTML(t, displayClient, server.address)
	if strings.Contains(page, "Emergency Alert") ||
		!strings.Contains(page, "Fixed Display notice") {
		t.Fatalf("Emergency clear did not restore current underlying Urgent Replace: %s", page)
	}
	server.stop(t)
}

func TestEmergencyAlertSurvivesPartialStorageFailureAndRecoversEvidence(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	displayClient := enrollAndAssignDisplay(
		t, administrator, server, "Degraded Display", "event-overview",
	)
	beforeFailure := readDisplaySnapshot(t, displayClient, server.address)
	if beforeFailure.EmergencyAlert.ID != "" {
		t.Fatalf("initial Display Snapshot has Emergency Alert: %+v", beforeFailure)
	}

	healthyPreviewResult := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/emergency-alerts/preview",
		map[string]any{
			"target": map[string]any{"type": "Event"},
			"text":   "Evacuate through the north exit",
		},
	)
	var healthyPreview struct {
		ConfirmationFingerprint string `json:"confirmation_fingerprint"`
		Nondurable              bool   `json:"nondurable"`
	}
	if err := json.Unmarshal([]byte(healthyPreviewResult.body), &healthyPreview); err != nil ||
		healthyPreviewResult.status != http.StatusOK ||
		healthyPreview.ConfirmationFingerprint == "" || healthyPreview.Nondurable {
		t.Fatalf(
			"preview durable Emergency Alert = %d: %s (%v)",
			healthyPreviewResult.status, healthyPreviewResult.body, err,
		)
	}

	databasePath := filepath.Join(server.dataDir, "beamers.db")
	if err := storetest.FailCommandEvidence(t.Context(), databasePath); err != nil {
		t.Fatalf("fail durable command evidence: %v", err)
	}
	ordinary := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/stage-messages",
		map[string]any{
			"text": "must roll back", "target_group_key": "crew",
			"until_cleared": true, "command_id": "reject-degraded-stage-message",
		},
	)
	if ordinary.status != http.StatusInternalServerError {
		t.Fatalf("ordinary mutation during failure = %d: %s", ordinary.status, ordinary.body)
	}
	if page := readDisplayHTML(t, displayClient, server.address); strings.Contains(page, "must roll back") {
		t.Fatalf("failed ordinary mutation reached Display: %s", page)
	}
	newSignIn := requestJSON(
		t.Context(), authenticatedClient(t), server.address, "/auth/sign-in",
		map[string]string{
			"name": "Ada Admin", "password": "correct horse battery staple",
		},
	)
	if newSignIn.status != http.StatusInternalServerError {
		t.Fatalf(
			"new sign-in before degraded Emergency detection = %d: %s",
			newSignIn.status, newSignIn.body,
		)
	}

	rejectedDurableConfirmation := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/emergency-alerts",
		map[string]any{
			"target":              map[string]any{"type": "Event"},
			"text":                "Evacuate through the north exit",
			"preview_fingerprint": healthyPreview.ConfirmationFingerprint,
			"confirmed":           true,
			"confirmation_method": "Keyboard",
			"command_id":          "reject-durable-confirmation-after-failure",
		},
	)
	if rejectedDurableConfirmation.status != http.StatusConflict {
		t.Fatalf(
			"durable confirmation after storage failure = %d: %s",
			rejectedDurableConfirmation.status, rejectedDurableConfirmation.body,
		)
	}
	if snapshot := readDisplaySnapshot(t, displayClient, server.address); snapshot.EmergencyAlert.ID != "" {
		t.Fatalf("rejected durable confirmation reached Display: %+v", snapshot)
	}

	emergencyPreview := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/emergency-alerts/preview",
		map[string]any{
			"target": map[string]any{"type": "Event"},
			"text":   "Evacuate through the north exit",
		},
	)
	var preview struct {
		ConfirmationFingerprint string `json:"confirmation_fingerprint"`
		Nondurable              bool   `json:"nondurable"`
	}
	if err := json.Unmarshal([]byte(emergencyPreview.body), &preview); err != nil ||
		emergencyPreview.status != http.StatusOK || preview.ConfirmationFingerprint == "" ||
		!preview.Nondurable {
		t.Fatalf(
			"preview degraded Emergency Alert = %d: %s (%v)",
			emergencyPreview.status, emergencyPreview.body, err,
		)
	}
	confirmation := get(
		t, administrator, server.address,
		"/crew/events/1/emergency-alerts/confirmation?target_type=Event&text="+
			url.QueryEscape("Evacuate through the north exit"),
	)
	confirmationBody, readErr := io.ReadAll(confirmation.Body)
	closeErr := confirmation.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil ||
		confirmation.StatusCode != http.StatusOK ||
		!bytes.Contains(confirmationBody, []byte("Severely degraded")) ||
		!bytes.Contains(confirmationBody, []byte("nondurable")) {
		t.Fatalf(
			"degraded Emergency confirmation before activation = %d: %s (%v)",
			confirmation.StatusCode, confirmationBody, err,
		)
	}
	activation := frontendNamedValues(
		string(confirmationBody),
		"target_type",
		"target_id",
		"target_key",
		"text",
		"preview_fingerprint",
		"command_id",
		"build_version",
	)
	activation.Set("confirmation_method", "Keyboard")
	activated := postFrontendForm(
		t, administrator, server.address,
		"/crew/events/1/emergency-alerts/confirmation", activation,
	)
	var emergency struct {
		ID         int  `json:"id"`
		Revision   int  `json:"revision"`
		Nondurable bool `json:"nondurable"`
	}
	if err := json.Unmarshal([]byte(activated.body), &emergency); err != nil ||
		activated.status != http.StatusOK || activated.header.Get("Location") != "" ||
		emergency.ID <= 0 ||
		emergency.Revision != 1 || !emergency.Nondurable {
		t.Fatalf(
			"activate degraded Emergency Alert = %d: %s (%v)",
			activated.status, activated.body, err,
		)
	}
	replayed := postFrontendForm(
		t, administrator, server.address,
		"/crew/events/1/emergency-alerts/confirmation", activation,
	)
	if replayed.status != http.StatusOK || replayed.body != activated.body {
		t.Fatalf(
			"retry degraded Emergency Alert = %d: %s, want %d: %s",
			replayed.status, replayed.body, activated.status, activated.body,
		)
	}

	applied := readDisplaySnapshot(t, displayClient, server.address)
	if applied.EmergencyAlert.ID != strconv.Itoa(emergency.ID) ||
		!strings.Contains(readDisplayHTML(t, displayClient, server.address), "Evacuate through the north exit") {
		t.Fatalf("degraded Emergency Alert Display Snapshot = %+v", applied)
	}

	clearConfirmation := get(
		t, administrator, server.address,
		fmt.Sprintf("/crew/events/1/overrides/%d/clear-confirmation", emergency.ID),
	)
	clearConfirmationBody, readErr := io.ReadAll(clearConfirmation.Body)
	closeErr = clearConfirmation.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil ||
		clearConfirmation.StatusCode != http.StatusOK ||
		!bytes.Contains(clearConfirmationBody, []byte("Severely degraded")) ||
		!bytes.Contains(clearConfirmationBody, []byte("nondurable")) {
		t.Fatalf(
			"degraded Emergency clear confirmation = %d: %s (%v)",
			clearConfirmation.StatusCode, clearConfirmationBody, err,
		)
	}
	clearForm := frontendNamedValues(
		string(clearConfirmationBody),
		"expected_revision",
		"command_id",
		"build_version",
	)
	clearForm.Set("confirmation_method", "Keyboard")
	cleared := postFrontendForm(
		t, administrator, server.address,
		fmt.Sprintf("/crew/events/1/overrides/%d/clear-confirmation", emergency.ID),
		clearForm,
	)
	var clearedEmergency struct {
		Revision   int  `json:"revision"`
		Nondurable bool `json:"nondurable"`
	}
	if err := json.Unmarshal([]byte(cleared.body), &clearedEmergency); err != nil ||
		cleared.status != http.StatusOK || cleared.header.Get("Location") != "" ||
		clearedEmergency.Revision != 2 ||
		!clearedEmergency.Nondurable {
		t.Fatalf(
			"clear degraded Emergency Alert = %d: %s (%v)",
			cleared.status, cleared.body, err,
		)
	}
	if snapshot := readDisplaySnapshot(t, displayClient, server.address); snapshot.EmergencyAlert.ID != "" {
		t.Fatalf("cleared degraded Emergency Alert remained in snapshot: %+v", snapshot)
	}

	readiness := requestProbe(t.Context(), server.address, "/readyz", 5*time.Second)
	assertProbeResult(t, "/readyz", readiness, http.StatusServiceUnavailable, "not ready\n")
	if err := storetest.AllowCommandEvidence(t.Context(), databasePath); err != nil {
		t.Fatalf("restore durable command evidence: %v", err)
	}
	beforeRecovery := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/stage-messages",
		map[string]any{
			"text": "wait for evidence recovery", "target_group_key": "crew",
			"until_cleared": true, "command_id": "wait-for-emergency-recovery",
		},
	)
	if beforeRecovery.status != http.StatusInternalServerError {
		t.Fatalf(
			"ordinary mutation before evidence recovery = %d: %s",
			beforeRecovery.status, beforeRecovery.body,
		)
	}
	assertProbe(t, server.address, "/readyz", "ready\n")
	afterRecovery := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/stage-messages",
		map[string]any{
			"text": "recovered", "target_group_key": "crew",
			"until_cleared": true, "command_id": "resume-after-emergency-recovery",
		},
	)
	if afterRecovery.status != http.StatusOK {
		t.Fatalf(
			"ordinary mutation after evidence recovery = %d: %s",
			afterRecovery.status, afterRecovery.body,
		)
	}
	assertRecoveredEvidence := func(label string) {
		entries, _ := readAuditHistory(t, administrator, server.address)
		remaining := map[string]int{
			"ActivateEmergencyAlert/Rejected/override_revision_conflict": 1,
			"ActivateEmergencyAlert/Succeeded/":                          1,
			"ClearDisplayOverride/Succeeded/":                            1,
		}
		for _, entry := range entries {
			key := entry.Action + "/" + entry.Outcome + "/" + entry.Reason
			if _, expected := remaining[key]; expected {
				remaining[key]--
			}
		}
		for evidence, count := range remaining {
			if count != 0 {
				t.Errorf("%s Emergency evidence %q remaining = %d", label, evidence, count)
			}
		}
	}
	assertRecoveredEvidence("recovered")
	assertProbe(t, server.address, "/readyz", "ready\n")
	assertRecoveredEvidence("repeated recovery")

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	server = startBeamers(t, bin, dataDir)
	if snapshot := readDisplaySnapshot(t, displayClient, server.address); snapshot.EmergencyAlert.ID != "" {
		t.Fatalf("cleared Emergency Alert returned after restart: %+v", snapshot)
	}
	server.stop(t)
}

func readDisplayHTML(t *testing.T, client *http.Client, address string) string {
	t.Helper()
	response := get(t, client, address, "/display")
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Display page: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Display page = %d: %s", response.StatusCode, body)
	}
	return string(body)
}

func TestAdministratorActivatesPublishedEventAcrossRestart(t *testing.T) {
	client, server := startAuthenticatedAdministrator(t)
	createdResult := requestJSON(
		t.Context(), client, server.address, "/admin/events",
		map[string]string{
			"name": "Revision 2026", "planned_start_date": "2026-08-21",
			"planned_end_date": "2026-08-23", "timezone": "Europe/Berlin",
			"event_locale": "de-DE", "event_day_boundary": "06:00",
			"command_id": "create-event-for-activation",
		},
	)
	if createdResult.err != nil || createdResult.status != http.StatusCreated {
		t.Fatalf("create Event = %d, %v; body: %s", createdResult.status, createdResult.err, createdResult.body)
	}
	var created struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal([]byte(createdResult.body), &created); err != nil {
		t.Fatalf("decode created Event: %v", err)
	}
	assertJSONRequest(
		t, client, server.address, "/admin/events/1/grants",
		map[string]any{"account_id": 1, "role": "Producer", "command_id": "grant-admin-producer"},
		http.StatusCreated, "{\"event_id\":1,\"account_id\":1,\"role\":\"Producer\"}\n",
	)

	rundownClient := rundownv1connect.NewRundownServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	plannedStart := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	edited, err := rundownClient.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: int64(created.ID), CommandId: "activation-draft", ExpectedDraftRevision: 0,
		Locations: []*rundownv1.LocationDraft{{Ref: "main", Name: "Main Hall"}},
		Lanes: []*rundownv1.LaneDraft{{
			Ref: "main-lane", Name: "Main Lane",
			Location: &rundownv1.TargetRef{Target: &rundownv1.TargetRef_Ref{Ref: "main"}},
		}},
		Sessions: []*rundownv1.SessionDraft{{
			Ref: "opening", Title: "Opening", Type: rundownv1.SessionType_SESSION_TYPE_CEREMONY,
			AudienceVisibility: rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_PUBLIC,
			PlannedStart:       timestamppb.New(plannedStart), PlannedEnd: timestamppb.New(plannedStart.Add(time.Hour)),
			TimingPolicy:    rundownv1.TimingPolicy_TIMING_POLICY_FIXED_END,
			MinimumDuration: durationpb.New(30 * time.Minute),
			StartBoundary:   rundownv1.Boundary_BOUNDARY_HARD, EndBoundary: rundownv1.Boundary_BOUNDARY_SOFT,
			Lanes: []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Ref{Ref: "main-lane"}}},
		}},
	}))
	if err != nil {
		t.Fatalf("Edit Draft RPC: %v", err)
	}
	changeIDs := make([]int64, 0, len(edited.Msg.GetChanges()))
	for _, change := range edited.Msg.GetChanges() {
		changeIDs = append(changeIDs, change.GetId())
	}
	preview, err := rundownClient.PublishPreview(t.Context(), connect.NewRequest(&rundownv1.PublishPreviewRequest{
		EventId: int64(created.ID), ChangeIds: changeIDs,
	}))
	if err != nil {
		t.Fatalf("Publish Preview RPC: %v", err)
	}
	if _, publishErr := rundownClient.Publish(t.Context(), connect.NewRequest(&rundownv1.PublishRequest{
		EventId: int64(created.ID), CommandId: "activation-publish",
		Confirmation: &rundownv1.PublishConfirmation{
			DraftRevision: preview.Msg.GetDraftRevision(), PublishedRevision: preview.Msg.GetPublishedRevision(),
			ChangeIds: preview.Msg.GetChangeIds(), Fingerprint: preview.Msg.GetFingerprint(),
		},
	})); publishErr != nil {
		t.Fatalf("Publish RPC: %v", publishErr)
	}

	activationClient := activationv1connect.NewActivationServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	preflight, err := activationClient.Preflight(t.Context(), connect.NewRequest(&activationv1.PreflightRequest{
		EventId: int64(created.ID),
	}))
	if err != nil {
		t.Fatalf("Activation Preflight RPC: %v", err)
	}
	if len(preflight.Msg.GetBlockers()) != 0 || preflight.Msg.GetConfirmation() == nil {
		t.Fatalf("Activation Preflight = %+v, want confirmation without blockers", preflight.Msg)
	}
	activated, err := activationClient.Activate(t.Context(), connect.NewRequest(&activationv1.ActivateRequest{
		EventId: int64(created.ID), CommandId: "activate-event-1",
		Confirmation: preflight.Msg.GetConfirmation(),
	}))
	if err != nil {
		t.Fatalf("Activate RPC: %v", err)
	}
	if activated.Msg.GetEventId() != int64(created.ID) || activated.Msg.GetGeneration() != 1 {
		t.Fatalf("Activate response = %+v, want Event %d generation 1", activated.Msg, created.ID)
	}

	dataDir := server.dataDir
	server.stop(t)
	restarted := startBeamers(t, server.bin, dataDir)
	restartedClient := activationv1connect.NewActivationServiceClient(
		client, "http://"+restarted.address, connect.WithProtoJSON(),
	)
	active, err := restartedClient.GetActiveEvent(
		t.Context(), connect.NewRequest(&activationv1.GetActiveEventRequest{}),
	)
	if err != nil {
		t.Fatalf("Get Active Event after restart: %v", err)
	}
	if active.Msg.EventId == nil || active.Msg.GetEventId() != int64(created.ID) || active.Msg.GetGeneration() != 1 {
		t.Fatalf("Active Event after restart = %+v, want Event %d generation 1", active.Msg, created.ID)
	}
	restarted.stop(t)
}

func TestPublicScheduleListsOnlyPublicSessions(t *testing.T) {
	client, server := startAuthenticatedAdministrator(t)
	publicSessionID := prepareActiveSchedule(t, client, server)

	response := get(t, authenticatedClient(t), server.address, "/events/revision-2099/schedule")
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read public Schedule: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /schedule = %d %q, want %d", response.StatusCode, body, http.StatusOK)
	}
	page := string(body)
	for _, want := range []string{
		`<html lang="en-GB" data-locale="en-GB">`,
		"Program day 2099-08-21",
		"Event timezone: Europe/Berlin",
		"Opening Keynote",
		"2099-08-21T10:00:00+02:00",
		"Main Hall",
		"Main Lane",
		"General",
		fmt.Sprintf(`href="/events/revision-2099/schedule/sessions/%d"`, publicSessionID),
	} {
		if !strings.Contains(page, want) {
			t.Errorf("public Schedule does not contain %q; body: %s", want, page)
		}
	}
	for _, private := range []string{
		"Private Soundcheck",
		"Old Announcement",
		"Call Pat on +44 20 7946 0958",
		"radio channel 4",
		"/srv/beamers/private/keynote.pdf",
	} {
		if strings.Contains(page, private) {
			t.Errorf("public Schedule contains private value %q; body: %s", private, page)
		}
	}
	server.stop(t)
}

func TestPublicScheduleSessionHidesCrewOnlyAndUnknownIdentically(t *testing.T) {
	client, server := startAuthenticatedAdministrator(t)
	publicSessionID := prepareActiveSchedule(t, client, server)

	public := get(t, authenticatedClient(t), server.address, fmt.Sprintf("/events/revision-2099/schedule/sessions/%d", publicSessionID))
	publicBody, readErr := io.ReadAll(public.Body)
	closeErr := public.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read public Session: %v", err)
	}
	if public.StatusCode != http.StatusOK || !strings.Contains(string(publicBody), "Opening Keynote") {
		t.Errorf("public Session = %d %q, want 200 with title", public.StatusCode, publicBody)
	}
	ended := get(t, authenticatedClient(t), server.address, "/events/revision-2099/schedule/sessions/3")
	endedBody, readErr := io.ReadAll(ended.Body)
	closeErr = ended.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read ended public Session: %v", err)
	}
	if ended.StatusCode != http.StatusOK || !strings.Contains(string(endedBody), "Old Announcement") {
		t.Errorf("ended public Session = %d %q, want stable 200 deep link", ended.StatusCode, endedBody)
	}

	crewOnly := get(t, authenticatedClient(t), server.address, "/events/revision-2099/schedule/sessions/2")
	crewOnlyBody, readErr := io.ReadAll(crewOnly.Body)
	closeErr = crewOnly.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Crew Only Session response: %v", err)
	}
	unknown := get(t, authenticatedClient(t), server.address, "/events/revision-2099/schedule/sessions/999999")
	unknownBody, readErr := io.ReadAll(unknown.Body)
	closeErr = unknown.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read unknown Session response: %v", err)
	}
	if crewOnly.StatusCode != http.StatusNotFound || unknown.StatusCode != http.StatusNotFound ||
		!bytes.Equal(crewOnlyBody, unknownBody) {
		t.Errorf(
			"private responses differ: Crew Only = %d %q; unknown = %d %q",
			crewOnly.StatusCode, crewOnlyBody, unknown.StatusCode, unknownBody,
		)
	}
	for _, body := range [][]byte{crewOnlyBody, unknownBody} {
		if bytes.Contains(body, []byte("Private Soundcheck")) || bytes.Contains(body, []byte("Crew")) {
			t.Errorf("generic not-found response leaks private information: %q", body)
		}
	}
	server.stop(t)
}

func TestPublicScheduleDeepLinkSurvivesPublishedChanges(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	client := rundownv1connect.NewRundownServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	current, err := client.GetCrewRundown(
		t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}),
	)
	if err != nil {
		t.Fatalf("load Rundown before stable-link changes: %v", err)
	}
	retimedStart := time.Date(2099, 8, 21, 8, 30, 0, 0, time.UTC)
	edited, err := client.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "rename-retime-public-session",
		ExpectedDraftRevision: current.Msg.GetDraftRevision(),
		Sessions: []*rundownv1.SessionDraft{{
			Id: sessionID, Title: "Renamed Keynote",
			PlannedStart: timestamppb.New(retimedStart),
			PlannedEnd:   timestamppb.New(retimedStart.Add(time.Hour)),
			UpdateMask: &fieldmaskpb.FieldMask{
				Paths: []string{"title", "planned_start", "planned_end"},
			},
		}},
	}))
	if err != nil {
		t.Fatalf("rename and retime public Session: %v", err)
	}
	publishEditedDraft(t, client, edited.Msg, "publish-renamed-retimed-session")
	path := fmt.Sprintf("/events/revision-2099/schedule/sessions/%d", sessionID)
	changed := get(t, authenticatedClient(t), server.address, path)
	changedBody, readErr := io.ReadAll(changed.Body)
	closeErr := changed.Body.Close()
	if joinedErr := errors.Join(readErr, closeErr); joinedErr != nil {
		t.Fatalf("read changed stable deep link: %v", joinedErr)
	}
	if changed.StatusCode != http.StatusOK ||
		!strings.Contains(string(changedBody), "Renamed Keynote") ||
		!strings.Contains(string(changedBody), "2099-08-21T10:30:00+02:00") {
		t.Errorf("changed stable deep link = %d %q", changed.StatusCode, changedBody)
	}

	current, err = client.GetCrewRundown(
		t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}),
	)
	if err != nil {
		t.Fatalf("load Rundown before hiding stable link: %v", err)
	}
	hidden, err := client.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "hide-public-session", ExpectedDraftRevision: current.Msg.GetDraftRevision(),
		Sessions: []*rundownv1.SessionDraft{{
			Id: sessionID, AudienceVisibility: rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_CREW_ONLY,
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"audience_visibility"}},
		}},
	}))
	if err != nil {
		t.Fatalf("hide public Session: %v", err)
	}
	publishEditedDraft(t, client, hidden.Msg, "publish-hidden-session")
	private := get(t, authenticatedClient(t), server.address, path)
	privateBody, readErr := io.ReadAll(private.Body)
	closeErr = private.Body.Close()
	if joinedErr := errors.Join(readErr, closeErr); joinedErr != nil {
		t.Fatalf("read hidden stable deep link: %v", joinedErr)
	}
	unknown := get(t, authenticatedClient(t), server.address, "/events/revision-2099/schedule/sessions/999999")
	unknownBody, readErr := io.ReadAll(unknown.Body)
	closeErr = unknown.Body.Close()
	if joinedErr := errors.Join(readErr, closeErr); joinedErr != nil {
		t.Fatalf("read unknown Session beside hidden stable link: %v", joinedErr)
	}
	if private.StatusCode != http.StatusNotFound || unknown.StatusCode != http.StatusNotFound ||
		!bytes.Equal(privateBody, unknownBody) {
		t.Errorf(
			"hidden stable link differs from unknown: %d %q; %d %q",
			private.StatusCode, privateBody, unknown.StatusCode, unknownBody,
		)
	}
	current, err = client.GetCrewRundown(
		t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}),
	)
	if err != nil {
		t.Fatalf("load Rundown before restoring public visibility: %v", err)
	}
	restored, err := client.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "restore-public-session", ExpectedDraftRevision: current.Msg.GetDraftRevision(),
		Sessions: []*rundownv1.SessionDraft{{
			Id: sessionID, AudienceVisibility: rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_PUBLIC,
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"audience_visibility"}},
		}},
	}))
	if err != nil {
		t.Fatalf("restore public Session visibility: %v", err)
	}
	publishEditedDraft(t, client, restored.Msg, "publish-restored-session")
	publicAgain := get(t, authenticatedClient(t), server.address, path)
	publicAgainBody, readErr := io.ReadAll(publicAgain.Body)
	closeErr = publicAgain.Body.Close()
	if joinedErr := errors.Join(readErr, closeErr); joinedErr != nil {
		t.Fatalf("read restored stable deep link: %v", joinedErr)
	}
	if publicAgain.StatusCode != http.StatusOK ||
		!strings.Contains(string(publicAgainBody), "Renamed Keynote") {
		t.Errorf("restored stable deep link = %d %q", publicAgain.StatusCode, publicAgainBody)
	}
	server.stop(t)
}

func publishEditedDraft(
	t *testing.T,
	client rundownv1connect.RundownServiceClient,
	edited *rundownv1.EditDraftResponse,
	commandID string,
) {
	t.Helper()
	changeIDs := make([]int64, 0, len(edited.GetChanges()))
	for _, change := range edited.GetChanges() {
		changeIDs = append(changeIDs, change.GetId())
	}
	preview, err := client.PublishPreview(t.Context(), connect.NewRequest(
		&rundownv1.PublishPreviewRequest{EventId: 1, ChangeIds: changeIDs},
	))
	if err != nil {
		t.Fatalf("preview edited Draft Publish: %v", err)
	}
	if _, err := client.Publish(t.Context(), connect.NewRequest(&rundownv1.PublishRequest{
		EventId: 1, CommandId: commandID,
		Confirmation: &rundownv1.PublishConfirmation{
			DraftRevision: preview.Msg.GetDraftRevision(), PublishedRevision: preview.Msg.GetPublishedRevision(),
			ChangeIds: preview.Msg.GetChangeIds(), Fingerprint: preview.Msg.GetFingerprint(),
		},
	})); err != nil {
		t.Fatalf("publish edited Draft: %v", err)
	}
}

func TestPublicScheduleSupportsCacheableSnapshotsAndLiveInvalidation(t *testing.T) {
	client, server := startAuthenticatedAdministrator(t)
	publicSessionID := prepareActiveSchedule(t, client, server)
	publicClient := authenticatedClient(t)

	initial := get(t, publicClient, server.address, "/events/revision-2099/schedule")
	initialBody, readErr := io.ReadAll(initial.Body)
	closeErr := initial.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read initial public Schedule: %v", err)
	}
	if initial.StatusCode != http.StatusOK || len(initialBody) == 0 {
		t.Fatalf("initial public Schedule = %d %q, want nonempty 200", initial.StatusCode, initialBody)
	}
	for _, want := range []string{
		`hx-ext="sse"`,
		`sse-connect="/schedule/events?`,
		`hx-trigger="sse:schedule"`,
		`id="schedule-location"`,
		`id="schedule-status" role="status" aria-live="polite"`,
		`src="/assets/htmx-2.0.10.min.js"`,
		`src="/assets/htmx-ext-sse-2.2.4.min.js"`,
	} {
		if !bytes.Contains(initialBody, []byte(want)) {
			t.Errorf("initial public Schedule missing %q", want)
		}
	}
	etag := initial.Header.Get("ETag")
	if etag == "" {
		t.Fatal("initial public Schedule has no ETag")
	}
	if got := initial.Header.Get("Cache-Control"); got != "public, max-age=15, must-revalidate" {
		t.Errorf("public Schedule Cache-Control = %q", got)
	}

	request, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet, "http://"+server.address+"/events/revision-2099/schedule", http.NoBody,
	)
	if err != nil {
		t.Fatalf("create conditional public Schedule request: %v", err)
	}
	request.Header.Set("If-None-Match", etag)
	conditional, err := publicClient.Do(request)
	if err != nil {
		t.Fatalf("conditional public Schedule request: %v", err)
	}
	conditionalBody, readErr := io.ReadAll(conditional.Body)
	closeErr = conditional.Body.Close()
	if joinedErr := errors.Join(readErr, closeErr); joinedErr != nil {
		t.Fatalf("read conditional public Schedule: %v", joinedErr)
	}
	if conditional.StatusCode != http.StatusNotModified || len(conditionalBody) != 0 {
		t.Errorf(
			"conditional public Schedule = %d %q, want empty 304",
			conditional.StatusCode, conditionalBody,
		)
	}

	streamPath := publicScheduleEventsPath(t, initialBody)
	streamResponse, streamReader := openPublicScheduleEvents(
		t,
		publicClient,
		server.address,
		streamPath,
	)
	rundownClient := rundownv1connect.NewRundownServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	current, err := rundownClient.GetCrewRundown(
		t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}),
	)
	if err != nil {
		t.Fatalf("load Rundown before Schedule invalidation: %v", err)
	}
	edited, err := rundownClient.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "edit-live-schedule",
		ExpectedDraftRevision: current.Msg.GetDraftRevision(),
		Sessions: []*rundownv1.SessionDraft{{
			Id: publicSessionID, Title: "Live Schedule Keynote",
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}},
		}},
	}))
	if err != nil {
		t.Fatalf("edit live Schedule: %v", err)
	}
	publishEditedDraft(t, rundownClient, edited.Msg, "publish-live-schedule")
	if eventID := readPublicScheduleInvalidation(t, streamReader); eventID == "" {
		t.Fatal("live Schedule invalidation has no Event ID")
	}
	if closeErr = streamResponse.Body.Close(); closeErr != nil {
		t.Fatalf("close live Schedule stream: %v", closeErr)
	}

	refreshedRequest, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet, "http://"+server.address+"/events/revision-2099/schedule", http.NoBody,
	)
	if err != nil {
		t.Fatalf("create refreshed Schedule request: %v", err)
	}
	refreshedRequest.Header.Set("Cache-Control", "no-cache")
	refreshed, err := publicClient.Do(refreshedRequest)
	if err != nil {
		t.Fatalf("refresh live Schedule: %v", err)
	}
	refreshedBody, readErr := io.ReadAll(refreshed.Body)
	closeErr = refreshed.Body.Close()
	if joinedErr := errors.Join(readErr, closeErr); joinedErr != nil {
		t.Fatalf("read refreshed live Schedule: %v", joinedErr)
	}
	if refreshed.StatusCode != http.StatusOK ||
		!bytes.Contains(refreshedBody, []byte("Live Schedule Keynote")) {
		t.Fatalf("refreshed live Schedule = %d %q", refreshed.StatusCode, refreshedBody)
	}

	gapResponse, gapReader := openPublicScheduleEvents(
		t,
		publicClient,
		server.address,
		streamPath,
	)
	readPublicScheduleInvalidation(t, gapReader)
	if closeErr = gapResponse.Body.Close(); closeErr != nil {
		t.Fatalf("close gap-recovery Schedule stream: %v", closeErr)
	}

	bin, dataDir := server.bin, server.dataDir
	server.stop(t)
	restarted := startBeamers(t, bin, dataDir)
	incompatibleResponse, incompatibleReader := openPublicScheduleEvents(
		t,
		publicClient,
		restarted.address,
		streamPath,
	)
	readPublicScheduleInvalidation(t, incompatibleReader)
	if closeErr = incompatibleResponse.Body.Close(); closeErr != nil {
		t.Fatalf("close restarted Schedule stream: %v", closeErr)
	}
	restarted.stop(t)
}

func publicScheduleEventsPath(t *testing.T, page []byte) string {
	t.Helper()
	match := regexp.MustCompile(`sse-connect="([^"]+)"`).FindSubmatch(page)
	if len(match) != 2 {
		t.Fatalf("public Schedule has no SSE connection path: %s", page)
	}
	return strings.ReplaceAll(string(match[1]), "&amp;", "&")
}

func openPublicScheduleEvents(
	t *testing.T,
	client *http.Client,
	address string,
	path string,
) (*http.Response, *bufio.Reader) {
	t.Helper()
	request, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet, "http://"+address+path, http.NoBody,
	)
	if err != nil {
		t.Fatalf("create public Schedule stream request: %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("open public Schedule stream: %v", err)
	}
	if response.StatusCode != http.StatusOK ||
		response.Header.Get("Content-Type") != "text/event-stream" {
		_ = response.Body.Close()
		t.Fatalf(
			"public Schedule stream = %d %q",
			response.StatusCode,
			response.Header.Get("Content-Type"),
		)
	}
	return response, bufio.NewReader(response.Body)
}

func readPublicScheduleInvalidation(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	event, id := "", ""
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read public Schedule invalidation: %v", err)
		}
		line = strings.TrimSpace(line)
		if value, ok := strings.CutPrefix(line, "event:"); ok {
			event = strings.TrimSpace(value)
		}
		if value, ok := strings.CutPrefix(line, "id:"); ok {
			id = strings.TrimSpace(value)
		}
		if line == "" && event == "schedule" {
			return id
		}
	}
}

func TestPublicScheduleEncodesFiltersAndLocalTimeInURL(t *testing.T) {
	client, server := startAuthenticatedAdministrator(t)
	publicSessionID := prepareActiveSchedule(t, client, server)
	publicClient := authenticatedClient(t)

	response := get(
		t, publicClient, server.address,
		"/events/revision-2099/schedule?day=2099-08-21&location=1&lane=1&track=1&time_zone=America%2FNew_York",
	)
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if joinedErr := errors.Join(readErr, closeErr); joinedErr != nil {
		t.Fatalf("read filtered local-time Schedule: %v", joinedErr)
	}
	page := string(body)
	for _, expected := range []string{
		`<option value="2099-08-21" selected>2099-08-21</option>`,
		`<option value="1" selected>Main Hall</option>`,
		`<option value="1" selected>Main Lane</option>`,
		`<option value="1" selected>General</option>`,
		`value="America/New_York"`,
		"Attendee-local conversion: America/New_York. Program days remain grouped in Event time.",
		"Event Time (CEST +02:00): Forecast Start 21 Aug 2099 10:00 CEST",
		"Attendee-local time (EDT -04:00): Forecast Start",
		`datetime="2099-08-21T04:00:00-04:00">21 Aug 2099 04:00 EDT`,
		fmt.Sprintf(`/events/revision-2099/schedule/sessions/%d?time_zone=America%%2FNew_York`, publicSessionID),
		`hx-get="/events/revision-2099/schedule?day=2099-08-21&amp;lane=1&amp;location=1&amp;time_zone=America%2FNew_York&amp;track=1"`,
	} {
		if !strings.Contains(page, expected) {
			t.Errorf("filtered local-time Schedule missing %q: %s", expected, page)
		}
	}
	for _, private := range []string{"Private Soundcheck", "radio channel 4"} {
		if strings.Contains(page, private) {
			t.Errorf("filtered local-time Schedule contains private value %q", private)
		}
	}

	empty := get(t, publicClient, server.address, "/events/revision-2099/schedule?location=999999")
	emptyBody, readErr := io.ReadAll(empty.Body)
	closeErr = empty.Body.Close()
	if joinedErr := errors.Join(readErr, closeErr); joinedErr != nil {
		t.Fatalf("read unmatched Schedule filter: %v", joinedErr)
	}
	if empty.StatusCode != http.StatusOK || strings.Contains(string(emptyBody), "Opening Keynote") {
		t.Errorf("unmatched Schedule filter = %d %q", empty.StatusCode, emptyBody)
	}
	invalid := get(t, publicClient, server.address, "/events/revision-2099/schedule?lane=not-an-id")
	invalidBody, readErr := io.ReadAll(invalid.Body)
	closeErr = invalid.Body.Close()
	if joinedErr := errors.Join(readErr, closeErr); joinedErr != nil {
		t.Fatalf("read invalid Schedule filter: %v", joinedErr)
	}
	if invalid.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid Schedule filter = %d %q", invalid.StatusCode, invalidBody)
	}
	server.stop(t)
}

func TestPublicScheduleNormalizesActualStartWithoutChangingCrewHistory(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	communicatedStart := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	sessionID := prepareCommunicatedTimeSchedule(t, administrator, server, communicatedStart)
	operator := provisionOperator(t, administrator, server)
	client := sessionv1connect.NewSessionControlServiceClient(
		operator, "http://"+server.address, connect.WithProtoJSON(),
	)
	started, err := client.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "start-with-communicated-time",
		ExpectedLiveStateRevision: proto.Int64(0),
	}))
	if err != nil {
		t.Fatalf("start Session for communicated-time presentation: %v", err)
	}
	exactActualStart := started.Msg.GetState().GetActualStart().AsTime()
	if exactActualStart.Equal(communicatedStart) {
		t.Fatal("test setup did not produce distinct exact and Communicated Times")
	}

	public := get(
		t, authenticatedClient(t), server.address,
		fmt.Sprintf("/events/communicated-time/schedule/sessions/%d", sessionID),
	)
	body, readErr := io.ReadAll(public.Body)
	closeErr := public.Body.Close()
	if joinedErr := errors.Join(readErr, closeErr); joinedErr != nil {
		t.Fatalf("read communicated-time Session: %v", joinedErr)
	}
	page := string(body)
	if public.StatusCode != http.StatusOK ||
		!strings.Contains(page, "Status: Live") ||
		!strings.Contains(page, `datetime="`+communicatedStart.Format(time.RFC3339)+`"`) ||
		strings.Contains(page, exactActualStart.Format(time.RFC3339)) {
		t.Errorf(
			"communicated-time Session = %d %q; exact Actual Start = %s",
			public.StatusCode, body, exactActualStart,
		)
	}
	history, err := client.GetSessionHistory(t.Context(), connect.NewRequest(
		&sessionv1.GetSessionHistoryRequest{EventId: 1, SessionId: sessionID},
	))
	if err != nil || len(history.Msg.GetRuns()) != 1 ||
		!history.Msg.GetRuns()[0].GetActualStart().AsTime().Equal(exactActualStart) {
		t.Errorf("crew Run history changed exact Actual Start: %+v, %v", history, err)
	}
	server.stop(t)
}

func TestPublicScheduleNormalizesActualEndWithoutChangingCrewHistory(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	communicatedEnd := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	sessionID := prepareCommunicatedTimeSchedule(
		t, administrator, server, communicatedEnd.Add(-30*time.Minute),
	)
	operator := provisionOperator(t, administrator, server)
	client := sessionv1connect.NewSessionControlServiceClient(
		operator, "http://"+server.address, connect.WithProtoJSON(),
	)
	if _, err := client.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "start-before-communicated-end",
		ExpectedLiveStateRevision: proto.Int64(0),
	})); err != nil {
		t.Fatalf("start Session before communicated End: %v", err)
	}
	ended, err := client.EndSession(t.Context(), connect.NewRequest(&sessionv1.EndSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "end-with-communicated-time",
		ExpectedLiveStateRevision: proto.Int64(1),
	}))
	if err != nil {
		t.Fatalf("end Session for communicated-time presentation: %v", err)
	}
	exactActualEnd := ended.Msg.GetState().GetActualEnd().AsTime()
	if exactActualEnd.Equal(communicatedEnd) {
		t.Fatal("test setup did not produce distinct exact End and Communicated Times")
	}
	public := get(
		t, authenticatedClient(t), server.address,
		fmt.Sprintf("/events/communicated-time/schedule/sessions/%d", sessionID),
	)
	body, readErr := io.ReadAll(public.Body)
	closeErr := public.Body.Close()
	if joinedErr := errors.Join(readErr, closeErr); joinedErr != nil {
		t.Fatalf("read communicated End Session: %v", joinedErr)
	}
	page := string(body)
	if public.StatusCode != http.StatusOK ||
		!strings.Contains(page, "Status: Ended") ||
		!strings.Contains(
			page,
			`Actual End: <time datetime="`+communicatedEnd.Format(time.RFC3339)+`"`,
		) ||
		strings.Contains(
			page,
			`Actual End: <time datetime="`+exactActualEnd.Format(time.RFC3339)+`"`,
		) {
		t.Errorf(
			"communicated End Session = %d %q; exact Actual End = %s",
			public.StatusCode, body, exactActualEnd,
		)
	}
	history, err := client.GetSessionHistory(t.Context(), connect.NewRequest(
		&sessionv1.GetSessionHistoryRequest{EventId: 1, SessionId: sessionID},
	))
	if err != nil || len(history.Msg.GetRuns()) != 1 ||
		!history.Msg.GetRuns()[0].GetActualEnd().AsTime().Equal(exactActualEnd) {
		t.Errorf("crew Run history changed exact Actual End: %+v, %v", history, err)
	}
	server.stop(t)
}

func TestProducerCreatesIncludedCompetitionEntry(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	competitionID, deadline := addCompetitionSession(t, administrator, server)
	client := competitionv1connect.NewCompetitionServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)

	configured, err := client.GetCompetition(t.Context(), connect.NewRequest(
		&competitionv1.GetCompetitionRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil {
		t.Fatalf("Get Competition: %v", err)
	}
	if !configured.Msg.GetSubmissionDeadline().AsTime().Equal(deadline) ||
		configured.Msg.GetEffectiveDefaultDisposition() !=
			rundownv1.EntryDisposition_ENTRY_DISPOSITION_INCLUDED {
		t.Fatalf("Competition configuration = %+v", configured.Msg)
	}
	schedulePage := get(t, authenticatedClient(t), server.address, "/events/revision-2099/schedule")
	schedulePageBody, readErr := io.ReadAll(schedulePage.Body)
	closeErr := schedulePage.Body.Close()
	if joinedErr := errors.Join(readErr, closeErr); joinedErr != nil {
		t.Fatalf("read Schedule before Entry invalidation: %v", joinedErr)
	}
	entryStream, entryStreamReader := openPublicScheduleEvents(
		t,
		authenticatedClient(t),
		server.address,
		publicScheduleEventsPath(t, schedulePageBody),
	)
	created, err := client.CreateEntry(t.Context(), connect.NewRequest(
		&competitionv1.CreateEntryRequest{
			EventId: 1, SessionId: competitionID, CommandId: "create-included-entry",
			Name: "Project Aurora", PublicDetails: "An attendee-visible demo",
			CrewNotes: "Needs the HDMI adapter",
		},
	))
	if err != nil {
		t.Fatalf("Create Entry: %v", err)
	}
	readPublicScheduleInvalidation(t, entryStreamReader)
	if closeErr = entryStream.Body.Close(); closeErr != nil {
		t.Fatalf("close Entry Schedule stream: %v", closeErr)
	}
	entry := created.Msg.GetEntry()
	if entry.GetId() <= 0 || entry.GetCompetitionSessionId() != competitionID ||
		entry.GetName() != "Project Aurora" ||
		entry.GetDisposition() != rundownv1.EntryDisposition_ENTRY_DISPOSITION_INCLUDED ||
		!entry.GetParticipating() || entry.GetRevision() != 1 {
		t.Fatalf("created Competition Entry = %+v", entry)
	}
	updated, err := client.UpdateEntry(t.Context(), connect.NewRequest(
		&competitionv1.UpdateEntryRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			CommandId: "update-included-entry", ExpectedRevision: entry.GetRevision(),
			Name: "Project Aurora Revised", PublicDetails: "An attendee-visible revised demo",
			CrewNotes: "Needs the HDMI adapter",
		},
	))
	if err != nil || updated.Msg.GetEntry().GetRevision() != 2 ||
		updated.Msg.GetEntry().GetName() != "Project Aurora Revised" {
		t.Fatalf("updated Competition Entry = %+v, %v", updated, err)
	}
	_, staleUpdateErr := client.UpdateEntry(t.Context(), connect.NewRequest(
		&competitionv1.UpdateEntryRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			CommandId: "stale-entry-update", ExpectedRevision: entry.GetRevision(),
			Name: "Stale Project",
		},
	))
	if connect.CodeOf(staleUpdateErr) != connect.CodeAborted {
		t.Fatalf("stale Entry update error = %v, want Aborted", staleUpdateErr)
	}
	entry = updated.Msg.GetEntry()
	scheduleBody := func() string {
		t.Helper()
		response := get(t, authenticatedClient(t), server.address, "/events/revision-2099/schedule/sessions/"+strconv.FormatInt(competitionID, 10))
		body, bodyReadErr := io.ReadAll(response.Body)
		bodyCloseErr := response.Body.Close()
		if joinedErr := errors.Join(bodyReadErr, bodyCloseErr); joinedErr != nil {
			t.Fatalf("read public Competition: %v", joinedErr)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("public Competition status = %d: %s", response.StatusCode, body)
		}
		return string(body)
	}
	if body := scheduleBody(); !strings.Contains(body, "Project Aurora Revised") ||
		strings.Contains(body, "Needs the HDMI adapter") {
		t.Fatalf("Included Entry public projection = %q", body)
	}
	pending, err := client.ChangeEntryDisposition(t.Context(), connect.NewRequest(
		&competitionv1.ChangeEntryDispositionRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			CommandId: "make-entry-pending", ExpectedRevision: entry.GetRevision(),
			Disposition: rundownv1.EntryDisposition_ENTRY_DISPOSITION_PENDING,
		},
	))
	if err != nil || pending.Msg.GetEntry().GetParticipating() {
		t.Fatalf("make Entry Pending = %+v, %v", pending, err)
	}
	if body := scheduleBody(); strings.Contains(body, "Project Aurora Revised") ||
		strings.Contains(body, "Needs the HDMI adapter") {
		t.Fatalf("Pending Entry leaked publicly = %q", body)
	}
	included, err := client.ChangeEntryDisposition(t.Context(), connect.NewRequest(
		&competitionv1.ChangeEntryDispositionRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			CommandId: "include-entry", ExpectedRevision: pending.Msg.GetEntry().GetRevision(),
			Disposition: rundownv1.EntryDisposition_ENTRY_DISPOSITION_INCLUDED,
		},
	))
	if err != nil || !included.Msg.GetEntry().GetParticipating() {
		t.Fatalf("include Entry = %+v, %v", included, err)
	}
	if _, err = client.ConfigureReadiness(t.Context(), connect.NewRequest(
		&competitionv1.ConfigureReadinessRequest{
			EventId: 1, SessionId: competitionID, CommandId: "disable-delivery-for-disposition-test",
			ExpectedReadinessRevision: 0, FileDeliveryRequired: false,
		},
	)); err != nil {
		t.Fatalf("disable file delivery for disposition test: %v", err)
	}
	sessionClient := sessionv1connect.NewSessionControlServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	started, err := sessionClient.StartSession(t.Context(), connect.NewRequest(
		&sessionv1.StartSessionRequest{
			EventId: 1, SessionId: competitionID, CommandId: "start-competition",
			ExpectedLiveStateRevision: new(int64(0)),
		},
	))
	if err != nil {
		t.Fatalf("start Competition: %v", err)
	}
	_, err = client.ChangeEntryDisposition(t.Context(), connect.NewRequest(
		&competitionv1.ChangeEntryDispositionRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			CommandId:        "reject-live-entry-without-confirmation",
			ExpectedRevision: included.Msg.GetEntry().GetRevision(),
			Disposition:      rundownv1.EntryDisposition_ENTRY_DISPOSITION_REJECTED,
		},
	))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("unconfirmed live disposition error = %v, want FailedPrecondition", err)
	}
	rejected, err := client.ChangeEntryDisposition(t.Context(), connect.NewRequest(
		&competitionv1.ChangeEntryDispositionRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			CommandId: "reject-live-entry", ExpectedRevision: included.Msg.GetEntry().GetRevision(),
			Disposition:           rundownv1.EntryDisposition_ENTRY_DISPOSITION_REJECTED,
			ConfirmedLiveOverride: true,
		},
	))
	if err != nil || rejected.Msg.GetEntry().GetParticipating() {
		t.Fatalf("reject live Entry = %+v, %v", rejected, err)
	}
	if body := scheduleBody(); strings.Contains(body, "Project Aurora Revised") {
		t.Fatalf("Rejected Entry remained public = %q", body)
	}
	preview, err := sessionClient.PreviewAdjustTarget(t.Context(), connect.NewRequest(
		&sessionv1.PreviewAdjustTargetRequest{
			EventId: 1, SessionId: competitionID,
			Adjustment: &sessionv1.PreviewAdjustTargetRequest_Custom{
				Custom: durationpb.New(5 * time.Minute),
			},
		},
	))
	if err != nil {
		t.Fatalf("preview Competition reschedule: %v", err)
	}
	_, err = sessionClient.AdjustTarget(t.Context(), connect.NewRequest(
		&sessionv1.AdjustTargetRequest{
			EventId: 1, SessionId: competitionID, CommandId: "reschedule-competition",
			ExpectedLiveStateRevision: new(started.Msg.GetState().GetLiveStateRevision()),
			Adjustment: &sessionv1.AdjustTargetRequest_Custom{
				Custom: durationpb.New(5 * time.Minute),
			},
			PreviewFingerprint: preview.Msg.GetPreviewFingerprint(),
			Confirmed:          true, HardBoundaryConfirmed: true,
		},
	))
	if err != nil {
		t.Fatalf("reschedule Competition: %v", err)
	}
	afterReschedule, err := client.GetCompetition(t.Context(), connect.NewRequest(
		&competitionv1.GetCompetitionRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || !afterReschedule.Msg.GetSubmissionDeadline().AsTime().Equal(deadline) {
		t.Fatalf("Competition Deadline moved during reschedule = %+v, %v", afterReschedule, err)
	}
	retained, err := client.GetCompetition(t.Context(), connect.NewRequest(
		&competitionv1.GetCompetitionRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || len(retained.Msg.GetEntries()) != 1 ||
		retained.Msg.GetEntries()[0].GetRevision() != rejected.Msg.GetEntry().GetRevision() ||
		retained.Msg.GetEntries()[0].GetCrewNotes() != "Needs the HDMI adapter" {
		t.Fatalf("closed Competition changed Entry history = %+v, %v", retained, err)
	}
	auditResponse := get(t, administrator, server.address, "/admin/audit")
	auditBody, readErr := io.ReadAll(auditResponse.Body)
	closeErr = auditResponse.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Competition Audit history: %v", err)
	}
	if !strings.Contains(string(auditBody), "ChangeCompetitionEntryDisposition") {
		t.Fatalf("Competition disposition change missing from Audit history: %s", auditBody)
	}
	server.stop(t)
}

func TestProducerStagesAndReviewsCompetitionResults(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	competitionClient := competitionv1connect.NewCompetitionServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	first, err := competitionClient.CreateEntry(t.Context(), connect.NewRequest(
		&competitionv1.CreateEntryRequest{
			EventId: 1, SessionId: competitionID, CommandId: "create-results-entry-first",
			Name: "First Result",
		},
	))
	if err != nil {
		t.Fatalf("create first Results Entry: %v", err)
	}
	second, err := competitionClient.CreateEntry(t.Context(), connect.NewRequest(
		&competitionv1.CreateEntryRequest{
			EventId: 1, SessionId: competitionID, CommandId: "create-results-entry-second",
			Name: "Second Result",
		},
	))
	if err != nil {
		t.Fatalf("create second Results Entry: %v", err)
	}
	resultsClient := resultsv1connect.NewResultsServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	initial, err := resultsClient.GetCompetitionResultsDraft(
		t.Context(),
		connect.NewRequest(&resultsv1.GetCompetitionResultsDraftRequest{
			EventId: 1, SessionId: competitionID,
		}),
	)
	if err != nil || initial.Msg.GetDraft().GetRevision() != 0 ||
		initial.Msg.GetDraft().GetDisposition() !=
			resultsv1.ResultsDisposition_RESULTS_DISPOSITION_PENDING {
		t.Fatalf("initial Results Draft = %+v, %v", initial, err)
	}
	saved, err := resultsClient.SaveCompetitionResultsDraft(
		t.Context(),
		connect.NewRequest(&resultsv1.SaveCompetitionResultsDraftRequest{
			EventId: 1, SessionId: competitionID, CommandId: "save-results-draft",
			ExpectedRevision: 0,
			Disposition:      resultsv1.ResultsDisposition_RESULTS_DISPOSITION_PUBLISH,
			Score: &resultsv1.ScorePolicy{
				Type:           resultsv1.ScoreType_SCORE_TYPE_NONE,
				Visibility:     resultsv1.ScoreVisibility_SCORE_VISIBILITY_PUBLIC,
				Requirement:    resultsv1.ScoreRequirement_SCORE_REQUIREMENT_OPTIONAL,
				Interpretation: resultsv1.ScoreInterpretation_SCORE_INTERPRETATION_INFORMATIONAL,
			},
			Standings: []*resultsv1.CompetitionResultStanding{
				{
					EntryId:   first.Msg.GetEntry().GetId(),
					Standing:  resultsv1.ResultStanding_RESULT_STANDING_PLACED,
					Placement: proto.Int64(1), DisplayOrder: 1,
				},
				{
					EntryId:      second.Msg.GetEntry().GetId(),
					Standing:     resultsv1.ResultStanding_RESULT_STANDING_UNPLACED,
					DisplayOrder: 2,
				},
			},
		}),
	)
	if err != nil || saved.Msg.GetDraft().GetRevision() != 1 ||
		saved.Msg.GetDraft().GetReady() {
		t.Fatalf("save Results Draft = %+v, %v", saved, err)
	}
	ready, err := resultsClient.MarkCompetitionResultsReady(
		t.Context(),
		connect.NewRequest(&resultsv1.MarkCompetitionResultsReadyRequest{
			EventId: 1, SessionId: competitionID, CommandId: "review-results-draft",
			ExpectedRevision: 1,
		}),
	)
	if err != nil || !ready.Msg.GetDraft().GetReady() ||
		ready.Msg.GetDraft().GetRevision() != 1 {
		t.Fatalf("mark Results Ready = %+v, %v", ready, err)
	}
	_, err = competitionClient.UpdateEntry(t.Context(), connect.NewRequest(
		&competitionv1.UpdateEntryRequest{
			EventId: 1, SessionId: competitionID, EntryId: first.Msg.GetEntry().GetId(),
			CommandId:        "update-reviewed-results-entry",
			ExpectedRevision: first.Msg.GetEntry().GetRevision(),
			Name:             "Renamed Result",
		},
	))
	if err != nil {
		t.Fatalf("update reviewed Results Entry: %v", err)
	}
	superseded, err := resultsClient.GetCompetitionResultsDraft(
		t.Context(),
		connect.NewRequest(&resultsv1.GetCompetitionResultsDraftRequest{
			EventId: 1, SessionId: competitionID,
		}),
	)
	if err != nil || superseded.Msg.GetDraft().GetRevision() != 2 ||
		superseded.Msg.GetDraft().GetReady() {
		t.Fatalf("superseded Results Draft = %+v, %v", superseded, err)
	}
	noPublic, err := resultsClient.SaveCompetitionResultsDraft(
		t.Context(),
		connect.NewRequest(&resultsv1.SaveCompetitionResultsDraftRequest{
			EventId: 1, SessionId: competitionID, CommandId: "withhold-public-results",
			ExpectedRevision:   2,
			Disposition:        resultsv1.ResultsDisposition_RESULTS_DISPOSITION_NO_PUBLIC_RESULTS,
			NoPublicCrewReason: "Judging could not be completed",
			PublicExplanation:  "No results published.",
			Score: &resultsv1.ScorePolicy{
				Type:           resultsv1.ScoreType_SCORE_TYPE_NONE,
				Visibility:     resultsv1.ScoreVisibility_SCORE_VISIBILITY_PUBLIC,
				Requirement:    resultsv1.ScoreRequirement_SCORE_REQUIREMENT_OPTIONAL,
				Interpretation: resultsv1.ScoreInterpretation_SCORE_INTERPRETATION_INFORMATIONAL,
			},
		}),
	)
	if err != nil || noPublic.Msg.GetDraft().GetRevision() != 3 ||
		noPublic.Msg.GetDraft().GetReady() ||
		noPublic.Msg.GetDraft().GetNoPublicCrewReason() == "" {
		t.Fatalf("save No Public Results revision = %+v, %v", noPublic, err)
	}
	_, staleReadyErr := resultsClient.MarkCompetitionResultsReady(
		t.Context(),
		connect.NewRequest(&resultsv1.MarkCompetitionResultsReadyRequest{
			EventId: 1, SessionId: competitionID, CommandId: "stale-results-review",
			ExpectedRevision: 1,
		}),
	)
	if connect.CodeOf(staleReadyErr) != connect.CodeAborted {
		t.Fatalf("stale Results Ready error = %v, want Aborted", staleReadyErr)
	}
	server.stop(t)
}

func TestCompetitionStartPreflightRequiresFinalPrimaryDelivery(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	competitionClient := competitionv1connect.NewCompetitionServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	created, err := competitionClient.CreateEntry(t.Context(), connect.NewRequest(
		&competitionv1.CreateEntryRequest{
			EventId: 1, SessionId: competitionID, CommandId: "create-preflight-entry",
			Name: "Ready Project",
		},
	))
	if err != nil {
		t.Fatalf("create preflight Entry: %v", err)
	}
	preflight, err := competitionClient.PreflightStart(t.Context(), connect.NewRequest(
		&competitionv1.PreflightStartRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil {
		t.Fatalf("preflight Competition Start: %v", err)
	}
	if preflight.Msg.GetRequireEntryReview() || !preflight.Msg.GetFileDeliveryRequired() ||
		len(preflight.Msg.GetBlockers()) != 1 ||
		preflight.Msg.GetBlockers()[0].GetCode() != "missing_file_delivery" ||
		preflight.Msg.GetBlockers()[0].GetEntryId() != created.Msg.GetEntry().GetId() {
		t.Fatalf("default Competition Preflight = %+v", preflight.Msg)
	}
	sessionClient := sessionv1connect.NewSessionControlServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	_, err = sessionClient.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: competitionID, CommandId: "start-unready-competition",
		ExpectedLiveStateRevision: proto.Int64(0),
	}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "missing_file_delivery") {
		t.Fatalf("unready Competition Start error = %v", err)
	}
	uploaded := decodeAttachmentVersion(t, requestMultipart(
		t.Context(), administrator, server.address, "/crew/events/1/attachments",
		map[string]string{
			"target_type": "Entry",
			"target_id":   strconv.FormatInt(created.Msg.GetEntry().GetId(), 10),
			"name":        "entry",
			"command_id":  "upload-preflight-entry",
		},
		"entry.zip", "application/zip", []byte("one complete entry"),
	))
	if !uploaded.Primary || uploaded.Final {
		t.Fatalf("sole Attachment Version readiness = %+v", uploaded)
	}
	ready, err := competitionClient.PreflightStart(t.Context(), connect.NewRequest(
		&competitionv1.PreflightStartRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || len(ready.Msg.GetBlockers()) != 0 ||
		len(ready.Msg.GetAttachments()) != 1 ||
		!ready.Msg.GetAttachments()[0].GetPrimary() ||
		!ready.Msg.GetAttachments()[0].GetFinal() ||
		ready.Msg.GetAttachments()[0].GetAttachmentVersionId() != int64(uploaded.ID) ||
		ready.Msg.GetAttachments()[0].GetRevision() <= int64(uploaded.ReadinessRevision) {
		t.Fatalf("ready Competition Preflight = %+v, %v", ready, err)
	}
	_, previewRevisionErr := competitionClient.SetEntryAttachmentReadiness(
		t.Context(),
		connect.NewRequest(&competitionv1.SetEntryAttachmentReadinessRequest{
			EventId: 1, SessionId: competitionID, EntryId: created.Msg.GetEntry().GetId(),
			AttachmentVersionId: int64(uploaded.ID), CommandId: "reject-preview-revision",
			ExpectedRevision: ready.Msg.GetAttachments()[0].GetRevision(),
			Final:            true,
			Primary:          true,
		}),
	)
	if connect.CodeOf(previewRevisionErr) != connect.CodeAborted {
		t.Fatalf("persisted Competition Preflight revision error = %v, want Aborted", previewRevisionErr)
	}
	if _, err = sessionClient.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: competitionID, CommandId: "start-ready-competition",
		ExpectedLiveStateRevision: proto.Int64(0),
	})); err != nil {
		t.Fatalf("start ready Competition: %v", err)
	}
	_, staleAfterStartErr := competitionClient.SetEntryAttachmentReadiness(
		t.Context(),
		connect.NewRequest(&competitionv1.SetEntryAttachmentReadinessRequest{
			EventId: 1, SessionId: competitionID, EntryId: created.Msg.GetEntry().GetId(),
			AttachmentVersionId: int64(uploaded.ID), CommandId: "reject-pre-start-revision",
			ExpectedRevision: int64(uploaded.ReadinessRevision),
			Final:            true,
			Primary:          true,
		}),
	)
	if connect.CodeOf(staleAfterStartErr) != connect.CodeAborted {
		t.Fatalf("unpersisted Start automation error = %v, want Aborted", staleAfterStartErr)
	}
	server.stop(t)
}

func TestEntryReviewFinalizesSoleUploadAndContentChangeInvalidatesIt(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	client := competitionv1connect.NewCompetitionServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	configured, err := client.ConfigureReadiness(t.Context(), connect.NewRequest(
		&competitionv1.ConfigureReadinessRequest{
			EventId: 1, SessionId: competitionID, CommandId: "require-entry-review",
			ExpectedReadinessRevision: 0, RequireEntryReview: true, FileDeliveryRequired: true,
		},
	))
	if err != nil || !configured.Msg.GetRequireEntryReview() ||
		!configured.Msg.GetFileDeliveryRequired() || configured.Msg.GetReadinessRevision() != 1 {
		t.Fatalf("configure Competition readiness = %+v, %v", configured, err)
	}
	created, err := client.CreateEntry(t.Context(), connect.NewRequest(
		&competitionv1.CreateEntryRequest{
			EventId: 1, SessionId: competitionID, CommandId: "create-reviewed-entry",
			Name: "Reviewed Project",
		},
	))
	if err != nil {
		t.Fatalf("create reviewed Entry: %v", err)
	}
	uploaded := decodeAttachmentVersion(t, requestMultipart(
		t.Context(), administrator, server.address, "/crew/events/1/attachments",
		map[string]string{
			"target_type": "Entry",
			"target_id":   strconv.FormatInt(created.Msg.GetEntry().GetId(), 10),
			"name":        "entry",
			"command_id":  "upload-reviewed-entry",
		},
		"entry.zip", "application/zip", []byte("review me"),
	))
	if !uploaded.Primary || uploaded.Final {
		t.Fatalf("review-gated sole upload = %+v", uploaded)
	}
	withoutReview, err := client.ConfigureReadiness(t.Context(), connect.NewRequest(
		&competitionv1.ConfigureReadinessRequest{
			EventId: 1, SessionId: competitionID, CommandId: "disable-entry-review",
			ExpectedReadinessRevision: 1, FileDeliveryRequired: true,
		},
	))
	if err != nil || withoutReview.Msg.GetReadinessRevision() != 2 {
		t.Fatalf("disable Entry Review = %+v, %v", withoutReview, err)
	}
	autoFinal, err := client.PreflightStart(t.Context(), connect.NewRequest(
		&competitionv1.PreflightStartRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || len(autoFinal.Msg.GetBlockers()) != 0 ||
		len(autoFinal.Msg.GetAttachments()) != 1 || !autoFinal.Msg.GetAttachments()[0].GetFinal() {
		t.Fatalf("review-disabled Preflight automation = %+v, %v", autoFinal, err)
	}
	withReview, err := client.ConfigureReadiness(t.Context(), connect.NewRequest(
		&competitionv1.ConfigureReadinessRequest{
			EventId: 1, SessionId: competitionID, CommandId: "restore-entry-review",
			ExpectedReadinessRevision: 2, RequireEntryReview: true, FileDeliveryRequired: true,
		},
	))
	if err != nil || withReview.Msg.GetReadinessRevision() != 3 {
		t.Fatalf("restore Entry Review = %+v, %v", withReview, err)
	}
	blocked, err := client.PreflightStart(t.Context(), connect.NewRequest(
		&competitionv1.PreflightStartRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || !findingCodesEqual(
		blocked.Msg.GetBlockers(), "unresolved_entry_review", "non_final_primary_attachment",
	) {
		t.Fatalf("review-gated Preflight = %+v, %v", blocked, err)
	}
	_, staleReviewErr := client.ReviewEntry(t.Context(), connect.NewRequest(
		&competitionv1.ReviewEntryRequest{
			EventId: 1, SessionId: competitionID, EntryId: created.Msg.GetEntry().GetId(),
			CommandId:        "stale-review-after-upload",
			ExpectedRevision: created.Msg.GetEntry().GetRevision(),
		},
	))
	if connect.CodeOf(staleReviewErr) != connect.CodeAborted {
		t.Fatalf("stale review after upload error = %v, want Aborted", staleReviewErr)
	}
	current, err := client.GetCompetition(t.Context(), connect.NewRequest(
		&competitionv1.GetCompetitionRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || len(current.Msg.GetEntries()) != 1 {
		t.Fatalf("load review-gated Entry: %+v, %v", current, err)
	}
	entry := current.Msg.GetEntries()[0]
	reviewed, err := client.ReviewEntry(t.Context(), connect.NewRequest(
		&competitionv1.ReviewEntryRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			CommandId: "review-entry", ExpectedRevision: entry.GetRevision(),
		},
	))
	if err != nil || !reviewed.Msg.GetEntry().GetReviewCurrent() {
		t.Fatalf("review Entry = %+v, %v", reviewed, err)
	}
	ready, err := client.PreflightStart(t.Context(), connect.NewRequest(
		&competitionv1.PreflightStartRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || len(ready.Msg.GetBlockers()) != 0 {
		t.Fatalf("reviewed Entry Preflight = %+v, %v", ready, err)
	}
	changed, err := client.UpdateEntry(t.Context(), connect.NewRequest(
		&competitionv1.UpdateEntryRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			CommandId: "change-reviewed-entry", ExpectedRevision: reviewed.Msg.GetEntry().GetRevision(),
			Name: "Reviewed Project Revised",
		},
	))
	if err != nil || changed.Msg.GetEntry().GetReviewCurrent() {
		t.Fatalf("change reviewed Entry = %+v, %v", changed, err)
	}
	invalidated, err := client.PreflightStart(t.Context(), connect.NewRequest(
		&competitionv1.PreflightStartRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || !findingCodesEqual(
		invalidated.Msg.GetBlockers(),
		"unresolved_entry_review", "non_final_primary_attachment",
	) {
		t.Fatalf("invalidated Entry review Preflight = %+v, %v", invalidated, err)
	}
	server.stop(t)
}

func TestCompetitionPreflightRequiresDispositionAndUnambiguousPrimary(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	client := competitionv1connect.NewCompetitionServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	created, err := client.CreateEntry(t.Context(), connect.NewRequest(
		&competitionv1.CreateEntryRequest{
			EventId: 1, SessionId: competitionID, CommandId: "create-primary-entry",
			Name: "Primary Project",
		},
	))
	if err != nil {
		t.Fatalf("create Primary Entry: %v", err)
	}
	entry := created.Msg.GetEntry()
	pending, err := client.ChangeEntryDisposition(t.Context(), connect.NewRequest(
		&competitionv1.ChangeEntryDispositionRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			CommandId: "make-primary-entry-pending", ExpectedRevision: entry.GetRevision(),
			Disposition: rundownv1.EntryDisposition_ENTRY_DISPOSITION_PENDING,
		},
	))
	if err != nil {
		t.Fatalf("make Entry Pending: %v", err)
	}
	blocked, err := client.PreflightStart(t.Context(), connect.NewRequest(
		&competitionv1.PreflightStartRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || !findingCodesEqual(blocked.Msg.GetBlockers(), "pending_entry") {
		t.Fatalf("Pending Entry Preflight = %+v, %v", blocked, err)
	}
	included, err := client.ChangeEntryDisposition(t.Context(), connect.NewRequest(
		&competitionv1.ChangeEntryDispositionRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			CommandId: "include-primary-entry", ExpectedRevision: pending.Msg.GetEntry().GetRevision(),
			Disposition: rundownv1.EntryDisposition_ENTRY_DISPOSITION_INCLUDED,
		},
	))
	if err != nil {
		t.Fatalf("include Primary Entry: %v", err)
	}
	first := decodeAttachmentVersion(t, requestMultipart(
		t.Context(), administrator, server.address, "/crew/events/1/attachments",
		map[string]string{
			"target_type": "Entry",
			"target_id":   strconv.FormatInt(included.Msg.GetEntry().GetId(), 10),
			"name":        "entry",
			"command_id":  "upload-primary-v1",
		},
		"entry-v1.zip", "application/zip", []byte("first"),
	))
	second := decodeAttachmentVersion(t, requestMultipart(
		t.Context(), administrator, server.address, "/crew/events/1/attachments",
		map[string]string{
			"target_type": "Entry",
			"target_id":   strconv.FormatInt(included.Msg.GetEntry().GetId(), 10),
			"name":        "entry",
			"command_id":  "upload-primary-v2",
		},
		"entry-v2.zip", "application/zip", []byte("second"),
	))
	if !first.Primary || first.Final || second.Primary || second.Final {
		t.Fatalf("two uploaded versions = %+v then %+v", first, second)
	}
	cleared, err := client.SetEntryAttachmentReadiness(t.Context(), connect.NewRequest(
		&competitionv1.SetEntryAttachmentReadinessRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			AttachmentVersionId: int64(first.ID), CommandId: "clear-primary-v1",
			ExpectedRevision: int64(first.ReadinessRevision), Final: true, Primary: false,
		},
	))
	if err != nil {
		t.Fatalf("clear first Primary: %v", err)
	}
	autoSelected, err := client.PreflightStart(t.Context(), connect.NewRequest(
		&competitionv1.PreflightStartRequest{EventId: 1, SessionId: competitionID},
	))
	firstCandidate := attachmentCandidate(autoSelected.Msg.GetAttachments(), int64(first.ID))
	if err != nil || len(autoSelected.Msg.GetBlockers()) != 0 ||
		firstCandidate == nil || !firstCandidate.GetPrimary() || !firstCandidate.GetFinal() {
		t.Fatalf("sole Final candidate Preflight = %+v, %v", autoSelected, err)
	}
	firstReadiness, err := client.SetEntryAttachmentReadiness(t.Context(), connect.NewRequest(
		&competitionv1.SetEntryAttachmentReadinessRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			AttachmentVersionId: int64(first.ID), CommandId: "clear-primary-v1-again",
			ExpectedRevision: cleared.Msg.GetAttachment().GetRevision(), Final: true, Primary: false,
		},
	))
	if err != nil {
		t.Fatalf("clear automatically selected Primary: %v", err)
	}
	secondFinal, err := client.SetEntryAttachmentReadiness(t.Context(), connect.NewRequest(
		&competitionv1.SetEntryAttachmentReadinessRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			AttachmentVersionId: int64(second.ID), CommandId: "finalize-v2",
			ExpectedRevision: int64(second.ReadinessRevision), Final: true, Primary: false,
		},
	))
	if err != nil || !secondFinal.Msg.GetAttachment().GetFinal() {
		t.Fatalf("finalize second version = %+v, %v", secondFinal, err)
	}
	ambiguous, err := client.PreflightStart(t.Context(), connect.NewRequest(
		&competitionv1.PreflightStartRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || !findingCodesEqual(ambiguous.Msg.GetBlockers(), "ambiguous_primary_attachment") {
		t.Fatalf("ambiguous Primary Preflight = %+v, %v", ambiguous, err)
	}
	primary, err := client.SetEntryAttachmentReadiness(t.Context(), connect.NewRequest(
		&competitionv1.SetEntryAttachmentReadinessRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			AttachmentVersionId: int64(second.ID), CommandId: "select-v2-primary",
			ExpectedRevision: secondFinal.Msg.GetAttachment().GetRevision(), Final: true, Primary: true,
		},
	))
	if err != nil || !primary.Msg.GetAttachment().GetPrimary() {
		t.Fatalf("select second Primary = %+v, %v", primary, err)
	}
	ready, err := client.PreflightStart(t.Context(), connect.NewRequest(
		&competitionv1.PreflightStartRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || len(ready.Msg.GetBlockers()) != 0 {
		t.Fatalf("multiple Final Versions with one Primary = %+v, %v", ready, err)
	}
	nonFinal, err := client.SetEntryAttachmentReadiness(t.Context(), connect.NewRequest(
		&competitionv1.SetEntryAttachmentReadinessRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			AttachmentVersionId: int64(second.ID), CommandId: "make-primary-non-final",
			ExpectedRevision: primary.Msg.GetAttachment().GetRevision(), Final: false, Primary: true,
		},
	))
	if err != nil || nonFinal.Msg.GetAttachment().GetFinal() {
		t.Fatalf("make Primary non-Final = %+v, %v", nonFinal, err)
	}
	nonFinalBlocked, err := client.PreflightStart(t.Context(), connect.NewRequest(
		&competitionv1.PreflightStartRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || !findingCodesEqual(
		nonFinalBlocked.Msg.GetBlockers(), "non_final_primary_attachment",
	) {
		t.Fatalf("non-Final Primary Preflight = %+v, %v", nonFinalBlocked, err)
	}
	if firstReadiness.Msg.GetAttachment().GetPrimary() {
		t.Fatal("first Attachment remained Primary after explicit clear")
	}
	server.stop(t)
}

func TestCompetitionEntryOrderPreviewIsDeterministicByDefault(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	client := competitionv1connect.NewCompetitionServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	var entryIDs []int64
	for index, name := range []string{"Alpha", "Bravo", "Charlie"} {
		created, err := client.CreateEntry(t.Context(), connect.NewRequest(
			&competitionv1.CreateEntryRequest{
				EventId: 1, SessionId: competitionID,
				CommandId: fmt.Sprintf("create-ordered-entry-%d", index), Name: name,
			},
		))
		if err != nil {
			t.Fatalf("create ordered Entry %q: %v", name, err)
		}
		entryIDs = append(entryIDs, created.Msg.GetEntry().GetId())
	}
	first, err := client.PreviewEntryOrder(t.Context(), connect.NewRequest(
		&competitionv1.PreviewEntryOrderRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil {
		t.Fatalf("preview default Entry Order: %v", err)
	}
	second, err := client.PreviewEntryOrder(t.Context(), connect.NewRequest(
		&competitionv1.PreviewEntryOrderRequest{EventId: 1, SessionId: competitionID},
	))
	order := first.Msg.GetEntryOrder()
	if err != nil ||
		order.GetPolicy() != competitionv1.EntryOrderPolicy_ENTRY_ORDER_POLICY_DETERMINISTIC_SHUFFLE ||
		order.GetSeed() <= 0 || order.GetRevision() != 0 || order.GetLocked() ||
		!sameInt64Set(order.GetEntryIds(), entryIDs) ||
		!slices.Equal(order.GetEntryIds(), second.Msg.GetEntryOrder().GetEntryIds()) ||
		first.Msg.GetFingerprint() == "" ||
		first.Msg.GetFingerprint() != second.Msg.GetFingerprint() {
		t.Fatalf("default deterministic Entry Order = %+v then %+v, %v", first, second, err)
	}
	configured, err := client.GetCompetition(t.Context(), connect.NewRequest(
		&competitionv1.GetCompetitionRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || configured.Msg.GetEntryOrder().GetSeed() != order.GetSeed() ||
		configured.Msg.GetEntryOrder().GetPolicy() != order.GetPolicy() {
		t.Fatalf("stored default Entry Order = %+v, %v", configured, err)
	}
	server.stop(t)
}

func TestCrewConfiguresCompetitionEntryOrder(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	client := competitionv1connect.NewCompetitionServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	var entries []*competitionv1.Entry
	for index, name := range []string{"Alpha", "Bravo", "Charlie"} {
		created, err := client.CreateEntry(t.Context(), connect.NewRequest(
			&competitionv1.CreateEntryRequest{
				EventId: 1, SessionId: competitionID,
				CommandId: fmt.Sprintf("create-configured-order-entry-%d", index), Name: name,
			},
		))
		if err != nil {
			t.Fatalf("create configured-order Entry %q: %v", name, err)
		}
		entries = append(entries, created.Msg.GetEntry())
	}
	submissionIDs := []int64{entries[0].GetId(), entries[1].GetId(), entries[2].GetId()}
	submission, err := client.ConfigureEntryOrder(t.Context(), connect.NewRequest(
		&competitionv1.ConfigureEntryOrderRequest{
			EventId: 1, SessionId: competitionID, CommandId: "use-submission-order",
			ExpectedRevision: 0,
			Policy:           competitionv1.EntryOrderPolicy_ENTRY_ORDER_POLICY_SUBMISSION_ORDER,
		},
	))
	if err != nil || submission.Msg.GetEntryOrder().GetRevision() != 1 ||
		!slices.Equal(submission.Msg.GetEntryOrder().GetEntryIds(), submissionIDs) {
		t.Fatalf("configure Submission Order = %+v, %v", submission, err)
	}
	manualIDs := []int64{entries[2].GetId(), entries[0].GetId(), entries[1].GetId()}
	manual, err := client.ConfigureEntryOrder(t.Context(), connect.NewRequest(
		&competitionv1.ConfigureEntryOrderRequest{
			EventId: 1, SessionId: competitionID, CommandId: "use-manual-order",
			ExpectedRevision: 1,
			Policy:           competitionv1.EntryOrderPolicy_ENTRY_ORDER_POLICY_MANUAL_ORDER,
			ManualEntryIds:   manualIDs,
		},
	))
	if err != nil || manual.Msg.GetEntryOrder().GetRevision() != 2 ||
		!slices.Equal(manual.Msg.GetEntryOrder().GetEntryIds(), manualIDs) {
		t.Fatalf("configure Manual Order = %+v, %v", manual, err)
	}
	shuffled, err := client.ConfigureEntryOrder(t.Context(), connect.NewRequest(
		&competitionv1.ConfigureEntryOrderRequest{
			EventId: 1, SessionId: competitionID, CommandId: "use-seeded-order",
			ExpectedRevision: 2,
			Policy:           competitionv1.EntryOrderPolicy_ENTRY_ORDER_POLICY_DETERMINISTIC_SHUFFLE,
			Seed:             4242,
		},
	))
	if err != nil || shuffled.Msg.GetEntryOrder().GetRevision() != 3 ||
		shuffled.Msg.GetEntryOrder().GetSeed() != 4242 ||
		!sameInt64Set(shuffled.Msg.GetEntryOrder().GetEntryIds(), submissionIDs) {
		t.Fatalf("configure Deterministic Shuffle = %+v, %v", shuffled, err)
	}
	_, err = client.ConfigureEntryOrder(t.Context(), connect.NewRequest(
		&competitionv1.ConfigureEntryOrderRequest{
			EventId: 1, SessionId: competitionID, CommandId: "restore-manual-order",
			ExpectedRevision: 3,
			Policy:           competitionv1.EntryOrderPolicy_ENTRY_ORDER_POLICY_MANUAL_ORDER,
			ManualEntryIds:   manualIDs,
		},
	))
	if err != nil {
		t.Fatalf("restore Manual Order: %v", err)
	}
	preview, err := client.PreviewEntryOrder(t.Context(), connect.NewRequest(
		&competitionv1.PreviewEntryOrderRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || !slices.Equal(preview.Msg.GetEntryOrder().GetEntryIds(), manualIDs) {
		t.Fatalf("preview Manual Order = %+v, %v", preview, err)
	}
	if _, err = client.ConfigureReadiness(t.Context(), connect.NewRequest(
		&competitionv1.ConfigureReadinessRequest{
			EventId: 1, SessionId: competitionID, CommandId: "disable-order-test-delivery",
			ExpectedReadinessRevision: 0,
		},
	)); err != nil {
		t.Fatalf("disable file delivery for Entry Order test: %v", err)
	}
	sessionClient := sessionv1connect.NewSessionControlServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	if _, err = sessionClient.StartSession(t.Context(), connect.NewRequest(
		&sessionv1.StartSessionRequest{
			EventId: 1, SessionId: competitionID, CommandId: "start-ordered-competition",
			ExpectedLiveStateRevision: proto.Int64(0),
		},
	)); err != nil {
		t.Fatalf("start ordered Competition: %v", err)
	}
	_, liveConfigureErr := client.ConfigureEntryOrder(t.Context(), connect.NewRequest(
		&competitionv1.ConfigureEntryOrderRequest{
			EventId: 1, SessionId: competitionID, CommandId: "rewrite-live-order",
			ExpectedRevision: 4,
			Policy:           competitionv1.EntryOrderPolicy_ENTRY_ORDER_POLICY_SUBMISSION_ORDER,
		},
	))
	if connect.CodeOf(liveConfigureErr) != connect.CodeFailedPrecondition {
		t.Fatalf("live Entry Order configuration error = %v, want FailedPrecondition", liveConfigureErr)
	}
	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	restarted := startBeamers(t, bin, dataDir)
	client = competitionv1connect.NewCompetitionServiceClient(
		administrator, "http://"+restarted.address, connect.WithProtoJSON(),
	)
	restored, err := client.PreviewEntryOrder(t.Context(), connect.NewRequest(
		&competitionv1.PreviewEntryOrderRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || restored.Msg.GetEntryOrder().GetLocked() ||
		!slices.Equal(restored.Msg.GetEntryOrder().GetEntryIds(), manualIDs) {
		t.Fatalf("restored Entry Order = %+v, %v", restored, err)
	}
	audit := get(t, administrator, restarted.address, "/admin/audit")
	auditBody, readErr := io.ReadAll(audit.Body)
	closeErr := audit.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Entry Order Audit history: %v", err)
	}
	if !bytes.Contains(auditBody, []byte("ConfigureCompetitionEntryOrder")) {
		t.Fatalf("Entry Order commands missing from Audit history: %s", auditBody)
	}
	restarted.stop(t)
}

func TestControlOwnerTakesCompetitionEntryToDurableProgramOutput(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	displayClient := enrollAndAssignDisplay(
		t, administrator, server, "Competition Display", "competition-output",
	)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	competitionClient := competitionv1connect.NewCompetitionServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	entry, err := competitionClient.CreateEntry(t.Context(), connect.NewRequest(
		&competitionv1.CreateEntryRequest{
			EventId: 1, SessionId: competitionID,
			CommandId: "create-program-entry", Name: "Aurora",
		},
	))
	if err != nil {
		t.Fatalf("create Program Entry: %v", err)
	}
	if _, err = competitionClient.ConfigureReadiness(t.Context(), connect.NewRequest(
		&competitionv1.ConfigureReadinessRequest{
			EventId: 1, SessionId: competitionID, CommandId: "disable-program-file-delivery",
			ExpectedReadinessRevision: 0,
		},
	)); err != nil {
		t.Fatalf("disable Program Competition file delivery: %v", err)
	}
	sessionClient := sessionv1connect.NewSessionControlServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	if _, err = sessionClient.StartSession(t.Context(), connect.NewRequest(
		&sessionv1.StartSessionRequest{
			EventId: 1, SessionId: competitionID, CommandId: "start-program-competition",
			ExpectedLiveStateRevision: proto.Int64(0),
		},
	)); err != nil {
		t.Fatalf("start Program Competition: %v", err)
	}
	order, err := competitionClient.PreviewEntryOrder(t.Context(), connect.NewRequest(
		&competitionv1.PreviewEntryOrderRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil {
		t.Fatalf("preview Program Entry Order: %v", err)
	}
	programClient := programv1connect.NewProgramControlServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	claimed, err := programClient.ChangeControl(t.Context(), connect.NewRequest(
		&programv1.ChangeControlRequest{
			EventId: 1, SessionId: competitionID,
			Action:    programv1.ControlAction_CONTROL_ACTION_CLAIM,
			CommandId: "claim-program-control",
		},
	))
	if err != nil || claimed.Msg.GetChannel().GetControlOwner().GetAccountId() != 1 ||
		claimed.Msg.GetChannel().GetPrevious() != nil ||
		claimed.Msg.GetChannel().GetCurrent() != nil ||
		claimed.Msg.GetChannel().GetNext().GetKind() !=
			programv1.ProgramItemKind_PROGRAM_ITEM_KIND_UPCOMING ||
		claimed.Msg.GetChannel().GetPreview().GetKind() !=
			programv1.ProgramItemKind_PROGRAM_ITEM_KIND_UPCOMING ||
		claimed.Msg.GetChannel().GetProgramOutput().GetKind() !=
			programv1.ProgramItemKind_PROGRAM_ITEM_KIND_STANDBY {
		t.Fatalf("claim Program Channel = %+v, %v", claimed, err)
	}
	controlView := get(
		t, administrator, server.address,
		fmt.Sprintf("/crew/program/%d?event_id=1", competitionID),
	)
	controlViewBody, readErr := io.ReadAll(controlView.Body)
	closeErr := controlView.Body.Close()
	if err = errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Program control View: %v", err)
	}
	for _, want := range []string{
		"Previous", "Current", "Next", "Preview", "Program Output",
		"Consuming Displays", "Take Preview", "Defer current Entry",
		"Back to Program Output and Overrides",
	} {
		if !bytes.Contains(controlViewBody, []byte(want)) {
			t.Fatalf("Program control View missing %q: %s", want, controlViewBody)
		}
	}
	openAndCloseProgramStream(t, administrator, server.address, competitionID)
	var presence *programv1.ProgramChannel
	for range 50 {
		current, currentErr := programClient.GetProgramChannel(t.Context(), connect.NewRequest(
			&programv1.GetProgramChannelRequest{EventId: 1, SessionId: competitionID},
		))
		if currentErr != nil {
			t.Fatalf("read disconnected Program owner: %v", currentErr)
		}
		presence = current.Msg.GetChannel()
		if !presence.GetControlOwner().GetConnected() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if presence.GetControlOwner().GetConnected() {
		t.Fatalf("closed control stream retained connected owner: %+v", presence)
	}
	entryItem := &programv1.ProgramItem{
		Kind:    programv1.ProgramItemKind_PROGRAM_ITEM_KIND_ENTRY,
		EntryId: entry.Msg.GetEntry().GetId(),
	}
	if len(claimed.Msg.GetChannel().GetItems()) != 5 {
		t.Fatalf("Competition Program Items = %+v", claimed.Msg.GetChannel().GetItems())
	}
	controlRevision := presence.GetControlStateRevision()
	for index, item := range claimed.Msg.GetChannel().GetItems() {
		previewed, previewErr := programClient.SelectPreview(t.Context(), connect.NewRequest(
			&programv1.SelectPreviewRequest{
				EventId: 1, SessionId: competitionID, Item: item,
				CommandId:                    fmt.Sprintf("preview-program-item-%d", index),
				ExpectedControlStateRevision: controlRevision,
			},
		))
		if previewErr != nil ||
			previewed.Msg.GetChannel().GetPreview().GetKind() != item.GetKind() ||
			previewed.Msg.GetChannel().GetProgramOutput().GetKind() !=
				programv1.ProgramItemKind_PROGRAM_ITEM_KIND_STANDBY {
			t.Fatalf("select %s Preview = %+v, %v", item.GetKind(), previewed, previewErr)
		}
		controlRevision = previewed.Msg.GetChannel().GetControlStateRevision()
	}
	selected, err := programClient.SelectPreview(t.Context(), connect.NewRequest(
		&programv1.SelectPreviewRequest{
			EventId: 1, SessionId: competitionID, Item: entryItem,
			CommandId:                    "select-entry-program-preview",
			ExpectedControlStateRevision: controlRevision,
		},
	))
	if err != nil ||
		selected.Msg.GetChannel().GetPreview().GetEntryId() != entryItem.GetEntryId() ||
		selected.Msg.GetChannel().GetProgramOutput().GetKind() !=
			programv1.ProgramItemKind_PROGRAM_ITEM_KIND_STANDBY {
		t.Fatalf("select Program Preview = %+v, %v", selected, err)
	}
	offline, err := programClient.GetProgramChannel(t.Context(), connect.NewRequest(
		&programv1.GetProgramChannelRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || len(offline.Msg.GetChannel().GetConsumingDisplays()) != 1 ||
		offline.Msg.GetChannel().GetConsumingDisplays()[0].GetDeliveryState() != "offline" {
		t.Fatalf("offline consuming Display = %+v, %v", offline, err)
	}
	acknowledgeDisplaySnapshot(
		t, displayClient, server.address, readDisplaySnapshot(t, displayClient, server.address),
	)
	applied, err := programClient.GetProgramChannel(t.Context(), connect.NewRequest(
		&programv1.GetProgramChannelRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil ||
		applied.Msg.GetChannel().GetConsumingDisplays()[0].GetDeliveryState() != "applied" {
		t.Fatalf("applied consuming Display = %+v, %v", applied, err)
	}
	operator := provisionOperator(t, administrator, server)
	observer := provisionObserver(t, administrator, server)
	operatorProgram := programv1connect.NewProgramControlServiceClient(
		operator, "http://"+server.address, connect.WithProtoJSON(),
	)
	observerProgram := programv1connect.NewProgramControlServiceClient(
		observer, "http://"+server.address, connect.WithProtoJSON(),
	)
	unauthorizedCommands := []func() error{
		func() error {
			_, commandErr := observerProgram.ChangeControl(t.Context(), connect.NewRequest(
				&programv1.ChangeControlRequest{
					EventId: 1, SessionId: competitionID,
					Action:                       programv1.ControlAction_CONTROL_ACTION_CLAIM,
					CommandId:                    "reject-observer-program-control",
					ExpectedControlStateRevision: selected.Msg.GetChannel().GetControlStateRevision(),
				},
			))
			return commandErr
		},
		func() error {
			_, commandErr := observerProgram.SelectPreview(t.Context(), connect.NewRequest(
				&programv1.SelectPreviewRequest{
					EventId: 1, SessionId: competitionID, Item: entryItem,
					CommandId:                    "reject-observer-program-preview",
					ExpectedControlStateRevision: selected.Msg.GetChannel().GetControlStateRevision(),
				},
			))
			return commandErr
		},
		func() error {
			_, commandErr := observerProgram.Take(t.Context(), connect.NewRequest(
				&programv1.TakeRequest{
					EventId: 1, SessionId: competitionID,
					CommandId:                 "reject-observer-program-take",
					ExpectedLiveStateRevision: 0, Preview: entryItem,
					ExpectedControlStateRevision: selected.Msg.GetChannel().GetControlStateRevision(),
				},
			))
			return commandErr
		},
	}
	for _, unauthorizedCommand := range unauthorizedCommands {
		if commandErr := unauthorizedCommand(); connect.CodeOf(commandErr) != connect.CodePermissionDenied {
			t.Fatalf("unauthorized Program command = %v, want PermissionDenied", commandErr)
		}
	}
	_, ownedErr := operatorProgram.ChangeControl(t.Context(), connect.NewRequest(
		&programv1.ChangeControlRequest{
			EventId: 1, SessionId: competitionID,
			Action:                       programv1.ControlAction_CONTROL_ACTION_CLAIM,
			CommandId:                    "reject-second-program-owner",
			ExpectedControlStateRevision: selected.Msg.GetChannel().GetControlStateRevision(),
		},
	))
	if connect.CodeOf(ownedErr) != connect.CodeFailedPrecondition {
		t.Fatalf("second Program owner claim = %v", ownedErr)
	}
	requested, err := operatorProgram.ChangeControl(t.Context(), connect.NewRequest(
		&programv1.ChangeControlRequest{
			EventId: 1, SessionId: competitionID,
			Action:                       programv1.ControlAction_CONTROL_ACTION_REQUEST_HANDOVER,
			CommandId:                    "request-program-handover",
			ExpectedControlStateRevision: selected.Msg.GetChannel().GetControlStateRevision(),
		},
	))
	if err != nil {
		t.Fatalf("request Program handover: %v", err)
	}
	openAndCloseProgramStream(t, operator, server.address, competitionID)
	requesterPresence := requested.Msg.GetChannel()
	for range 50 {
		current, currentErr := programClient.GetProgramChannel(t.Context(), connect.NewRequest(
			&programv1.GetProgramChannelRequest{EventId: 1, SessionId: competitionID},
		))
		if currentErr != nil {
			t.Fatalf("read disconnected Program requester: %v", currentErr)
		}
		requesterPresence = current.Msg.GetChannel()
		if !requesterPresence.GetHandoverRequester().GetConnected() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if requesterPresence.GetHandoverRequester().GetConnected() {
		t.Fatalf("closed control stream retained connected requester: %+v", requesterPresence)
	}
	_, staleHandoverErr := programClient.ChangeControl(t.Context(), connect.NewRequest(
		&programv1.ChangeControlRequest{
			EventId: 1, SessionId: competitionID,
			Action:                       programv1.ControlAction_CONTROL_ACTION_HANDOVER,
			CommandId:                    "reject-stale-program-handover",
			ExpectedControlStateRevision: selected.Msg.GetChannel().GetControlStateRevision(),
		},
	))
	if connect.CodeOf(staleHandoverErr) != connect.CodeAborted {
		t.Fatalf("stale Program handover = %v, want Aborted", staleHandoverErr)
	}
	handed, err := programClient.ChangeControl(t.Context(), connect.NewRequest(
		&programv1.ChangeControlRequest{
			EventId: 1, SessionId: competitionID,
			Action:                       programv1.ControlAction_CONTROL_ACTION_HANDOVER,
			CommandId:                    "hand-over-program-control",
			ExpectedControlStateRevision: requesterPresence.GetControlStateRevision(),
		},
	))
	if err != nil || handed.Msg.GetChannel().GetControlOwner().GetAccountId() != 2 ||
		handed.Msg.GetChannel().GetControlOwner().GetConnected() {
		t.Fatalf("hand over Program Channel = %+v, %v", handed, err)
	}
	takeRequest := &programv1.TakeRequest{
		EventId: 1, SessionId: competitionID, CommandId: "take-aurora-program-slide",
		ExpectedLiveStateRevision: 0, Preview: entryItem,
		ExpectedEntryOrderRevision:   order.Msg.GetEntryOrder().GetRevision(),
		EntryOrderFingerprint:        order.Msg.GetFingerprint(),
		ExpectedControlStateRevision: handed.Msg.GetChannel().GetControlStateRevision(),
	}
	staleOrderRequest := &programv1.TakeRequest{
		EventId: 1, SessionId: competitionID,
		CommandId:                    "reject-stale-entry-order-program-take",
		ExpectedLiveStateRevision:    takeRequest.GetExpectedLiveStateRevision(),
		Preview:                      entryItem,
		ExpectedEntryOrderRevision:   takeRequest.GetExpectedEntryOrderRevision() + 1,
		EntryOrderFingerprint:        takeRequest.GetEntryOrderFingerprint(),
		ExpectedControlStateRevision: takeRequest.GetExpectedControlStateRevision(),
	}
	_, staleOrderErr := operatorProgram.Take(t.Context(), connect.NewRequest(staleOrderRequest))
	if connect.CodeOf(staleOrderErr) != connect.CodeAborted {
		t.Fatalf("stale Entry Order Program Take = %v, want Aborted", staleOrderErr)
	}
	taken, err := operatorProgram.Take(t.Context(), connect.NewRequest(takeRequest))
	if err != nil || taken.Msg.GetChannel().GetLiveStateRevision() != 1 ||
		taken.Msg.GetChannel().GetProgramOutput().GetEntryId() != entryItem.GetEntryId() ||
		taken.Msg.GetChannel().GetConsumingDisplays()[0].GetDeliveryState() != "lagging" {
		t.Fatalf("Take Program Output = %+v, %v", taken, err)
	}
	retried, err := operatorProgram.Take(t.Context(), connect.NewRequest(takeRequest))
	if err != nil || retried.Msg.GetChannel().GetLiveStateRevision() != 1 {
		t.Fatalf("retry Take Program Output = %+v, %v", retried, err)
	}
	replayedPreview, err := programClient.SelectPreview(t.Context(), connect.NewRequest(
		&programv1.SelectPreviewRequest{
			EventId: 1, SessionId: competitionID, Item: entryItem,
			CommandId:                    "select-entry-program-preview",
			ExpectedControlStateRevision: controlRevision,
		},
	))
	if err != nil ||
		replayedPreview.Msg.GetChannel().GetControlStateRevision() !=
			selected.Msg.GetChannel().GetControlStateRevision() ||
		replayedPreview.Msg.GetChannel().GetPreview().GetEntryId() != entryItem.GetEntryId() {
		t.Fatalf("replay original Program Preview outcome = %+v, %v", replayedPreview, err)
	}
	_, staleTakeErr := operatorProgram.Take(t.Context(), connect.NewRequest(
		&programv1.TakeRequest{
			EventId: 1, SessionId: competitionID, CommandId: "reject-stale-program-take",
			ExpectedLiveStateRevision: 0, Preview: claimed.Msg.GetChannel().GetNext(),
			ExpectedControlStateRevision: taken.Msg.GetChannel().GetControlStateRevision(),
		},
	))
	if connect.CodeOf(staleTakeErr) != connect.CodeAborted {
		t.Fatalf("stale Program Take = %v, want Aborted", staleTakeErr)
	}
	displaySnapshot := readDisplaySnapshot(t, displayClient, server.address)
	if displaySnapshot.ProgramOutput.Title != "Aurora" ||
		displaySnapshot.ProgramOutputRevision != "1" {
		t.Fatalf("Display Program Output = %+v", displaySnapshot)
	}
	acknowledgeDisplaySnapshot(t, displayClient, server.address, displaySnapshot)
	appliedOutput, err := operatorProgram.GetProgramChannel(t.Context(), connect.NewRequest(
		&programv1.GetProgramChannelRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil ||
		appliedOutput.Msg.GetChannel().GetConsumingDisplays()[0].GetDeliveryState() != "applied" {
		t.Fatalf("applied Program Output Display = %+v, %v", appliedOutput, err)
	}
	disconnected, err := operatorProgram.ChangeControl(t.Context(), connect.NewRequest(
		&programv1.ChangeControlRequest{
			EventId: 1, SessionId: competitionID,
			Action:                       programv1.ControlAction_CONTROL_ACTION_DISCONNECT,
			CommandId:                    "disconnect-program-owner",
			ExpectedControlStateRevision: taken.Msg.GetChannel().GetControlStateRevision(),
		},
	))
	if err != nil || disconnected.Msg.GetChannel().GetControlOwner().GetConnected() {
		t.Fatalf("disconnect Program owner = %+v, %v", disconnected, err)
	}
	replayedHandover, err := programClient.ChangeControl(t.Context(), connect.NewRequest(
		&programv1.ChangeControlRequest{
			EventId: 1, SessionId: competitionID,
			Action:                       programv1.ControlAction_CONTROL_ACTION_HANDOVER,
			CommandId:                    "hand-over-program-control",
			ExpectedControlStateRevision: requesterPresence.GetControlStateRevision(),
		},
	))
	if err != nil ||
		replayedHandover.Msg.GetChannel().GetControlStateRevision() !=
			handed.Msg.GetChannel().GetControlStateRevision() ||
		replayedHandover.Msg.GetChannel().GetControlOwner().GetAccountId() != 2 {
		t.Fatalf("replay original Program handover outcome = %+v, %v", replayedHandover, err)
	}
	replayedTake, err := operatorProgram.Take(t.Context(), connect.NewRequest(takeRequest))
	if err != nil ||
		replayedTake.Msg.GetChannel().GetControlStateRevision() !=
			taken.Msg.GetChannel().GetControlStateRevision() ||
		replayedTake.Msg.GetChannel().GetProgramOutput().GetEntryId() != entryItem.GetEntryId() {
		t.Fatalf("replay original Program Take outcome = %+v, %v", replayedTake, err)
	}
	_, unconfirmedErr := programClient.ChangeControl(t.Context(), connect.NewRequest(
		&programv1.ChangeControlRequest{
			EventId: 1, SessionId: competitionID,
			Action:                       programv1.ControlAction_CONTROL_ACTION_TAKEOVER,
			CommandId:                    "reject-unconfirmed-program-takeover",
			ExpectedControlStateRevision: disconnected.Msg.GetChannel().GetControlStateRevision(),
		},
	))
	if connect.CodeOf(unconfirmedErr) != connect.CodeFailedPrecondition {
		t.Fatalf("unconfirmed Program takeover = %v", unconfirmedErr)
	}
	if _, err = programClient.ChangeControl(t.Context(), connect.NewRequest(
		&programv1.ChangeControlRequest{
			EventId: 1, SessionId: competitionID,
			Action: programv1.ControlAction_CONTROL_ACTION_TAKEOVER, Confirmed: true,
			CommandId:                    "confirm-program-takeover",
			ExpectedControlStateRevision: disconnected.Msg.GetChannel().GetControlStateRevision(),
		},
	)); err != nil {
		t.Fatalf("confirmed Program takeover: %v", err)
	}
	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	restarted := startBeamers(t, bin, dataDir)
	programClient = programv1connect.NewProgramControlServiceClient(
		administrator, "http://"+restarted.address, connect.WithProtoJSON(),
	)
	restored, err := programClient.GetProgramChannel(t.Context(), connect.NewRequest(
		&programv1.GetProgramChannelRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || restored.Msg.GetChannel().GetControlOwner() != nil ||
		restored.Msg.GetChannel().GetProgramOutput().GetEntryId() != entryItem.GetEntryId() ||
		restored.Msg.GetChannel().GetPreview().GetKind() !=
			programv1.ProgramItemKind_PROGRAM_ITEM_KIND_UPCOMING {
		t.Fatalf("restored Program Channel = %+v, %v", restored, err)
	}
	restoredDisplay := readDisplaySnapshot(t, displayClient, restarted.address)
	if restoredDisplay.ProgramOutput.Title != "Aurora" ||
		restoredDisplay.ProgramOutputRevision != "1" {
		t.Fatalf("restored Display Program Output = %+v", restoredDisplay)
	}
	audit := get(t, administrator, restarted.address, "/admin/audit")
	auditBody, readErr := io.ReadAll(audit.Body)
	closeErr = audit.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Program Output Audit: %v", err)
	}
	if bytes.Count(auditBody, []byte(`"action":"TakeProgramOutput"`)) != 4 {
		t.Fatalf("Program Output Audit entries = %s", auditBody)
	}
	if !bytes.Contains(auditBody, []byte("ChangeProgramControlTakeover")) ||
		!bytes.Contains(auditBody, []byte("program_takeover_confirmation_required")) ||
		!bytes.Contains(auditBody, []byte("program_revision_conflict")) ||
		!bytes.Contains(auditBody, []byte("program_control_revision_conflict")) ||
		!bytes.Contains(auditBody, []byte("competition_entry_order_revision_conflict")) ||
		bytes.Count(auditBody, []byte("program_operator_required")) != 3 {
		t.Fatalf("Program takeover Audit evidence missing: %s", auditBody)
	}
	restarted.stop(t)
}

func TestControlOwnerDefersCompetitionEntry(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	competitionClient := competitionv1connect.NewCompetitionServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	entries := make([]*competitionv1.Entry, 0, 2)
	for index, name := range []string{"Aurora", "Beacon"} {
		created, err := competitionClient.CreateEntry(t.Context(), connect.NewRequest(
			&competitionv1.CreateEntryRequest{
				EventId: 1, SessionId: competitionID,
				CommandId: fmt.Sprintf("create-defer-entry-%d", index), Name: name,
			},
		))
		if err != nil {
			t.Fatalf("create defer Entry: %v", err)
		}
		entries = append(entries, created.Msg.GetEntry())
	}
	if _, err := competitionClient.ConfigureReadiness(t.Context(), connect.NewRequest(
		&competitionv1.ConfigureReadinessRequest{
			EventId: 1, SessionId: competitionID, CommandId: "disable-defer-file-delivery",
			ExpectedReadinessRevision: 0,
		},
	)); err != nil {
		t.Fatalf("disable defer Competition file delivery: %v", err)
	}
	sessionClient := sessionv1connect.NewSessionControlServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	if _, err := sessionClient.StartSession(t.Context(), connect.NewRequest(
		&sessionv1.StartSessionRequest{
			EventId: 1, SessionId: competitionID, CommandId: "start-defer-competition",
			ExpectedLiveStateRevision: proto.Int64(0),
		},
	)); err != nil {
		t.Fatalf("start defer Competition: %v", err)
	}
	order, err := competitionClient.PreviewEntryOrder(t.Context(), connect.NewRequest(
		&competitionv1.PreviewEntryOrderRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil {
		t.Fatalf("preview defer Entry Order: %v", err)
	}
	entryByID := map[int64]*competitionv1.Entry{}
	for _, entry := range entries {
		entryByID[entry.GetId()] = entry
	}
	orderedIDs := order.Msg.GetEntryOrder().GetEntryIds()
	deferredEntry := entryByID[orderedIDs[0]]
	programClient := programv1connect.NewProgramControlServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	claimed, err := programClient.ChangeControl(t.Context(), connect.NewRequest(
		&programv1.ChangeControlRequest{
			EventId: 1, SessionId: competitionID,
			Action:    programv1.ControlAction_CONTROL_ACTION_CLAIM,
			CommandId: "claim-defer-control",
		},
	))
	if err != nil {
		t.Fatalf("claim defer control: %v", err)
	}
	channel := claimed.Msg.GetChannel()
	for _, commandID := range []string{"take-defer-upcoming", "take-defer-starting"} {
		taken, takeErr := programClient.Take(t.Context(), connect.NewRequest(
			&programv1.TakeRequest{
				EventId: 1, SessionId: competitionID, CommandId: commandID,
				ExpectedLiveStateRevision:    channel.GetLiveStateRevision(),
				ExpectedControlStateRevision: channel.GetControlStateRevision(),
				Preview:                      channel.GetPreview(),
			},
		))
		if takeErr != nil {
			t.Fatalf("advance to first defer Entry: %v", takeErr)
		}
		channel = taken.Msg.GetChannel()
	}
	if channel.GetNext().GetEntryId() != orderedIDs[0] {
		t.Fatalf("canonical defer Next = %+v", channel.GetNext())
	}
	operator := provisionOperator(t, administrator, server)
	operatorProgram := programv1connect.NewProgramControlServiceClient(
		operator, "http://"+server.address, connect.WithProtoJSON(),
	)
	deferRequest := &programv1.DeferEntryRequest{
		EventId: 1, SessionId: competitionID, EntryId: deferredEntry.GetId(),
		CommandId: "defer-first-entry", ExpectedEntryRevision: deferredEntry.GetRevision(),
		ExpectedProgramRevision:      channel.GetLiveStateRevision(),
		ExpectedControlStateRevision: channel.GetControlStateRevision(),
	}
	if _, err = operatorProgram.DeferEntry(
		t.Context(), connect.NewRequest(deferRequest),
	); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("non-owner Defer = %v, want PermissionDenied", err)
	}
	deferRequest.CommandId = "defer-first-entry-as-owner"
	deferred, err := programClient.DeferEntry(t.Context(), connect.NewRequest(deferRequest))
	if err != nil {
		t.Fatalf("owner Defer Entry: %v", err)
	}
	channel = deferred.Msg.GetChannel()
	if channel.GetCurrent().GetEntryId() != orderedIDs[0] ||
		channel.GetNext().GetEntryId() != orderedIDs[1] ||
		channel.GetPreview().GetEntryId() != orderedIDs[1] ||
		channel.GetControlStateRevision() != deferRequest.GetExpectedControlStateRevision()+1 {
		t.Fatalf("deferred Program Channel = %+v", channel)
	}
	var retries []*programv1.ProgramItem
	for _, item := range channel.GetItems() {
		if item.GetRetry() {
			retries = append(retries, item)
		}
	}
	if len(retries) != 1 || retries[0].GetEntryId() != orderedIDs[0] {
		t.Fatalf("defer retry queue = %+v", retries)
	}
	server.stop(t)
}

func sameInt64Set(left, right []int64) bool {
	if len(left) != len(right) {
		return false
	}
	left = slices.Clone(left)
	right = slices.Clone(right)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}

func findingCodesEqual(findings []*competitionv1.PreflightFinding, want ...string) bool {
	if len(findings) != len(want) {
		return false
	}
	for index, finding := range findings {
		if finding.GetCode() != want[index] {
			return false
		}
	}
	return true
}

func attachmentCandidate(
	attachments []*competitionv1.AttachmentReadiness,
	versionID int64,
) *competitionv1.AttachmentReadiness {
	for _, attachment := range attachments {
		if attachment.GetAttachmentVersionId() == versionID {
			return attachment
		}
	}
	return nil
}

func TestAnonymousUploadLinkRoutesAreRemoved(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	presentationID := prepareActiveSchedule(t, administrator, server)
	issued := requestJSON(
		t.Context(),
		administrator,
		server.address,
		"/crew/events/1/upload-links",
		map[string]any{
			"target_type": "Presentation",
			"target_id":   presentationID,
			"command_id":  "removed-upload-link-issuance",
		},
	)
	if issued.status != http.StatusNotFound {
		t.Fatalf("removed Upload Link issuance = %d %q", issued.status, issued.body)
	}
	for _, path := range []string{
		"/crew/events/1/upload-links/1/revoke",
		"/upload/retired-credential",
	} {
		result := requestJSON(
			t.Context(),
			administrator,
			server.address,
			path,
			map[string]string{"command_id": "removed-upload-link-route"},
		)
		if result.status != http.StatusNotFound {
			t.Fatalf("removed Upload Link route %q = %d %q", path, result.status, result.body)
		}
	}
	server.stop(t)
}

func TestFinalAttachmentsReleaseByPolicyAndSurviveRestart(t *testing.T) {
	fixture := prepareReleasedEntryAttachments(t)
	administrator, server := fixture.administrator, fixture.server
	competitionID, entryID := fixture.competitionID, fixture.entryID
	competitionClient := fixture.competitionClient
	publicVersion := fixture.publicVersion
	sessionClient := sessionv1connect.NewSessionControlServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)

	override := requestJSONMethod(
		t.Context(), http.MethodPatch, administrator, server.address,
		fmt.Sprintf("/crew/events/1/competitions/%d/attachment-release", competitionID),
		map[string]any{
			"policy": "OnEventReleaseCue", "override": true, "expected_revision": 0,
			"command_id": "configure-cue-attachment-release",
		},
	)
	if override.status != http.StatusOK {
		t.Fatalf("configure Competition Attachment release = %d: %s", override.status, override.body)
	}
	assertReleasedAttachmentsOnListeners(t, server)
	staleOverride := requestJSONMethod(
		t.Context(), http.MethodPatch, administrator, server.address,
		fmt.Sprintf("/crew/events/1/competitions/%d/attachment-release", competitionID),
		map[string]any{
			"policy": "OnEnded", "override": true, "expected_revision": 0,
			"command_id": "stale-competition-attachment-release",
		},
	)
	if staleOverride.status != http.StatusConflict {
		t.Fatalf("stale Competition Attachment release = %d: %s", staleOverride.status, staleOverride.body)
	}
	staleOverrideRetry := requestJSONMethod(
		t.Context(), http.MethodPatch, administrator, server.address,
		fmt.Sprintf("/crew/events/1/competitions/%d/attachment-release", competitionID),
		map[string]any{
			"policy": "OnEnded", "override": true, "expected_revision": 0,
			"command_id": "stale-competition-attachment-release",
		},
	)
	if staleOverrideRetry.status != http.StatusConflict {
		t.Fatalf(
			"retried stale Competition Attachment release = %d: %s",
			staleOverrideRetry.status,
			staleOverrideRetry.body,
		)
	}
	rundownClient := rundownv1connect.NewRundownServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	rundown, err := rundownClient.GetCrewRundown(t.Context(), connect.NewRequest(
		&rundownv1.GetCrewRundownRequest{EventId: 1},
	))
	if err != nil {
		t.Fatalf("load ceremony Session for Attachment Release Cue: %v", err)
	}
	var cueSessionID, presentationID int64
	for _, candidate := range rundown.Msg.GetSessions() {
		if candidate.GetType() == rundownv1.SessionType_SESSION_TYPE_CEREMONY {
			cueSessionID = candidate.GetId()
		}
		if candidate.GetType() == rundownv1.SessionType_SESSION_TYPE_PRESENTATION {
			presentationID = candidate.GetId()
		}
	}
	if cueSessionID == 0 || presentationID == 0 {
		t.Fatal("published Ceremony or Presentation missing for bound Attachment Release Cue")
	}
	invalidBinding := requestJSONMethod(
		t.Context(), http.MethodPatch, administrator, server.address,
		"/crew/events/1/attachment-release",
		map[string]any{
			"policy": "OnEventReleaseCue", "cue_session_id": presentationID,
			"expected_revision": 1, "command_id": "reject-nonceremony-release-cue",
		},
	)
	if invalidBinding.status != http.StatusNotFound {
		t.Fatalf("non-Ceremony Attachment Release Cue = %d: %s", invalidBinding.status, invalidBinding.body)
	}
	bound := requestJSONMethod(
		t.Context(), http.MethodPatch, administrator, server.address,
		"/crew/events/1/attachment-release",
		map[string]any{
			"policy": "OnEventReleaseCue", "cue_session_id": cueSessionID,
			"expected_revision": 1, "command_id": "bind-attachment-release-cue",
		},
	)
	if bound.status != http.StatusOK {
		t.Fatalf("bind Attachment Release Cue = %d: %s", bound.status, bound.body)
	}
	if _, err = sessionClient.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: cueSessionID, CommandId: "start-attachment-release-cue",
		ExpectedLiveStateRevision: proto.Int64(0),
	})); err != nil {
		t.Fatalf("start bound Attachment Release Cue Session: %v", err)
	}
	assertReleasedAttachmentsOnListeners(t, server, publicVersion.ID)
	cuePreview := requestJSONMethod(
		t.Context(), http.MethodGet, administrator, server.address,
		"/crew/events/1/attachment-release-cue", nil,
	)
	var releaseImpact struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err = json.Unmarshal([]byte(cuePreview.body), &releaseImpact); err != nil ||
		cuePreview.status != http.StatusOK || releaseImpact.Fingerprint == "" {
		t.Fatalf("preview Attachment Release Cue = %d: %s (%v)", cuePreview.status, cuePreview.body, err)
	}
	cue := requestJSON(
		t.Context(), administrator, server.address, "/crew/events/1/attachment-release-cue",
		map[string]any{
			"expected_revision": 3, "preview_fingerprint": releaseImpact.Fingerprint,
			"confirmed": true, "command_id": "fire-attachment-release-cue",
		},
	)
	if cue.status != http.StatusOK {
		t.Fatalf("fire Attachment Release Cue = %d: %s", cue.status, cue.body)
	}
	assertReleasedAttachmentsOnListeners(t, server, publicVersion.ID)

	current, err := competitionClient.GetCompetition(t.Context(), connect.NewRequest(
		&competitionv1.GetCompetitionRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || len(current.Msg.GetEntries()) != 1 {
		t.Fatalf("load Entry for Release Hold = %+v, %v", current, err)
	}
	entryHeld, err := competitionClient.SetEntryReleaseHold(t.Context(), connect.NewRequest(
		&competitionv1.SetEntryReleaseHoldRequest{
			EventId: 1, SessionId: competitionID, EntryId: entryID,
			CommandId:        "hold-entry-attachments",
			ExpectedRevision: current.Msg.GetEntries()[0].GetRevision(),
			Hold:             true,
			CrewReason:       "review public package",
		},
	))
	if err != nil || !entryHeld.Msg.GetEntry().GetReleaseHold() {
		t.Fatalf("apply Entry Release Hold = %+v, %v", entryHeld, err)
	}
	assertReleasedAttachmentsOnListeners(t, server)
	assertPublicAttachmentOnListeners(
		t, server, publicVersion.ID,
		http.StatusNotFound, "Attachment Version not found\n",
	)
	entryLifted, err := competitionClient.SetEntryReleaseHold(t.Context(), connect.NewRequest(
		&competitionv1.SetEntryReleaseHoldRequest{
			EventId: 1, SessionId: competitionID, EntryId: entryID,
			CommandId:        "lift-entry-attachment-hold",
			ExpectedRevision: entryHeld.Msg.GetEntry().GetRevision(),
			CrewReason:       "public package reviewed",
		},
	))
	if err != nil || entryLifted.Msg.GetEntry().GetReleaseHold() {
		t.Fatalf("lift Entry Release Hold = %+v, %v", entryLifted, err)
	}
	assertReleasedAttachmentsOnListeners(t, server, publicVersion.ID)

	held := requestJSONMethod(
		t.Context(), http.MethodPatch, administrator, server.address,
		fmt.Sprintf("/crew/events/1/attachment-versions/%d/release", publicVersion.ID),
		map[string]any{
			"hold": true, "expected_revision": 0,
			"command_id": "hold-public-attachment",
		},
	)
	if held.status != http.StatusOK {
		t.Fatalf("hold public Attachment = %d: %s", held.status, held.body)
	}
	staleHold := requestJSONMethod(
		t.Context(), http.MethodPatch, administrator, server.address,
		fmt.Sprintf("/crew/events/1/attachment-versions/%d/release", publicVersion.ID),
		map[string]any{
			"hold": false, "expected_revision": 0,
			"command_id": "reject-stale-public-attachment-hold",
		},
	)
	if staleHold.status != http.StatusConflict {
		t.Fatalf("stale public Attachment hold = %d: %s", staleHold.status, staleHold.body)
	}
	assertReleasedAttachmentsOnListeners(t, server)
	assertPublicAttachmentOnListeners(
		t, server, publicVersion.ID,
		http.StatusNotFound, "Attachment Version not found\n",
	)
	lifted := requestJSONMethod(
		t.Context(), http.MethodPatch, administrator, server.address,
		fmt.Sprintf("/crew/events/1/attachment-versions/%d/release", publicVersion.ID),
		map[string]any{
			"hold": false, "expected_revision": 1,
			"command_id": "lift-public-attachment-hold",
		},
	)
	if lifted.status != http.StatusOK {
		t.Fatalf("lift public Attachment hold = %d: %s", lifted.status, lifted.body)
	}
	staleHoldRetry := requestJSONMethod(
		t.Context(), http.MethodPatch, administrator, server.address,
		fmt.Sprintf("/crew/events/1/attachment-versions/%d/release", publicVersion.ID),
		map[string]any{
			"hold": false, "expected_revision": 0,
			"command_id": "reject-stale-public-attachment-hold",
		},
	)
	if staleHoldRetry.status != http.StatusConflict {
		t.Fatalf(
			"retried stale public Attachment hold = %d: %s",
			staleHoldRetry.status,
			staleHoldRetry.body,
		)
	}
	audit := get(t, administrator, server.address, "/admin/audit")
	auditBody, readErr := io.ReadAll(audit.Body)
	closeErr := audit.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Attachment release Audit history: %v", err)
	}
	if bytes.Count(auditBody, []byte(`"action":"ConfigureCompetitionAttachmentRelease"`)) != 2 ||
		bytes.Count(auditBody, []byte(`"reason":"stale_revision"`)) != 2 ||
		!bytes.Contains(auditBody, []byte(`"reason":"attachment_target_not_found"`)) {
		t.Fatalf("Attachment release rejection Audit evidence = %s", auditBody)
	}

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	restarted := startBeamersWithPublicListener(t, bin, dataDir)
	assertReleasedAttachmentsOnListeners(t, restarted, publicVersion.ID)
	assertPublicAttachmentOnListeners(
		t, restarted, publicVersion.ID, http.StatusOK, "public release",
	)
	prepareAndActivateSecondEvent(t, administrator, restarted)
	assertReleasedAttachmentsOnListeners(t, restarted)
	assertPublicAttachmentOnListeners(
		t, restarted, publicVersion.ID,
		http.StatusNotFound, "Attachment Version not found\n",
	)
	restarted.stop(t)
}

func TestFinalFilesExportDownloadAndDestination(t *testing.T) {
	fixture := prepareReleasedEntryAttachments(t)
	administrator, server := fixture.administrator, fixture.server

	unauthenticatedPreview := requestJSON(
		t.Context(), http.DefaultClient, server.address,
		"/admin/final-files/preview",
		map[string]any{"event_id": 1},
	)
	if unauthenticatedPreview.status != http.StatusUnauthorized {
		t.Fatalf(
			"unauthenticated Final Files Export preview = %d: %s",
			unauthenticatedPreview.status, unauthenticatedPreview.body,
		)
	}
	webPreview := requestJSON(
		t.Context(), administrator, server.address,
		"/admin/final-files/preview",
		map[string]any{"event_id": 1},
	)
	var downloadable struct {
		PreviewDigest string `json:"preview_digest"`
		Files         []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if decodeErr := json.Unmarshal([]byte(webPreview.body), &downloadable); decodeErr != nil ||
		webPreview.status != http.StatusOK ||
		downloadable.PreviewDigest == "" ||
		len(downloadable.Files) != 1 {
		t.Fatalf(
			"Administrator Final Files Export preview = %d: %s (%v)",
			webPreview.status, webPreview.body, decodeErr,
		)
	}
	download := requestJSON(
		t.Context(), administrator, server.address,
		"/admin/final-files",
		map[string]any{
			"event_id":       1,
			"preview_digest": downloadable.PreviewDigest,
		},
	)
	if download.status != http.StatusOK ||
		download.header.Get("Content-Type") != "application/zip" {
		t.Fatalf(
			"Administrator Final Files Export download = %d, %q: %s",
			download.status, download.header.Get("Content-Type"), download.body,
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
		archive.File[0].Name != downloadable.Files[0].Path ||
		archive.File[1].Name != "manifest.json" {
		t.Fatalf("Final Files Export download entries = %+v", archive.File)
	}

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)

	outputDir := filepath.Join(t.TempDir(), "final-files")
	var preview struct {
		PreviewDigest string   `json:"preview_digest"`
		Collisions    []string `json:"collisions"`
		Files         []struct {
			Path             string `json:"path"`
			SHA256           string `json:"sha256"`
			OriginalFilename string `json:"original_filename"`
		} `json:"files"`
	}
	if err = json.Unmarshal([]byte(runBeamersOutput(
		t, bin,
		"export-final-files", "preview",
		"--data-dir", dataDir,
		"--event-id", "1",
		"--output", outputDir,
	)), &preview); err != nil {
		t.Fatalf("decode Final Files Export preview: %v", err)
	}
	if preview.PreviewDigest == "" || len(preview.Collisions) != 0 || len(preview.Files) != 1 {
		t.Fatalf("Final Files Export preview = %+v", preview)
	}
	exported := preview.Files[0]
	if !strings.HasPrefix(exported.Path, "untracked/competitions/") ||
		exported.OriginalFilename != "public.txt" ||
		exported.SHA256 == "" {
		t.Fatalf("Final Files Export file = %+v", exported)
	}
	otherOutput := filepath.Join(t.TempDir(), "other-final-files")
	var otherPreview struct {
		PreviewDigest string `json:"preview_digest"`
	}
	if err = json.Unmarshal([]byte(runBeamersOutput(
		t, bin,
		"export-final-files", "preview",
		"--data-dir", dataDir,
		"--event-id", "1",
		"--output", otherOutput,
	)), &otherPreview); err != nil {
		t.Fatalf("decode alternate Final Files Export preview: %v", err)
	}
	if otherPreview.PreviewDigest == preview.PreviewDigest {
		t.Fatal("Final Files Export preview digest did not bind the destination")
	}
	runBeamersFails(
		t, bin,
		"export-final-files", "apply",
		"--data-dir", dataDir,
		"--event-id", "1",
		"--output", otherOutput,
		"--preview-digest", preview.PreviewDigest,
		"--approve-export",
	)
	if _, statErr := os.Lstat(otherOutput); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("mismatched Final Files Export created output: %v", statErr)
	}
	runBeamersFails(
		t, bin,
		"export-final-files", "apply",
		"--data-dir", dataDir,
		"--event-id", "1",
		"--output", outputDir,
		"--preview-digest", "stale-preview",
		"--approve-export",
	)
	if _, statErr := os.Lstat(outputDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("stale Final Files Export created output: %v", statErr)
	}
	runBeamers(
		t, bin,
		"export-final-files", "apply",
		"--data-dir", dataDir,
		"--event-id", "1",
		"--output", outputDir,
		"--preview-digest", preview.PreviewDigest,
		"--approve-export",
	)
	content, err := os.ReadFile(filepath.Join(outputDir, filepath.FromSlash(exported.Path)))
	if err != nil || string(content) != "public release" {
		t.Fatalf("read Final Files Export content = %q, %v", content, err)
	}
	firstManifest, err := os.ReadFile(filepath.Join(outputDir, "manifest.json"))
	if err != nil || !bytes.Contains(firstManifest, []byte(`"original_filename":"public.txt"`)) ||
		bytes.Contains(firstManifest, []byte(`"original_filename":"crew.txt"`)) {
		t.Fatalf("Final Files Export manifest = %s, %v", firstManifest, err)
	}
	var repeated struct {
		PreviewDigest string   `json:"preview_digest"`
		Collisions    []string `json:"collisions"`
	}
	if err = json.Unmarshal([]byte(runBeamersOutput(
		t, bin,
		"export-final-files", "preview",
		"--data-dir", dataDir,
		"--event-id", "1",
		"--output", outputDir,
	)), &repeated); err != nil {
		t.Fatalf("decode repeated Final Files Export preview: %v", err)
	}
	if repeated.PreviewDigest == preview.PreviewDigest || len(repeated.Collisions) == 0 {
		t.Fatalf("repeated Final Files Export preview = %+v", repeated)
	}
	runBeamersFails(
		t, bin,
		"export-final-files", "apply",
		"--data-dir", dataDir,
		"--event-id", "1",
		"--output", outputDir,
		"--preview-digest", preview.PreviewDigest,
		"--approve-export",
	)
}

type releasedEntryAttachments struct {
	administrator     *http.Client
	server            *runningServer
	competitionClient competitionv1connect.CompetitionServiceClient
	competitionID     int64
	entryID           int64
	publicVersion     attachmentVersionResponse
	crewVersion       attachmentVersionResponse
}

func prepareReleasedEntryAttachments(t *testing.T) releasedEntryAttachments {
	t.Helper()
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	competitionClient := competitionv1connect.NewCompetitionServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	created, err := competitionClient.CreateEntry(t.Context(), connect.NewRequest(
		&competitionv1.CreateEntryRequest{
			EventId: 1, SessionId: competitionID, CommandId: "create-release-entry",
			Name: "Release Project",
		},
	))
	if err != nil {
		t.Fatalf("create Entry for Attachment release: %v", err)
	}
	entryID := created.Msg.GetEntry().GetId()
	publicVersion := decodeAttachmentVersion(t, requestMultipart(
		t.Context(), administrator, server.address, "/crew/events/1/attachments",
		map[string]string{
			"target_type": "Entry",
			"target_id":   strconv.FormatInt(entryID, 10),
			"name":        "public",
			"command_id":  "upload-public-release",
		},
		"public.txt", "text/plain", []byte("public release"),
	))
	crewVersion := decodeAttachmentVersion(t, requestMultipart(
		t.Context(), administrator, server.address, "/crew/events/1/attachments",
		map[string]string{
			"target_type": "Entry",
			"target_id":   strconv.FormatInt(entryID, 10),
			"name":        "crew",
			"command_id":  "upload-crew-release",
			"crew_only":   "true",
		},
		"crew.txt", "text/plain", []byte("crew only"),
	))
	if publicVersion.ReleaseEligibility != "Public" ||
		crewVersion.ReleaseEligibility != "CrewOnly" {
		t.Fatalf(
			"upload release eligibility = %q then %q",
			publicVersion.ReleaseEligibility, crewVersion.ReleaseEligibility,
		)
	}
	assertPublicAttachmentOnListeners(
		t, server, publicVersion.ID,
		http.StatusNotFound, "Attachment Version not found\n",
	)
	for _, version := range []attachmentVersionResponse{publicVersion, crewVersion} {
		_, err = competitionClient.SetEntryAttachmentReadiness(t.Context(), connect.NewRequest(
			&competitionv1.SetEntryAttachmentReadinessRequest{
				EventId: 1, SessionId: competitionID, EntryId: entryID,
				AttachmentVersionId: int64(version.ID),
				CommandId:           fmt.Sprintf("finalize-release-%d", version.ID),
				ExpectedRevision:    int64(version.ReadinessRevision),
				Final:               true,
				Primary:             version.ID == publicVersion.ID,
			},
		))
		if err != nil {
			t.Fatalf("finalize Attachment Version %d: %v", version.ID, err)
		}
	}
	configured := requestJSONMethod(
		t.Context(), http.MethodPatch, administrator, server.address,
		"/crew/events/1/attachment-release",
		map[string]any{
			"policy": "OnLive", "expected_revision": 0,
			"command_id": "configure-live-attachment-release",
		},
	)
	if configured.status != http.StatusOK {
		t.Fatalf("configure Attachment Release Policy = %d: %s", configured.status, configured.body)
	}
	assertReleasedAttachmentsOnListeners(t, server)
	sessionClient := sessionv1connect.NewSessionControlServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	if _, err = sessionClient.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: competitionID, CommandId: "start-release-competition",
		ExpectedLiveStateRevision: proto.Int64(0),
	})); err != nil {
		t.Fatalf("start Competition for Attachment release: %v", err)
	}
	assertReleasedAttachmentsOnListeners(t, server, publicVersion.ID)
	assertPublicAttachmentOnListeners(
		t, server, publicVersion.ID, http.StatusOK, "public release",
	)
	assertPublicAttachmentOnListeners(
		t, server, crewVersion.ID,
		http.StatusNotFound, "Attachment Version not found\n",
	)
	assertPublicAttachmentOnListeners(
		t, server, 999999,
		http.StatusNotFound, "Attachment Version not found\n",
	)
	return releasedEntryAttachments{
		administrator: administrator, server: server,
		competitionClient: competitionClient,
		competitionID:     competitionID, entryID: entryID,
		publicVersion: publicVersion, crewVersion: crewVersion,
	}
}

func setPresentationUploadDeadline(
	t *testing.T,
	client rundownv1connect.RundownServiceClient,
	draftRevision, presentationID int64,
	deadline time.Time,
) {
	t.Helper()
	edited, err := client.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "set-presentation-upload-deadline",
		ExpectedDraftRevision: draftRevision,
		Sessions: []*rundownv1.SessionDraft{{
			Id: presentationID, UploadDeadline: timestamppb.New(deadline),
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"upload_deadline"}},
		}},
	}))
	if err != nil {
		t.Fatalf("set Presentation Upload Deadline: %v", err)
	}
	publishEditedDraft(t, client, edited.Msg, "publish-presentation-upload-deadline")
}

type attachmentVersionResponse struct {
	ID                 int    `json:"id"`
	AttachmentID       int    `json:"attachment_id"`
	Version            int    `json:"version"`
	SHA256             string `json:"sha256"`
	UploaderType       string `json:"uploader_type"`
	UploaderID         int    `json:"uploader_id"`
	Primary            bool   `json:"primary"`
	Final              bool   `json:"final"`
	ReadinessRevision  int    `json:"readiness_revision"`
	ReleaseEligibility string `json:"release_eligibility"`
	ReleaseRevision    int    `json:"release_revision"`
}

func decodeAttachmentVersion(t *testing.T, response jsonResponse) attachmentVersionResponse {
	t.Helper()
	var version attachmentVersionResponse
	if err := json.Unmarshal([]byte(response.body), &version); err != nil ||
		response.status != http.StatusCreated || version.ID <= 0 {
		t.Fatalf("decode Attachment Version = %d %+v: %s (%v)", response.status, version, response.body, err)
	}
	return version
}

func assertReleasedAttachmentIDs(t *testing.T, address string, want ...int) {
	t.Helper()
	response := get(t, http.DefaultClient, address, "/public/attachments")
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read released Attachment list: %v", err)
	}
	var versions []struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(body, &versions); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("released Attachments = %d %q (%v)", response.StatusCode, body, err)
	}
	got := make([]int, 0, len(versions))
	for _, version := range versions {
		got = append(got, version.ID)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("released Attachment IDs = %v, want %v", got, want)
	}
}

func assertPublicAttachmentBytes(
	t *testing.T,
	address string,
	versionID, wantStatus int,
	wantBody string,
) {
	t.Helper()
	response := get(
		t, http.DefaultClient, address,
		fmt.Sprintf("/public/attachments/%d", versionID),
	)
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read public Attachment Version %d: %v", versionID, err)
	}
	if response.StatusCode != wantStatus || string(body) != wantBody {
		t.Fatalf(
			"public Attachment Version %d = %d %q, want %d %q",
			versionID, response.StatusCode, body, wantStatus, wantBody,
		)
	}
}

func assertReleasedAttachmentsOnListeners(
	t *testing.T,
	server *runningServer,
	want ...int,
) {
	t.Helper()
	assertReleasedAttachmentIDs(t, server.address, want...)
	if server.publicAddress != "" {
		assertReleasedAttachmentIDs(t, server.publicAddress, want...)
	}
}

func assertPublicAttachmentOnListeners(
	t *testing.T,
	server *runningServer,
	versionID, wantStatus int,
	wantBody string,
) {
	t.Helper()
	assertPublicAttachmentBytes(t, server.address, versionID, wantStatus, wantBody)
	if server.publicAddress != "" {
		assertPublicAttachmentBytes(
			t, server.publicAddress, versionID, wantStatus, wantBody,
		)
	}
}

func addCompetitionSession(
	t *testing.T,
	client *http.Client,
	server *runningServer,
) (int64, time.Time) {
	t.Helper()
	rundownClient := rundownv1connect.NewRundownServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	current, err := rundownClient.GetCrewRundown(
		t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}),
	)
	if err != nil {
		t.Fatalf("load Rundown before adding Competition: %v", err)
	}
	deadline := time.Date(2099, 8, 21, 11, 30, 0, 0, time.UTC)
	plannedStart := time.Date(2099, 8, 21, 12, 0, 0, 0, time.UTC)
	edited, err := rundownClient.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "add-competition-session",
		ExpectedDraftRevision: current.Msg.GetDraftRevision(),
		Sessions: []*rundownv1.SessionDraft{{
			Ref: "competition", Title: "Demo Competition",
			Type:               rundownv1.SessionType_SESSION_TYPE_COMPETITION,
			AudienceVisibility: rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_PUBLIC,
			PublicDetails:      "Projects presented by attendees",
			PlannedStart:       timestamppb.New(plannedStart),
			PlannedEnd:         timestamppb.New(plannedStart.Add(time.Hour)),
			TimingPolicy:       rundownv1.TimingPolicy_TIMING_POLICY_FIXED_END,
			MinimumDuration:    durationpb.New(30 * time.Minute),
			StartBoundary:      rundownv1.Boundary_BOUNDARY_HARD,
			EndBoundary:        rundownv1.Boundary_BOUNDARY_HARD,
			Lanes: []*rundownv1.TargetRef{{
				Target: &rundownv1.TargetRef_Id{Id: current.Msg.GetLanes()[0].GetId()},
			}},
			Locations: []*rundownv1.TargetRef{{
				Target: &rundownv1.TargetRef_Id{Id: current.Msg.GetLocations()[0].GetId()},
			}},
			SubmissionDeadline:      timestamppb.New(deadline),
			EntryDefaultDisposition: rundownv1.EntryDisposition_ENTRY_DISPOSITION_INCLUDED,
		}},
	}))
	if err != nil {
		t.Fatalf("add Competition Session: %v", err)
	}
	var competitionID int64
	for _, change := range edited.Msg.GetChanges() {
		if change.GetKind() == "CreateSession" {
			competitionID = change.GetTargetId()
		}
	}
	publishEditedDraft(t, rundownClient, edited.Msg, "publish-competition-session")
	return competitionID, deadline
}

func setCompetitionSubmissionDeadline(
	t *testing.T,
	client *http.Client,
	server *runningServer,
	competitionID int64,
	deadline time.Time,
) {
	t.Helper()
	rundownClient := rundownv1connect.NewRundownServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	current, err := rundownClient.GetCrewRundown(
		t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}),
	)
	if err != nil {
		t.Fatalf("load Rundown before closing Competition uploads: %v", err)
	}
	edited, err := rundownClient.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "close-competition-uploads",
		ExpectedDraftRevision: current.Msg.GetDraftRevision(),
		Sessions: []*rundownv1.SessionDraft{{
			Id: competitionID, SubmissionDeadline: timestamppb.New(deadline),
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"submission_deadline"}},
		}},
	}))
	if err != nil {
		t.Fatalf("set Competition Submission Deadline: %v", err)
	}
	publishEditedDraft(t, rundownClient, edited.Msg, "publish-closed-competition-uploads")
}

func prepareCommunicatedTimeSchedule(
	t *testing.T,
	client *http.Client,
	server *runningServer,
	plannedStart time.Time,
) int64 {
	t.Helper()
	assertJSONRequest(
		t, client, server.address, "/admin/events",
		map[string]string{
			"name": "Communicated Time", "planned_start_date": plannedStart.Format(time.DateOnly),
			"planned_end_date": plannedStart.AddDate(0, 0, 1).Format(time.DateOnly), "timezone": "UTC",
			"event_locale": "en-GB", "content_language": "en-GB",
			"event_day_boundary": "00:00", "command_id": "create-communicated-time-event",
		},
		http.StatusCreated,
		fmt.Sprintf(
			"{\"id\":1,\"name\":\"Communicated Time\",\"planned_start_date\":%q,\"planned_end_date\":%q,\"timezone\":\"UTC\",\"event_locale\":\"en-GB\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"00:00\",\"revision\":1}\n",
			plannedStart.Format(time.DateOnly), plannedStart.AddDate(0, 0, 1).Format(time.DateOnly),
		),
	)
	assertJSONRequest(
		t, client, server.address, "/admin/events/1/grants",
		map[string]any{"account_id": 1, "role": "Producer", "command_id": "grant-communicated-time-producer"},
		http.StatusCreated, "{\"event_id\":1,\"account_id\":1,\"role\":\"Producer\"}\n",
	)
	publishEventListing(
		t,
		client,
		server,
		"Communicated Time",
		"communicated-time",
		plannedStart.Format(time.DateOnly),
		plannedStart.AddDate(0, 0, 1).Format(time.DateOnly),
		"UTC",
		"00:00",
	)
	rundownClient := rundownv1connect.NewRundownServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	edited, err := rundownClient.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "edit-communicated-time", ExpectedDraftRevision: 0,
		Locations: []*rundownv1.LocationDraft{{Ref: "room", Name: "Room"}},
		Lanes: []*rundownv1.LaneDraft{{
			Ref: "lane", Name: "Lane",
			Location: &rundownv1.TargetRef{Target: &rundownv1.TargetRef_Ref{Ref: "room"}},
		}},
		Sessions: []*rundownv1.SessionDraft{{
			Ref: "session", Title: "Communicated Session",
			Type:               rundownv1.SessionType_SESSION_TYPE_PRESENTATION,
			AudienceVisibility: rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_PUBLIC,
			PlannedStart:       timestamppb.New(plannedStart),
			PlannedEnd:         timestamppb.New(plannedStart.Add(30 * time.Minute)),
			TimingPolicy:       rundownv1.TimingPolicy_TIMING_POLICY_FIXED_END,
			MinimumDuration:    durationpb.New(15 * time.Minute),
			StartBoundary:      rundownv1.Boundary_BOUNDARY_HARD,
			EndBoundary:        rundownv1.Boundary_BOUNDARY_HARD,
			Locations: []*rundownv1.TargetRef{{
				Target: &rundownv1.TargetRef_Ref{Ref: "room"},
			}},
			Lanes: []*rundownv1.TargetRef{{
				Target: &rundownv1.TargetRef_Ref{Ref: "lane"},
			}},
		}},
	}))
	if err != nil {
		t.Fatalf("edit communicated-time Rundown: %v", err)
	}
	var sessionID int64
	changeIDs := make([]int64, 0, len(edited.Msg.GetChanges()))
	for _, change := range edited.Msg.GetChanges() {
		changeIDs = append(changeIDs, change.GetId())
		if change.GetKind() == "CreateSession" {
			sessionID = change.GetTargetId()
		}
	}
	preview, err := rundownClient.PublishPreview(t.Context(), connect.NewRequest(
		&rundownv1.PublishPreviewRequest{EventId: 1, ChangeIds: changeIDs},
	))
	if err != nil {
		t.Fatalf("preview communicated-time Publish: %v", err)
	}
	if _, publishErr := rundownClient.Publish(t.Context(), connect.NewRequest(&rundownv1.PublishRequest{
		EventId: 1, CommandId: "publish-communicated-time",
		Confirmation: &rundownv1.PublishConfirmation{
			DraftRevision: preview.Msg.GetDraftRevision(), PublishedRevision: preview.Msg.GetPublishedRevision(),
			ChangeIds: preview.Msg.GetChangeIds(), Fingerprint: preview.Msg.GetFingerprint(),
		},
	})); publishErr != nil {
		t.Fatalf("publish communicated-time Rundown: %v", publishErr)
	}
	activationClient := activationv1connect.NewActivationServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	preflight, err := activationClient.Preflight(
		t.Context(), connect.NewRequest(&activationv1.PreflightRequest{EventId: 1}),
	)
	if err != nil {
		t.Fatalf("preflight communicated-time Event: %v", err)
	}
	if _, err := activationClient.Activate(t.Context(), connect.NewRequest(&activationv1.ActivateRequest{
		EventId: 1, CommandId: "activate-communicated-time", Confirmation: preflight.Msg.GetConfirmation(),
	})); err != nil {
		t.Fatalf("activate communicated-time Event: %v", err)
	}
	return sessionID
}

func prepareActiveSchedule(t *testing.T, client *http.Client, server *runningServer) int64 {
	t.Helper()
	assertJSONRequest(
		t, client, server.address, "/admin/events",
		map[string]string{
			"name": "Revision 2099", "planned_start_date": "2099-08-21",
			"planned_end_date": "2099-08-23", "timezone": "Europe/Berlin",
			"event_locale": "en-GB", "content_language": "en-GB",
			"event_day_boundary": "06:00", "command_id": "create-schedule-event",
		},
		http.StatusCreated,
		"{\"id\":1,\"name\":\"Revision 2099\",\"planned_start_date\":\"2099-08-21\",\"planned_end_date\":\"2099-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"en-GB\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	assertJSONRequest(
		t, client, server.address, "/admin/events/1/grants",
		map[string]any{"account_id": 1, "role": "Producer", "command_id": "grant-schedule-producer"},
		http.StatusCreated, "{\"event_id\":1,\"account_id\":1,\"role\":\"Producer\"}\n",
	)
	publishEventListing(
		t,
		client,
		server,
		"Revision 2099",
		"revision-2099",
		"2099-08-21",
		"2099-08-23",
		"Europe/Berlin",
		"06:00",
	)

	rundownClient := rundownv1connect.NewRundownServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	plannedStart := time.Date(2099, 8, 21, 8, 0, 0, 0, time.UTC)
	edited, err := rundownClient.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "edit-schedule", ExpectedDraftRevision: 0,
		Locations: []*rundownv1.LocationDraft{{Ref: "main", Name: "Main Hall"}},
		Lanes: []*rundownv1.LaneDraft{{
			Ref: "main-lane", Name: "Main Lane",
			Location: &rundownv1.TargetRef{Target: &rundownv1.TargetRef_Ref{Ref: "main"}},
		}},
		Tracks: []*rundownv1.TrackDraft{{Ref: "general", Name: "General"}},
		Sessions: []*rundownv1.SessionDraft{
			{
				Ref: "keynote", Title: "Opening Keynote",
				Speaker:            "Original Speaker",
				Type:               rundownv1.SessionType_SESSION_TYPE_PRESENTATION,
				AudienceVisibility: rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_PUBLIC,
				PublicDetails:      "Welcome to Revision 2099",
				CrewNotes:          "Call Pat on +44 20 7946 0958; /srv/beamers/private/keynote.pdf",
				PlannedStart:       timestamppb.New(plannedStart), PlannedEnd: timestamppb.New(plannedStart.Add(time.Hour)),
				TimingPolicy:    rundownv1.TimingPolicy_TIMING_POLICY_FIXED_END,
				MinimumDuration: durationpb.New(30 * time.Minute),
				StartBoundary:   rundownv1.Boundary_BOUNDARY_HARD,
				EndBoundary:     rundownv1.Boundary_BOUNDARY_HARD,
				Locations:       []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Ref{Ref: "main"}}},
				Lanes:           []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Ref{Ref: "main-lane"}}},
				Tracks:          []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Ref{Ref: "general"}}},
			},
			{
				Ref: "soundcheck", Title: "Private Soundcheck",
				Type:               rundownv1.SessionType_SESSION_TYPE_ACTIVITY,
				AudienceVisibility: rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_CREW_ONLY,
				PublicDetails:      "must remain undiscoverable", CrewNotes: "radio channel 4",
				PlannedStart: timestamppb.New(plannedStart.Add(-time.Hour)), PlannedEnd: timestamppb.New(plannedStart),
				TimingPolicy:    rundownv1.TimingPolicy_TIMING_POLICY_FIXED_END,
				MinimumDuration: durationpb.New(30 * time.Minute),
				StartBoundary:   rundownv1.Boundary_BOUNDARY_HARD,
				EndBoundary:     rundownv1.Boundary_BOUNDARY_SOFT,
				Locations:       []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Ref{Ref: "main"}}},
				Lanes:           []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Ref{Ref: "main-lane"}}},
			},
			{
				Ref: "old-announcement", Title: "Old Announcement",
				Type:               rundownv1.SessionType_SESSION_TYPE_PRESENTATION,
				AudienceVisibility: rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_PUBLIC,
				PublicDetails:      "This historical Session is no longer upcoming",
				PlannedStart:       timestamppb.New(time.Date(2000, 1, 1, 8, 0, 0, 0, time.UTC)),
				PlannedEnd:         timestamppb.New(time.Date(2000, 1, 1, 9, 0, 0, 0, time.UTC)),
				TimingPolicy:       rundownv1.TimingPolicy_TIMING_POLICY_FIXED_END,
				MinimumDuration:    durationpb.New(30 * time.Minute),
				StartBoundary:      rundownv1.Boundary_BOUNDARY_HARD,
				EndBoundary:        rundownv1.Boundary_BOUNDARY_SOFT,
				Locations:          []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Ref{Ref: "main"}}},
				Lanes:              []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Ref{Ref: "main-lane"}}},
			},
			{
				Ref: "closing", Title: "Closing Session",
				Type:               rundownv1.SessionType_SESSION_TYPE_CEREMONY,
				AudienceVisibility: rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_PUBLIC,
				PublicDetails:      "The unchanged later Session",
				PlannedStart:       timestamppb.New(plannedStart.Add(2 * time.Hour)),
				PlannedEnd:         timestamppb.New(plannedStart.Add(3 * time.Hour)),
				TimingPolicy:       rundownv1.TimingPolicy_TIMING_POLICY_FIXED_END,
				MinimumDuration:    durationpb.New(30 * time.Minute),
				StartBoundary:      rundownv1.Boundary_BOUNDARY_HARD,
				EndBoundary:        rundownv1.Boundary_BOUNDARY_SOFT,
				Locations:          []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Ref{Ref: "main"}}},
				Lanes:              []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Ref{Ref: "main-lane"}}},
			},
		},
	}))
	if err != nil {
		t.Fatalf("Edit public Schedule Draft: %v", err)
	}
	var publicSessionID int64
	changeIDs := make([]int64, 0, len(edited.Msg.GetChanges()))
	for _, change := range edited.Msg.GetChanges() {
		changeIDs = append(changeIDs, change.GetId())
		if change.GetKind() == "CreateSession" && publicSessionID == 0 {
			publicSessionID = change.GetTargetId()
		}
	}
	preview, err := rundownClient.PublishPreview(t.Context(), connect.NewRequest(&rundownv1.PublishPreviewRequest{
		EventId: 1, ChangeIds: changeIDs,
	}))
	if err != nil {
		t.Fatalf("Preview public Schedule Publish: %v", err)
	}
	if _, publishErr := rundownClient.Publish(t.Context(), connect.NewRequest(&rundownv1.PublishRequest{
		EventId: 1, CommandId: "publish-schedule",
		Confirmation: &rundownv1.PublishConfirmation{
			DraftRevision: preview.Msg.GetDraftRevision(), PublishedRevision: preview.Msg.GetPublishedRevision(),
			ChangeIds: preview.Msg.GetChangeIds(), Fingerprint: preview.Msg.GetFingerprint(),
		},
	})); publishErr != nil {
		t.Fatalf("Publish public Schedule: %v", publishErr)
	}

	activationClient := activationv1connect.NewActivationServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	preflight, err := activationClient.Preflight(t.Context(), connect.NewRequest(&activationv1.PreflightRequest{EventId: 1}))
	if err != nil {
		t.Fatalf("Preflight public Schedule Event: %v", err)
	}
	if _, err := activationClient.Activate(t.Context(), connect.NewRequest(&activationv1.ActivateRequest{
		EventId: 1, CommandId: "activate-schedule", Confirmation: preflight.Msg.GetConfirmation(),
	})); err != nil {
		t.Fatalf("Activate public Schedule Event: %v", err)
	}
	return publicSessionID
}

func addSoftRippleSession(t *testing.T, client *http.Client, server *runningServer) int64 {
	t.Helper()
	rundownClient := rundownv1connect.NewRundownServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	current, err := rundownClient.GetCrewRundown(
		t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}),
	)
	if err != nil {
		t.Fatalf("load Rundown before adding ripple Session: %v", err)
	}
	plannedStart := time.Date(2099, 8, 21, 9, 0, 0, 0, time.UTC)
	edited, err := rundownClient.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "add-soft-ripple-session",
		ExpectedDraftRevision: current.Msg.GetDraftRevision(),
		Sessions: []*rundownv1.SessionDraft{{
			Ref: "soft-ripple", Title: "Soft Ripple Session",
			Type:               rundownv1.SessionType_SESSION_TYPE_PRESENTATION,
			AudienceVisibility: rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_PUBLIC,
			PlannedStart:       timestamppb.New(plannedStart),
			PlannedEnd:         timestamppb.New(plannedStart.Add(time.Hour)),
			TimingPolicy:       rundownv1.TimingPolicy_TIMING_POLICY_FIXED_DURATION,
			MinimumDuration:    durationpb.New(55 * time.Minute),
			StartBoundary:      rundownv1.Boundary_BOUNDARY_SOFT,
			EndBoundary:        rundownv1.Boundary_BOUNDARY_SOFT,
			Locations: []*rundownv1.TargetRef{{
				Target: &rundownv1.TargetRef_Id{Id: 1},
			}},
			Lanes: []*rundownv1.TargetRef{{
				Target: &rundownv1.TargetRef_Id{Id: 1},
			}},
		}},
	}))
	if err != nil {
		t.Fatalf("add soft ripple Session: %v", err)
	}
	var sessionID int64
	changeIDs := make([]int64, 0, len(edited.Msg.GetChanges()))
	for _, change := range edited.Msg.GetChanges() {
		changeIDs = append(changeIDs, change.GetId())
		if change.GetKind() == "CreateSession" {
			sessionID = change.GetTargetId()
		}
	}
	preview, err := rundownClient.PublishPreview(t.Context(), connect.NewRequest(
		&rundownv1.PublishPreviewRequest{EventId: 1, ChangeIds: changeIDs},
	))
	if err != nil {
		t.Fatalf("preview soft ripple Session Publish: %v", err)
	}
	if _, err := rundownClient.Publish(t.Context(), connect.NewRequest(&rundownv1.PublishRequest{
		EventId: 1, CommandId: "publish-soft-ripple-session",
		Confirmation: &rundownv1.PublishConfirmation{
			DraftRevision:     preview.Msg.GetDraftRevision(),
			PublishedRevision: preview.Msg.GetPublishedRevision(),
			ChangeIds:         preview.Msg.GetChangeIds(), Fingerprint: preview.Msg.GetFingerprint(),
		},
	})); err != nil {
		t.Fatalf("publish soft ripple Session: %v", err)
	}
	if sessionID <= 0 {
		t.Fatal("soft ripple Session ID is missing")
	}
	return sessionID
}

func publishEventListing(
	t *testing.T,
	client *http.Client,
	server *runningServer,
	name, slug, startDate, endDate, timezone, dayBoundary string,
) {
	t.Helper()
	result := requestJSONMethod(
		t.Context(),
		http.MethodPut,
		client,
		server.address,
		"/crew/events/1",
		map[string]any{
			"name": name, "public": true, "public_slug": slug,
			"planned_start_date": startDate, "planned_end_date": endDate,
			"timezone": timezone, "event_locale": "en-GB",
			"content_language": "en-GB", "event_day_boundary": dayBoundary,
			"expected_revision": 1, "command_id": "publish-test-event-listing",
		},
	)
	if result.err != nil || result.status != http.StatusOK {
		t.Fatalf("publish test Event listing = %d %q, %v", result.status, result.body, result.err)
	}
}

func addPlacementLane(
	t *testing.T,
	client *http.Client,
	server *runningServer,
) (int64, int64) {
	t.Helper()
	rundownClient := rundownv1connect.NewRundownServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	current, err := rundownClient.GetCrewRundown(
		t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}),
	)
	if err != nil {
		t.Fatalf("load Rundown before adding placement Lane: %v", err)
	}
	edited, err := rundownClient.EditDraft(t.Context(), connect.NewRequest(
		&rundownv1.EditDraftRequest{
			EventId: 1, CommandId: "add-placement-lane",
			ExpectedDraftRevision: current.Msg.GetDraftRevision(),
			Locations: []*rundownv1.LocationDraft{{
				Ref: "side-hall", Name: "Side Hall",
			}},
			Lanes: []*rundownv1.LaneDraft{{
				Ref: "side-lane", Name: "Side Lane",
				Location: &rundownv1.TargetRef{
					Target: &rundownv1.TargetRef_Ref{Ref: "side-hall"},
				},
			}},
		},
	))
	if err != nil {
		t.Fatalf("add placement Lane: %v", err)
	}
	var locationID, laneID int64
	changeIDs := make([]int64, 0, len(edited.Msg.GetChanges()))
	for _, change := range edited.Msg.GetChanges() {
		changeIDs = append(changeIDs, change.GetId())
		switch change.GetKind() {
		case "CreateLocation":
			locationID = change.GetTargetId()
		case "CreateLane":
			laneID = change.GetTargetId()
		}
	}
	preview, err := rundownClient.PublishPreview(t.Context(), connect.NewRequest(
		&rundownv1.PublishPreviewRequest{EventId: 1, ChangeIds: changeIDs},
	))
	if err != nil {
		t.Fatalf("preview placement Lane Publish: %v", err)
	}
	if _, err := rundownClient.Publish(t.Context(), connect.NewRequest(
		&rundownv1.PublishRequest{
			EventId: 1, CommandId: "publish-placement-lane",
			Confirmation: &rundownv1.PublishConfirmation{
				DraftRevision:     preview.Msg.GetDraftRevision(),
				PublishedRevision: preview.Msg.GetPublishedRevision(),
				ChangeIds:         preview.Msg.GetChangeIds(),
				Fingerprint:       preview.Msg.GetFingerprint(),
			},
		},
	)); err != nil {
		t.Fatalf("publish placement Lane: %v", err)
	}
	if locationID <= 0 || laneID <= 0 {
		t.Fatalf("placement identity IDs = Location %d, Lane %d", locationID, laneID)
	}
	return locationID, laneID
}

func prepareAndActivateSecondEvent(t *testing.T, client *http.Client, server *runningServer) {
	t.Helper()
	assertJSONRequest(
		t, client, server.address, "/admin/events",
		map[string]string{
			"name": "Revision 2100", "planned_start_date": "2100-09-01",
			"planned_end_date": "2100-09-02", "timezone": "Europe/Berlin",
			"event_locale": "en-GB", "content_language": "en-GB",
			"event_day_boundary": "06:00", "command_id": "create-second-display-event",
		},
		http.StatusCreated,
		"{\"id\":2,\"name\":\"Revision 2100\",\"planned_start_date\":\"2100-09-01\",\"planned_end_date\":\"2100-09-02\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"en-GB\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	assertJSONRequest(
		t, client, server.address, "/admin/events/2/grants",
		map[string]any{"account_id": 1, "role": "Producer", "command_id": "grant-second-display-event"},
		http.StatusCreated, "{\"event_id\":2,\"account_id\":1,\"role\":\"Producer\"}\n",
	)
	rundownClient := rundownv1connect.NewRundownServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	start := time.Date(2100, 9, 1, 8, 0, 0, 0, time.UTC)
	edited, err := rundownClient.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 2, CommandId: "edit-second-display-event", ExpectedDraftRevision: 0,
		Locations: []*rundownv1.LocationDraft{{Ref: "annex", Name: "Annex"}},
		Lanes: []*rundownv1.LaneDraft{{
			Ref: "annex-lane", Name: "Annex Lane",
			Location: &rundownv1.TargetRef{Target: &rundownv1.TargetRef_Ref{Ref: "annex"}},
		}},
		Sessions: []*rundownv1.SessionDraft{{
			Ref: "annex-opening", Title: "Annex Opening",
			Type:               rundownv1.SessionType_SESSION_TYPE_PRESENTATION,
			AudienceVisibility: rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_PUBLIC,
			PlannedStart:       timestamppb.New(start), PlannedEnd: timestamppb.New(start.Add(time.Hour)),
			TimingPolicy:    rundownv1.TimingPolicy_TIMING_POLICY_FIXED_END,
			MinimumDuration: durationpb.New(30 * time.Minute),
			StartBoundary:   rundownv1.Boundary_BOUNDARY_SOFT, EndBoundary: rundownv1.Boundary_BOUNDARY_SOFT,
			Locations: []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Ref{Ref: "annex"}}},
			Lanes:     []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Ref{Ref: "annex-lane"}}},
		}},
	}))
	if err != nil {
		t.Fatalf("Edit second Event Draft: %v", err)
	}
	changeIDs := make([]int64, 0, len(edited.Msg.GetChanges()))
	for _, change := range edited.Msg.GetChanges() {
		changeIDs = append(changeIDs, change.GetId())
	}
	preview, err := rundownClient.PublishPreview(t.Context(), connect.NewRequest(&rundownv1.PublishPreviewRequest{
		EventId: 2, ChangeIds: changeIDs,
	}))
	if err != nil {
		t.Fatalf("Preview second Event Publish: %v", err)
	}
	if _, publishErr := rundownClient.Publish(t.Context(), connect.NewRequest(&rundownv1.PublishRequest{
		EventId: 2, CommandId: "publish-second-display-event",
		Confirmation: &rundownv1.PublishConfirmation{
			DraftRevision: preview.Msg.GetDraftRevision(), PublishedRevision: preview.Msg.GetPublishedRevision(),
			ChangeIds: preview.Msg.GetChangeIds(), Fingerprint: preview.Msg.GetFingerprint(),
		},
	})); publishErr != nil {
		t.Fatalf("Publish second Event: %v", publishErr)
	}
	activationClient := activationv1connect.NewActivationServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	preflight, err := activationClient.Preflight(t.Context(), connect.NewRequest(&activationv1.PreflightRequest{EventId: 2}))
	if err != nil {
		t.Fatalf("Preflight second Event: %v", err)
	}
	if _, err := activationClient.Activate(t.Context(), connect.NewRequest(&activationv1.ActivateRequest{
		EventId: 2, CommandId: "activate-second-display-event", Confirmation: preflight.Msg.GetConfirmation(),
	})); err != nil {
		t.Fatalf("Activate second Event: %v", err)
	}
}

func TestEventCreationRejectsInvalidTimezoneAndLocale(t *testing.T) {
	client, server := startAuthenticatedAdministrator(t)
	valid := map[string]string{
		"name":               "Revision 2026",
		"planned_start_date": "2026-08-21",
		"planned_end_date":   "2026-08-23",
		"timezone":           "Europe/Berlin",
		"event_locale":       "de-DE",
		"event_day_boundary": "06:00",
		"command_id":         "create-event-invalid",
	}
	tests := []struct {
		name     string
		field    string
		value    string
		wantBody string
	}{
		{
			name: "timezone", field: "timezone", value: "Mars/Olympus_Mons",
			wantBody: `{"field":"timezone","message":"must be a recognized IANA timezone such as Europe/Berlin"}` + "\n",
		},
		{
			name: "Event Locale", field: "event_locale", value: "not_a_locale",
			wantBody: `{"field":"event_locale","message":"must be a recognized BCP 47 language tag such as en-GB"}` + "\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := make(map[string]string, len(valid))
			maps.Copy(input, valid)
			input[test.field] = test.value
			input["command_id"] = "create-event-invalid-" + strings.ReplaceAll(strings.ToLower(test.name), " ", "-")
			assertJSONRequest(
				t, client, server.address, "/admin/events", input,
				http.StatusUnprocessableEntity, test.wantBody,
			)
		})
	}
	server.stop(t)
}

func TestAdministratorCreatesIndividualAccount(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	created := assertJSONRequest(
		t,
		administrator,
		server.address,
		"/admin/accounts",
		map[string]string{
			"name":       "Pat Producer",
			"password":   "producer correct horse battery staple",
			"command_id": "create-account-pat",
		},
		http.StatusCreated,
		"{\"id\":2,\"name\":\"Pat Producer\",\"administrator\":false}\n",
	)
	_ = created

	producer := authenticatedClient(t)
	assertJSONRequest(
		t,
		producer,
		server.address,
		"/auth/sign-in",
		map[string]string{
			"name":     "Pat Producer",
			"password": "producer correct horse battery staple",
		},
		http.StatusNoContent,
		"",
	)
	response := get(t, producer, server.address, "/auth/session")
	if err := response.Body.Close(); err != nil {
		t.Errorf("close created Account session response: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Errorf("created Account sign-in status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	server.stop(t)
}

func TestAdministratorGrantsOperatorAccess(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	assertJSONRequest(
		t, administrator, server.address, "/admin/events",
		validEventInput(), http.StatusCreated,
		"{\"id\":1,\"name\":\"Revision 2026\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts",
		map[string]string{
			"name": "Opal Operator", "password": "operator correct horse battery staple",
			"command_id": "create-account-opal",
		},
		http.StatusCreated, "{\"id\":2,\"name\":\"Opal Operator\",\"administrator\":false}\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/events/1/grants",
		map[string]any{
			"account_id": 2, "role": "Operator", "command_id": "grant-opal-operator",
			"display_group_keys": []string{"crew:stage"},
			"capabilities":       []string{"EmergencyAlert", "ViewResults"},
		},
		http.StatusCreated,
		"{\"event_id\":1,\"account_id\":2,\"role\":\"Operator\",\"display_group_keys\":[\"crew:stage\"],\"capabilities\":[\"EmergencyAlert\",\"ViewResults\"]}\n",
	)
	server.stop(t)
}

func TestUnscopedOperatorCannotStartSession(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	operator := provisionOperatorWithLanes(t, administrator, server, nil)
	client := sessionv1connect.NewSessionControlServiceClient(
		operator, "http://"+server.address, connect.WithProtoJSON(),
	)
	_, err := client.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "unscoped-operator-start",
		ExpectedLiveStateRevision: proto.Int64(0),
	}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("unscoped Operator Start error = %v, want PermissionDenied", err)
	}
	server.stop(t)
}

func TestConcurrentDraftEditsConflictOnlyOnChangedFacts(t *testing.T) {
	producer, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, producer, server)
	client := rundownv1connect.NewRundownServiceClient(
		producer, "http://"+server.address, connect.WithProtoJSON(),
	)
	current, err := client.GetCrewRundown(t.Context(), connect.NewRequest(
		&rundownv1.GetCrewRundownRequest{EventId: 1},
	))
	if err != nil {
		t.Fatalf("read current Rundown revision: %v", err)
	}
	baseRevision := current.Msg.GetDraftRevision()

	titleEdit, err := client.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "concurrent-title", ExpectedDraftRevision: baseRevision,
		Sessions: []*rundownv1.SessionDraft{{
			Id: sessionID, Title: "Opening Keynote Updated",
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}},
		}},
	}))
	if err != nil {
		t.Fatalf("edit Session title: %v", err)
	}
	notesEdit, err := client.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "concurrent-notes", ExpectedDraftRevision: baseRevision,
		Sessions: []*rundownv1.SessionDraft{{
			Id: sessionID, CrewNotes: "updated cue notes",
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"crew_notes"}},
		}},
	}))
	if err != nil {
		t.Fatalf("independent stale Session edit: %v", err)
	}
	if notesEdit.Msg.GetDraftRevision() != titleEdit.Msg.GetDraftRevision()+1 {
		t.Errorf("independent Draft revisions = %d then %d, want consecutive", titleEdit.Msg.GetDraftRevision(), notesEdit.Msg.GetDraftRevision())
	}

	_, err = client.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "conflicting-title", ExpectedDraftRevision: baseRevision,
		Sessions: []*rundownv1.SessionDraft{{
			Id: sessionID, Title: "Last write must not win",
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}},
		}},
	}))
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Errorf("overlapping stale Edit error = %v, want Aborted", err)
	}
	var conflictErr *connect.Error
	if !errors.As(err, &conflictErr) || len(conflictErr.Details()) != 1 {
		t.Fatalf("overlapping stale Edit detail = %v, want one current-state detail", err)
	}
	conflictValue, detailErr := conflictErr.Details()[0].Value()
	if detailErr != nil {
		t.Fatalf("decode Draft conflict detail: %v", detailErr)
	}
	conflict, ok := conflictValue.(*rundownv1.DraftRevisionConflict)
	if !ok || conflict.GetCurrentDraftRevision() != notesEdit.Msg.GetDraftRevision() ||
		len(conflict.GetOverlappingChanges()) != 1 || conflict.GetOverlappingChanges()[0].GetFactKey() != "title" ||
		conflict.GetOverlappingChanges()[0].GetCurrentValueJson() != `"Opening Keynote Updated"` {
		t.Errorf("Draft conflict detail = %+v", conflictValue)
	}

	preview, err := client.PublishPreview(t.Context(), connect.NewRequest(&rundownv1.PublishPreviewRequest{
		EventId: 1, ChangeIds: []int64{titleEdit.Msg.GetChanges()[0].GetId()},
	}))
	if err != nil {
		t.Fatalf("preview selected title fact: %v", err)
	}
	if _, err = client.Publish(t.Context(), connect.NewRequest(&rundownv1.PublishRequest{
		EventId: 1, CommandId: "publish-selected-title",
		Confirmation: &rundownv1.PublishConfirmation{
			DraftRevision: preview.Msg.GetDraftRevision(), PublishedRevision: preview.Msg.GetPublishedRevision(),
			ChangeIds: preview.Msg.GetChangeIds(), Fingerprint: preview.Msg.GetFingerprint(),
		},
	})); err != nil {
		t.Fatalf("publish selected title fact: %v", err)
	}
	published, err := client.GetCrewRundown(t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}))
	if err != nil {
		t.Fatalf("read partially Published Rundown: %v", err)
	}
	var publishedSession *rundownv1.CrewSession
	for _, candidate := range published.Msg.GetSessions() {
		if candidate.GetId() == sessionID {
			publishedSession = candidate
		}
	}
	if publishedSession == nil || publishedSession.GetTitle() != "Opening Keynote Updated" || publishedSession.GetCrewNotes() == "updated cue notes" {
		t.Errorf("partially Published Session = %+v, want selected title without unselected notes", publishedSession)
	}
	_, staleAfterPublishErr := client.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "stale-title-after-publish", ExpectedDraftRevision: baseRevision,
		Sessions: []*rundownv1.SessionDraft{{Id: sessionID, Title: "Stale after Publish",
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}}}},
	}))
	if connect.CodeOf(staleAfterPublishErr) != connect.CodeAborted {
		t.Errorf("stale title after Publish = %v, want Aborted", staleAfterPublishErr)
	}
	var staleAfterPublishConnectErr *connect.Error
	if !errors.As(staleAfterPublishErr, &staleAfterPublishConnectErr) || len(staleAfterPublishConnectErr.Details()) != 1 {
		t.Fatalf("stale title after Publish detail = %v", staleAfterPublishErr)
	}
	staleAfterPublishValue, detailErr := staleAfterPublishConnectErr.Details()[0].Value()
	staleAfterPublishConflict, ok := staleAfterPublishValue.(*rundownv1.DraftRevisionConflict)
	if detailErr != nil || !ok || len(staleAfterPublishConflict.GetOverlappingChanges()) != 1 ||
		staleAfterPublishConflict.GetOverlappingChanges()[0].GetCurrentValueJson() != `"Opening Keynote Updated"` {
		t.Errorf("stale title after Publish current state = %+v, %v", staleAfterPublishValue, detailErr)
	}
	if _, err = client.PublishPreview(t.Context(), connect.NewRequest(&rundownv1.PublishPreviewRequest{
		EventId: 1, ChangeIds: []int64{notesEdit.Msg.GetChanges()[0].GetId()},
	})); err != nil {
		t.Errorf("unselected notes no longer effective: %v", err)
	}
	discarded, err := client.DiscardDraftChanges(t.Context(), connect.NewRequest(&rundownv1.DiscardDraftChangesRequest{
		EventId: 1, CommandId: "discard-unselected-notes", ExpectedDraftRevision: published.Msg.GetDraftRevision(),
		ChangeIds: []int64{notesEdit.Msg.GetChanges()[0].GetId()},
	}))
	if err != nil {
		t.Fatalf("discard unselected notes: %v", err)
	}
	if len(discarded.Msg.GetChanges()) != 1 || discarded.Msg.GetChanges()[0].GetStatus() != "Discarded" {
		t.Errorf("Discard response = %+v", discarded.Msg)
	}
	discardPreview, err := client.PublishPreview(t.Context(), connect.NewRequest(&rundownv1.PublishPreviewRequest{
		EventId: 1, ChangeIds: []int64{notesEdit.Msg.GetChanges()[0].GetId()},
	}))
	if err != nil || len(discardPreview.Msg.GetValidationFailures()) == 0 {
		t.Errorf("discarded notes Preview = %+v, %v; want validation failure", discardPreview, err)
	}
	reverted, err := client.RevertDraftChange(t.Context(), connect.NewRequest(&rundownv1.RevertDraftChangeRequest{
		EventId: 1, CommandId: "revert-published-title", ExpectedDraftRevision: discarded.Msg.GetDraftRevision(),
		ChangeId: titleEdit.Msg.GetChanges()[0].GetId(),
	}))
	if err != nil {
		t.Fatalf("revert Published title: %v", err)
	}
	if len(reverted.Msg.GetChanges()) != 1 || reverted.Msg.GetChanges()[0].GetKind() != "RevertSession" {
		t.Fatalf("Revert response = %+v", reverted.Msg)
	}
	revertPreview, err := client.PublishPreview(t.Context(), connect.NewRequest(&rundownv1.PublishPreviewRequest{
		EventId: 1, ChangeIds: []int64{reverted.Msg.GetChanges()[0].GetId()},
	}))
	if err != nil {
		t.Fatalf("preview Draft Revert: %v", err)
	}
	if _, err = client.Publish(t.Context(), connect.NewRequest(&rundownv1.PublishRequest{
		EventId: 1, CommandId: "publish-reverted-title", Confirmation: &rundownv1.PublishConfirmation{
			DraftRevision: revertPreview.Msg.GetDraftRevision(), PublishedRevision: revertPreview.Msg.GetPublishedRevision(),
			ChangeIds: revertPreview.Msg.GetChangeIds(), Fingerprint: revertPreview.Msg.GetFingerprint(),
		},
	})); err != nil {
		t.Fatalf("publish Draft Revert: %v", err)
	}
	revertedRundown, err := client.GetCrewRundown(t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}))
	if err != nil {
		t.Fatalf("read Reverted Rundown: %v", err)
	}
	for _, candidate := range revertedRundown.Msg.GetSessions() {
		if candidate.GetId() == sessionID && candidate.GetTitle() != "Opening Keynote" {
			t.Errorf("Reverted Published title = %q", candidate.GetTitle())
		}
	}
	server.stop(t)
}

func TestConcurrentSessionMembershipEditsUsePerMemberFacts(t *testing.T) {
	producer, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, producer, server)
	client := rundownv1connect.NewRundownServiceClient(producer, "http://"+server.address, connect.WithProtoJSON())
	current, err := client.GetCrewRundown(t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}))
	if err != nil {
		t.Fatalf("read current Rundown: %v", err)
	}
	created, err := client.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "create-membership-lanes", ExpectedDraftRevision: current.Msg.GetDraftRevision(),
		Locations: []*rundownv1.LocationDraft{{Ref: "membership-location-two", Name: "Membership Hall Two"}, {Ref: "membership-location-three", Name: "Membership Hall Three"}},
		Lanes: []*rundownv1.LaneDraft{
			{Ref: "membership-two", Name: "Membership Two", Location: &rundownv1.TargetRef{Target: &rundownv1.TargetRef_Ref{Ref: "membership-location-two"}}},
			{Ref: "membership-three", Name: "Membership Three", Location: &rundownv1.TargetRef{Target: &rundownv1.TargetRef_Ref{Ref: "membership-location-three"}}},
		},
		Sessions: []*rundownv1.SessionDraft{{
			Id: sessionID,
			AddLanes: []*rundownv1.TargetRef{
				{Target: &rundownv1.TargetRef_Ref{Ref: "membership-two"}},
				{Target: &rundownv1.TargetRef_Ref{Ref: "membership-three"}},
			},
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"add_lanes"}},
		}},
	}))
	if err != nil {
		t.Fatalf("create Session membership facts: %v", err)
	}
	var laneIDs []int64
	var firstLaneCreationID int64
	for _, change := range created.Msg.GetChanges() {
		if change.GetKind() == "CreateLane" {
			laneIDs = append(laneIDs, change.GetTargetId())
			if firstLaneCreationID == 0 {
				firstLaneCreationID = change.GetId()
			}
		}
	}
	if len(laneIDs) != 2 {
		t.Fatalf("created Lane IDs = %v", laneIDs)
	}
	baseRevision := created.Msg.GetDraftRevision()
	first, err := client.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "remove-membership-two", ExpectedDraftRevision: baseRevision,
		Sessions: []*rundownv1.SessionDraft{{Id: sessionID,
			RemoveLanes: []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Id{Id: laneIDs[0]}}},
			UpdateMask:  &fieldmaskpb.FieldMask{Paths: []string{"remove_lanes"}},
		}},
	}))
	if err != nil {
		t.Fatalf("remove first independent membership: %v", err)
	}
	second, err := client.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "remove-membership-three", ExpectedDraftRevision: baseRevision,
		Sessions: []*rundownv1.SessionDraft{{Id: sessionID,
			RemoveLanes: []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Id{Id: laneIDs[1]}}},
			UpdateMask:  &fieldmaskpb.FieldMask{Paths: []string{"remove_lanes"}},
		}},
	}))
	if err != nil || second.Msg.GetDraftRevision() != first.Msg.GetDraftRevision()+1 {
		t.Fatalf("independent stale membership edit = %+v, %v", second, err)
	}
	_, err = client.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "repeat-membership-two", ExpectedDraftRevision: baseRevision,
		Sessions: []*rundownv1.SessionDraft{{Id: sessionID,
			RemoveLanes: []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Id{Id: laneIDs[0]}}},
			UpdateMask:  &fieldmaskpb.FieldMask{Paths: []string{"remove_lanes"}},
		}},
	}))
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Errorf("same-membership stale edit = %v, want Aborted", err)
	}
	reverted, err := client.RevertDraftChange(t.Context(), connect.NewRequest(&rundownv1.RevertDraftChangeRequest{
		EventId: 1, CommandId: "revert-membership-two-removal", ExpectedDraftRevision: second.Msg.GetDraftRevision(),
		ChangeId: first.Msg.GetChanges()[0].GetId(),
	}))
	if err != nil {
		t.Fatalf("Revert membership removal: %v", err)
	}
	preview, err := client.PublishPreview(t.Context(), connect.NewRequest(&rundownv1.PublishPreviewRequest{
		EventId: 1, ChangeIds: []int64{reverted.Msg.GetChanges()[0].GetId()},
	}))
	if err != nil {
		t.Fatalf("Preview membership Revert: %v", err)
	}
	if !slices.Contains(preview.Msg.GetAutoIncludedChangeIds(), firstLaneCreationID) {
		t.Errorf("membership Revert auto-included changes = %v, want Lane creation %d", preview.Msg.GetAutoIncludedChangeIds(), firstLaneCreationID)
	}
	server.stop(t)
}

func TestOperatorStartsPublishedSessionDurably(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	operator := provisionOperator(t, administrator, server)
	client := sessionv1connect.NewSessionControlServiceClient(
		operator, "http://"+server.address, connect.WithProtoJSON(),
	)

	started, err := client.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "start-keynote",
		ExpectedLiveStateRevision: proto.Int64(0),
	}))
	if err != nil {
		t.Fatalf("Start Session RPC: %v", err)
	}
	state := started.Msg.GetState()
	if state.GetSessionId() != sessionID || state.GetSessionRunId() <= 0 ||
		state.GetLifecycle() != sessionv1.SessionLifecycle_SESSION_LIFECYCLE_LIVE ||
		state.GetLiveStateRevision() != 1 || state.GetActualStart() == nil || state.GetActualEnd() != nil {
		t.Errorf("started Session state = %+v", state)
	}
	wantActualStart := state.GetActualStart().AsTime().In(time.FixedZone("CEST", 2*60*60)).Format(time.RFC3339)
	public := get(t, authenticatedClient(t), server.address, "/events/revision-2099/schedule")
	publicBody, readErr := io.ReadAll(public.Body)
	closeErr := public.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Live public Schedule: %v", err)
	}
	if public.StatusCode != http.StatusOK || !strings.Contains(string(publicBody), "Status: Live") ||
		!strings.Contains(string(publicBody), wantActualStart) {
		t.Errorf("Live public Schedule = %d %q", public.StatusCode, publicBody)
	}

	dataDir := server.dataDir
	bin := server.bin
	server.stop(t)
	restarted := startBeamers(t, bin, dataDir)
	deepLink := get(t, authenticatedClient(t), restarted.address, fmt.Sprintf("/events/revision-2099/schedule/sessions/%d", sessionID))
	deepLinkBody, readErr := io.ReadAll(deepLink.Body)
	closeErr = deepLink.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read restarted Live public Session: %v", err)
	}
	if deepLink.StatusCode != http.StatusOK || !strings.Contains(string(deepLinkBody), "Status: Live") ||
		!strings.Contains(string(deepLinkBody), wantActualStart) {
		t.Errorf("restarted Live public Session = %d %q", deepLink.StatusCode, deepLinkBody)
	}
	restarted.stop(t)
}

func TestOperatorCancelsScheduledSessionWithPublicMessage(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	operator := provisionOperator(t, administrator, server)
	client := sessionv1connect.NewSessionControlServiceClient(
		operator, "http://"+server.address, connect.WithProtoJSON(),
	)
	crewNotes := strings.Repeat("n", 1001)
	request := &sessionv1.CancelSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "cancel-keynote",
		ExpectedLiveStateRevision: proto.Int64(0),
		Confirmed:                 true,
		PublicCancellationMessage: "Speaker travel was disrupted.",
		CrewNotes:                 crewNotes,
	}

	unconfirmedMessage := proto.Clone(request)
	unconfirmed, ok := unconfirmedMessage.(*sessionv1.CancelSessionRequest)
	if !ok {
		t.Fatalf("cloned Cancel Session request type = %T", unconfirmedMessage)
	}
	unconfirmed.CommandId = "reject-unconfirmed-cancel"
	unconfirmed.Confirmed = false
	for range 2 {
		_, unconfirmedErr := client.CancelSession(
			t.Context(), connect.NewRequest(unconfirmed),
		)
		if connect.CodeOf(unconfirmedErr) != connect.CodeFailedPrecondition {
			t.Fatalf("unconfirmed Cancel Session = %v", unconfirmedErr)
		}
	}
	invalidTextMessage := proto.Clone(request)
	invalidText, ok := invalidTextMessage.(*sessionv1.CancelSessionRequest)
	if !ok {
		t.Fatalf("cloned Cancel Session text request type = %T", invalidTextMessage)
	}
	invalidText.CommandId = "reject-cancellation-text"
	invalidText.PublicCancellationMessage = strings.Repeat("x", 10001)
	for range 2 {
		_, invalidTextErr := client.CancelSession(
			t.Context(), connect.NewRequest(invalidText),
		)
		if connect.CodeOf(invalidTextErr) != connect.CodeInvalidArgument {
			t.Fatalf("invalid Cancel Session text = %v", invalidTextErr)
		}
	}
	audits, _ := readAuditHistory(t, administrator, server.address)
	rejections := map[string]int{
		"cancel_confirmation_required": 1,
		"cancellation_text_invalid":    1,
	}
	for _, entry := range audits {
		if entry.Action == "CancelSession" &&
			entry.TargetType == "Session" &&
			entry.TargetID == strconv.FormatInt(sessionID, 10) &&
			entry.Outcome == "Rejected" {
			rejections[entry.Reason]--
		}
	}
	for reason, remaining := range rejections {
		if remaining != 0 {
			t.Fatalf("Cancel Session rejection %q remaining = %d", reason, remaining)
		}
	}
	canceled, err := client.CancelSession(t.Context(), connect.NewRequest(request))
	if err != nil {
		t.Fatalf("Cancel Session RPC: %v", err)
	}
	if canceled.Msg.GetState().GetLifecycle() !=
		sessionv1.SessionLifecycle_SESSION_LIFECYCLE_CANCELED ||
		canceled.Msg.GetState().GetLiveStateRevision() != 1 ||
		canceled.Msg.GetState().GetSessionRunId() != 0 {
		t.Fatalf("canceled Session state = %+v", canceled.Msg.GetState())
	}
	retried, err := client.CancelSession(t.Context(), connect.NewRequest(request))
	if err != nil || !proto.Equal(retried.Msg, canceled.Msg) {
		t.Fatalf("exact Cancel Session retry = %+v, %v", retried, err)
	}
	public := get(
		t, authenticatedClient(t), server.address, "/events/revision-2099/schedule",
	)
	body, readErr := io.ReadAll(public.Body)
	closeErr := public.Body.Close()
	if joinedErr := errors.Join(readErr, closeErr); joinedErr != nil {
		t.Fatalf("read canceled public Session: %v", joinedErr)
	}
	if public.StatusCode != http.StatusOK ||
		!strings.Contains(string(body), "Status: Canceled") ||
		!strings.Contains(string(body), "Speaker travel was disrupted.") ||
		strings.Contains(string(body), crewNotes) {
		t.Fatalf("canceled public Session = %d %q", public.StatusCode, body)
	}
	history, err := client.GetSessionHistory(t.Context(), connect.NewRequest(
		&sessionv1.GetSessionHistoryRequest{EventId: 1, SessionId: sessionID},
	))
	if err != nil || len(history.Msg.GetCancellations()) != 1 ||
		history.Msg.GetCancellations()[0].SessionRunId != nil ||
		history.Msg.GetCancellations()[0].GetPublicCancellationMessage() !=
			"Speaker travel was disrupted." ||
		history.Msg.GetCancellations()[0].GetCrewNotes() != crewNotes {
		t.Fatalf("cancellation history = %+v, %v", history, err)
	}
	server.stop(t)
}

func TestProducerReinstatesCanceledLiveSessionFromPlacementPreview(t *testing.T) {
	producer, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, producer, server)
	locationID, laneID := addPlacementLane(t, producer, server)
	operator := provisionOperator(t, producer, server)
	operatorClient := sessionv1connect.NewSessionControlServiceClient(
		operator, "http://"+server.address, connect.WithProtoJSON(),
	)
	started, err := operatorClient.StartSession(t.Context(), connect.NewRequest(
		&sessionv1.StartSessionRequest{
			EventId: 1, SessionId: sessionID, CommandId: "start-before-cancel",
			ExpectedLiveStateRevision: proto.Int64(0),
		},
	))
	if err != nil {
		t.Fatalf("Start Session before cancellation: %v", err)
	}
	canceled, err := operatorClient.CancelSession(t.Context(), connect.NewRequest(
		&sessionv1.CancelSessionRequest{
			EventId: 1, SessionId: sessionID, CommandId: "cancel-live-keynote",
			ExpectedLiveStateRevision: new(started.Msg.GetState().GetLiveStateRevision()),
			Confirmed:                 true,
		},
	))
	if err != nil {
		t.Fatalf("Cancel Live Session: %v", err)
	}
	if canceled.Msg.GetState().GetActualEnd() == nil {
		t.Fatalf("canceled Live Session has no Actual End: %+v", canceled.Msg.GetState())
	}
	canceledPublic := get(
		t, authenticatedClient(t), server.address,
		fmt.Sprintf("/events/revision-2099/schedule/sessions/%d", sessionID),
	)
	canceledBody, readErr := io.ReadAll(canceledPublic.Body)
	closeErr := canceledPublic.Body.Close()
	if joinedErr := errors.Join(readErr, closeErr); joinedErr != nil {
		t.Fatalf("read canceled Live Session: %v", joinedErr)
	}
	if canceledPublic.StatusCode != http.StatusOK ||
		!strings.Contains(string(canceledBody), "Status: Canceled") ||
		!strings.Contains(string(canceledBody), "Canceled.") {
		t.Fatalf(
			"canceled Live public Session = %d %q",
			canceledPublic.StatusCode, canceledBody,
		)
	}

	producerClient := sessionv1connect.NewSessionControlServiceClient(
		producer, "http://"+server.address, connect.WithProtoJSON(),
	)
	proposedStart := time.Date(2099, 8, 21, 9, 30, 0, 0, time.UTC)
	hardPreview, err := producerClient.PreviewReinstateSession(
		t.Context(), connect.NewRequest(&sessionv1.PreviewReinstateSessionRequest{
			EventId: 1, SessionId: sessionID, ForecastStart: timestamppb.New(proposedStart),
			LaneIds: []int64{1}, LocationIds: []int64{1},
		}),
	)
	if err != nil || !hardPreview.Msg.GetRequiresHardBoundaryConfirmation() ||
		len(hardPreview.Msg.GetEffects()) < 2 {
		t.Fatalf("Hard placement preview = %+v, %v", hardPreview, err)
	}
	_, missingHardErr := producerClient.ReinstateSession(
		t.Context(), connect.NewRequest(&sessionv1.ReinstateSessionRequest{
			EventId: 1, SessionId: sessionID, CommandId: "reject-hard-reinstatement",
			ExpectedLiveStateRevision: new(canceled.Msg.GetState().GetLiveStateRevision()),
			ForecastStart:             timestamppb.New(proposedStart),
			LaneIds:                   []int64{1},
			LocationIds:               []int64{1},
			PreviewFingerprint:        hardPreview.Msg.GetPreviewFingerprint(),
			Confirmed:                 true,
		}),
	)
	if connect.CodeOf(missingHardErr) != connect.CodeFailedPrecondition {
		t.Fatalf("Reinstate without Hard confirmation = %v", missingHardErr)
	}
	previewRequest := &sessionv1.PreviewReinstateSessionRequest{
		EventId: 1, SessionId: sessionID, ForecastStart: timestamppb.New(proposedStart),
		LaneIds: []int64{laneID}, LocationIds: []int64{locationID},
	}
	preview, err := producerClient.PreviewReinstateSession(
		t.Context(), connect.NewRequest(previewRequest),
	)
	if err != nil {
		t.Fatalf("Preview Reinstate Session: %v", err)
	}
	if preview.Msg.GetPreviewFingerprint() == "" ||
		preview.Msg.GetRequiresHardBoundaryConfirmation() ||
		!slices.Equal(preview.Msg.GetCurrentLaneIds(), []int64{1}) ||
		!slices.Equal(preview.Msg.GetProposedLaneIds(), []int64{laneID}) ||
		!slices.Equal(preview.Msg.GetCurrentLocationIds(), []int64{1}) ||
		!slices.Equal(preview.Msg.GetProposedLocationIds(), []int64{locationID}) ||
		len(preview.Msg.GetEffects()) != 1 ||
		len(preview.Msg.GetChanges()) != 1 ||
		preview.Msg.GetChanges()[0].GetSessionId() != sessionID {
		t.Fatalf("Reinstate Session preview = %+v", preview.Msg)
	}
	request := &sessionv1.ReinstateSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "reinstate-keynote",
		ExpectedLiveStateRevision: new(canceled.Msg.GetState().GetLiveStateRevision()),
		ForecastStart:             timestamppb.New(proposedStart),
		LaneIds:                   []int64{laneID},
		LocationIds:               []int64{locationID},
		PreviewFingerprint:        preview.Msg.GetPreviewFingerprint(),
	}
	_, unconfirmedErr := producerClient.ReinstateSession(
		t.Context(), connect.NewRequest(request),
	)
	if connect.CodeOf(unconfirmedErr) != connect.CodeFailedPrecondition {
		t.Fatalf("unconfirmed Reinstate Session = %v", unconfirmedErr)
	}
	request.Confirmed = true
	request.CommandId = "reinstate-keynote-confirmed"
	databasePath := filepath.Join(server.dataDir, "beamers.db")
	if fixtureErr := storetest.FailSessionForecastUpdate(
		t.Context(), databasePath, sessionID,
	); fixtureErr != nil {
		t.Fatalf("install Reinstate Session rollback fixture: %v", fixtureErr)
	}
	_, rollbackErr := producerClient.ReinstateSession(
		t.Context(), connect.NewRequest(request),
	)
	if connect.CodeOf(rollbackErr) != connect.CodeInternal {
		t.Fatalf("forced Reinstate Session rollback = %v", rollbackErr)
	}
	if fixtureErr := storetest.AllowSessionForecastUpdates(
		t.Context(), databasePath,
	); fixtureErr != nil {
		t.Fatalf("remove Reinstate Session rollback fixture: %v", fixtureErr)
	}
	afterRollback, err := producerClient.PreviewReinstateSession(
		t.Context(), connect.NewRequest(previewRequest),
	)
	if err != nil ||
		afterRollback.Msg.GetPreviewFingerprint() != preview.Msg.GetPreviewFingerprint() {
		t.Fatalf("Reinstate Session rollback changed placement = %+v, %v", afterRollback, err)
	}
	reinstated, err := producerClient.ReinstateSession(
		t.Context(), connect.NewRequest(request),
	)
	if err != nil {
		t.Fatalf("Reinstate Session: %v", err)
	}
	if reinstated.Msg.GetState().GetLifecycle() !=
		sessionv1.SessionLifecycle_SESSION_LIFECYCLE_SCHEDULED ||
		reinstated.Msg.GetState().GetLiveStateRevision() != 3 ||
		!reinstated.Msg.GetPreviousForecastStart().AsTime().Equal(
			started.Msg.GetState().GetActualStart().AsTime(),
		) {
		t.Fatalf("reinstated Session = %+v", reinstated.Msg)
	}
	retried, err := producerClient.ReinstateSession(
		t.Context(), connect.NewRequest(request),
	)
	if err != nil || !proto.Equal(retried.Msg, reinstated.Msg) {
		t.Fatalf("exact Reinstate Session retry = %+v, %v", retried, err)
	}
	history, err := producerClient.GetSessionHistory(t.Context(), connect.NewRequest(
		&sessionv1.GetSessionHistoryRequest{EventId: 1, SessionId: sessionID},
	))
	if err != nil || len(history.Msg.GetRuns()) != 1 ||
		history.Msg.GetRuns()[0].GetActualEnd() == nil ||
		history.Msg.GetRuns()[0].GetOutcome() !=
			sessionv1.SessionRunOutcome_SESSION_RUN_OUTCOME_CANCELED ||
		len(history.Msg.GetCancellations()) != 1 ||
		history.Msg.GetCancellations()[0].GetSessionRunId() !=
			history.Msg.GetRuns()[0].GetId() {
		t.Fatalf("reinstated Session history = %+v, %v", history, err)
	}
	public := get(
		t, authenticatedClient(t), server.address,
		fmt.Sprintf("/events/revision-2099/schedule/sessions/%d", sessionID),
	)
	body, readErr := io.ReadAll(public.Body)
	closeErr = public.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read reinstated public Session: %v", err)
	}
	if public.StatusCode != http.StatusOK ||
		!strings.Contains(string(body), "Status: Scheduled") ||
		!strings.Contains(string(body), "Forecast Start:") ||
		!strings.Contains(string(body), "Location: Side Hall") ||
		!strings.Contains(string(body), "Lane: Side Lane") ||
		strings.Contains(string(body), "Actual Start:") ||
		strings.Contains(string(body), "Actual End:") ||
		!strings.Contains(
			string(body),
			proposedStart.
				In(time.FixedZone("CEST", 2*60*60)).
				Format("02 Jan 2006 15:04 MST"),
		) {
		t.Fatalf("reinstated public Session = %d %q", public.StatusCode, body)
	}
	_, oldLaneScopeErr := operatorClient.StartSession(
		t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
			EventId: 1, SessionId: sessionID, CommandId: "reject-old-lane-scope",
			ExpectedLiveStateRevision: new(reinstated.Msg.GetState().GetLiveStateRevision()),
		}),
	)
	if connect.CodeOf(oldLaneScopeErr) != connect.CodePermissionDenied {
		t.Fatalf("old Lane scope after reinstatement = %v", oldLaneScopeErr)
	}
	server.stop(t)
}

func TestOperatorPreviewsAndAdjustsLiveSessionTarget(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	rippleSessionID := addSoftRippleSession(t, administrator, server)
	operator := provisionOperator(t, administrator, server)
	client := sessionv1connect.NewSessionControlServiceClient(
		operator, "http://"+server.address, connect.WithProtoJSON(),
	)
	started, err := client.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "start-before-target-adjustment",
		ExpectedLiveStateRevision: proto.Int64(0),
	}))
	if err != nil {
		t.Fatalf("Start Session before Adjust Target: %v", err)
	}
	customPreview, err := client.PreviewAdjustTarget(t.Context(), connect.NewRequest(
		&sessionv1.PreviewAdjustTargetRequest{
			EventId: 1, SessionId: sessionID,
			Adjustment: &sessionv1.PreviewAdjustTargetRequest_Custom{
				Custom: durationpb.New(-2 * time.Minute),
			},
		},
	))
	if err != nil || !customPreview.Msg.GetProposedTarget().AsTime().Equal(
		customPreview.Msg.GetCurrentTarget().AsTime().Add(-2*time.Minute),
	) {
		t.Fatalf("custom Adjust Target preview = %+v, %v", customPreview, err)
	}
	_, unknownPresetErr := client.PreviewAdjustTarget(t.Context(), connect.NewRequest(
		&sessionv1.PreviewAdjustTargetRequest{
			EventId: 1, SessionId: sessionID,
			Adjustment: &sessionv1.PreviewAdjustTargetRequest_Preset{
				Preset: durationpb.New(7 * time.Minute),
			},
		},
	))
	if connect.CodeOf(unknownPresetErr) != connect.CodeInvalidArgument {
		t.Fatalf("unknown Adjust Target preset error = %v, want InvalidArgument", unknownPresetErr)
	}
	previewRequest := &sessionv1.PreviewAdjustTargetRequest{
		EventId: 1, SessionId: sessionID,
		Adjustment: &sessionv1.PreviewAdjustTargetRequest_Preset{
			Preset: durationpb.New(5 * time.Minute),
		},
	}
	preview, err := client.PreviewAdjustTarget(t.Context(), connect.NewRequest(previewRequest))
	if err != nil {
		t.Fatalf("Preview Adjust Target: %v", err)
	}
	if preview.Msg.GetPreviewFingerprint() == "" ||
		!preview.Msg.GetProposedTarget().AsTime().Equal(preview.Msg.GetCurrentTarget().AsTime().Add(5*time.Minute)) ||
		len(preview.Msg.GetConfiguredPresets()) != 3 ||
		!preview.Msg.GetRequiresHardBoundaryConfirmation() ||
		len(preview.Msg.GetEffects()) != 1 ||
		preview.Msg.GetEffects()[0].GetSessionId() != rippleSessionID ||
		!preview.Msg.GetEffects()[0].GetProposedForecastStart().AsTime().Equal(
			time.Date(2099, 8, 21, 9, 5, 0, 0, time.UTC),
		) ||
		!preview.Msg.GetEffects()[0].GetProposedForecastEnd().AsTime().Equal(
			time.Date(2099, 8, 21, 10, 0, 0, 0, time.UTC),
		) {
		t.Fatalf("Adjust Target preview = %+v", preview.Msg)
	}
	targetRequest := func(
		commandID string,
		expectedRevision int64,
		fingerprint string,
		confirmed bool,
		hardBoundaryConfirmed bool,
	) *sessionv1.AdjustTargetRequest {
		return &sessionv1.AdjustTargetRequest{
			EventId: 1, SessionId: sessionID, CommandId: commandID,
			ExpectedLiveStateRevision: new(expectedRevision),
			Adjustment: &sessionv1.AdjustTargetRequest_Preset{
				Preset: durationpb.New(5 * time.Minute),
			},
			PreviewFingerprint: fingerprint, Confirmed: confirmed,
			HardBoundaryConfirmed: hardBoundaryConfirmed,
		}
	}
	unconfirmed := targetRequest(
		"reject-unconfirmed-target", started.Msg.GetState().GetLiveStateRevision(),
		preview.Msg.GetPreviewFingerprint(), false, false,
	)
	_, unconfirmedErr := client.AdjustTarget(t.Context(), connect.NewRequest(unconfirmed))
	if connect.CodeOf(unconfirmedErr) != connect.CodeFailedPrecondition {
		t.Fatalf("unconfirmed Adjust Target error = %v, want FailedPrecondition", unconfirmedErr)
	}
	missingHardConfirmation := targetRequest(
		"reject-unconfirmed-hard-boundary", started.Msg.GetState().GetLiveStateRevision(),
		preview.Msg.GetPreviewFingerprint(), true, false,
	)
	_, missingHardErr := client.AdjustTarget(t.Context(), connect.NewRequest(missingHardConfirmation))
	if connect.CodeOf(missingHardErr) != connect.CodeFailedPrecondition {
		t.Fatalf("unconfirmed Hard Boundary error = %v, want FailedPrecondition", missingHardErr)
	}
	corrected, err := client.CorrectLiveDetails(t.Context(), connect.NewRequest(
		&sessionv1.CorrectLiveDetailsRequest{
			EventId: 1, SessionId: sessionID, CommandId: "correct-before-stale-target",
			ExpectedLiveStateRevision: new(started.Msg.GetState().GetLiveStateRevision()),
			Confirmed:                 true, Title: "Target-adjusted Keynote",
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}},
		},
	))
	if err != nil || corrected.Msg.GetState().GetLiveStateRevision() != 2 {
		t.Fatalf("Live correction before stale Adjust Target = %+v, %v", corrected, err)
	}
	staleRequest := targetRequest(
		"reject-stale-target-preview", started.Msg.GetState().GetLiveStateRevision(),
		preview.Msg.GetPreviewFingerprint(), true, true,
	)
	_, staleErr := client.AdjustTarget(t.Context(), connect.NewRequest(staleRequest))
	if connect.CodeOf(staleErr) != connect.CodeAborted {
		t.Fatalf("stale Adjust Target preview error = %v, want Aborted", staleErr)
	}
	freshPreview, err := client.PreviewAdjustTarget(t.Context(), connect.NewRequest(previewRequest))
	if err != nil {
		t.Fatalf("refresh Adjust Target preview: %v", err)
	}
	revisionConflict := targetRequest(
		"reject-target-revision-conflict", started.Msg.GetState().GetLiveStateRevision(),
		freshPreview.Msg.GetPreviewFingerprint(), true, true,
	)
	_, revisionErr := client.AdjustTarget(t.Context(), connect.NewRequest(revisionConflict))
	if connect.CodeOf(revisionErr) != connect.CodeAborted {
		t.Fatalf("Adjust Target revision error = %v, want Aborted", revisionErr)
	}
	databasePath := filepath.Join(server.dataDir, "beamers.db")
	if fixtureErr := storetest.FailSessionRunUpdates(t.Context(), databasePath); fixtureErr != nil {
		t.Fatalf("install Adjust Target rollback fixture: %v", fixtureErr)
	}
	rollbackRequest := targetRequest(
		"force-target-rollback", corrected.Msg.GetState().GetLiveStateRevision(),
		freshPreview.Msg.GetPreviewFingerprint(), true, true,
	)
	_, rollbackErr := client.AdjustTarget(t.Context(), connect.NewRequest(rollbackRequest))
	if connect.CodeOf(rollbackErr) != connect.CodeInternal {
		t.Fatalf("forced Adjust Target rollback error = %v, want Internal", rollbackErr)
	}
	if fixtureErr := storetest.AllowSessionRunUpdates(t.Context(), databasePath); fixtureErr != nil {
		t.Fatalf("remove Adjust Target rollback fixture: %v", fixtureErr)
	}
	request := targetRequest(
		"adjust-keynote-target", corrected.Msg.GetState().GetLiveStateRevision(),
		freshPreview.Msg.GetPreviewFingerprint(), true, true,
	)
	adjusted, err := client.AdjustTarget(t.Context(), connect.NewRequest(request))
	if err != nil {
		t.Fatalf("Adjust Target: %v", err)
	}
	if adjusted.Msg.GetState().GetLiveStateRevision() != 3 ||
		!adjusted.Msg.GetForecastEnd().AsTime().Equal(freshPreview.Msg.GetProposedTarget().AsTime()) ||
		len(adjusted.Msg.GetChanges()) != 2 {
		t.Errorf("adjusted target = %+v", adjusted.Msg)
	}
	retried, err := client.AdjustTarget(t.Context(), connect.NewRequest(request))
	if err != nil || !retried.Msg.GetForecastEnd().AsTime().Equal(adjusted.Msg.GetForecastEnd().AsTime()) {
		t.Fatalf("exact Adjust Target retry = %+v, %v", retried, err)
	}
	listing := get(t, authenticatedClient(t), server.address, "/events/revision-2099/schedule")
	body, readErr := io.ReadAll(listing.Body)
	closeErr := listing.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read adjusted public Schedule: %v", err)
	}
	if !strings.Contains(string(body), freshPreview.Msg.GetProposedTarget().AsTime().
		In(time.FixedZone("CEST", 2*60*60)).Format(time.RFC3339)) {
		t.Errorf("adjusted public Schedule missing Forecast End: %s", body)
	}
	server.stop(t)
}

func TestOperatorPullsForwardOnlyAfterExplicitEndAndPreview(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	rippleSessionID := addSoftRippleSession(t, administrator, server)
	operator := provisionOperator(t, administrator, server)
	client := sessionv1connect.NewSessionControlServiceClient(
		operator, "http://"+server.address, connect.WithProtoJSON(),
	)
	started, err := client.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "start-before-pull-forward",
		ExpectedLiveStateRevision: proto.Int64(0),
	}))
	if err != nil {
		t.Fatalf("Start Session before Pull Forward: %v", err)
	}
	ended, err := client.EndSession(t.Context(), connect.NewRequest(&sessionv1.EndSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "end-before-pull-forward",
		ExpectedLiveStateRevision: new(started.Msg.GetState().GetLiveStateRevision()),
	}))
	if err != nil {
		t.Fatalf("End Session before Pull Forward: %v", err)
	}
	preview, err := client.PreviewPullForward(t.Context(), connect.NewRequest(
		&sessionv1.PreviewPullForwardRequest{EventId: 1, SessionId: sessionID},
	))
	if err != nil {
		t.Fatalf("Preview Pull Forward: %v", err)
	}
	if preview.Msg.GetPreviewFingerprint() == "" || len(preview.Msg.GetChanges()) != 1 ||
		len(preview.Msg.GetEffects()) != 1 ||
		preview.Msg.GetChanges()[0].GetSessionId() != rippleSessionID ||
		!preview.Msg.GetEffects()[0].GetCurrentForecastStart().AsTime().Equal(
			time.Date(2099, 8, 21, 9, 0, 0, 0, time.UTC),
		) ||
		!preview.Msg.GetChanges()[0].GetForecastStart().AsTime().Equal(
			ended.Msg.GetState().GetActualEnd().AsTime(),
		) {
		t.Fatalf("Pull Forward preview = %+v", preview.Msg)
	}
	unconfirmed := &sessionv1.PullForwardRequest{
		EventId: 1, SessionId: sessionID, CommandId: "reject-unconfirmed-pull-forward",
		ExpectedLiveStateRevision: new(ended.Msg.GetState().GetLiveStateRevision()),
		PreviewFingerprint:        preview.Msg.GetPreviewFingerprint(),
	}
	_, unconfirmedErr := client.PullForward(t.Context(), connect.NewRequest(unconfirmed))
	if connect.CodeOf(unconfirmedErr) != connect.CodeFailedPrecondition {
		t.Fatalf("unconfirmed Pull Forward error = %v, want FailedPrecondition", unconfirmedErr)
	}
	databasePath := filepath.Join(server.dataDir, "beamers.db")
	if fixtureErr := storetest.FailSessionForecastUpdate(
		t.Context(), databasePath, rippleSessionID,
	); fixtureErr != nil {
		t.Fatalf("install Pull Forward rollback fixture: %v", fixtureErr)
	}
	request := &sessionv1.PullForwardRequest{
		EventId: 1, SessionId: sessionID, CommandId: "pull-forward-after-end",
		ExpectedLiveStateRevision: new(ended.Msg.GetState().GetLiveStateRevision()),
		PreviewFingerprint:        preview.Msg.GetPreviewFingerprint(), Confirmed: true,
	}
	_, rollbackErr := client.PullForward(t.Context(), connect.NewRequest(request))
	if connect.CodeOf(rollbackErr) != connect.CodeInternal {
		t.Fatalf("forced Pull Forward rollback error = %v, want Internal", rollbackErr)
	}
	if fixtureErr := storetest.AllowSessionForecastUpdates(
		t.Context(), databasePath,
	); fixtureErr != nil {
		t.Fatalf("remove Pull Forward rollback fixture: %v", fixtureErr)
	}
	afterRollback, err := client.PreviewPullForward(t.Context(), connect.NewRequest(
		&sessionv1.PreviewPullForwardRequest{EventId: 1, SessionId: sessionID},
	))
	if err != nil ||
		afterRollback.Msg.GetPreviewFingerprint() != preview.Msg.GetPreviewFingerprint() {
		t.Fatalf("Pull Forward rollback changed timing = %+v, %v", afterRollback, err)
	}
	pulled, err := client.PullForward(t.Context(), connect.NewRequest(request))
	if err != nil {
		t.Fatalf(
			"Pull Forward with expected revision %d: %v",
			request.GetExpectedLiveStateRevision(), err,
		)
	}
	if pulled.Msg.GetState().GetLifecycle() !=
		sessionv1.SessionLifecycle_SESSION_LIFECYCLE_ENDED ||
		pulled.Msg.GetState().GetLiveStateRevision() !=
			ended.Msg.GetState().GetLiveStateRevision() ||
		len(pulled.Msg.GetChanges()) != 1 ||
		pulled.Msg.GetChanges()[0].GetSessionId() != rippleSessionID {
		t.Fatalf("Pull Forward result = %+v", pulled.Msg)
	}
	retried, err := client.PullForward(t.Context(), connect.NewRequest(request))
	if err != nil || !proto.Equal(retried.Msg, pulled.Msg) {
		t.Fatalf("exact Pull Forward retry = %+v, %v", retried, err)
	}
	server.stop(t)
}

func TestOperatorCorrectsLiveDetailsWithoutRewritingRunSnapshot(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	operator := provisionOperator(t, administrator, server)
	client := sessionv1connect.NewSessionControlServiceClient(
		operator, "http://"+server.address, connect.WithProtoJSON(),
	)
	started, err := client.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "start-before-detail-correction",
		ExpectedLiveStateRevision: proto.Int64(0),
	}))
	if err != nil {
		t.Fatalf("Start Session before Live Detail Correction: %v", err)
	}
	rundownClient := rundownv1connect.NewRundownServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	current, err := rundownClient.GetCrewRundown(t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}))
	if err != nil {
		t.Fatalf("Get Rundown before conflicting Draft edit: %v", err)
	}
	pending, err := rundownClient.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "draft-title-before-live-correction", ExpectedDraftRevision: current.Msg.GetDraftRevision(),
		Sessions: []*rundownv1.SessionDraft{{
			Id: sessionID, Title: "Pending Draft Title",
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}},
		}},
	}))
	if err != nil {
		t.Fatalf("Edit Draft before Live Detail Correction: %v", err)
	}
	request := &sessionv1.CorrectLiveDetailsRequest{
		EventId: 1, SessionId: sessionID, CommandId: "correct-live-details",
		ExpectedLiveStateRevision: new(started.Msg.GetState().GetLiveStateRevision()),
		Confirmed:                 true, Title: "Corrected Keynote", Speaker: "Avery Speaker",
		PublicDetails: "Corrected public description",
		UpdateMask:    &fieldmaskpb.FieldMask{Paths: []string{"title", "speaker", "public_details"}},
	}
	corrected, err := client.CorrectLiveDetails(t.Context(), connect.NewRequest(request))
	if err != nil {
		t.Fatalf("Correct Live Details RPC: %v", err)
	}
	if corrected.Msg.GetState().GetLiveStateRevision() != 2 || corrected.Msg.GetAmendmentId() <= 0 {
		t.Errorf("corrected Live state = %+v, amendment %d", corrected.Msg.GetState(), corrected.Msg.GetAmendmentId())
	}
	retried, err := client.CorrectLiveDetails(t.Context(), connect.NewRequest(request))
	if err != nil || retried.Msg.GetAmendmentId() != corrected.Msg.GetAmendmentId() {
		t.Fatalf("exact Live Detail Correction retry = %+v, %v", retried, err)
	}
	unconfirmedRequest := &sessionv1.CorrectLiveDetailsRequest{
		EventId: 1, SessionId: sessionID, CommandId: "unconfirmed-live-details",
		ExpectedLiveStateRevision: proto.Int64(2), Title: "Must Not Apply",
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}},
	}
	for range 2 {
		_, unconfirmedErr := client.CorrectLiveDetails(
			t.Context(), connect.NewRequest(unconfirmedRequest),
		)
		if connect.CodeOf(unconfirmedErr) != connect.CodeFailedPrecondition {
			t.Errorf("unconfirmed Live Detail Correction error = %v, want FailedPrecondition", unconfirmedErr)
		}
	}
	broadCorrectionRequest := &sessionv1.CorrectLiveDetailsRequest{
		EventId: 1, SessionId: sessionID, CommandId: "reject-broad-live-correction",
		ExpectedLiveStateRevision: proto.Int64(2), Confirmed: true,
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"crew_notes"}},
	}
	for range 2 {
		_, broadCorrectionErr := client.CorrectLiveDetails(
			t.Context(), connect.NewRequest(broadCorrectionRequest),
		)
		if connect.CodeOf(broadCorrectionErr) != connect.CodeInvalidArgument {
			t.Errorf("broad Live Detail Correction error = %v, want InvalidArgument", broadCorrectionErr)
		}
	}
	emptyCorrectionRequest := &sessionv1.CorrectLiveDetailsRequest{
		EventId: 1, SessionId: sessionID, CommandId: "reject-empty-live-correction",
		ExpectedLiveStateRevision: proto.Int64(2), Confirmed: true,
	}
	for range 2 {
		_, emptyCorrectionErr := client.CorrectLiveDetails(
			t.Context(), connect.NewRequest(emptyCorrectionRequest),
		)
		if connect.CodeOf(emptyCorrectionErr) != connect.CodeInvalidArgument {
			t.Errorf(
				"empty Live Detail Correction error = %v, want InvalidArgument",
				emptyCorrectionErr,
			)
		}
	}

	public := get(t, authenticatedClient(t), server.address, "/events/revision-2099/schedule")
	publicBody, readErr := io.ReadAll(public.Body)
	closeErr := public.Body.Close()
	if combinedErr := errors.Join(readErr, closeErr); combinedErr != nil {
		t.Fatalf("read corrected public Schedule: %v", combinedErr)
	}
	for _, expected := range []string{"Corrected Keynote", "Avery Speaker", "Corrected public description"} {
		if !strings.Contains(string(publicBody), expected) {
			t.Errorf("corrected public Schedule missing %q: %s", expected, publicBody)
		}
	}

	if _, endErr := client.EndSession(t.Context(), connect.NewRequest(&sessionv1.EndSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "end-after-live-detail-correction",
		ExpectedLiveStateRevision: proto.Int64(2),
	})); endErr != nil {
		t.Fatalf("End Session after Live Detail Correction: %v", endErr)
	}
	conflictPreview, err := rundownClient.PublishPreview(t.Context(), connect.NewRequest(&rundownv1.PublishPreviewRequest{
		EventId: 1, ChangeIds: []int64{pending.Msg.GetChanges()[0].GetId()},
	}))
	if err != nil {
		t.Fatalf("Preview Draft conflict after Live Detail Correction: %v", err)
	}
	if len(conflictPreview.Msg.GetValidationFailures()) != 1 ||
		!strings.Contains(conflictPreview.Msg.GetValidationFailures()[0], "live detail correction") {
		t.Errorf("corrected fact Draft conflict = %v", conflictPreview.Msg.GetValidationFailures())
	}
	afterCorrection, err := rundownClient.GetCrewRundown(t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}))
	if err != nil {
		t.Fatalf("Get Rundown after Live Detail Correction: %v", err)
	}
	reviewed, err := rundownClient.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "review-corrected-title", ExpectedDraftRevision: afterCorrection.Msg.GetDraftRevision(),
		Sessions: []*rundownv1.SessionDraft{{
			Id: sessionID, Title: "Reviewed Corrected Keynote",
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}},
		}},
	}))
	if err != nil {
		t.Fatalf("review corrected Draft fact: %v", err)
	}
	reviewedPreview, err := rundownClient.PublishPreview(t.Context(), connect.NewRequest(&rundownv1.PublishPreviewRequest{
		EventId: 1, ChangeIds: []int64{reviewed.Msg.GetChanges()[0].GetId()},
	}))
	if err != nil || len(reviewedPreview.Msg.GetValidationFailures()) != 0 {
		t.Fatalf("Preview reviewed correction = %+v, %v", reviewedPreview, err)
	}
	if _, publishErr := rundownClient.Publish(t.Context(), connect.NewRequest(&rundownv1.PublishRequest{
		EventId: 1, CommandId: "publish-reviewed-correction",
		Confirmation: &rundownv1.PublishConfirmation{
			DraftRevision: reviewedPreview.Msg.GetDraftRevision(), PublishedRevision: reviewedPreview.Msg.GetPublishedRevision(),
			ChangeIds: reviewedPreview.Msg.GetChangeIds(), Fingerprint: reviewedPreview.Msg.GetFingerprint(),
		},
	})); publishErr != nil {
		t.Fatalf("Publish reviewed correction: %v", publishErr)
	}
	deepLink := get(t, authenticatedClient(t), server.address, fmt.Sprintf("/events/revision-2099/schedule/sessions/%d", sessionID))
	deepLinkBody, readErr := io.ReadAll(deepLink.Body)
	closeErr = deepLink.Body.Close()
	if combinedErr := errors.Join(readErr, closeErr); combinedErr != nil {
		t.Fatalf("read reviewed corrected Session: %v", combinedErr)
	}
	if !strings.Contains(string(deepLinkBody), "Reviewed Corrected Keynote") || strings.Contains(string(deepLinkBody), ">Corrected Keynote<") {
		t.Errorf("reviewed corrected Session = %s", deepLinkBody)
	}

	history, err := client.GetSessionHistory(t.Context(), connect.NewRequest(&sessionv1.GetSessionHistoryRequest{
		EventId: 1, SessionId: sessionID,
	}))
	if err != nil {
		t.Fatalf("Get Session history RPC: %v", err)
	}
	if len(history.Msg.GetRuns()) != 1 {
		t.Fatalf("Session Run history = %+v, want one Run", history.Msg.GetRuns())
	}
	run := history.Msg.GetRuns()[0]
	if run.GetSnapshot().GetTitle() != "Opening Keynote" || run.GetSnapshot().GetSpeaker() != "Original Speaker" ||
		run.GetSnapshot().GetPublishedRevision() != 1 ||
		run.GetSnapshot().GetType() != rundownv1.SessionType_SESSION_TYPE_PRESENTATION ||
		run.GetSnapshot().GetTimingPolicy() != rundownv1.TimingPolicy_TIMING_POLICY_FIXED_END ||
		run.GetSnapshot().GetStartBoundary() != rundownv1.Boundary_BOUNDARY_HARD ||
		run.GetSnapshot().GetEndBoundary() != rundownv1.Boundary_BOUNDARY_HARD ||
		run.GetSnapshot().GetMinimumDuration().AsDuration() != 30*time.Minute ||
		len(run.GetSnapshot().GetLaneIds()) != 1 || len(run.GetSnapshot().GetLocationIds()) != 1 ||
		len(run.GetAmendments()) != 1 || run.GetAmendments()[0].GetId() != corrected.Msg.GetAmendmentId() ||
		run.GetAmendments()[0].GetDetails().GetTitle() != "Corrected Keynote" {
		t.Errorf("Session Run immutable history = %+v", run)
	}
	audits, _ := readAuditHistory(t, administrator, server.address)
	correctionAudits := 0
	rejectedCorrectionReasons := map[string]int{
		"live_detail_confirmation_required": 1,
		"live_detail_fields_invalid":        2,
	}
	for _, entry := range audits {
		if entry.Action != "CorrectLiveDetails" ||
			entry.TargetType != "Session" ||
			entry.TargetID != strconv.FormatInt(sessionID, 10) {
			continue
		}
		switch entry.Outcome {
		case "Succeeded":
			correctionAudits++
		case "Rejected":
			rejectedCorrectionReasons[entry.Reason]--
		}
	}
	if correctionAudits != 1 {
		t.Errorf("successful Live Detail Correction Audit Entries = %d, want 1", correctionAudits)
	}
	for reason, remaining := range rejectedCorrectionReasons {
		if remaining != 0 {
			t.Errorf("Live Detail Correction rejection %q remaining = %d", reason, remaining)
		}
	}
	server.stop(t)
}

func TestOrdinaryPublishDoesNotAlterLiveSession(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	operator := provisionOperator(t, administrator, server)
	sessionClient := sessionv1connect.NewSessionControlServiceClient(
		operator, "http://"+server.address, connect.WithProtoJSON(),
	)
	if _, err := sessionClient.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "start-before-blocked-publish",
		ExpectedLiveStateRevision: proto.Int64(0),
	})); err != nil {
		t.Fatalf("Start Session before blocked Publish: %v", err)
	}
	rundownClient := rundownv1connect.NewRundownServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	current, err := rundownClient.GetCrewRundown(t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}))
	if err != nil {
		t.Fatalf("Get current Rundown before Live edit: %v", err)
	}
	edited, err := rundownClient.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "edit-live-session-title", ExpectedDraftRevision: current.Msg.GetDraftRevision(),
		Sessions: []*rundownv1.SessionDraft{{
			Id: sessionID, Title: "Draft Must Not Reach Live",
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}},
		}},
	}))
	if err != nil {
		t.Fatalf("Edit currently Live Session Draft: %v", err)
	}
	preview, err := rundownClient.PublishPreview(t.Context(), connect.NewRequest(&rundownv1.PublishPreviewRequest{
		EventId: 1, ChangeIds: []int64{edited.Msg.GetChanges()[0].GetId()},
	}))
	if err != nil {
		t.Fatalf("Preview currently Live Session Publish: %v", err)
	}
	if len(preview.Msg.GetValidationFailures()) != 1 ||
		!strings.Contains(preview.Msg.GetValidationFailures()[0], "currently Live Session") {
		t.Errorf("Live Session Publish validation = %v", preview.Msg.GetValidationFailures())
	}
	structuralEdit, err := rundownClient.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "rename-live-session-lane", ExpectedDraftRevision: edited.Msg.GetDraftRevision(),
		Lanes: []*rundownv1.LaneDraft{{
			Id: current.Msg.GetLanes()[0].GetId(), Name: "Renamed Live Lane",
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"name"}},
		}},
	}))
	if err != nil {
		t.Fatalf("Edit Live Session Lane Draft: %v", err)
	}
	structuralPreview, err := rundownClient.PublishPreview(t.Context(), connect.NewRequest(&rundownv1.PublishPreviewRequest{
		EventId: 1, ChangeIds: []int64{structuralEdit.Msg.GetChanges()[0].GetId()},
	}))
	if err != nil {
		t.Fatalf("Preview Live Session Lane Publish: %v", err)
	}
	if len(structuralPreview.Msg.GetValidationFailures()) != 1 ||
		!strings.Contains(structuralPreview.Msg.GetValidationFailures()[0], "currently Live Session") {
		t.Errorf("Live Session Lane Publish validation = %v", structuralPreview.Msg.GetValidationFailures())
	}
	public := get(t, authenticatedClient(t), server.address, "/events/revision-2099/schedule")
	publicBody, readErr := io.ReadAll(public.Body)
	closeErr := public.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Schedule after blocked Live Publish: %v", err)
	}
	if !strings.Contains(string(publicBody), "Opening Keynote") || strings.Contains(string(publicBody), "Draft Must Not Reach Live") {
		t.Errorf("public Schedule after blocked Live Publish = %s", publicBody)
	}
	server.stop(t)
}

func TestProducerDeletesOnlyNeverPublishedDraftSession(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	publishedSessionID := prepareActiveSchedule(t, administrator, server)
	client := rundownv1connect.NewRundownServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	current, err := client.GetCrewRundown(t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}))
	if err != nil || len(current.Msg.GetLanes()) == 0 || len(current.Msg.GetLocations()) == 0 {
		t.Fatalf("Get Rundown for Draft Session deletion = %+v, %v", current, err)
	}
	created, err := client.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "create-disposable-draft-session", ExpectedDraftRevision: current.Msg.GetDraftRevision(),
		Sessions: []*rundownv1.SessionDraft{{
			Ref: "disposable", Title: "Disposable Draft Session",
			Type:               rundownv1.SessionType_SESSION_TYPE_PRESENTATION,
			AudienceVisibility: rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_PUBLIC,
			PlannedStart:       timestamppb.New(time.Date(2099, 8, 21, 11, 0, 0, 0, time.UTC)),
			PlannedEnd:         timestamppb.New(time.Date(2099, 8, 21, 12, 0, 0, 0, time.UTC)),
			TimingPolicy:       rundownv1.TimingPolicy_TIMING_POLICY_FIXED_END,
			MinimumDuration:    durationpb.New(30 * time.Minute),
			StartBoundary:      rundownv1.Boundary_BOUNDARY_SOFT, EndBoundary: rundownv1.Boundary_BOUNDARY_SOFT,
			Lanes:     []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Id{Id: current.Msg.GetLanes()[0].GetId()}}},
			Locations: []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Id{Id: current.Msg.GetLocations()[0].GetId()}}},
		}},
	}))
	if err != nil {
		t.Fatalf("Create disposable Draft Session: %v", err)
	}
	var draftSessionID int64
	for _, change := range created.Msg.GetChanges() {
		if change.GetKind() == "CreateSession" {
			draftSessionID = change.GetTargetId()
		}
	}
	request := &rundownv1.DeleteDraftSessionRequest{
		EventId: 1, SessionId: draftSessionID, CommandId: "delete-disposable-draft-session",
		ExpectedDraftRevision: created.Msg.GetDraftRevision(),
	}
	deleted, err := client.DeleteDraftSession(t.Context(), connect.NewRequest(request))
	if err != nil || deleted.Msg.GetSessionId() != draftSessionID ||
		deleted.Msg.GetDraftRevision() != created.Msg.GetDraftRevision()+1 {
		t.Fatalf("Delete Draft Session = %+v, %v", deleted, err)
	}
	retried, err := client.DeleteDraftSession(t.Context(), connect.NewRequest(request))
	if err != nil || retried.Msg.GetDraftRevision() != deleted.Msg.GetDraftRevision() {
		t.Fatalf("exact Delete Draft Session retry = %+v, %v", retried, err)
	}
	_, publishedErr := client.DeleteDraftSession(t.Context(), connect.NewRequest(&rundownv1.DeleteDraftSessionRequest{
		EventId: 1, SessionId: publishedSessionID, CommandId: "reject-published-session-deletion",
		ExpectedDraftRevision: deleted.Msg.GetDraftRevision(),
	}))
	if connect.CodeOf(publishedErr) != connect.CodeFailedPrecondition {
		t.Errorf("Published Session deletion error = %v, want FailedPrecondition", publishedErr)
	}
	server.stop(t)
}

func TestProducerImportsCSVAsReviewedDraftProposals(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	client := rundownv1connect.NewRundownServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	mappings := []*rundownv1.CSVFieldMapping{
		{SourceColumn: "key", TargetField: "external_key"},
		{SourceColumn: "title", TargetField: "title"},
		{SourceColumn: "speaker", TargetField: "speaker"},
		{SourceColumn: "details", TargetField: "public_details"},
		{SourceColumn: "start", TargetField: "planned_start"},
		{SourceColumn: "end", TargetField: "planned_end"},
		{SourceColumn: "lane", TargetField: "lane"},
	}
	csvData := []byte("key,title,speaker,details,start,end,lane,vendor_only\n" +
		"fosdem-1,Imported Session,Ada Speaker,Imported public details,2099-08-21 12:00,2099-08-21 13:00,Main Lane,ignored\n")
	preview, err := client.PreviewCSVImport(t.Context(), connect.NewRequest(&rundownv1.PreviewCSVImportRequest{
		EventId: 1, CsvData: csvData, Mappings: mappings,
	}))
	if err != nil {
		t.Fatalf("Preview CSV Import: %v", err)
	}
	if len(preview.Msg.GetValidationFailures()) != 0 || len(preview.Msg.GetProposals()) != 1 ||
		preview.Msg.GetProposals()[0].GetClassification() != "Addition" ||
		len(preview.Msg.GetIgnoredFields()) != 1 || preview.Msg.GetIgnoredFields()[0] != "vendor_only" {
		t.Fatalf("CSV Import Preview = %+v", preview.Msg)
	}
	request := &rundownv1.ImportCSVRequest{
		EventId: 1, CommandId: "import-csv-session", ExpectedDraftRevision: preview.Msg.GetDraftRevision(),
		CsvData: csvData, Mappings: mappings, Fingerprint: preview.Msg.GetFingerprint(),
		ProposalIds: []string{preview.Msg.GetProposals()[0].GetId()},
	}
	imported, err := client.ImportCSV(t.Context(), connect.NewRequest(request))
	if err != nil || len(imported.Msg.GetChanges()) != 1 || imported.Msg.GetChanges()[0].GetKind() != "CreateSession" {
		t.Fatalf("Import CSV = %+v, %v", imported, err)
	}
	retried, err := client.ImportCSV(t.Context(), connect.NewRequest(request))
	if err != nil || retried.Msg.GetDraftRevision() != imported.Msg.GetDraftRevision() {
		t.Fatalf("exact CSV Import retry = %+v, %v", retried, err)
	}
	published, err := client.GetCrewRundown(t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}))
	if err != nil {
		t.Fatalf("Get Published Rundown after CSV Import: %v", err)
	}
	for _, session := range published.Msg.GetSessions() {
		if session.GetTitle() == "Imported Session" {
			t.Error("CSV Import mutated Published state")
		}
	}

	repeatData := []byte("key,title,details\nfosdem-1,Reviewed Imported Session,Updated imported details\n")
	repeatMappings := []*rundownv1.CSVFieldMapping{
		{SourceColumn: "key", TargetField: "external_key"},
		{SourceColumn: "title", TargetField: "title"},
		{SourceColumn: "details", TargetField: "public_details"},
	}
	repeat, err := client.PreviewCSVImport(t.Context(), connect.NewRequest(&rundownv1.PreviewCSVImportRequest{
		EventId: 1, CsvData: repeatData, Mappings: repeatMappings,
	}))
	if err != nil || len(repeat.Msg.GetProposals()) != 2 {
		t.Fatalf("Preview repeat CSV Import = %+v, %v", repeat, err)
	}
	selected := []string{}
	for _, proposal := range repeat.Msg.GetProposals() {
		if proposal.GetClassification() != "Update" {
			t.Errorf("repeat CSV proposal = %+v, want Update", proposal)
		}
		if proposal.GetField() == "title" {
			selected = append(selected, proposal.GetId())
		}
	}
	updated, err := client.ImportCSV(t.Context(), connect.NewRequest(&rundownv1.ImportCSVRequest{
		EventId: 1, CommandId: "import-reviewed-csv-title", ExpectedDraftRevision: repeat.Msg.GetDraftRevision(),
		CsvData: repeatData, Mappings: repeatMappings, Fingerprint: repeat.Msg.GetFingerprint(), ProposalIds: selected,
	}))
	if err != nil || len(updated.Msg.GetChanges()) != 1 || updated.Msg.GetChanges()[0].GetFactKey() != "title" {
		t.Fatalf("Apply reviewed CSV field = %+v, %v", updated, err)
	}

	duplicate, err := client.PreviewCSVImport(t.Context(), connect.NewRequest(&rundownv1.PreviewCSVImportRequest{
		EventId: 1, CsvData: []byte("key\nduplicate\nduplicate\n"),
		Mappings: []*rundownv1.CSVFieldMapping{{SourceColumn: "key", TargetField: "external_key"}},
	}))
	if err != nil || len(duplicate.Msg.GetValidationFailures()) == 0 ||
		!strings.Contains(duplicate.Msg.GetValidationFailures()[0], "duplicate Import Reference") {
		t.Errorf("duplicate CSV Import References = %+v, %v", duplicate, err)
	}
	unsafe, err := client.PreviewCSVImport(t.Context(), connect.NewRequest(&rundownv1.PreviewCSVImportRequest{
		EventId: 1, CsvData: []byte("key,notes\nunsafe,secret\n"),
		Mappings: []*rundownv1.CSVFieldMapping{
			{SourceColumn: "key", TargetField: "external_key"},
			{SourceColumn: "notes", TargetField: "crew_notes"},
		},
	}))
	if err != nil || len(unsafe.Msg.GetValidationFailures()) == 0 ||
		!strings.Contains(strings.Join(unsafe.Msg.GetValidationFailures(), " "), "cannot target crew_notes") {
		t.Errorf("unsafe CSV target = %+v, %v", unsafe, err)
	}
	_, malformedErr := client.PreviewCSVImport(t.Context(), connect.NewRequest(&rundownv1.PreviewCSVImportRequest{
		EventId: 1, CsvData: []byte("key,title\nmalformed,\"unterminated\n"),
		Mappings: []*rundownv1.CSVFieldMapping{
			{SourceColumn: "key", TargetField: "external_key"},
			{SourceColumn: "title", TargetField: "title"},
		},
	}))
	if connect.CodeOf(malformedErr) != connect.CodeInvalidArgument {
		t.Errorf("malformed CSV error = %v, want InvalidArgument", malformedErr)
	}
	server.stop(t)
}

func TestProducerImportsICalendarWithEventTimeReview(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	client := rundownv1connect.NewRundownServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	calendar := []byte("BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"X-WR-TIMEZONE:Europe/Berlin\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:fosdem-style-1\r\n" +
		"DTSTART;TZID=Europe/Berlin:20990821T140000\r\n" +
		"DTEND;TZID=Europe/Berlin:20990821T150000\r\n" +
		"SUMMARY:Imported Café & λ\r\n" +
		"DESCRIPTION:Line one\\nline two\r\n" +
		"LOCATION:Main Lane\r\n" +
		"CATEGORIES:General\r\n" +
		"URL:https://fosdem.org/example\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n")
	preview, err := client.PreviewICalendarImport(t.Context(), connect.NewRequest(&rundownv1.PreviewICalendarImportRequest{
		EventId: 1, IcalendarData: calendar,
	}))
	if err != nil || len(preview.Msg.GetProposals()) != 1 ||
		preview.Msg.GetProposals()[0].GetClassification() != "Addition" ||
		!slices.Contains(preview.Msg.GetUnsupportedFields(), "URL") || len(preview.Msg.GetAppliedDefaults()) == 0 {
		t.Fatalf("Preview iCalendar Import = %+v, %v", preview, err)
	}
	request := &rundownv1.ImportICalendarRequest{
		EventId: 1, CommandId: "import-icalendar-session",
		ExpectedDraftRevision: preview.Msg.GetDraftRevision(), IcalendarData: calendar,
		Fingerprint: preview.Msg.GetFingerprint(), ProposalIds: []string{preview.Msg.GetProposals()[0].GetId()},
	}
	imported, err := client.ImportICalendar(t.Context(), connect.NewRequest(request))
	if err != nil || len(imported.Msg.GetChanges()) != 1 || imported.Msg.GetChanges()[0].GetKind() != "CreateSession" {
		t.Fatalf("Import iCalendar = %+v, %v", imported, err)
	}
	retried, err := client.ImportICalendar(t.Context(), connect.NewRequest(request))
	if err != nil || retried.Msg.GetDraftRevision() != imported.Msg.GetDraftRevision() {
		t.Fatalf("exact iCalendar Import retry = %+v, %v", retried, err)
	}
	published, err := client.GetCrewRundown(t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}))
	if err != nil {
		t.Fatalf("Get Published Rundown after iCalendar Import: %v", err)
	}
	for _, session := range published.Msg.GetSessions() {
		if session.GetTitle() == "Imported Café & λ" {
			t.Error("iCalendar Import mutated Published state")
		}
	}

	ambiguous := []byte("BEGIN:VCALENDAR\nBEGIN:VEVENT\nUID:ambiguous\n" +
		"DTSTART;TZID=Europe/Berlin:20261025T023000\nDTEND;TZID=Europe/Berlin:20261025T034500\n" +
		"SUMMARY:Repeated hour\nLOCATION:Main Lane\nEND:VEVENT\nEND:VCALENDAR\n")
	unresolved, err := client.PreviewICalendarImport(t.Context(), connect.NewRequest(&rundownv1.PreviewICalendarImportRequest{
		EventId: 1, IcalendarData: ambiguous,
	}))
	if err != nil || len(unresolved.Msg.GetProposals()) != 1 ||
		unresolved.Msg.GetProposals()[0].GetClassification() != "Unresolved" ||
		!strings.Contains(unresolved.Msg.GetProposals()[0].GetMessage(), "choose Earlier or Later") {
		t.Fatalf("ambiguous iCalendar preview = %+v, %v", unresolved, err)
	}
	resolved, err := client.PreviewICalendarImport(t.Context(), connect.NewRequest(&rundownv1.PreviewICalendarImportRequest{
		EventId: 1, IcalendarData: ambiguous,
		Choices: []*rundownv1.ICalendarOccurrenceChoice{{
			Uid: "ambiguous", Property: "DTSTART", Occurrence: "Later",
		}},
	}))
	if err != nil || len(resolved.Msg.GetProposals()) != 1 || resolved.Msg.GetProposals()[0].GetClassification() != "Addition" {
		t.Fatalf("resolved repeated-hour preview = %+v, %v", resolved, err)
	}

	nonexistent := []byte("BEGIN:VCALENDAR\nBEGIN:VEVENT\nUID:nonexistent\n" +
		"DTSTART;TZID=Europe/Berlin:20260329T023000\nDTEND;TZID=Europe/Berlin:20260329T034500\n" +
		"SUMMARY:Missing hour\nLOCATION:Main Lane\nEND:VEVENT\nEND:VCALENDAR\n")
	blocked, err := client.PreviewICalendarImport(t.Context(), connect.NewRequest(&rundownv1.PreviewICalendarImportRequest{
		EventId: 1, IcalendarData: nonexistent,
	}))
	if err != nil || len(blocked.Msg.GetProposals()) != 1 ||
		blocked.Msg.GetProposals()[0].GetClassification() != "Unresolved" ||
		!strings.Contains(blocked.Msg.GetProposals()[0].GetMessage(), "does not exist") {
		t.Fatalf("nonexistent-time iCalendar preview = %+v, %v", blocked, err)
	}
	server.stop(t)
}

func TestOperatorEndsLiveSessionWithoutMovingLaterSessions(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	operator := provisionOperator(t, administrator, server)
	client := sessionv1connect.NewSessionControlServiceClient(
		operator, "http://"+server.address, connect.WithProtoJSON(),
	)
	started, err := client.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "start-keynote-before-end",
		ExpectedLiveStateRevision: proto.Int64(0),
	}))
	if err != nil {
		t.Fatalf("Start Session before End: %v", err)
	}
	ended, err := client.EndSession(t.Context(), connect.NewRequest(&sessionv1.EndSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "end-keynote",
		ExpectedLiveStateRevision: proto.Int64(1),
	}))
	if err != nil {
		t.Fatalf("End Session RPC: %v", err)
	}
	state := ended.Msg.GetState()
	if state.GetSessionId() != sessionID || state.GetSessionRunId() != started.Msg.GetState().GetSessionRunId() ||
		state.GetLifecycle() != sessionv1.SessionLifecycle_SESSION_LIFECYCLE_ENDED ||
		state.GetLiveStateRevision() != 2 || state.GetActualEnd() == nil ||
		state.GetActualEnd().AsTime().Before(state.GetActualStart().AsTime()) {
		t.Errorf("ended Session state = %+v", state)
	}

	listing := get(t, authenticatedClient(t), server.address, "/events/revision-2099/schedule")
	listingBody, readErr := io.ReadAll(listing.Body)
	closeErr := listing.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Schedule after End: %v", err)
	}
	if listing.StatusCode != http.StatusOK || strings.Contains(string(listingBody), "Opening Keynote") ||
		!strings.Contains(string(listingBody), "Closing Session") ||
		!strings.Contains(string(listingBody), "2099-08-21T12:00:00+02:00") {
		t.Errorf("Schedule after End = %d %q", listing.StatusCode, listingBody)
	}
	deepLink := get(t, authenticatedClient(t), server.address, fmt.Sprintf("/events/revision-2099/schedule/sessions/%d", sessionID))
	deepLinkBody, readErr := io.ReadAll(deepLink.Body)
	closeErr = deepLink.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read ended Session deep link: %v", err)
	}
	if deepLink.StatusCode != http.StatusOK || !strings.Contains(string(deepLinkBody), "Status: Ended") ||
		!strings.Contains(string(deepLinkBody), "Actual End:") {
		t.Errorf("ended Session deep link = %d %q", deepLink.StatusCode, deepLinkBody)
	}
	server.stop(t)
}

func TestSessionCommandsRejectStaleAndConflictingRetries(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	operator := provisionOperator(t, administrator, server)
	client := sessionv1connect.NewSessionControlServiceClient(
		operator, "http://"+server.address, connect.WithProtoJSON(),
	)
	_, missingRevisionErr := client.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "missing-live-state-revision",
	}))
	if connect.CodeOf(missingRevisionErr) != connect.CodeInvalidArgument {
		t.Fatalf("missing expected Live State Revision error = %v, want InvalidArgument", missingRevisionErr)
	}
	request := connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "idempotent-start",
		ExpectedLiveStateRevision: proto.Int64(0),
	})
	started, err := client.StartSession(t.Context(), request)
	if err != nil {
		t.Fatalf("first Start Session: %v", err)
	}
	retried, err := client.StartSession(t.Context(), connect.NewRequest(request.Msg))
	if err != nil {
		t.Fatalf("exact Start Session retry: %v", err)
	}
	if retried.Msg.GetState().GetSessionRunId() != started.Msg.GetState().GetSessionRunId() ||
		retried.Msg.GetState().GetLiveStateRevision() != 1 ||
		!retried.Msg.GetState().GetActualStart().AsTime().Equal(started.Msg.GetState().GetActualStart().AsTime()) {
		t.Errorf("exact retry = %+v, want original %+v", retried.Msg.GetState(), started.Msg.GetState())
	}

	staleRequest := &sessionv1.StartSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "stale-start",
		ExpectedLiveStateRevision: proto.Int64(0),
	}
	_, staleErr := client.StartSession(t.Context(), connect.NewRequest(staleRequest))
	if connect.CodeOf(staleErr) != connect.CodeAborted {
		t.Fatalf("stale Start error = %v, want Aborted", staleErr)
	}
	var staleConnectErr *connect.Error
	if !errors.As(staleErr, &staleConnectErr) {
		t.Fatalf("stale Start error type = %T", staleErr)
	}
	var current *sessionv1.SessionState
	for _, detail := range staleConnectErr.Details() {
		value, detailErr := detail.Value()
		if detailErr != nil {
			t.Fatalf("decode stale Start detail: %v", detailErr)
		}
		if state, ok := value.(*sessionv1.SessionState); ok {
			current = state
		}
	}
	if current == nil || current.GetSessionRunId() != started.Msg.GetState().GetSessionRunId() ||
		current.GetLifecycle() != sessionv1.SessionLifecycle_SESSION_LIFECYCLE_LIVE ||
		current.GetLiveStateRevision() != 1 {
		t.Errorf("stale Start current state = %+v", current)
	}
	_, staleRetryErr := client.StartSession(t.Context(), connect.NewRequest(staleRequest))
	var staleRetryConnectErr *connect.Error
	if connect.CodeOf(staleRetryErr) != connect.CodeAborted ||
		!errors.As(staleRetryErr, &staleRetryConnectErr) || len(staleRetryConnectErr.Details()) != 1 {
		t.Fatalf("stale Start retry error = %v, want original Aborted detail", staleRetryErr)
	}
	retriedDetail, detailErr := staleRetryConnectErr.Details()[0].Value()
	if detailErr != nil {
		t.Fatalf("decode stale Start retry detail: %v", detailErr)
	}
	retriedCurrent, ok := retriedDetail.(*sessionv1.SessionState)
	if !ok || retriedCurrent.GetSessionRunId() != current.GetSessionRunId() ||
		retriedCurrent.GetLiveStateRevision() != current.GetLiveStateRevision() {
		t.Errorf("stale Start retry detail = %+v, want original %+v", retriedCurrent, current)
	}

	_, conflictErr := client.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "idempotent-start",
		ExpectedLiveStateRevision: proto.Int64(1),
	}))
	if connect.CodeOf(conflictErr) != connect.CodeAlreadyExists {
		t.Errorf("conflicting Command ID error = %v, want AlreadyExists", conflictErr)
	}
	ended, err := client.EndSession(t.Context(), connect.NewRequest(&sessionv1.EndSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "end-after-rejections",
		ExpectedLiveStateRevision: proto.Int64(1),
	}))
	if err != nil {
		t.Fatalf("End Session after rejected commands: %v", err)
	}
	if ended.Msg.GetState().GetSessionRunId() != started.Msg.GetState().GetSessionRunId() ||
		ended.Msg.GetState().GetLiveStateRevision() != 2 {
		t.Errorf("state mutated by rejected commands: %+v", ended.Msg.GetState())
	}
	server.stop(t)
}

func provisionOperator(
	t *testing.T,
	administrator *http.Client,
	server *runningServer,
) *http.Client {
	t.Helper()
	return provisionOperatorWithLanes(t, administrator, server, []int{1})
}

func openAndCloseProgramStream(
	t *testing.T,
	client *http.Client,
	address string,
	sessionID int64,
) {
	t.Helper()
	streamContext, cancelStream := context.WithCancel(t.Context())
	request, err := http.NewRequestWithContext(
		streamContext,
		http.MethodGet,
		fmt.Sprintf(
			"http://%s/crew/program/%d/events?event_id=1",
			address,
			sessionID,
		),
		http.NoBody,
	)
	if err != nil {
		t.Fatalf("create Program control stream request: %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("open Program control stream: %v", err)
	}
	cancelStream()
	if err = response.Body.Close(); err != nil {
		t.Fatalf("close Program control stream: %v", err)
	}
}

func provisionObserver(
	t *testing.T,
	administrator *http.Client,
	server *runningServer,
) *http.Client {
	t.Helper()
	const password = "observer correct horse battery staple"
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts",
		map[string]string{
			"name": "Oli Observer", "password": password,
			"command_id": "create-account-oli",
		},
		http.StatusCreated, "{\"id\":3,\"name\":\"Oli Observer\",\"administrator\":false}\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/events/1/grants",
		map[string]any{
			"account_id": 3, "role": "Observer", "command_id": "grant-oli-observer",
		},
		http.StatusCreated,
		"{\"event_id\":1,\"account_id\":3,\"role\":\"Observer\"}\n",
	)
	observer := authenticatedClient(t)
	assertJSONRequest(
		t, observer, server.address, "/auth/sign-in",
		map[string]string{"name": "Oli Observer", "password": password},
		http.StatusNoContent, "",
	)
	return observer
}

func provisionOperatorWithLanes(
	t *testing.T,
	administrator *http.Client,
	server *runningServer,
	laneIDs []int,
) *http.Client {
	t.Helper()
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts",
		map[string]string{
			"name": "Opal Operator", "password": "operator correct horse battery staple",
			"command_id": "create-account-opal",
		},
		http.StatusCreated, "{\"id\":2,\"name\":\"Opal Operator\",\"administrator\":false}\n",
	)
	grant := map[string]any{
		"account_id": 2, "role": "Operator", "command_id": "grant-opal-operator",
	}
	wantGrant := "{\"event_id\":1,\"account_id\":2,\"role\":\"Operator\"}\n"
	if len(laneIDs) > 0 {
		grant["lane_ids"] = laneIDs
		wantGrant = "{\"event_id\":1,\"account_id\":2,\"role\":\"Operator\",\"lane_ids\":[1]}\n"
	}
	assertJSONRequest(
		t, administrator, server.address, "/admin/events/1/grants",
		grant, http.StatusCreated, wantGrant,
	)
	operator := authenticatedClient(t)
	assertJSONRequest(
		t, operator, server.address, "/auth/sign-in",
		map[string]string{
			"name": "Opal Operator", "password": "operator correct horse battery staple",
		},
		http.StatusNoContent, "",
	)
	return operator
}

func TestAdministratorSelectsExistingAccountForEventGrant(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts",
		map[string]string{
			"name": "Pat Producer", "password": "producer correct horse battery staple",
			"command_id": "create-account-pat",
		},
		http.StatusCreated,
		"{\"id\":2,\"name\":\"Pat Producer\",\"administrator\":false}\n",
	)
	assertGETResponse(
		t, administrator, server.address, "/admin/accounts", http.StatusOK,
		"[{\"id\":1,\"name\":\"Ada Admin\",\"administrator\":true},{\"id\":2,\"name\":\"Pat Producer\",\"administrator\":false}]\n",
	)
	server.stop(t)
}

func TestAdministratorInspectsAuditHistory(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	const password = "audited account correct horse battery staple"
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts",
		map[string]string{
			"name": "Avery Audited", "password": password,
			"command_id": "create-account-for-audit",
		},
		http.StatusCreated,
		"{\"id\":2,\"name\":\"Avery Audited\",\"administrator\":false}\n",
	)
	response := get(t, administrator, server.address, "/admin/audit")
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Audit history: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Audit history status = %d, want %d: %s", response.StatusCode, http.StatusOK, body)
	}
	var entries []acceptanceAuditEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		t.Fatalf("decode Audit history: %v", err)
	}
	if len(entries) != 1 || entries[0].ActorAccountID != 1 || entries[0].ActorName != "Ada Admin" ||
		entries[0].ServerTime.IsZero() || entries[0].Action != "CreateAccount" ||
		entries[0].TargetType != "Account" || entries[0].TargetID != "2" ||
		entries[0].Outcome != "Succeeded" || entries[0].Reason != "" || entries[0].Note != "" {
		t.Errorf("Audit history = %+v", entries)
	}
	if strings.Contains(string(body), password) {
		t.Error("Audit history contains an Account password")
	}
	server.stop(t)
}

func TestAdministratorDisablesAccountImmediately(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	const password = "retired account correct horse battery staple"
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts",
		map[string]string{
			"name": "Riley Retired", "password": password,
			"command_id": "create-account-to-disable",
		},
		http.StatusCreated,
		"{\"id\":2,\"name\":\"Riley Retired\",\"administrator\":false}\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/events",
		validEventInput(), http.StatusCreated,
		"{\"id\":1,\"name\":\"Revision 2026\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/events/1/grants",
		map[string]any{"account_id": 2, "role": "Producer", "command_id": "grant-retiring-producer"},
		http.StatusCreated, "{\"event_id\":1,\"account_id\":2,\"role\":\"Producer\"}\n",
	)
	retired := authenticatedClient(t)
	assertJSONRequest(
		t, retired, server.address, "/auth/sign-in",
		map[string]string{"name": "Riley Retired", "password": password},
		http.StatusNoContent, "",
	)
	assertJSONRequest(
		t, retired, server.address, "/admin/accounts",
		map[string]string{
			"name": "Forbidden Account", "password": "forbidden correct horse battery staple",
			"command_id": "retired-actor-rejected-command",
		},
		http.StatusForbidden, "Administrator authority required\n",
	)
	disable := map[string]string{
		"command_id": "disable-riley", "reason": "crew_departed",
	}
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts/2/disable",
		disable, http.StatusNoContent, "",
	)
	assertSessionRejected(t, retired, server.address)
	assertJSONRequest(
		t, authenticatedClient(t), server.address, "/auth/sign-in",
		map[string]string{"name": "Riley Retired", "password": password},
		http.StatusUnauthorized, "authentication failed\n",
	)
	assertGETResponse(
		t, administrator, server.address, "/admin/accounts", http.StatusOK,
		"[{\"id\":1,\"name\":\"Ada Admin\",\"administrator\":true}]\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts/2/disable",
		disable, http.StatusNoContent, "",
	)

	dataDir := server.dataDir
	bin := server.bin
	server.stop(t)
	restarted := startBeamers(t, bin, dataDir)
	entries, body := readAuditHistory(t, administrator, restarted.address)
	var disableCount int
	var retainedActor bool
	for _, entry := range entries {
		if entry.Action == "DisableAccount" && entry.TargetType == "Account" && entry.TargetID == "2" {
			disableCount++
			if entry.Outcome != "Succeeded" || entry.Reason != disable["reason"] {
				t.Errorf("Disable Account Audit Entry = %+v", entry)
			}
		}
		if entry.ActorAccountID == 2 && entry.ActorName == "Riley Retired" &&
			entry.Action == "CreateAccount" && entry.Outcome == "Rejected" {
			retainedActor = true
		}
	}
	if disableCount != 1 {
		t.Errorf("Disable Account Audit Entry count = %d, want 1", disableCount)
	}
	if !retainedActor {
		t.Error("Audit history lost the disabled actor identity")
	}
	if strings.Contains(string(body), password) || strings.Contains(string(body), "forbidden correct horse battery staple") {
		t.Error("Audit history contains a password")
	}
	assertJSONRequest(
		t, authenticatedClient(t), restarted.address, "/auth/sign-in",
		map[string]string{"name": "Riley Retired", "password": password},
		http.StatusUnauthorized, "authentication failed\n",
	)
	restarted.stop(t)
}

func TestAuditSeparatesDomainRejectionsFromTransportFailures(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	const password = "audit boundary correct horse battery staple"
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts",
		map[string]string{
			"name": "Taylor Transport", "password": password,
			"command_id": "create-audit-boundary-account",
		},
		http.StatusCreated,
		"{\"id\":2,\"name\":\"Taylor Transport\",\"administrator\":false}\n",
	)
	malformed, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost,
		"http://"+server.address+"/admin/accounts/2/disable",
		strings.NewReader("{"),
	)
	if err != nil {
		t.Fatalf("create malformed Disable Account request: %v", err)
	}
	malformed.Header.Set("Content-Type", "application/json")
	malformedResponse, err := administrator.Do(malformed)
	if err != nil {
		t.Fatalf("send malformed Disable Account request: %v", err)
	}
	if closeErr := malformedResponse.Body.Close(); closeErr != nil {
		t.Errorf("close malformed Disable Account response: %v", closeErr)
	}
	if malformedResponse.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed Disable Account status = %d, want %d", malformedResponse.StatusCode, http.StatusBadRequest)
	}
	assertJSONRequest(
		t, authenticatedClient(t), server.address, "/admin/accounts/2/disable",
		map[string]string{"command_id": "unauthenticated-disable", "reason": "access_revoked"},
		http.StatusUnauthorized, "authentication required\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts/2/disable",
		map[string]string{"reason": "access_revoked"},
		http.StatusBadRequest, "invalid request\n",
	)
	entries, _ := readAuditHistory(t, administrator, server.address)
	if len(entries) != 1 {
		t.Fatalf("Audit Entry count after transport failures = %d, want 1", len(entries))
	}

	const rejectedSecret = "Bearer should-not-enter-audit"
	invalid := map[string]string{"command_id": "validation-blocked-disable", "reason": rejectedSecret}
	for range 2 {
		assertJSONRequest(
			t, administrator, server.address, "/admin/accounts/2/disable",
			invalid, http.StatusUnprocessableEntity, "valid command_id and reason are required\n",
		)
	}
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts/2/disable",
		map[string]string{
			"command_id": "validation-blocked-disable", "reason": "access_revoked",
		},
		http.StatusConflict, "command_id was already used with a different payload\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts/1/disable",
		map[string]string{"command_id": "disable-last-administrator", "reason": "access_revoked"},
		http.StatusConflict, "last Administrator cannot be disabled\n",
	)
	transportActor := authenticatedClient(t)
	assertJSONRequest(
		t, transportActor, server.address, "/auth/sign-in",
		map[string]string{"name": "Taylor Transport", "password": password},
		http.StatusNoContent, "",
	)
	assertGETResponse(
		t, transportActor, server.address, "/admin/audit",
		http.StatusForbidden, "Administrator authority required\n",
	)
	assertJSONRequest(
		t, transportActor, server.address, "/admin/accounts/1/disable",
		map[string]string{"command_id": "unauthorized-disable", "reason": "access_revoked"},
		http.StatusForbidden, "Administrator authority required\n",
	)
	entries, auditBody := readAuditHistory(t, administrator, server.address)
	wantReasons := map[string]int{
		"disable_reason_required": 1,
		"command_id_conflict":     1,
		"last_administrator":      1,
		"administrator_required":  1,
	}
	for _, entry := range entries {
		if entry.Outcome == "Rejected" {
			wantReasons[entry.Reason]--
		}
	}
	for reason, remaining := range wantReasons {
		if remaining != 0 {
			t.Errorf("Rejected Audit Entries with reason %q remaining = %d", reason, remaining)
		}
	}
	if len(entries) != 5 {
		t.Errorf("Audit Entry count = %d, want 5", len(entries))
	}
	if strings.Contains(string(auditBody), rejectedSecret) {
		t.Error("Audit history contains rejected secret-like reason text")
	}
	server.stop(t)
}

type acceptanceAuditEntry struct {
	ActorAccountID int       `json:"actor_account_id"`
	ActorName      string    `json:"actor_name"`
	ServerTime     time.Time `json:"server_time"`
	Action         string    `json:"action"`
	TargetType     string    `json:"target_type"`
	TargetID       string    `json:"target_id"`
	Outcome        string    `json:"outcome"`
	Reason         string    `json:"reason"`
	Note           string    `json:"note"`
}

func readAuditHistory(
	t *testing.T,
	client *http.Client,
	address string,
) ([]acceptanceAuditEntry, []byte) {
	t.Helper()
	response := get(t, client, address, "/admin/audit")
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Audit history: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Audit history status = %d, want %d: %s", response.StatusCode, http.StatusOK, body)
	}
	var entries []acceptanceAuditEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		t.Fatalf("decode Audit history: %v", err)
	}
	return entries, body
}

func TestRejectedEventGrantRetryReturnsOriginalOutcome(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts",
		map[string]string{
			"name": "Pat Producer", "password": "producer correct horse battery staple",
			"command_id": "create-account-pat",
		},
		http.StatusCreated,
		"{\"id\":2,\"name\":\"Pat Producer\",\"administrator\":false}\n",
	)
	grant := map[string]any{
		"account_id": 2, "role": "Producer", "command_id": "grant-missing-event",
	}
	for range 2 {
		assertJSONRequest(
			t, administrator, server.address, "/admin/events/99/grants",
			grant, http.StatusNotFound, "Event not found\n",
		)
	}
	server.stop(t)
}

func TestProducerGrantControlsEventCrewRead(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	assertJSONRequest(
		t, administrator, server.address, "/admin/events",
		validEventInput(), http.StatusCreated,
		"{\"id\":1,\"name\":\"Revision 2026\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts",
		map[string]string{
			"name": "Pat Producer", "password": "producer correct horse battery staple",
			"command_id": "create-account-pat",
		},
		http.StatusCreated,
		"{\"id\":2,\"name\":\"Pat Producer\",\"administrator\":false}\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/events/1/grants",
		map[string]any{"account_id": 2, "role": "Producer", "command_id": "grant-pat-producer"},
		http.StatusCreated,
		"{\"event_id\":1,\"account_id\":2,\"role\":\"Producer\"}\n",
	)

	producer := authenticatedClient(t)
	assertJSONRequest(
		t, producer, server.address, "/auth/sign-in",
		map[string]string{
			"name": "Pat Producer", "password": "producer correct horse battery staple",
		},
		http.StatusNoContent, "",
	)
	assertGETResponse(
		t, producer, server.address, "/crew/events/1", http.StatusOK,
		"{\"id\":1,\"name\":\"Revision 2026\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	assertGETResponse(
		t, administrator, server.address, "/crew/events/1",
		http.StatusForbidden, "Event access denied\n",
	)
	server.stop(t)
}

func TestAdministratorAuthorityDoesNotPermitEventCrewMutation(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	assertJSONRequest(
		t, administrator, server.address, "/admin/events",
		validEventInput(), http.StatusCreated,
		"{\"id\":1,\"name\":\"Revision 2026\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	changed := validEventInput()
	changed["name"] = "Changed without an Event Grant"
	changed["command_id"] = "update-event-without-grant"
	assertJSONMethodRequest(
		t, http.MethodPut, administrator, server.address, "/crew/events/1",
		changed, http.StatusForbidden, "Event access denied\n",
	)
	server.stop(t)
}

func validEventInput() map[string]string {
	return map[string]string{
		"name":               "Revision 2026",
		"planned_start_date": "2026-08-21",
		"planned_end_date":   "2026-08-23",
		"timezone":           "Europe/Berlin",
		"event_locale":       "de-DE",
		"content_language":   "en-GB",
		"event_day_boundary": "06:00",
		"command_id":         "create-event-1",
	}
}

func startAuthenticatedAdministrator(t *testing.T) (*http.Client, *runningServer) {
	t.Helper()
	return startAuthenticatedAdministratorWithListeners(t, false)
}

func startAuthenticatedAdministratorWithPublicListener(
	t *testing.T,
) (*http.Client, *runningServer) {
	t.Helper()
	return startAuthenticatedAdministratorWithListeners(t, true)
}

func startAuthenticatedAdministratorWithListeners(
	t *testing.T,
	separatePublic bool,
) (*http.Client, *runningServer) {
	t.Helper()

	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)
	bootstrapToken := strings.TrimSpace(runBeamersOutput(t, bin, "bootstrap", "--data-dir", dataDir))
	client := authenticatedClient(t)
	var server *runningServer
	if separatePublic {
		server = startBeamersWithPublicListener(t, bin, dataDir)
	} else {
		server = startBeamers(t, bin, dataDir)
	}
	assertJSONRequest(
		t,
		client,
		server.address,
		"/auth/bootstrap",
		map[string]string{
			"bootstrap_token": bootstrapToken,
			"name":            "Ada Admin",
			"password":        "correct horse battery staple",
		},
		http.StatusCreated,
		"",
	)
	return client, server
}

func TestSignInFailuresAreGenericAndRateLimited(t *testing.T) {
	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)
	bootstrapToken := strings.TrimSpace(runBeamersOutput(t, bin, "bootstrap", "--data-dir", dataDir))

	client := authenticatedClient(t)
	server := startBeamers(t, bin, dataDir)
	assertJSONRequest(
		t,
		client,
		server.address,
		"/auth/bootstrap",
		map[string]string{
			"bootstrap_token": bootstrapToken,
			"name":            "Ada Admin",
			"password":        "correct horse battery staple",
		},
		http.StatusCreated,
		"",
	)
	assertJSONRequest(t, client, server.address, "/auth/sign-out", nil, http.StatusNoContent, "")

	assertJSONRequest(
		t,
		client,
		server.address,
		"/auth/sign-in",
		map[string]string{"name": "Unknown Account", "password": "wrong password"},
		http.StatusUnauthorized,
		"authentication failed\n",
	)
	for range 5 {
		assertJSONRequest(
			t,
			client,
			server.address,
			"/auth/sign-in",
			map[string]string{"name": "Ada Admin", "password": "wrong password"},
			http.StatusUnauthorized,
			"authentication failed\n",
		)
	}
	assertJSONRequest(
		t,
		client,
		server.address,
		"/auth/sign-in",
		map[string]string{
			"name":     "Ada Admin",
			"password": "correct horse battery staple",
		},
		http.StatusTooManyRequests,
		"authentication failed\n",
	)
	server.stop(t)
}

func TestInvalidDisplayCookiesCannotBypassEnrollmentRateLimit(t *testing.T) {
	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)
	server := startBeamers(t, bin, dataDir)
	client := &http.Client{Timeout: 5 * time.Second}

	for attempt := range 11 {
		request, err := http.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			"http://"+server.address+"/display",
			http.NoBody,
		)
		if err != nil {
			t.Fatalf("create Display Enrollment request: %v", err)
		}
		request.AddCookie(&http.Cookie{Name: "beamers_display", Value: "invalid"})
		request.AddCookie(&http.Cookie{Name: "beamers_display_enrollment", Value: "invalid"})
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("send Display Enrollment request: %v", err)
		}
		if closeErr := response.Body.Close(); closeErr != nil {
			t.Fatalf("close Display Enrollment response: %v", closeErr)
		}
		want := http.StatusOK
		if attempt == 10 {
			want = http.StatusTooManyRequests
		}
		if response.StatusCode != want {
			t.Fatalf(
				"Display Enrollment attempt %d = %d, want %d",
				attempt+1,
				response.StatusCode,
				want,
			)
		}
	}
	server.stop(t)
}

func TestPlaintextNonLoopbackRefusesAuthentication(t *testing.T) {
	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)
	bootstrapToken := strings.TrimSpace(runBeamersOutput(t, bin, "bootstrap", "--data-dir", dataDir))

	server := startBeamersAt(t, bin, dataDir, "0.0.0.0:0")
	_, port, err := net.SplitHostPort(server.address)
	if err != nil {
		t.Fatalf("parse non-loopback listener address: %v", err)
	}
	dialAddress := net.JoinHostPort("127.0.0.1", port)
	assertJSONRequest(
		t,
		authenticatedClient(t),
		dialAddress,
		"/auth/bootstrap",
		map[string]string{
			"bootstrap_token": bootstrapToken,
			"name":            "Ada Admin",
			"password":        "correct horse battery staple",
		},
		http.StatusForbidden,
		"secure transport required\n",
	)
	server.stop(t)
}

func TestInstallationStartsHealthyAndRestarts(t *testing.T) {
	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")

	runBeamers(t, bin, "init", "--data-dir", dataDir)
	databasePath := filepath.Join(dataDir, "beamers.db")
	initialDatabase, err := os.Stat(databasePath)
	if err != nil {
		t.Fatalf("stat initialized database: %v", err)
	}

	first := startBeamers(t, bin, dataDir)
	assertProbe(t, first.address, "/livez", "live\n")
	assertProbe(t, first.address, "/readyz", "ready\n")
	first.stop(t)

	second := startBeamers(t, bin, dataDir)
	assertProbe(t, second.address, "/livez", "live\n")
	assertProbe(t, second.address, "/readyz", "ready\n")
	second.stop(t)
	restartedDatabase, err := os.Stat(databasePath)
	if err != nil {
		t.Fatalf("stat restarted database: %v", err)
	}
	if !os.SameFile(initialDatabase, restartedDatabase) {
		t.Error("restart replaced the initialized database")
	}

	output, err := exec.CommandContext(t.Context(), bin, "init", "--data-dir", dataDir).CombinedOutput()
	if err == nil {
		t.Fatalf("second initialization succeeded; output:\n%s", output)
	}
}

func TestHealthyInstallationExposesBoundedLocalDiagnostics(t *testing.T) {
	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)
	server := startBeamers(t, bin, dataDir)

	response := get(t, authenticatedClient(t), server.address, "/diagnostics")
	defer func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("close diagnostics response: %v", err)
		}
	}()
	var found struct {
		Mode    string `json:"mode"`
		Storage struct {
			Status string `json:"status"`
		} `json:"storage"`
		Backup struct {
			Status string `json:"status"`
		} `json:"backup"`
		Replication struct {
			Status string `json:"status"`
		} `json:"replication"`
		Streams map[string]struct {
			Status      string `json:"status"`
			Subscribers int    `json:"subscribers"`
		} `json:"streams"`
		Displays struct {
			Total    int            `json:"total"`
			Delivery map[string]int `json:"delivery"`
		} `json:"displays"`
		Telemetry struct {
			Status string `json:"status"`
		} `json:"telemetry"`
	}
	if err := json.NewDecoder(response.Body).Decode(&found); err != nil {
		t.Fatalf("decode diagnostics: %v", err)
	}
	if response.StatusCode != http.StatusOK ||
		response.Header.Get("Cache-Control") != "no-store" ||
		found.Mode != "normal" ||
		found.Storage.Status != "ready" ||
		found.Backup.Status != "available" ||
		found.Replication.Status != "disabled" ||
		found.Streams["display"].Status != "ready" ||
		found.Streams["program"].Status != "ready" ||
		found.Displays.Total != 0 ||
		found.Telemetry.Status != "disabled" {
		t.Fatalf("diagnostics = %d %+v, headers %v", response.StatusCode, found, response.Header)
	}
	server.stop(t)
}

func TestServeRequiresExactApprovalForRollbackIncompatibleUpgrade(t *testing.T) {
	bin := buildBeamers(t)
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)
	if err := storetest.DowngradeBeforeUpgradeContracts(
		t.Context(),
		filepath.Join(dataDir, "beamers.db"),
	); err != nil {
		t.Fatalf("prepare schema 47 fixture: %v", err)
	}

	server := startBeamers(t, bin, dataDir)
	assertProbe(t, server.address, "/livez", "live\n")
	readiness := requestProbe(t.Context(), server.address, "/readyz", time.Second)
	assertProbeResult(
		t,
		"/readyz",
		readiness,
		http.StatusServiceUnavailable,
		"not ready\n",
	)
	page := get(t, authenticatedClient(t), server.address, "/admin/events?event=1")
	body, readErr := io.ReadAll(page.Body)
	closeErr := page.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil ||
		page.StatusCode != http.StatusNotFound ||
		page.Header.Get("X-Beamers-Maintenance") != "" ||
		!bytes.Contains(body, []byte("404 page not found")) {
		t.Fatalf(
			"unregistered upgrade route = %d: %s (%v)",
			page.StatusCode,
			body,
			err,
		)
	}
	server.stop(t)

	var preview struct {
		PreviewDigest string `json:"preview_digest"`
	}
	if err := json.Unmarshal([]byte(runBeamersOutput(
		t,
		bin,
		"upgrade", "preview",
		"--data-dir", dataDir,
	)), &preview); err != nil || preview.PreviewDigest == "" {
		t.Fatalf("decode upgrade preview = %+v, %v", preview, err)
	}
	runBeamers(
		t,
		bin,
		"upgrade", "apply",
		"--data-dir", dataDir,
		"--approve-consequences",
		"--acknowledge-no-down-migration",
		"--preview-digest", preview.PreviewDigest,
	)
	upgraded := startBeamers(t, bin, dataDir)
	assertProbe(t, upgraded.address, "/readyz", "ready\n")
	upgraded.stop(t)

	backups, err := filepath.Glob(
		filepath.Join(root, ".data.beamers-upgrade-backup-*", "backup.zip"),
	)
	if err != nil || len(backups) != 1 {
		t.Fatalf("approved upgrade Backups = %v, %v", backups, err)
	}
	runBeamers(t, bin, "backup", "verify", "--input", backups[0])
}

func TestServeDoesNotInitializeStorage(t *testing.T) {
	bin := buildBeamers(t)
	missingDataDir := filepath.Join(t.TempDir(), "missing")

	missing := startBeamersAt(t, bin, missingDataDir, "0.0.0.0:0")
	assertRecoveryProbes(t, missing.address)
	assertLoopbackAddress(t, missing.address)
	missing.stop(t)
	if _, err := os.Stat(missingDataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("serve created missing data directory: %v", err)
	}

	uninitializedDataDir := t.TempDir()
	databasePath := filepath.Join(uninitializedDataDir, "beamers.db")
	if err := os.WriteFile(databasePath, nil, 0o600); err != nil {
		t.Fatalf("create uninitialized database: %v", err)
	}
	uninitialized := startBeamers(t, bin, uninitializedDataDir)
	assertRecoveryProbes(t, uninitialized.address)
	uninitialized.stop(t)
	contents, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatalf("read uninitialized database after serve: %v", err)
	}
	if len(contents) != 0 {
		t.Fatalf("serve changed uninitialized database to %d bytes", len(contents))
	}
	entries, err := os.ReadDir(uninitializedDataDir)
	if err != nil {
		t.Fatalf("read uninitialized data directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "beamers.db" {
		t.Fatalf("serve changed uninitialized data directory: %v", entries)
	}
}

func TestServeRefusesUnsupportedSchema(t *testing.T) {
	bin := buildBeamers(t)
	tests := []struct {
		name    string
		prepare func(context.Context, string) error
	}{
		{name: "newer version", prepare: storetest.MarkSchemaNewer},
		{name: "unknown migration", prepare: storetest.ReplaceMigrationChecksum},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataDir := filepath.Join(t.TempDir(), "data")
			runBeamers(t, bin, "init", "--data-dir", dataDir)
			if err := test.prepare(t.Context(), filepath.Join(dataDir, "beamers.db")); err != nil {
				t.Fatalf("prepare unsupported schema: %v", err)
			}
			server := startBeamers(t, bin, dataDir)
			assertRecoveryProbes(t, server.address)
			server.stop(t)
		})
	}
}

func TestUnsafeStartupOnlyServesLocalRecoveryDiagnostics(t *testing.T) {
	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)
	if err := storetest.MarkSchemaNewer(
		t.Context(),
		filepath.Join(dataDir, "beamers.db"),
	); err != nil {
		t.Fatalf("make installation schema unsafe: %v", err)
	}

	server := startBeamersAt(t, bin, dataDir, "0.0.0.0:0")
	assertLoopbackAddress(t, server.address)
	assertRecoveryProbes(t, server.address)
	assertRecoveryDiagnostics(t, server.address, "unsupported_schema", "")
	assertGETResponse(
		t,
		authenticatedClient(t),
		server.address,
		"/schedule",
		http.StatusNotFound,
		"404 page not found\n",
	)
	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"http://"+server.address+"/admin/events",
		strings.NewReader("{}"),
	)
	if err != nil {
		t.Fatalf("create recovery Crew request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	response, err := authenticatedClient(t).Do(request)
	if err != nil {
		t.Fatalf("send recovery Crew request: %v", err)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err = errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read recovery Crew response: %v", err)
	}
	if response.StatusCode != http.StatusNotFound ||
		response.Header.Get("X-Beamers-Build") != "" ||
		string(body) != "404 page not found\n" {
		t.Fatalf("recovery Crew response = %d %q, headers %v", response.StatusCode, body, response.Header)
	}
	server.stop(t)
}

func TestUnreadableInstallationMarkerStaysInLocalRecovery(t *testing.T) {
	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)
	markerPath := filepath.Join(dataDir, ".beamers.lock")
	if err := os.Chmod(markerPath, 0); err != nil {
		t.Fatalf("make installation marker unreadable: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(markerPath, 0o600)
	})

	server := startBeamersAt(t, bin, dataDir, "0.0.0.0:0")
	assertLoopbackAddress(t, server.address)
	assertRecoveryProbes(t, server.address)
	assertRecoveryDiagnostics(t, server.address, "unavailable", "open installation lock")
	server.stop(t)
}

func TestDamagedRestoreJournalStaysInLocalRecovery(t *testing.T) {
	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)
	backupPath := filepath.Join(t.TempDir(), "backup.zip")
	runBeamers(
		t,
		bin,
		"backup",
		"--data-dir", dataDir,
		"--output", backupPath,
	)
	journalPath := dataDir + ".beamers-restore.json"
	if err := os.WriteFile(journalPath, []byte("damaged"), 0o600); err != nil {
		t.Fatalf("damage Restore journal: %v", err)
	}

	server := startBeamersAt(t, bin, dataDir, "0.0.0.0:0")
	assertLoopbackAddress(t, server.address)
	assertRecoveryProbes(t, server.address)
	assertRecoveryDiagnostics(t, server.address, "unavailable", "recover interrupted Restore")
	server.stop(t)
	if contents, err := os.ReadFile(journalPath); err != nil || string(contents) != "damaged" {
		t.Fatalf("damaged Restore journal changed = %q, %v", contents, err)
	}

	runBeamersFails(
		t,
		bin,
		"restore", "quarantine-journal",
		"--data-dir", dataDir,
	)
	output := strings.TrimSpace(runBeamersOutput(
		t,
		bin,
		"restore", "quarantine-journal",
		"--data-dir", dataDir,
		"--acknowledge-damaged-journal",
	))
	const prefix = "preserved damaged Restore journal at "
	if !strings.HasPrefix(output, prefix) {
		t.Fatalf("quarantine journal output = %q", output)
	}
	preservedPath := strings.TrimPrefix(output, prefix)
	if contents, err := os.ReadFile(preservedPath); err != nil || string(contents) != "damaged" {
		t.Fatalf("preserved Restore journal = %q, %v", contents, err)
	}
	runBeamersOutput(
		t,
		bin,
		"restore", "preview",
		"--input", backupPath,
		"--data-dir", dataDir,
	)
}

func TestMissingDatabaseCannotBeReinitialized(t *testing.T) {
	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)
	if err := os.Remove(filepath.Join(dataDir, "beamers.db")); err != nil {
		t.Fatalf("remove initialized database: %v", err)
	}
	runBeamersFails(t, bin, "init", "--data-dir", dataDir)
}

func TestInitializationRequiresCompleteUnusedInstallationState(t *testing.T) {
	bin := buildBeamers(t)
	root := t.TempDir()

	failedCommandDataDir := filepath.Join(root, "failed-command-data")
	runBeamersFails(
		t,
		bin,
		"backup",
		"--data-dir", failedCommandDataDir,
		"--output", filepath.Join(root, "missing-backup.zip"),
	)
	runBeamersFails(t, bin, "bootstrap", "--data-dir", failedCommandDataDir)
	runBeamers(t, bin, "init", "--data-dir", failedCommandDataDir)

	journalDataDir := filepath.Join(root, "journal-data")
	if err := os.WriteFile(
		journalDataDir+".beamers-restore.json",
		[]byte("interrupted"),
		0o600,
	); err != nil {
		t.Fatalf("write Restore journal: %v", err)
	}
	runBeamersFails(t, bin, "init", "--data-dir", journalDataDir)
	if _, err := os.Stat(journalDataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("initialization changed journaled installation: %v", err)
	}

	usedAttachments := filepath.Join(root, "used-attachments")
	if err := os.Mkdir(usedAttachments, 0o700); err != nil {
		t.Fatalf("create used Attachment Store: %v", err)
	}
	if err := os.WriteFile(filepath.Join(usedAttachments, "owned"), []byte("state"), 0o600); err != nil {
		t.Fatalf("write Attachment state: %v", err)
	}
	attachmentDataDir := filepath.Join(root, "attachment-data")
	runBeamersFails(
		t,
		bin,
		"init",
		"--data-dir", attachmentDataDir,
		"--attachments-dir", usedAttachments,
	)
	if _, err := os.Stat(attachmentDataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("initialization changed installation with Attachment state: %v", err)
	}

	emptyAttachments := filepath.Join(root, "empty-attachments")
	if err := os.Mkdir(emptyAttachments, 0o700); err != nil {
		t.Fatalf("create empty Attachment Store: %v", err)
	}
	unusedDataDir := filepath.Join(root, "unused-data")
	runBeamers(
		t,
		bin,
		"init",
		"--data-dir", unusedDataDir,
		"--attachments-dir", emptyAttachments,
	)
}

func TestRestoreRequiresExclusiveDatabaseAndAttachmentRoots(t *testing.T) {
	bin := buildBeamers(t)
	root := t.TempDir()

	sourceDataDir := filepath.Join(root, "source")
	runBeamers(t, bin, "init", "--data-dir", sourceDataDir)
	backupPath := filepath.Join(root, "backup.zip")
	runBeamers(t, bin, "backup", "--data-dir", sourceDataDir, "--output", backupPath)

	targetDataDir := filepath.Join(root, "target")
	runBeamers(t, bin, "init", "--data-dir", targetDataDir)
	sharedAttachments := filepath.Join(root, "shared-attachments")
	if err := os.Mkdir(sharedAttachments, 0o700); err != nil {
		t.Fatalf("create shared Attachment Store: %v", err)
	}
	oldAttachment := filepath.Join(sharedAttachments, "owned")
	if err := os.WriteFile(oldAttachment, []byte("current"), 0o600); err != nil {
		t.Fatalf("write current Attachment state: %v", err)
	}
	planOutput := runBeamersOutput(
		t,
		bin,
		"restore", "preview",
		"--input", backupPath,
		"--data-dir", targetDataDir,
		"--attachments-dir", sharedAttachments,
	)
	var plan struct {
		JournalPath string `json:"journal_path"`
	}
	if err := json.Unmarshal([]byte(planOutput), &plan); err != nil || plan.JournalPath == "" {
		t.Fatalf("decode Restore plan: %+v, %v", plan, err)
	}

	holderDataDir := filepath.Join(root, "holder")
	runBeamers(t, bin, "init", "--data-dir", holderDataDir)
	holder := startBeamersWithAttachments(t, bin, holderDataDir, sharedAttachments)
	runBeamersFails(
		t,
		bin,
		"restore", "apply",
		"--journal", plan.JournalPath,
		"--acknowledge-replacement",
	)
	if contents, err := os.ReadFile(oldAttachment); err != nil || string(contents) != "current" {
		t.Fatalf("contended Restore changed Attachment state = %q, %v", contents, err)
	}
	holder.stop(t)

	runBeamers(
		t,
		bin,
		"restore", "apply",
		"--journal", plan.JournalPath,
		"--acknowledge-replacement",
	)
}

type runningServer struct {
	address       string
	publicAddress string
	bin           string
	dataDir       string
	cmd           *exec.Cmd
	done          chan error
}

var beamersTestBinary string

func TestMain(m *testing.M) {
	directory, err := os.MkdirTemp("", "beamers-acceptance-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "prepare acceptance binary: %v\n", err)
		os.Exit(1)
	}
	beamersTestBinary = filepath.Join(directory, "beamers")
	cmd := exec.CommandContext(
		context.Background(),
		"go",
		"build",
		"-o",
		beamersTestBinary,
		"../cmd/beamers",
	)
	if output, buildErr := cmd.CombinedOutput(); buildErr != nil {
		fmt.Fprintf(os.Stderr, "build acceptance binary: %v\n%s", buildErr, output)
		_ = os.RemoveAll(directory)
		os.Exit(1)
	}

	code := m.Run()
	if err = os.RemoveAll(directory); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "remove acceptance binary: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

func buildBeamers(t *testing.T) string {
	t.Helper()
	if beamersTestBinary == "" {
		t.Fatal("acceptance binary is unavailable")
	}
	return beamersTestBinary
}

func runBeamers(t *testing.T, bin string, args ...string) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), bin, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run beamers %v: %v\n%s", args, err, output)
	}
}

func runBeamersOutput(t *testing.T, bin string, args ...string) string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), bin, args...)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("run beamers %v: %v", args, err)
	}
	return string(output)
}

func runBeamersFails(t *testing.T, bin string, args ...string) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), bin, args...)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("beamers %v succeeded; output:\n%s", args, output)
	}
}

func startBeamers(t *testing.T, bin, dataDir string) *runningServer {
	t.Helper()
	return startBeamersAt(t, bin, dataDir, "127.0.0.1:0")
}

func startBeamersAt(t *testing.T, bin, dataDir, listenAddress string) *runningServer {
	t.Helper()
	return startBeamersWithAttachmentsAt(t, bin, dataDir, "", listenAddress)
}

func startBeamersWithAttachments(
	t *testing.T,
	bin, dataDir, attachmentsDir string,
) *runningServer {
	t.Helper()
	return startBeamersWithAttachmentsAt(
		t,
		bin,
		dataDir,
		attachmentsDir,
		"127.0.0.1:0",
	)
}

func startBeamersWithAttachmentsAt(
	t *testing.T,
	bin, dataDir, attachmentsDir, listenAddress string,
) *runningServer {
	t.Helper()
	return startBeamersWithAttachmentsAndPublicAt(
		t, bin, dataDir, attachmentsDir, listenAddress, false,
	)
}

func startBeamersWithPublicListener(t *testing.T, bin, dataDir string) *runningServer {
	t.Helper()
	return startBeamersWithAttachmentsAndPublicAt(
		t, bin, dataDir, "", "127.0.0.1:0", true,
	)
}

func startBeamersWithAttachmentsAndPublicAt(
	t *testing.T,
	bin, dataDir, attachmentsDir, listenAddress string,
	separatePublic bool,
) *runningServer {
	t.Helper()

	args := []string{"serve", "--data-dir", dataDir, "--listen", listenAddress}
	if attachmentsDir != "" {
		args = append(args, "--attachments-dir", attachmentsDir)
	}
	if separatePublic {
		args = append(args, "--public-listen", "127.0.0.1:0")
	}
	cmd := exec.CommandContext(t.Context(), bin, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("capture beamers stderr: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start beamers: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	server := &runningServer{bin: bin, dataDir: dataDir, cmd: cmd, done: done}
	t.Cleanup(func() {
		if server.cmd.Process != nil {
			_ = server.cmd.Process.Kill()
		}
	})
	server.address, server.publicAddress = waitForListeningAddresses(
		t, stderr, done, separatePublic,
	)
	return server
}

func waitForListeningAddresses(
	t *testing.T,
	stderr io.Reader,
	done <-chan error,
	separatePublic bool,
) (string, string) {
	t.Helper()

	type result struct {
		privateAddress string
		publicAddress  string
		err            error
	}
	listening := make(chan result, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		var privateAddress, publicAddress string
		for scanner.Scan() {
			var entry struct {
				Message string `json:"msg"`
				Address string `json:"address"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
				continue
			}
			switch entry.Message {
			case "server listening":
				privateAddress = entry.Address
			case "public server listening":
				publicAddress = entry.Address
			}
			if privateAddress != "" && (!separatePublic || publicAddress != "") {
				listening <- result{
					privateAddress: privateAddress,
					publicAddress:  publicAddress,
				}
				return
			}
		}
		listening <- result{err: scanner.Err()}
	}()

	select {
	case got := <-listening:
		if got.err != nil {
			t.Fatalf("read server startup: %v", got.err)
		}
		if got.privateAddress == "" || separatePublic && got.publicAddress == "" {
			t.Fatal("server exited without announcing its address")
		}
		return got.privateAddress, got.publicAddress
	case err := <-done:
		t.Fatalf("server exited during startup: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("server did not announce its address")
	}
	return "", ""
}

func assertProbe(t *testing.T, address, path, wantBody string) {
	t.Helper()
	result := requestProbe(t.Context(), address, path, 5*time.Second)
	assertProbeResult(t, path, result, http.StatusOK, wantBody)
}

func installationThemeForm(csrf, action, commandID string) url.Values {
	return url.Values{
		"csrf_token":       {csrf},
		"action":           {action},
		"command_id":       {commandID},
		"brand_asset":      {"signal"},
		"background_color": {"#080b15"},
		"surface_color":    {"#141a2c"},
		"border_color":     {"#7180aa"},
		"text_color":       {"#f1f5ff"},
		"muted_color":      {"#c2cbe0"},
		"accent_color":     {"#62ebcb"},
		"link_color":       {"#79d7ff"},
		"focus_color":      {"#ffdf6e"},
		"background":       {"nebula"},
		"typeface":         {"demoscene"},
		"transition":       {"fade"},
		"effect":           {"starfield"},
		"motion":           {"subtle"},
	}
}

func eventThemeForm(csrf, action, commandID string) url.Values {
	return url.Values{
		"csrf_token": {csrf},
		"action":     {action},
		"command_id": {commandID},
	}
}

func browserCSRFToken(
	t *testing.T,
	client *http.Client,
	address string,
	path string,
) string {
	t.Helper()

	response := get(t, client, address, path)
	defer func() { _ = response.Body.Close() }()
	_ = readResponseBody(t, response)
	pageURL, err := url.Parse("http://" + address + path)
	if err != nil {
		t.Fatalf("parse browser page URL: %v", err)
	}
	for _, cookie := range client.Jar.Cookies(pageURL) {
		if cookie.Name == "beamers_csrf" {
			return cookie.Value
		}
	}
	t.Fatal("browser page did not set a CSRF cookie")
	return ""
}

func postBrowserForm(
	t *testing.T,
	client *http.Client,
	address string,
	path string,
	values url.Values,
) (int, string) {
	t.Helper()

	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"http://"+address+path,
		strings.NewReader(values.Encode()),
	)
	if err != nil {
		t.Fatalf("create browser form request: %v", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("submit browser form: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode, readResponseBody(t, response)
}

func readResponseBody(t *testing.T, response *http.Response) string {
	t.Helper()

	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return string(body)
}

func assertGETContains(
	t *testing.T,
	client *http.Client,
	address string,
	path string,
	want string,
) {
	t.Helper()

	response := get(t, client, address, path)
	body := readResponseBody(t, response)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, want) {
		t.Fatalf("GET %s = %d %q, want %d containing %q", path, response.StatusCode, body, http.StatusOK, want)
	}
}

func postForm(
	t *testing.T,
	client *http.Client,
	address string,
	values url.Values,
) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"http://"+address+"/admin/displays/enroll",
		strings.NewReader(values.Encode()),
	)
	if err != nil {
		t.Fatalf("create form request: %v", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("send form request: %v", err)
	}
	return response
}

func crewBuild(t *testing.T, client *http.Client, address string) string {
	t.Helper()

	response := get(t, client, address, "/admin/displays")
	if closeErr := response.Body.Close(); closeErr != nil {
		t.Fatalf("close crew build response: %v", closeErr)
	}
	buildVersion := response.Header.Get("X-Beamers-Build")
	if buildVersion == "" {
		t.Fatal("crew response does not identify the server build")
	}
	return buildVersion
}

func assertRecoveryProbes(t *testing.T, address string) {
	t.Helper()
	assertProbe(t, address, "/livez", "live\n")
	readiness := requestProbe(t.Context(), address, "/readyz", 5*time.Second)
	assertProbeResult(t, "/readyz", readiness, http.StatusServiceUnavailable, "not ready\n")
}

func assertRecoveryDiagnostics(t *testing.T, address, wantStorage, wantDetail string) {
	t.Helper()
	response := get(t, authenticatedClient(t), address, "/diagnostics")
	var diagnostic struct {
		Mode    string `json:"mode"`
		Storage string `json:"storage"`
		Detail  string `json:"detail"`
	}
	decodeErr := json.NewDecoder(response.Body).Decode(&diagnostic)
	closeErr := response.Body.Close()
	if err := errors.Join(decodeErr, closeErr); err != nil {
		t.Fatalf("read recovery diagnostics: %v", err)
	}
	if response.StatusCode != http.StatusOK ||
		response.Header.Get("Cache-Control") != "no-store" ||
		diagnostic.Mode != "recovery" ||
		diagnostic.Storage != wantStorage ||
		diagnostic.Detail == "" ||
		len(diagnostic.Detail) > 512 ||
		!strings.Contains(diagnostic.Detail, wantDetail) {
		t.Fatalf("recovery diagnostics = %d %+v, headers %v", response.StatusCode, diagnostic, response.Header)
	}
}

func assertLoopbackAddress(t *testing.T, address string) {
	t.Helper()
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("parse server address %q: %v", address, err)
	}
	if host != "127.0.0.1" {
		t.Errorf("recovery server host = %q, want 127.0.0.1", host)
	}
}

type probeResult struct {
	status int
	body   string
	err    error
}

func requestProbe(ctx context.Context, address, path string, timeout time.Duration) probeResult {
	client := &http.Client{Timeout: timeout}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+path, http.NoBody)
	if err != nil {
		return probeResult{err: err}
	}
	response, err := client.Do(request)
	if err != nil {
		return probeResult{err: err}
	}
	body, err := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err != nil || closeErr != nil {
		return probeResult{err: errors.Join(err, closeErr)}
	}
	return probeResult{status: response.StatusCode, body: string(body)}
}

func authenticatedClient(t *testing.T) *http.Client {
	t.Helper()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("create cookie jar: %v", err)
	}
	return &http.Client{Jar: jar, Timeout: 5 * time.Second}
}

func assertJSONRequest(
	t *testing.T,
	client *http.Client,
	address string,
	path string,
	body any,
	wantStatus int,
	wantBody string,
) http.Header {
	t.Helper()

	result := requestJSON(t.Context(), client, address, path, body)
	if result.err != nil {
		t.Fatalf("POST %s: %v", path, result.err)
	}
	if result.status != wantStatus || result.body != wantBody {
		t.Fatalf(
			"POST %s = %d %q, want %d %q",
			path,
			result.status,
			result.body,
			wantStatus,
			wantBody,
		)
	}
	return result.header
}

func assertDisplayListContains(
	t *testing.T,
	client *http.Client,
	address string,
	want string,
) {
	t.Helper()

	const path = "/admin/displays"
	response := get(t, client, address, path)
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read GET %s: %v", path, err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), want) {
		t.Fatalf("GET %s = %d %q, want %d containing %q", path, response.StatusCode, body, http.StatusOK, want)
	}
}

func assertJSONMethodRequest(
	t *testing.T,
	method string,
	client *http.Client,
	address string,
	path string,
	body any,
	wantStatus int,
	wantBody string,
) {
	t.Helper()

	result := requestJSONMethod(t.Context(), method, client, address, path, body)
	if result.err != nil {
		t.Fatalf("%s %s: %v", method, path, result.err)
	}
	if result.status != wantStatus || result.body != wantBody {
		t.Fatalf(
			"%s %s = %d %q, want %d %q",
			method, path, result.status, result.body, wantStatus, wantBody,
		)
	}
}

type jsonResponse struct {
	header http.Header
	status int
	body   string
	err    error
}

type displaySnapshotState struct {
	ProtocolVersion      string `json:"protocolVersion"`
	AssetVersion         string `json:"assetVersion"`
	StreamID             string `json:"streamId"`
	StreamPosition       string `json:"streamPosition"`
	ActiveEventID        string `json:"activeEventId"`
	ActivationGeneration string `json:"activationGeneration"`
	PublishedRevision    string `json:"publishedRevision"`
	StageMessage         struct {
		ID       string `json:"id"`
		Revision string `json:"revision"`
	} `json:"stageMessage"`
	TechnicalDifficulties struct {
		ID       string `json:"id"`
		Revision string `json:"revision"`
	} `json:"technicalDifficulties"`
	UrgentNotice struct {
		ID       string `json:"id"`
		Revision string `json:"revision"`
	} `json:"urgentNotice"`
	EmergencyAlert struct {
		ID       string `json:"id"`
		Revision string `json:"revision"`
	} `json:"emergencyAlert"`
	Standby               bool   `json:"standby"`
	SnapshotToken         string `json:"snapshotToken"`
	ProgramOutputRevision string `json:"programOutputRevision"`
	ProgramOutput         struct {
		Kind  string `json:"kind"`
		Title string `json:"title"`
	} `json:"programOutput"`
	Composition struct {
		Layout struct {
			Key             string `json:"key"`
			RotationSeconds int    `json:"rotationSeconds"`
			Regions         []struct {
				Name       string `json:"name"`
				Widget     string `json:"widget"`
				Persistent bool   `json:"persistent"`
			} `json:"regions"`
		} `json:"layout"`
	} `json:"composition"`
}

type displayHealth struct {
	clockOffsetMilliseconds      int64
	clockUncertaintyMilliseconds int64
	rendererUnstable             bool
}

func readDisplaySnapshot(
	t *testing.T,
	client *http.Client,
	address string,
) displaySnapshotState {
	t.Helper()

	result := requestJSON(
		t.Context(),
		client,
		address,
		"/beamers.display.v1.DisplayService/GetSnapshot",
		map[string]any{},
	)
	if result.err != nil || result.status != http.StatusOK {
		t.Fatalf("Get Display Snapshot = %d %q, %v", result.status, result.body, result.err)
	}
	var decoded struct {
		Snapshot displaySnapshotState `json:"snapshot"`
	}
	if err := json.Unmarshal([]byte(result.body), &decoded); err != nil {
		t.Fatalf("decode Display Snapshot: %v", err)
	}
	return decoded.Snapshot
}

func acknowledgeDisplaySnapshot(
	t *testing.T,
	client *http.Client,
	address string,
	snapshot displaySnapshotState,
) {
	t.Helper()

	acknowledgeDisplaySnapshotWithHealth(
		t,
		client,
		address,
		snapshot,
		displayHealth{},
	)
}

func acknowledgeDisplaySnapshotWithHealth(
	t *testing.T,
	client *http.Client,
	address string,
	snapshot displaySnapshotState,
	health displayHealth,
) {
	t.Helper()

	result := requestDisplayAcknowledgment(t, client, address, snapshot, health)
	if result.err != nil || result.status != http.StatusOK {
		t.Fatalf("Acknowledge Display state = %d %q, %v", result.status, result.body, result.err)
	}
}

func requestDisplayAcknowledgment(
	t *testing.T,
	client *http.Client,
	address string,
	snapshot displaySnapshotState,
	health displayHealth,
) jsonResponse {
	t.Helper()

	result := requestJSON(
		t.Context(),
		client,
		address,
		"/beamers.display.v1.DisplayService/Acknowledge",
		map[string]any{
			"protocol_version":          snapshot.ProtocolVersion,
			"asset_version":             snapshot.AssetVersion,
			"stream_id":                 snapshot.StreamID,
			"stream_position":           snapshot.StreamPosition,
			"active_event_id":           snapshot.ActiveEventID,
			"activation_generation":     snapshot.ActivationGeneration,
			"published_revision":        snapshot.PublishedRevision,
			"stage_message_id":          protoJSONInteger(snapshot.StageMessage.ID),
			"stage_message_revision":    protoJSONInteger(snapshot.StageMessage.Revision),
			"technical_difficulties_id": protoJSONInteger(snapshot.TechnicalDifficulties.ID),
			"technical_difficulties_revision": protoJSONInteger(
				snapshot.TechnicalDifficulties.Revision,
			),
			"urgent_notice_id":       protoJSONInteger(snapshot.UrgentNotice.ID),
			"urgent_notice_revision": protoJSONInteger(snapshot.UrgentNotice.Revision),
			"emergency_alert_id":     protoJSONInteger(snapshot.EmergencyAlert.ID),
			"emergency_alert_revision": protoJSONInteger(
				snapshot.EmergencyAlert.Revision,
			),
			"standby":                        snapshot.Standby,
			"clock_offset_milliseconds":      health.clockOffsetMilliseconds,
			"clock_uncertainty_milliseconds": health.clockUncertaintyMilliseconds,
			"renderer_unstable":              health.rendererUnstable,
			"snapshot_token":                 snapshot.SnapshotToken,
		},
	)
	return result
}

func protoJSONInteger(value string) string {
	if value == "" {
		return "0"
	}
	return value
}

func requestJSON(
	ctx context.Context,
	client *http.Client,
	address string,
	path string,
	body any,
) jsonResponse {
	return requestJSONMethod(ctx, http.MethodPost, client, address, path, body)
}

func requestMultipart(
	ctx context.Context,
	client *http.Client,
	address, path string,
	fields map[string]string,
	filename, mediaType string,
	content []byte,
) jsonResponse {
	var encoded bytes.Buffer
	writer := multipart.NewWriter(&encoded)
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			return jsonResponse{err: err}
		}
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	header.Set("Content-Type", mediaType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return jsonResponse{err: err}
	}
	if _, err = part.Write(content); err != nil {
		return jsonResponse{err: err}
	}
	if err = writer.Close(); err != nil {
		return jsonResponse{err: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+path, &encoded)
	if err != nil {
		return jsonResponse{err: err}
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := client.Do(request)
	if err != nil {
		return jsonResponse{err: err}
	}
	responseBody, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return jsonResponse{err: err}
	}
	return jsonResponse{header: response.Header.Clone(), status: response.StatusCode, body: string(responseBody)}
}

func requestJSONMethod(
	ctx context.Context,
	method string,
	client *http.Client,
	address string,
	path string,
	body any,
) jsonResponse {
	encoded, err := json.Marshal(body)
	if err != nil {
		return jsonResponse{err: errors.Join(errors.New("encode JSON request"), err)}
	}
	request, err := http.NewRequestWithContext(
		ctx,
		method,
		"http://"+address+path,
		bytes.NewReader(encoded),
	)
	if err != nil {
		return jsonResponse{err: errors.Join(errors.New("create JSON request"), err)}
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return jsonResponse{err: err}
	}
	responseBody, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return jsonResponse{err: err}
	}
	return jsonResponse{
		header: response.Header.Clone(),
		status: response.StatusCode,
		body:   string(responseBody),
	}
}

func assertProtectedSessionCookie(t *testing.T, headers http.Header) {
	t.Helper()

	cookie := headers.Get("Set-Cookie")
	for _, attribute := range []string{"Path=/", "Expires=", "HttpOnly", "SameSite=Lax"} {
		if !strings.Contains(cookie, attribute) {
			t.Errorf("session cookie %q does not contain %q", cookie, attribute)
		}
	}
	if got := headers.Get("Cache-Control"); got != "no-store" {
		t.Errorf("authentication Cache-Control = %q, want no-store", got)
	}
}

func assertAuthenticated(t *testing.T, client *http.Client, address, wantName string) {
	t.Helper()

	response := get(t, client, address, "/auth/session")
	defer func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("close session response: %v", err)
		}
	}()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /auth/session status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	var got struct {
		Name          string `json:"name"`
		Administrator bool   `json:"administrator"`
	}
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatalf("decode session response: %v", err)
	}
	if got.Name != wantName || !got.Administrator {
		t.Errorf("session = %+v, want name %q and Administrator", got, wantName)
	}
}

func assertSessionRejected(t *testing.T, client *http.Client, address string) {
	t.Helper()

	response := get(t, client, address, "/auth/session")
	defer func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("close rejected session response: %v", err)
		}
	}()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read rejected session response: %v", err)
	}
	if response.StatusCode != http.StatusUnauthorized || string(body) != "authentication required\n" {
		t.Errorf(
			"GET /auth/session = %d %q, want %d %q",
			response.StatusCode,
			body,
			http.StatusUnauthorized,
			"authentication required\n",
		)
	}
}

func get(t *testing.T, client *http.Client, address, path string) *http.Response {
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
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return response
}

func assertGETResponse(
	t *testing.T,
	client *http.Client,
	address string,
	path string,
	wantStatus int,
	wantBody string,
) {
	t.Helper()
	response := get(t, client, address, path)
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read GET %s response: %v", path, err)
	}
	if response.StatusCode != wantStatus || string(body) != wantBody {
		t.Errorf(
			"GET %s = %d %q, want %d %q",
			path, response.StatusCode, body, wantStatus, wantBody,
		)
	}
}

func assertProbeResult(t *testing.T, path string, result probeResult, wantStatus int, wantBody string) {
	t.Helper()
	if result.err != nil {
		t.Fatalf("request %s: %v", path, result.err)
	}
	if result.status != wantStatus || result.body != wantBody {
		t.Errorf("GET %s = %d %q, want %d %q", path, result.status, result.body, wantStatus, wantBody)
	}
}

func (server *runningServer) stop(t *testing.T) {
	t.Helper()

	if err := server.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("stop beamers: %v", err)
	}
	select {
	case err := <-server.done:
		if err != nil {
			t.Fatalf("beamers shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("beamers did not stop after %s", 10*time.Second)
	}
	server.cmd.Process = nil
}
