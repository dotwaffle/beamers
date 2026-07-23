package acceptance_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/cookiejar"
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

	claim := url.Values{"code": {code}, "name": {"Stage Left"}, "command_id": {"claim-stage-left"}}
	claimed := postForm(t, administrator, server.address, "/admin/displays/enroll", claim)
	claimedBody, readErr := io.ReadAll(claimed.Body)
	closeErr = claimed.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Display claim response: %v", err)
	}
	if claimed.StatusCode != http.StatusCreated || !strings.Contains(string(claimedBody), "Stage Left") {
		t.Fatalf("claim Display = %d %q", claimed.StatusCode, claimedBody)
	}
	reused := postForm(t, administrator, server.address, "/admin/displays/enroll", url.Values{
		"code": {code}, "name": {"Other Name"}, "command_id": {"reuse-stage-left-code"},
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
	claimed := postForm(t, administrator, server.address, "/admin/displays/enroll", url.Values{
		"code": {code}, "name": {"Lobby Display"}, "command_id": {"claim-lobby-display"},
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
		"[{\"id\":1,\"name\":\"Lobby Display\",\"active_event_id\":1,\"standby\":true,\"event_name\":\"Revision 2099\"}]\n",
	)
	operator := provisionOperator(t, administrator, server)
	assertGETResponse(
		t, operator, server.address, "/admin/displays", http.StatusOK,
		"[{\"id\":1,\"name\":\"Lobby Display\",\"active_event_id\":1,\"standby\":true,\"event_name\":\"Revision 2099\"}]\n",
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
		"[{\"id\":1,\"name\":\"Lobby Display\",\"active_event_id\":2,\"standby\":true,\"event_name\":\"Revision 2100\"}]\n",
	)
	restarted.stop(t)
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

	response := get(t, authenticatedClient(t), server.address, "/schedule")
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
		fmt.Sprintf(`href="/schedule/sessions/%d"`, publicSessionID),
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

	public := get(t, authenticatedClient(t), server.address, fmt.Sprintf("/schedule/sessions/%d", publicSessionID))
	publicBody, readErr := io.ReadAll(public.Body)
	closeErr := public.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read public Session: %v", err)
	}
	if public.StatusCode != http.StatusOK || !strings.Contains(string(publicBody), "Opening Keynote") {
		t.Errorf("public Session = %d %q, want 200 with title", public.StatusCode, publicBody)
	}
	ended := get(t, authenticatedClient(t), server.address, "/schedule/sessions/3")
	endedBody, readErr := io.ReadAll(ended.Body)
	closeErr = ended.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read ended public Session: %v", err)
	}
	if ended.StatusCode != http.StatusOK || !strings.Contains(string(endedBody), "Old Announcement") {
		t.Errorf("ended public Session = %d %q, want stable 200 deep link", ended.StatusCode, endedBody)
	}

	crewOnly := get(t, authenticatedClient(t), server.address, "/schedule/sessions/2")
	crewOnlyBody, readErr := io.ReadAll(crewOnly.Body)
	closeErr = crewOnly.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Crew Only Session response: %v", err)
	}
	unknown := get(t, authenticatedClient(t), server.address, "/schedule/sessions/999999")
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

