package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/dotwaffle/beamers/gen/beamers/program/v1/programv1connect"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/displays"
	"github.com/dotwaffle/beamers/internal/displaystream"
	"github.com/dotwaffle/beamers/internal/programconnect"
	"github.com/dotwaffle/beamers/internal/programcontrol"
	"github.com/dotwaffle/beamers/internal/programview"
)

const maxProgramControlRPCBodyBytes = 64 << 10

func registerProgramControlRoutes(
	mux *http.ServeMux,
	authentication *auth.Service,
	service *programcontrol.Service,
	displayService *displays.Service,
	displayStream *displaystream.Hub,
	programStream *displaystream.Hub,
	listenerAddress net.Addr,
	tracerProvider trace.TracerProvider,
	meterProvider metric.MeterProvider,
	propagator propagation.TextMapPropagator,
	buildVersion string,
	logger *slog.Logger,
) error {
	adapter, err := programconnect.NewHandler(
		service, displayService, displayStream, programStream,
	)
	if err != nil {
		return err
	}
	if err := registerConnectRoute(mux, connectRouteConfig{
		name: "Program control", authentication: authentication, listenerAddress: listenerAddress,
		tracerProvider: tracerProvider, meterProvider: meterProvider, propagator: propagator,
		errorInterceptor: programconnect.ErrorInterceptor(),
		maxBodyBytes:     maxProgramControlRPCBodyBytes,
		build: func(options ...connect.HandlerOption) (string, http.Handler) {
			return programv1connect.NewProgramControlServiceHandler(adapter, options...)
		},
	}); err != nil {
		return err
	}
	handlers := programViewHandlers{
		authentication: authentication,
		service:        service, logger: logger,
		allowPlaintextCrew: listenerIsLoopback(listenerAddress),
		buildVersion:       buildVersion,
		stream:             programStream,
	}
	mux.HandleFunc("/crew/program/{sessionID}", handlers.view)
	mux.HandleFunc("/crew/program/{sessionID}/events", handlers.events)
	mux.HandleFunc("/crew/program/assets/control.js", handlers.clientJavaScript)
	mux.HandleFunc("/crew/program/assets/control.css", handlers.stylesheet)
	return nil
}

type programViewHandlers struct {
	authentication     *auth.Service
	service            *programcontrol.Service
	logger             *slog.Logger
	allowPlaintextCrew bool
	buildVersion       string
	stream             *displaystream.Hub
}

type programInvalidation struct {
	StreamID        string `json:"stream_id"`
	StreamPosition  uint64 `json:"stream_position"`
	LiveRevision    int    `json:"live_state_revision"`
	ControlRevision int    `json:"control_state_revision"`
}

func (handlers programViewHandlers) events(response http.ResponseWriter, request *http.Request) {
	if !requestAllowed(response, request, http.MethodGet, handlers.allowPlaintextCrew) {
		return
	}
	actor, ok := handlers.authenticate(response, request)
	if !ok {
		return
	}
	sessionID, err := positivePathID(request, "sessionID")
	if err != nil {
		http.Error(response, "Program Channel not found", http.StatusNotFound)
		return
	}
	eventID, err := strconv.Atoi(request.URL.Query().Get("event_id"))
	if err != nil || eventID <= 0 {
		http.Error(response, "invalid Event", http.StatusBadRequest)
		return
	}
	after, err := strconv.ParseUint(request.Header.Get("Last-Event-ID"), 10, 64)
	if err != nil && request.Header.Get("Last-Event-ID") != "" {
		http.Error(response, "invalid Program stream position", http.StatusBadRequest)
		return
	}
	state, connectedChanged, release, err := handlers.service.OpenConnection(
		request.Context(), actor, eventID, sessionID,
	)
	if err != nil {
		http.Error(response, "Program stream unavailable", http.StatusForbidden)
		return
	}
	if connectedChanged {
		handlers.stream.Notify()
	}
	defer func() {
		if release() {
			handlers.stream.Notify()
		}
	}()
	cursor := handlers.stream.Cursor()
	if after > cursor.Position {
		after = cursor.Position
	}
	subscription := handlers.stream.Subscribe(after)
	defer subscription.Close()

	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("X-Accel-Buffering", "no")
	if err = writeProgramInvalidation(response, cursor, state); err != nil {
		return
	}
	heartbeats := time.NewTicker(displayHeartbeatInterval)
	defer heartbeats.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case notification, open := <-subscription.Notifications:
			if !open {
				return
			}
			state, err = handlers.service.Current(request.Context(), actor, eventID, sessionID)
			if err != nil || writeProgramInvalidation(response, notification, state) != nil {
				return
			}
		case <-heartbeats.C:
			if writeDisplayHeartbeat(response) != nil {
				return
			}
		}
	}
}

