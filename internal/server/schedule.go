package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/a-h/templ"

	"github.com/dotwaffle/beamers/internal/displaystream"
	"github.com/dotwaffle/beamers/internal/schedule"
)

type scheduleHandlers struct {
	schedule *schedule.Service
	stream   *displaystream.Hub
	metrics  *scheduleStreamMetrics
	logger   *slog.Logger
}

type scheduleStreamMetrics struct {
	connections            atomic.Uint64
	gapRecoveries          atomic.Uint64
	incompatibleRecoveries atomic.Uint64
	resnapshots            atomic.Uint64
	slowDrops              atomic.Uint64
	disconnects            atomic.Uint64
}

type scheduleStreamMetricSnapshot struct {
	Connections            uint64
	GapRecoveries          uint64
	IncompatibleRecoveries uint64
	Resnapshots            uint64
	SlowDrops              uint64
	Disconnects            uint64
}

func (metrics *scheduleStreamMetrics) snapshot() scheduleStreamMetricSnapshot {
	return scheduleStreamMetricSnapshot{
		Connections:            metrics.connections.Load(),
		GapRecoveries:          metrics.gapRecoveries.Load(),
		IncompatibleRecoveries: metrics.incompatibleRecoveries.Load(),
		Resnapshots:            metrics.resnapshots.Load(),
		SlowDrops:              metrics.slowDrops.Load(),
		Disconnects:            metrics.disconnects.Load(),
	}
}

func registerScheduleRoutes(
	mux *routeMux,
	service *schedule.Service,
	stream *displaystream.Hub,
	metrics *scheduleStreamMetrics,
	logger *slog.Logger,
) {
	handlers := scheduleHandlers{
		schedule: service, stream: stream, metrics: metrics, logger: logger,
	}
	mux.HandleFunc("/schedule", browserPageRoute(), handlers.list)
	mux.HandleFunc(
		"/schedule/events",
		routeContract{kind: publicInterface, persistent: true},
		handlers.events,
	)
	mux.HandleFunc("/schedule/sessions/{sessionID}", browserPageRoute(), handlers.session)
	mux.HandleFunc("/assets/schedule.css", publicRoute(), handlers.stylesheet)
	mux.HandleFunc("/assets/schedule.js", publicRoute(), handlers.script)
}

func (handlers scheduleHandlers) list(response http.ResponseWriter, request *http.Request) {
	if !publicMethodAllowed(response, request) {
		return
	}
	filter, err := publicScheduleFilter(request)
	if err != nil {
		http.Error(response, "invalid Schedule filters", http.StatusBadRequest)
		return
	}
	snapshot, err := currentScheduleSnapshot(
		request.Context(),
		handlers.stream,
		func(ctx context.Context) (schedule.Snapshot, error) {
			return handlers.schedule.Current(ctx, filter)
		},
	)
	if err != nil {
		if errors.Is(err, schedule.ErrInvalidFilter) {
			http.Error(response, "invalid Schedule filters", http.StatusBadRequest)
			return
		}
		handlers.logger.ErrorContext(request.Context(), "public Schedule read failed", "error", err)
		http.Error(response, "Schedule unavailable", http.StatusInternalServerError)
		return
	}
	if request.Header.Get("HX-Request") == "true" ||
		strings.Contains(request.Header.Get("Cache-Control"), "no-cache") {
		handlers.metrics.resnapshots.Add(1)
	}
	handlers.render(response, request, snapshot.ETag, schedule.Page(snapshot), "public Schedule") //nolint:contextcheck // Generated templ closures receive context when rendered.
}

func currentScheduleSnapshot(
	ctx context.Context,
	stream *displaystream.Hub,
	load func(context.Context) (schedule.Snapshot, error),
) (schedule.Snapshot, error) {
	cursor := stream.Cursor()
	snapshot, err := load(ctx)
	if err != nil {
		return schedule.Snapshot{}, err
	}
	snapshot.StreamID = cursor.StreamID
	snapshot.StreamPosition = cursor.Position
	return snapshot, nil
}

func (handlers scheduleHandlers) events(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", http.MethodGet)
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cursor := handlers.stream.Cursor()
	after, err := scheduleStreamPosition(request)
	if err != nil {
		http.Error(response, "invalid Schedule stream position", http.StatusBadRequest)
		return
	}
	streamChanged := request.URL.Query().Get("stream_id") != cursor.StreamID
	unknownPosition := after > cursor.Position
	incompatible := streamChanged || unknownPosition
	if incompatible {
		handlers.metrics.incompatibleRecoveries.Add(1)
		after = cursor.Position
	} else if after < cursor.Position {
		handlers.metrics.gapRecoveries.Add(1)
	}
	subscription := handlers.stream.Subscribe(after)
	defer subscription.Close()
	handlers.metrics.connections.Add(1)

	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("X-Accel-Buffering", "no")
	if err = writeDisplayHeartbeat(response); err != nil {
		handlers.metrics.disconnects.Add(1)
		return
	}
	if incompatible {
		if err = writeScheduleInvalidation(response, cursor); err != nil {
			handlers.metrics.disconnects.Add(1)
			return
		}
	}
	heartbeats := time.NewTicker(displayHeartbeatInterval)
	defer heartbeats.Stop()
	for {
		select {
		case <-request.Context().Done():
			handlers.metrics.disconnects.Add(1)
			return
		case notification, open := <-subscription.Notifications:
			if !open {
				handlers.metrics.slowDrops.Add(1)
				return
			}
			if writeScheduleInvalidation(response, notification) != nil {
				handlers.metrics.disconnects.Add(1)
				return
			}
		case <-heartbeats.C:
			if writeDisplayHeartbeat(response) != nil {
				handlers.metrics.disconnects.Add(1)
				return
			}
		}
	}
}

