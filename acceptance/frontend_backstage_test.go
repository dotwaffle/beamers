package acceptance_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	competitionv1 "github.com/dotwaffle/beamers/gen/beamers/competition/v1"
	"github.com/dotwaffle/beamers/internal/backup"
)

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
