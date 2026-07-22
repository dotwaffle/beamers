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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
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
		`<html lang="en-GB">`,
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