func TestPublicScheduleSupportsConditionalPolling(t *testing.T) {
	client, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, client, server)
	publicClient := authenticatedClient(t)

	initial := get(t, publicClient, server.address, "/schedule")
	initialBody, readErr := io.ReadAll(initial.Body)
	closeErr := initial.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read initial public Schedule: %v", err)
	}
	if initial.StatusCode != http.StatusOK || len(initialBody) == 0 {
		t.Fatalf("initial public Schedule = %d %q, want nonempty 200", initial.StatusCode, initialBody)
	}
	etag := initial.Header.Get("ETag")
	if etag == "" {
		t.Fatal("initial public Schedule has no ETag")
	}
	if got := initial.Header.Get("Cache-Control"); got != "public, max-age=15, must-revalidate" {
		t.Errorf("public Schedule Cache-Control = %q", got)
	}

	request, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet, "http://"+server.address+"/schedule", http.NoBody,
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
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read conditional public Schedule: %v", err)
	}
	if conditional.StatusCode != http.StatusNotModified || len(conditionalBody) != 0 {
		t.Errorf(
			"conditional public Schedule = %d %q, want empty 304",
			conditional.StatusCode, conditionalBody,
		)
	}
	server.stop(t)
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
				EndBoundary:     rundownv1.Boundary_BOUNDARY_SOFT,
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
	public := get(t, authenticatedClient(t), server.address, "/schedule")
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
	deepLink := get(t, authenticatedClient(t), restarted.address, fmt.Sprintf("/schedule/sessions/%d", sessionID))
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
	_, unconfirmedErr := client.CorrectLiveDetails(t.Context(), connect.NewRequest(&sessionv1.CorrectLiveDetailsRequest{
		EventId: 1, SessionId: sessionID, CommandId: "unconfirmed-live-details",
		ExpectedLiveStateRevision: proto.Int64(2), Title: "Must Not Apply",
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}},
	}))
	if connect.CodeOf(unconfirmedErr) != connect.CodeFailedPrecondition {
		t.Errorf("unconfirmed Live Detail Correction error = %v, want FailedPrecondition", unconfirmedErr)
	}
	_, broadCorrectionErr := client.CorrectLiveDetails(t.Context(), connect.NewRequest(&sessionv1.CorrectLiveDetailsRequest{
		EventId: 1, SessionId: sessionID, CommandId: "reject-broad-live-correction",
		ExpectedLiveStateRevision: proto.Int64(2), Confirmed: true,
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"crew_notes"}},
	}))
	if connect.CodeOf(broadCorrectionErr) != connect.CodeInvalidArgument {
		t.Errorf("broad Live Detail Correction error = %v, want InvalidArgument", broadCorrectionErr)
	}

	public := get(t, authenticatedClient(t), server.address, "/schedule")
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
	deepLink := get(t, authenticatedClient(t), server.address, fmt.Sprintf("/schedule/sessions/%d", sessionID))
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
		run.GetSnapshot().GetEndBoundary() != rundownv1.Boundary_BOUNDARY_SOFT ||
		run.GetSnapshot().GetMinimumDuration().AsDuration() != 30*time.Minute ||
		len(run.GetSnapshot().GetLaneIds()) != 1 || len(run.GetSnapshot().GetLocationIds()) != 1 ||
		len(run.GetAmendments()) != 1 || run.GetAmendments()[0].GetId() != corrected.Msg.GetAmendmentId() ||
		run.GetAmendments()[0].GetDetails().GetTitle() != "Corrected Keynote" {
		t.Errorf("Session Run immutable history = %+v", run)
	}
	audits, _ := readAuditHistory(t, administrator, server.address)
	correctionAudits := 0
	for _, entry := range audits {
		if entry.Action == "CorrectLiveDetails" && entry.TargetType == "Session" &&
			entry.TargetID == strconv.FormatInt(sessionID, 10) && entry.Outcome == "Succeeded" {
			correctionAudits++
		}
	}
	if correctionAudits != 1 {
		t.Errorf("successful Live Detail Correction Audit Entries = %d, want 1", correctionAudits)
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
	public := get(t, authenticatedClient(t), server.address, "/schedule")
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

	listing := get(t, authenticatedClient(t), server.address, "/schedule")
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
	deepLink := get(t, authenticatedClient(t), server.address, fmt.Sprintf("/schedule/sessions/%d", sessionID))
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

func TestMissingDatabaseCannotBeReinitialized(t *testing.T) {
	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)
	if err := os.Remove(filepath.Join(dataDir, "beamers.db")); err != nil {
		t.Fatalf("remove initialized database: %v", err)
	}
	runBeamersFails(t, bin, "init", "--data-dir", dataDir)
}

type runningServer struct {
	address string
	bin     string
	dataDir string
	cmd     *exec.Cmd
	done    chan error
}

func buildBeamers(t *testing.T) string {
	t.Helper()

	bin := filepath.Join(t.TempDir(), "beamers")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "../cmd/beamers")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build beamers: %v\n%s", err, output)
	}
	return bin
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

	cmd := exec.CommandContext(t.Context(), bin, "serve", "--data-dir", dataDir, "--listen", listenAddress)
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
	server.address = waitForListeningAddress(t, stderr, done)
	return server
}

func waitForListeningAddress(t *testing.T, stderr io.Reader, done <-chan error) string {
	t.Helper()

	type result struct {
		address string
		err     error
	}
	listening := make(chan result, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			var entry struct {
				Message string `json:"msg"`
				Address string `json:"address"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
				continue
			}
			if entry.Message == "server listening" {
				listening <- result{address: entry.Address}
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
		if got.address == "" {
			t.Fatal("server exited without announcing its address")
		}
		return got.address
	case err := <-done:
		t.Fatalf("server exited during startup: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("server did not announce its address")
	}
	return ""
}

func assertProbe(t *testing.T, address, path, wantBody string) {
	t.Helper()
	result := requestProbe(t.Context(), address, path, 5*time.Second)
	assertProbeResult(t, path, result, http.StatusOK, wantBody)
}

func postForm(
	t *testing.T,
	client *http.Client,
	address string,
	path string,
	values url.Values,
) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost, "http://"+address+path, strings.NewReader(values.Encode()),
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

func assertRecoveryProbes(t *testing.T, address string) {
	t.Helper()
	assertProbe(t, address, "/livez", "live\n")
	readiness := requestProbe(t.Context(), address, "/readyz", 5*time.Second)
	assertProbeResult(t, "/readyz", readiness, http.StatusServiceUnavailable, "not ready\n")
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

func assertJSONMethodRequest(
	t *testing.T,
	method string,
	client *http.Client,
	address string,
	path string,
	body any,
	wantStatus int,
	wantBody string,
) http.Header {
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
	return result.header
}

type jsonResponse struct {
	header http.Header
	status int
	body   string
	err    error
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
	for _, attribute := range []string{"Path=/", "Expires=", "HttpOnly", "SameSite=Strict"} {
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
