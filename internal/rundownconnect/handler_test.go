package rundownconnect_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	_ "github.com/dotwaffle/beamers/ent/runtime"
	rundownv1 "github.com/dotwaffle/beamers/gen/beamers/rundown/v1"
	"github.com/dotwaffle/beamers/gen/beamers/rundown/v1/rundownv1connect"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/connectapi"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/rundownconnect"
	"github.com/dotwaffle/beamers/internal/store"
)

func TestRundownHandlerTracer(t *testing.T) {
	storage, authentication, session, eventID := openHandlerTest(t)
	commands, err := rundown.NewCommands(storage, time.Now)
	if err != nil {
		t.Fatalf("create Rundown Commands: %v", err)
	}
	queries, err := rundown.NewQueries(storage)
	if err != nil {
		t.Fatalf("create Rundown Queries: %v", err)
	}
	adapter, err := rundownconnect.NewHandler(commands, queries)
	if err != nil {
		t.Fatalf("create Rundown Connect handler: %v", err)
	}
	interceptor, err := connectapi.AuthenticationInterceptor(authentication)
	if err != nil {
		t.Fatalf("create authentication interceptor: %v", err)
	}
	telemetryInterceptor, err := otelconnect.NewInterceptor(
		otelconnect.WithTracerProvider(tracenoop.NewTracerProvider()),
		otelconnect.WithMeterProvider(noop.NewMeterProvider()),
		otelconnect.WithPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{}, propagation.Baggage{},
		)),
		otelconnect.WithoutServerPeerAttributes(),
		otelconnect.WithoutTraceEvents(),
	)
	if err != nil {
		t.Fatalf("create telemetry interceptor: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle(rundownv1connect.NewRundownServiceHandler(
		adapter,
		connect.WithInterceptors(
			telemetryInterceptor,
			connectapi.RequestIDInterceptor(),
			rundownconnect.ErrorInterceptor(),
			interceptor,
			rundownconnect.ValidationInterceptor(),
		),
	))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := rundownv1connect.NewRundownServiceClient(server.Client(), server.URL, connect.WithProtoJSON())

	start := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	editRequest := connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: int64(eventID), CommandId: "rpc-edit-draft", ExpectedDraftRevision: 0,
		Locations: []*rundownv1.LocationDraft{{Ref: "main", Name: "Main Hall"}},
		Lanes: []*rundownv1.LaneDraft{{
			Ref: "main-lane", Name: "Main Lane",
			Location: &rundownv1.TargetRef{Target: &rundownv1.TargetRef_Ref{Ref: "main"}},
		}},
		Sessions: []*rundownv1.SessionDraft{{
			Ref: "opening", Title: "Opening", Type: rundownv1.SessionType_SESSION_TYPE_CEREMONY,
			AudienceVisibility: rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_CREW_ONLY,
			PlannedStart:       timestamppb.New(start), PlannedEnd: timestamppb.New(start.Add(time.Hour)),
			TimingPolicy:    rundownv1.TimingPolicy_TIMING_POLICY_FIXED_DURATION,
			MinimumDuration: durationpb.New(30 * time.Minute),
			StartBoundary:   rundownv1.Boundary_BOUNDARY_HARD,
			EndBoundary:     rundownv1.Boundary_BOUNDARY_SOFT,
			Lanes:           []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Ref{Ref: "main-lane"}}},
		}},
	})
	setSessionCookie(editRequest.Header(), session.Token)
	edited, err := client.EditDraft(t.Context(), editRequest)
	if err != nil {
		t.Fatalf("EditDraft RPC: %v", err)
	}
	if len(edited.Msg.GetChanges()) != 3 {
		t.Fatalf("EditDraft changes = %d, want 3", len(edited.Msg.GetChanges()))
	}
	if edited.Header().Get("X-Request-ID") == "" {
		t.Fatal("EditDraft response has no request ID")
	}
	sessionChangeID := edited.Msg.GetChanges()[2].GetId()
	previewRequest := connect.NewRequest(&rundownv1.PublishPreviewRequest{
		EventId: int64(eventID), ChangeIds: []int64{sessionChangeID},
	})
	setSessionCookie(previewRequest.Header(), session.Token)
	preview, err := client.PublishPreview(t.Context(), previewRequest)
	if err != nil {
		t.Fatalf("PublishPreview RPC: %v", err)
	}
	if len(preview.Msg.GetChangeIds()) != 3 || len(preview.Msg.GetAutoIncludedChangeIds()) != 2 {
		t.Fatalf("PublishPreview = %+v, want closed three-change selection", preview.Msg)
	}
	publishRequest := connect.NewRequest(&rundownv1.PublishRequest{
		EventId: int64(eventID), CommandId: "rpc-publish",
		Confirmation: &rundownv1.PublishConfirmation{
			DraftRevision:     preview.Msg.GetDraftRevision(),
			PublishedRevision: preview.Msg.GetPublishedRevision(),
			ChangeIds:         preview.Msg.GetChangeIds(), Fingerprint: preview.Msg.GetFingerprint(),
		},
	})
	setSessionCookie(publishRequest.Header(), session.Token)
	if _, publishErr := client.Publish(t.Context(), publishRequest); publishErr != nil {
		t.Fatalf("Publish RPC: %v", publishErr)
	}
	crewRequest := connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: int64(eventID)})
	setSessionCookie(crewRequest.Header(), session.Token)
	crew, err := client.GetCrewRundown(t.Context(), crewRequest)
	if err != nil {
		t.Fatalf("GetCrewRundown RPC: %v", err)
	}
	if crew.Msg.GetPublishedRevision() != 1 || len(crew.Msg.GetSessions()) != 1 ||
		len(crew.Msg.GetSessions()[0].GetLocationIds()) != 1 {
		t.Fatalf("Crew Rundown = %+v, want one Published Session with its Lane-derived Location", crew.Msg)
	}
}

func setSessionCookie(header http.Header, token string) {
	header.Set("Cookie", "beamers_session="+token)
}

func openHandlerTest(t *testing.T) (*store.SQLite, *auth.Service, auth.Session, int) {
	t.Helper()
	dataDir := t.TempDir()
	if err := store.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize storage: %v", err)
	}
	storage, err := store.Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := storage.Close(); closeErr != nil {
			t.Errorf("close storage: %v", closeErr)
		}
	})
	authentication, err := auth.New(storage, auth.DefaultConfig())
	if err != nil {
		t.Fatalf("create Authentication service: %v", err)
	}
	bootstrap, err := authentication.IssueBootstrap(t.Context())
	if err != nil {
		t.Fatalf("issue Administrator bootstrap: %v", err)
	}
	session, err := authentication.BootstrapAdministrator(
		t.Context(), bootstrap, "Producer", "correct horse battery staple",
	)
	if err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	eventService, err := events.New(storage, time.Now)
	if err != nil {
		t.Fatalf("create Event service: %v", err)
	}
	event, err := eventService.Create(t.Context(), session.Account, events.CreateInput{
		Name: "Revision 2026", PlannedStartDate: "2026-08-21", PlannedEndDate: "2026-08-23",
		Timezone: "Europe/Berlin", EventLocale: "de-DE", EventDayBoundary: "06:00",
		CommandID: "rpc-create-event",
	})
	if err != nil {
		t.Fatalf("create Event: %v", err)
	}
	if _, err := eventService.GrantEventAccess(
		t.Context(), session.Account, event.ID, session.Account.ID, "Producer", "rpc-grant-producer",
	); err != nil {
		t.Fatalf("grant Producer: %v", err)
	}
	return storage, authentication, session, event.ID
}
