package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/dotwaffle/beamers/internal/displays"
	"github.com/dotwaffle/beamers/internal/displaystream"
)

const (
	displayHeartbeatInterval = 5 * time.Second
	displayWriteTimeout      = 5 * time.Second
)

type displayInvalidation struct {
	ProtocolVersion      string `json:"protocol_version"`
	StreamID             string `json:"stream_id"`
	StreamPosition       uint64 `json:"stream_position"`
	ActiveEventID        int    `json:"active_event_id"`
	ActivationGeneration int    `json:"activation_generation"`
	PublishedRevision    int    `json:"published_revision"`
}

func (handlers displayHandlers) events(response http.ResponseWriter, request *http.Request) {
	if !requestAllowed(response, request, http.MethodGet, handlers.allowPlaintextDisplay) {
		return
	}
	after, err := displayStreamPosition(request)
	if err != nil {
		http.Error(response, "invalid Display stream position", http.StatusBadRequest)
		return
	}
	credential := cookieValue(request, displayCookieName)
	current := handlers.stream.Cursor()
	snapshot, err := handlers.service.Current(request.Context(), credential)
	if err != nil {
		if errors.Is(err, displays.ErrDisplayAuthentication) {
			http.Error(response, "Display authentication required", http.StatusUnauthorized)
			return
		}
		handlers.logger.ErrorContext(request.Context(), "authorize Display stream", "error", err)
		http.Error(response, "Display stream unavailable", http.StatusServiceUnavailable)
		return
	}

	streamChanged := request.URL.Query().Get("stream_id") != current.StreamID
	if streamChanged {
		after = current.Position
	}
	subscription := handlers.stream.Subscribe(after)
	defer subscription.Close()

	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("X-Accel-Buffering", "no")
	if err := writeDisplayHeartbeat(response); err != nil {
		return
	}
	if streamChanged {
		if err := writeDisplayInvalidation(response, current, snapshot); err != nil {
			return
		}
	}

	heartbeats := time.NewTicker(displayHeartbeatInterval)
	defer heartbeats.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case notification, ok := <-subscription.Notifications:
			if !ok {
				return
			}
			snapshot, err := handlers.service.Current(request.Context(), credential)
			if err != nil {
				return
			}
			if err := writeDisplayInvalidation(response, notification, snapshot); err != nil {
				return
			}
		case <-heartbeats.C:
			if err := writeDisplayHeartbeat(response); err != nil {
				return
			}
		}
	}
}

func displayStreamPosition(request *http.Request) (uint64, error) {
	value := request.URL.Query().Get("after")
	if value == "" {
		return 0, nil
	}
	return strconv.ParseUint(value, 10, 64)
}

func writeDisplayHeartbeat(response http.ResponseWriter) error {
	return writeDisplaySSE(response, ": heartbeat\n\n")
}

func writeDisplayInvalidation(
	response http.ResponseWriter,
	cursor displaystream.Cursor,
	snapshot displays.Snapshot,
) error {
	invalidation := displayInvalidation{
		ProtocolVersion: snapshot.ProtocolVersion, StreamID: cursor.StreamID,
		StreamPosition: cursor.Position, ActiveEventID: snapshot.ActiveEventID,
		ActivationGeneration: snapshot.ActivationGeneration,
		PublishedRevision:    snapshot.PublishedRevision,
	}
	data, err := json.Marshal(invalidation)
	if err != nil {
		return err
	}
	return writeDisplaySSE(response, fmt.Sprintf(
		"id: %d\nevent: invalidate\ndata: %s\n\n",
		cursor.Position,
		data,
	))
}

func writeDisplaySSE(response http.ResponseWriter, payload string) error {
	controller := http.NewResponseController(response)
	if err := controller.SetWriteDeadline(time.Now().Add(displayWriteTimeout)); err != nil {
		return err
	}
	if _, err := fmt.Fprint(response, payload); err != nil {
		return err
	}
	return controller.Flush()
}