func scheduleStreamPosition(request *http.Request) (uint64, error) {
	value := request.Header.Get("Last-Event-ID")
	if value == "" {
		value = request.URL.Query().Get("after")
	}
	if value == "" {
		return 0, nil
	}
	return strconv.ParseUint(value, 10, 64)
}

func writeScheduleInvalidation(
	response http.ResponseWriter,
	cursor displaystream.Cursor,
) error {
	return writeDisplaySSE(response, fmt.Sprintf(
		"id: %d\nevent: schedule\ndata: refresh\n\n",
		cursor.Position,
	))
}

func (handlers scheduleHandlers) session(response http.ResponseWriter, request *http.Request) {
	if !publicMethodAllowed(response, request) {
		return
	}
	sessionID, err := positivePathID(request, "sessionID")
	if err != nil {
		publicSessionNotFound(response)
		return
	}
	snapshot, session, ok, err := handlers.schedule.Find(
		request.Context(), sessionID, request.URL.Query().Get("time_zone"),
	)
	if err != nil {
		if errors.Is(err, schedule.ErrInvalidFilter) {
			http.Error(response, "invalid Schedule filters", http.StatusBadRequest)
			return
		}
		handlers.logger.ErrorContext(request.Context(), "public Session read failed", "error", err)
		http.Error(response, "Schedule unavailable", http.StatusInternalServerError)
		return
	}
	if !ok {
		publicSessionNotFound(response)
		return
	}
	handlers.render(response, request, snapshot.ETag, schedule.SessionPage(snapshot, session), "public Session") //nolint:contextcheck // Generated templ closures receive context when rendered.
}

func publicScheduleFilter(request *http.Request) (schedule.Filter, error) {
	locationID, err := optionalPositiveQueryID(request, "location")
	if err != nil {
		return schedule.Filter{}, err
	}
	laneID, err := optionalPositiveQueryID(request, "lane")
	if err != nil {
		return schedule.Filter{}, err
	}
	trackID, err := optionalPositiveQueryID(request, "track")
	if err != nil {
		return schedule.Filter{}, err
	}
	return schedule.Filter{
		Day: request.URL.Query().Get("day"), LocationID: locationID,
		LaneID: laneID, TrackID: trackID,
		ViewerTimezone: request.URL.Query().Get("time_zone"),
	}, nil
}

func optionalPositiveQueryID(request *http.Request, name string) (int, error) {
	value := request.URL.Query().Get(name)
	if value == "" {
		return 0, nil
	}
	id, err := strconv.Atoi(value)
	if err != nil || id <= 0 {
		return 0, schedule.ErrInvalidFilter
	}
	return id, nil
}

func (handlers scheduleHandlers) render(
	response http.ResponseWriter,
	request *http.Request,
	etag string,
	component templ.Component,
	name string,
) {
	setScheduleHeaders(response, etag)
	if scheduleNotModified(response, request, etag) {
		return
	}
	var content bytes.Buffer
	if err := component.Render(request.Context(), &content); err != nil {
		handlers.logger.ErrorContext(request.Context(), "render "+name, "error", err)
		http.Error(response, "Schedule unavailable", http.StatusInternalServerError)
		return
	}
	if request.Method == http.MethodHead {
		return
	}
	_, _ = response.Write(content.Bytes())
}

func publicSessionNotFound(response http.ResponseWriter) {
	http.Error(response, "Session not found", http.StatusNotFound)
}

func (handlers scheduleHandlers) stylesheet(response http.ResponseWriter, request *http.Request) {
	if !publicMethodAllowed(response, request) {
		return
	}
	content, err := schedule.Stylesheet()
	if err != nil {
		handlers.logger.ErrorContext(request.Context(), "read public Schedule stylesheet", "error", err)
		http.Error(response, "stylesheet unavailable", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Cache-Control", "public, max-age=3600")
	response.Header().Set("Content-Type", "text/css; charset=utf-8")
	if request.Method == http.MethodHead {
		return
	}
	_, _ = response.Write(content)
}

func (handlers scheduleHandlers) script(response http.ResponseWriter, request *http.Request) {
	if !publicMethodAllowed(response, request) {
		return
	}
	content, err := schedule.Script()
	if err != nil {
		handlers.logger.ErrorContext(request.Context(), "read public Schedule script", "error", err)
		http.Error(response, "script unavailable", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Cache-Control", "public, max-age=3600")
	response.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	if request.Method == http.MethodHead {
		return
	}
	_, _ = response.Write(content)
}

func publicMethodAllowed(response http.ResponseWriter, request *http.Request) bool {
	if request.Method == http.MethodGet || request.Method == http.MethodHead {
		return true
	}
	response.Header().Set("Allow", "GET, HEAD")
	http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

func setScheduleHeaders(response http.ResponseWriter, etag string) {
	response.Header().Set("Cache-Control", "public, max-age=15, must-revalidate")
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.Header().Set("ETag", etag)
}

func scheduleNotModified(response http.ResponseWriter, request *http.Request, etag string) bool {
	if request.Header.Get("If-None-Match") != etag {
		return false
	}
	response.WriteHeader(http.StatusNotModified)
	return true
}
