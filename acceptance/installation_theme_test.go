package acceptance_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

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
	profileResponse := get(t, administrator, server.address, "/profile")
	defer func() { _ = profileResponse.Body.Close() }()
	profileBody := readResponseBody(t, profileResponse)
	profileCSRF := browserCSRFToken(t, administrator, server.address, "/profile")
	_, _ = postBrowserForm(
		t,
		administrator,
		server.address,
		"/profile",
		url.Values{
			"csrf_token":   {profileCSRF},
			"command_id":   {frontendNamedValues(profileBody, "command_id").Get("command_id")},
			"display_name": {"Ada Admin"},
			"published":    {"true"},
		},
	)

	// Selecting a bundled Preset populates the Draft form and stores nothing, so
	// the Producer can edit any value before saving. See ADR 0057.
	presetStatus, presetBody := postBrowserForm(
		t,
		administrator,
		server.address,
		"/backstage/themes",
		url.Values{
			"csrf_token": {csrf},
			"action":     {"load-preset"},
			"preset":     {"Demoscene"},
		},
	)
	if presetStatus != http.StatusOK {
		t.Fatalf("load Theme Preset = %d %q", presetStatus, presetBody)
	}
	for _, want := range []string{
		`value="#080b15"`,
		`value="#62ebcb"`,
		`value="starfield" selected`,
	} {
		if !strings.Contains(presetBody, want) {
			t.Errorf("loaded Demoscene Preset missing %q: %s", want, presetBody)
		}
	}
	unknownStatus, _ := postBrowserForm(
		t,
		administrator,
		server.address,
		"/backstage/themes",
		url.Values{
			"csrf_token": {csrf},
			"action":     {"load-preset"},
			"preset":     {"Bespoke"},
		},
	)
	if unknownStatus != http.StatusBadRequest {
		t.Errorf("unknown Theme Preset = %d, want %d", unknownStatus, http.StatusBadRequest)
	}

	invalid := installationThemeForm(csrf, "create-draft", "create-invalid-theme")
	invalid.Set("background_color", "retain-invalid")
	status, body := postBrowserForm(
		t,
		administrator,
		server.address,
		"/backstage/themes",
		invalid,
	)
	if status != http.StatusBadRequest ||
		!strings.Contains(body, `value="retain-invalid"`) {
		t.Fatalf("invalid Theme Draft = %d %q", status, body)
	}
	assertAccessibleFormErrors(t, frontendResponse{status: status, body: body}, map[string]string{
		"theme-background-color": "six-digit hexadecimal color",
	})

	inaccessible := installationThemeForm(csrf, "create-draft", "create-inaccessible-theme")
	inaccessible.Set("text_color", "#777777")
	inaccessible.Set("surface_color", "#ffffff")
	inaccessible.Set("background_color", "#ffffff")
	_, body = postBrowserForm(
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
	assertGETContains(t, administrator, server.address, "/assets/installation-theme.css", "#0d1117")

	blocked := installationThemeForm(csrf, "activate", "activate-inaccessible-theme")
	blocked.Set("revision_id", "1")
	blocked.Set("expected_active_revision_id", "0")
	status, body = postBrowserForm(
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
	assertGETContains(t, administrator, server.address, "/assets/installation-theme.css", "#0d1117")

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

	rollback := installationThemeForm(csrf, "activate", "stale-rollback-built-in-theme")
	rollback.Set("revision_id", "0")
	rollback.Set("expected_active_revision_id", "0")
	status, body = postBrowserForm(
		t,
		administrator,
		server.address,
		"/backstage/themes",
		rollback,
	)
	if status != http.StatusConflict {
		t.Fatalf("stale Theme rollback = %d %q", status, body)
	}
	assertAccessibleFormErrors(t, frontendResponse{status: status, body: body}, map[string]string{
		"theme-activation-confirmation": "active Theme changed",
	})
	correctedRollback := frontendActivationFormValues(
		t,
		body,
		"csrf_token",
		"command_id",
		"revision_id",
		"expected_active_revision_id",
	)
	correctedRollback.Set("action", "activate")
	status, body = postBrowserForm(
		t,
		administrator,
		server.address,
		"/backstage/themes",
		correctedRollback,
	)
	if status != http.StatusOK {
		t.Fatalf("corrected Theme rollback = %d %q", status, body)
	}
	assertGETContains(t, administrator, server.address, "/assets/installation-theme.css", "#0d1117")

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

	presetStatus, presetBody := postBrowserForm(
		t,
		producer,
		server.address,
		"/backstage/events/1/theme",
		url.Values{
			"csrf_token": {csrf},
			"action":     {"load-preset"},
			"preset":     {"Conference"},
		},
	)
	if presetStatus != http.StatusOK {
		t.Fatalf("load Event Theme Preset = %d %q", presetStatus, presetBody)
	}
	for _, want := range []string{
		`value="#11131a"`,
		`value="#9ab8ff"`,
		`value="fade" selected`,
	} {
		if !strings.Contains(presetBody, want) {
			t.Errorf("loaded Conference Event Preset missing %q: %s", want, presetBody)
		}
	}

	draft := eventThemeForm(csrf, "create-draft", "create-event-theme")
	draft.Set("background_color", "#112233")
	draft.Set("location-signage_accent_color", "#ffdf6e")
	draft.Set("location-signage_motion", "still")
	draft.Set("timeline_accent_color", "#f5b544")
	draft.Set("crew-overview_motion", "still")
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
		!strings.Contains(body, "#ffdf6e") ||
		!strings.Contains(body, `data-theme-variant="timeline"`) ||
		!strings.Contains(body, `data-theme-variant="crew-overview"`) {
		t.Fatalf("Event Theme preview = %d %q", status, body)
	}
	assertGETContains(
		t,
		producer,
		server.address,
		"/assets/events/1/theme.css",
		"#0d1117",
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
		"/events/beamconf-2099",
		"/assets/events/1/theme.css",
	)
	assertGETContains(
		t,
		producer,
		server.address,
		"/events/beamconf-2099/schedule",
		"/assets/events/1/theme.css",
	)
	assertGETContains(t, producer, server.address, "/events/beamconf-2099/schedule", "Pause effects")
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

	invalidEvent := eventThemeForm(csrf, "create-draft", "create-invalid-event-theme")
	invalidEvent.Set("background_color", "retain-event-invalid")
	status, body = postBrowserForm(
		t,
		producer,
		server.address,
		"/backstage/events/1/theme",
		invalidEvent,
	)
	if status != http.StatusBadRequest ||
		!strings.Contains(body, `value="retain-event-invalid"`) {
		t.Fatalf("invalid Event Theme Draft = %d %q", status, body)
	}
	assertAccessibleFormErrors(t, frontendResponse{status: status, body: body}, map[string]string{
		"event-theme-background-color": "six-digit hexadecimal color",
	})
	invalidVariant := eventThemeForm(
		csrf,
		"create-draft",
		"create-invalid-event-theme-variant",
	)
	invalidVariant.Set("accent_color", "#62ebcb")
	invalidVariant.Set("competition-output_accent_color", "retain-variant-invalid")
	invalidVariant.Set("location-signage_motion", "retain-second-invalid")
	status, body = postBrowserForm(
		t,
		producer,
		server.address,
		"/backstage/events/1/theme",
		invalidVariant,
	)
	if status != http.StatusBadRequest ||
		!strings.Contains(body, `value="retain-variant-invalid"`) {
		t.Fatalf("invalid Event Theme variant = %d %q", status, body)
	}
	assertAccessibleFormErrors(t, frontendResponse{status: status, body: body}, map[string]string{
		"event-theme-competition-output-accent-color": "six-digit hexadecimal color",
	})

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
	assertAccessibleFormErrors(t, frontendResponse{status: status, body: body}, map[string]string{
		"event-theme-activation-confirmation": "Activation is blocked",
	})

	rollback := eventThemeForm(csrf, "activate", "stale-rollback-event-theme")
	rollback.Set("revision_id", "0")
	rollback.Set("expected_active_revision_id", "0")
	status, body = postBrowserForm(
		t,
		producer,
		server.address,
		"/backstage/events/1/theme",
		rollback,
	)
	if status != http.StatusConflict {
		t.Fatalf("stale Event Theme rollback = %d %q", status, body)
	}
	assertAccessibleFormErrors(t, frontendResponse{status: status, body: body}, map[string]string{
		"event-theme-activation-confirmation": "active Event Theme changed",
	})
	correctedRollback := frontendActivationFormValues(
		t,
		body,
		"csrf_token",
		"command_id",
		"revision_id",
		"expected_active_revision_id",
	)
	correctedRollback.Set("action", "activate")
	status, body = postBrowserForm(
		t,
		producer,
		server.address,
		"/backstage/events/1/theme",
		correctedRollback,
	)
	if status != http.StatusOK {
		t.Fatalf("corrected Event Theme rollback = %d %q", status, body)
	}
	assertGETContains(
		t,
		producer,
		server.address,
		"/assets/events/1/theme.css",
		"#0d1117",
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