func writeProgramInvalidation(
	response http.ResponseWriter,
	cursor displaystream.Cursor,
	state programcontrol.State,
) error {
	data, err := json.Marshal(programInvalidation{
		StreamID: cursor.StreamID, StreamPosition: cursor.Position,
		LiveRevision: state.Channel.Revision, ControlRevision: state.ControlRevision,
	})
	if err != nil {
		return err
	}
	return writeDisplaySSE(response, fmt.Sprintf(
		"id: %d\nevent: invalidate\ndata: %s\n\n",
		cursor.Position,
		data,
	))
}

func (handlers programViewHandlers) view(response http.ResponseWriter, request *http.Request) {
	if !requestAllowed(response, request, http.MethodGet, handlers.allowPlaintextCrew) {
		return
	}
	actor, ok := handlers.authenticate(response, request)
	if !ok {
		return
	}
	sessionID, err := positivePathID(request, "sessionID")
	if err != nil {
		http.Error(response, "Program Channel not found", http.StatusNotFound)
		return
	}
	eventID, err := strconv.Atoi(request.URL.Query().Get("event_id"))
	if err != nil || eventID <= 0 {
		http.Error(response, "invalid Event", http.StatusBadRequest)
		return
	}
	state, err := handlers.service.Current(request.Context(), actor, eventID, sessionID)
	if err != nil {
		handlers.logger.ErrorContext(request.Context(), "load Program control View", "error", err)
		http.Error(response, "Program control unavailable", http.StatusForbidden)
		return
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	if err = programview.Page(
		eventID, sessionID, state.Channel.Name, handlers.buildVersion,
	).Render(request.Context(), response); err != nil {
		handlers.logger.ErrorContext(request.Context(), "render Program control View", "error", err)
	}
}

func (handlers programViewHandlers) clientJavaScript(
	response http.ResponseWriter,
	request *http.Request,
) {
	handlers.asset(response, request, "text/javascript; charset=utf-8", programview.ClientJavaScript())
}

func (handlers programViewHandlers) stylesheet(response http.ResponseWriter, request *http.Request) {
	handlers.asset(response, request, "text/css; charset=utf-8", programview.Stylesheet())
}

func (handlers programViewHandlers) asset(
	response http.ResponseWriter,
	request *http.Request,
	contentType string,
	content string,
) {
	if !publicMethodAllowed(response, request) {
		return
	}
	response.Header().Set("Content-Type", contentType)
	response.Header().Set("Cache-Control", "no-cache")
	if request.Method == http.MethodGet {
		_, _ = response.Write([]byte(content))
	}
}

func (handlers programViewHandlers) authenticate(
	response http.ResponseWriter,
	request *http.Request,
) (auth.Account, bool) {
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil {
		http.Error(response, "authentication required", http.StatusUnauthorized)
		return auth.Account{}, false
	}
	actor, err := handlers.authentication.Authenticate(request.Context(), cookie.Value)
	if errors.Is(err, auth.ErrInvalidSession) {
		http.Error(response, "authentication required", http.StatusUnauthorized)
		return auth.Account{}, false
	}
	if err != nil {
		handlers.logger.ErrorContext(request.Context(), "Program View authentication failed", "error", err)
		http.Error(response, "authentication unavailable", http.StatusInternalServerError)
		return auth.Account{}, false
	}
	return actor, true
}
