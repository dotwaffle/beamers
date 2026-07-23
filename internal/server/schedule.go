package server

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/a-h/templ"

	"github.com/dotwaffle/beamers/internal/schedule"
)

type scheduleHandlers struct {
	schedule *schedule.Service
	logger   *slog.Logger
}

func registerScheduleRoutes(mux *http.ServeMux, service *schedule.Service, logger *slog.Logger) {
	handlers := scheduleHandlers{schedule: service, logger: logger}
	mux.HandleFunc("/schedule", handlers.list)
	mux.HandleFunc("/schedule/sessions/{sessionID}", handlers.session)
	mux.HandleFunc("/assets/schedule.css", handlers.stylesheet)
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
	snapshot, err := handlers.schedule.Current(request.Context(), filter)
	if err != nil {
		if errors.Is(err, schedule.ErrInvalidFilter) {
			http.Error(response, "invalid Schedule filters", http.StatusBadRequest)
			return
		}
		handlers.logger.ErrorContext(request.Context(), "public Schedule read failed", "error", err)
		http.Error(response, "Schedule unavailable", http.StatusInternalServerError)
		return
	}
	handlers.render(response, request, snapshot.ETag, schedule.Page(snapshot), "public Schedule") //nolint:contextcheck // Generated templ closures receive context when rendered.
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
