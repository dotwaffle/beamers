package acceptance_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
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
)

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

	rundownClient := connectClient(rundownv1connect.NewRundownServiceClient, client, server.address)
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

	activationClient := connectClient(activationv1connect.NewActivationServiceClient, client, server.address)
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
	restartedClient := connectClient(activationv1connect.NewActivationServiceClient, client, restarted.address)
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

	response := get(t, authenticatedClient(t), server.address, "/events/beamconf-2099/schedule")
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
		fmt.Sprintf(`href="/events/beamconf-2099/schedule/sessions/%d"`, publicSessionID),
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

	public := get(t, authenticatedClient(t), server.address, fmt.Sprintf("/events/beamconf-2099/schedule/sessions/%d", publicSessionID))
	publicBody, readErr := io.ReadAll(public.Body)
	closeErr := public.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read public Session: %v", err)
	}
	if public.StatusCode != http.StatusOK || !strings.Contains(string(publicBody), "Opening Keynote") {
		t.Errorf("public Session = %d %q, want 200 with title", public.StatusCode, publicBody)
	}
	ended := get(t, authenticatedClient(t), server.address, "/events/beamconf-2099/schedule/sessions/3")
	endedBody, readErr := io.ReadAll(ended.Body)
	closeErr = ended.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read ended public Session: %v", err)
	}
	if ended.StatusCode != http.StatusOK || !strings.Contains(string(endedBody), "Old Announcement") {
		t.Errorf("ended public Session = %d %q, want stable 200 deep link", ended.StatusCode, endedBody)
	}

	crewOnly := get(t, authenticatedClient(t), server.address, "/events/beamconf-2099/schedule/sessions/2")
	crewOnlyBody, readErr := io.ReadAll(crewOnly.Body)
	closeErr = crewOnly.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Crew Only Session response: %v", err)
	}
	unknown := get(t, authenticatedClient(t), server.address, "/events/beamconf-2099/schedule/sessions/999999")
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
	client := connectClient(rundownv1connect.NewRundownServiceClient, administrator, server.address)
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
	path := fmt.Sprintf("/events/beamconf-2099/schedule/sessions/%d", sessionID)
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
	unknown := get(t, authenticatedClient(t), server.address, "/events/beamconf-2099/schedule/sessions/999999")
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

	initial := get(t, publicClient, server.address, "/events/beamconf-2099/schedule")
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
		t.Context(), http.MethodGet, "http://"+server.address+"/events/beamconf-2099/schedule", http.NoBody,
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
	rundownClient := connectClient(rundownv1connect.NewRundownServiceClient, client, server.address)
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
		t.Context(), http.MethodGet, "http://"+server.address+"/events/beamconf-2099/schedule", http.NoBody,
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
		"/events/beamconf-2099/schedule?day=2099-08-21&location=1&lane=1&track=1&time_zone=America%2FNew_York",
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
		`<time datetime="2099-08-21T04:00:00-04:00">04:00</time>`,
		"CEST +02:00 10:00",
		fmt.Sprintf(`/events/beamconf-2099/schedule/sessions/%d?time_zone=America%%2FNew_York`, publicSessionID),
		`hx-get="/events/beamconf-2099/schedule?day=2099-08-21&amp;lane=1&amp;location=1&amp;time_zone=America%2FNew_York&amp;track=1"`,
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

	empty := get(t, publicClient, server.address, "/events/beamconf-2099/schedule?location=999999")
	emptyBody, readErr := io.ReadAll(empty.Body)
	closeErr = empty.Body.Close()
	if joinedErr := errors.Join(readErr, closeErr); joinedErr != nil {
		t.Fatalf("read unmatched Schedule filter: %v", joinedErr)
	}
	if empty.StatusCode != http.StatusOK || strings.Contains(string(emptyBody), "Opening Keynote") {
		t.Errorf("unmatched Schedule filter = %d %q", empty.StatusCode, emptyBody)
	}
	invalid := get(t, publicClient, server.address, "/events/beamconf-2099/schedule?lane=not-an-id")
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
	client := connectClient(sessionv1connect.NewSessionControlServiceClient, operator, server.address)
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
	client := connectClient(sessionv1connect.NewSessionControlServiceClient, operator, server.address)
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
