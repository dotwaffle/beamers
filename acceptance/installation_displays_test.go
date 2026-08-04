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
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	activationv1 "github.com/dotwaffle/beamers/gen/beamers/activation/v1"
	"github.com/dotwaffle/beamers/gen/beamers/activation/v1/activationv1connect"
	rundownv1 "github.com/dotwaffle/beamers/gen/beamers/rundown/v1"
	"github.com/dotwaffle/beamers/gen/beamers/rundown/v1/rundownv1connect"
	sessionv1 "github.com/dotwaffle/beamers/gen/beamers/session/v1"
	"github.com/dotwaffle/beamers/gen/beamers/session/v1/sessionv1connect"
)

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
		"[{\"id\":1,\"name\":\"Lobby Display\",\"active_event_id\":1,\"standby\":true,\"event_name\":\"BeamConf 2099\",\"delivery_state\":\"offline\",\"applied_active_event_id\":0,\"applied_activation_generation\":0,\"applied_published_revision\":0,\"applied_standby\":true,\"clock_offset_milliseconds\":0,\"clock_uncertainty_milliseconds\":0}]\n",
	)
	operator := provisionOperator(t, administrator, server)
	assertGETResponse(
		t, operator, server.address, "/admin/displays", http.StatusOK,
		"[{\"id\":1,\"name\":\"Lobby Display\",\"active_event_id\":1,\"standby\":true,\"event_name\":\"BeamConf 2099\",\"delivery_state\":\"offline\",\"applied_active_event_id\":0,\"applied_activation_generation\":0,\"applied_published_revision\":0,\"applied_standby\":true,\"clock_offset_milliseconds\":0,\"clock_uncertainty_milliseconds\":0}]\n",
	)
	activationClient := connectClient(activationv1connect.NewActivationServiceClient, administrator, server.address)
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
	rundownClient := connectClient(rundownv1connect.NewRundownServiceClient, administrator, server.address)
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
	for _, want := range []string{"Lobby Display", "BeamConf 2099", "Main Hall", "event-overview"} {
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

	sessionClient := connectClient(sessionv1connect.NewSessionControlServiceClient, administrator, server.address)
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
		`--display-foreground:#f0f6fc`,
		`--display-background:#0d1117`,
		`--display-surface:#171c23`,
		`--display-signal:#6cb6ff`,
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
	if staleCloseErr := stale.Body.Close(); staleCloseErr != nil {
		t.Errorf("close stale Display client response: %v", staleCloseErr)
	}

	// ADR 0048 puts every Display asset behind one version, so the stylesheet
	// must be content-addressed under the same digest as the client. A
	// stylesheet served off a separate version could let a cached kiosk pair
	// new markup with old styles.
	styleMatch := regexp.MustCompile(
		`href="(/display/assets/([0-9a-f]{64})/display\.css)"`,
	).FindStringSubmatch(page)
	if len(styleMatch) != 3 {
		t.Fatalf("Display entry document has no content-addressed stylesheet: %s", page)
	}
	if styleMatch[2] != assetMatch[2] {
		t.Errorf(
			"Display stylesheet asset version = %q, client = %q; want one version",
			styleMatch[2],
			assetMatch[2],
		)
	}
	stylesheet := get(t, displayClient, server.address, styleMatch[1])
	stylesheetBody, readErr := io.ReadAll(stylesheet.Body)
	closeErr = stylesheet.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read content-addressed Display stylesheet: %v", err)
	}
	if stylesheet.StatusCode != http.StatusOK || !strings.Contains(string(stylesheetBody), ".display-view") {
		t.Errorf(
			"content-addressed Display stylesheet = %d %q, want %d carrying .display-view",
			stylesheet.StatusCode,
			stylesheetBody,
			http.StatusOK,
		)
	}
	if got := stylesheet.Header.Get("Content-Type"); got != "text/css; charset=utf-8" {
		t.Errorf("Display stylesheet Content-Type = %q", got)
	}
	if got := stylesheet.Header.Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("Display stylesheet Cache-Control = %q", got)
	}
	for _, name := range []string{"chakra-petch-regular.ttf", "open-sans.ttf"} {
		fontPath := "/display/assets/" + styleMatch[2] + "/" + name
		if !strings.Contains(string(stylesheetBody), `url("`+name+`")`) {
			t.Errorf("Display stylesheet does not reference versioned font %q", name)
		}
		font := get(t, displayClient, server.address, fontPath)
		fontBody, fontReadErr := io.ReadAll(font.Body)
		fontCloseErr := font.Body.Close()
		if err := errors.Join(fontReadErr, fontCloseErr); err != nil {
			t.Fatalf("read content-addressed Display font %q: %v", name, err)
		}
		if font.StatusCode != http.StatusOK || len(fontBody) == 0 {
			t.Errorf(
				"content-addressed Display font %q = %d with %d bytes",
				name,
				font.StatusCode,
				len(fontBody),
			)
		}
		if got := font.Header.Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
			t.Errorf("Display font %q Cache-Control = %q", name, got)
		}
	}
	staleStyle := get(
		t,
		displayClient,
		server.address,
		"/display/assets/"+strings.Repeat("0", 64)+"/display.css",
	)
	if staleStyle.StatusCode != http.StatusNotFound {
		t.Errorf("stale Display stylesheet = %d, want %d", staleStyle.StatusCode, http.StatusNotFound)
	}
	if staleStyleCloseErr := staleStyle.Body.Close(); staleStyleCloseErr != nil {
		t.Errorf("close stale Display stylesheet response: %v", staleStyleCloseErr)
	}
	staleFont := get(
		t,
		displayClient,
		server.address,
		"/display/assets/"+strings.Repeat("0", 64)+"/open-sans.ttf",
	)
	if staleFont.StatusCode != http.StatusNotFound {
		t.Errorf("stale Display font = %d, want %d", staleFont.StatusCode, http.StatusNotFound)
	}
	if staleFontCloseErr := staleFont.Body.Close(); staleFontCloseErr != nil {
		t.Errorf("close stale Display font response: %v", staleFontCloseErr)
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
