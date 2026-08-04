package acceptance_test

import (
	"database/sql"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	sessionv1 "github.com/dotwaffle/beamers/gen/beamers/session/v1"
	"github.com/dotwaffle/beamers/gen/beamers/session/v1/sessionv1connect"
)

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
		"session." + strconv.FormatInt(sessionID, 10) + ".threshold-seconds-0": "remaining seconds must be between 1 and 86400",
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
