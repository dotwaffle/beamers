package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
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
	AssetVersion         string `json:"asset_version"`
	StreamID             string `json:"stream_id"`
	StreamPosition       uint64 `json:"stream_position"`
	ActiveEventID        int    `json:"active_event_id"`
	ActivationGeneration int    `json:"activation_generation"`
	PublishedRevision    int    `json:"published_revision"`
}

type displayInvalidationCache struct {
	mu     sync.Mutex
	cursor displaystream.Cursor
	cached displayInvalidation
	ready  chan struct{}
	valid  bool
}

func (cache *displayInvalidationCache) current(
	ctx context.Context,
	cursor displaystream.Cursor,
	load func(context.Context) (displayInvalidation, error),
) (displayInvalidation, error) {
	for {
		cache.mu.Lock()
		if cache.valid && cache.cursor == cursor {
			current := cache.cached
			cache.mu.Unlock()
			return current, nil
		}
		if cache.ready != nil {
			ready := cache.ready
			cache.mu.Unlock()
			select {
			case <-ready:
				continue
			case <-ctx.Done():
				return displayInvalidation{}, context.Cause(ctx)
			}
		}
		cache.ready = make(chan struct{})
		cache.mu.Unlock()

		current, err := load(ctx)
		cache.mu.Lock()
		if err == nil {
			cache.cursor = cursor
			cache.cached = current
			cache.valid = true
		}
		close(cache.ready)
		cache.ready = nil
		cache.mu.Unlock()
		return current, err
	}
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
	unknownPosition := after > current.Position
	if streamChanged || unknownPosition {
		after = current.Position
	}
	subscription := handlers.stream.SubscribeDisplay(after, int64(snapshot.Display.ID))
	defer subscription.Close()

	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("X-Accel-Buffering", "no")
	if err := writeDisplayHeartbeat(response); err != nil {
		return
	}
	if streamChanged || unknownPosition {
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
			if err := handlers.service.Authenticate(request.Context(), credential); err != nil {
				return
			}
			invalidation, err := handlers.invalidations.current(
				request.Context(),
				notification,
				func(ctx context.Context) (displayInvalidation, error) {
					snapshot, currentErr := handlers.service.Current(ctx, credential)
					return newDisplayInvalidation(notification, snapshot), currentErr
				},
			)
			if err != nil {
				return
			}
			if err := writeDisplayInvalidationState(response, notification, invalidation); err != nil {
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
	return writeDisplayInvalidationState(response, cursor, newDisplayInvalidation(cursor, snapshot))
}

func newDisplayInvalidation(
	cursor displaystream.Cursor,
	snapshot displays.Snapshot,
) displayInvalidation {
	return displayInvalidation{
		ProtocolVersion: snapshot.ProtocolVersion, AssetVersion: snapshot.AssetVersion,
		StreamID:       cursor.StreamID,
		StreamPosition: cursor.Position, ActiveEventID: snapshot.ActiveEventID,
		ActivationGeneration: snapshot.ActivationGeneration,
		PublishedRevision:    snapshot.PublishedRevision,
	}
}

func writeDisplayInvalidationState(
	response http.ResponseWriter,
	cursor displaystream.Cursor,
	invalidation displayInvalidation,
) error {
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

func writeDisplaySSE(response http.ResponseWriter, payload string) (err error) {
	controller := http.NewResponseController(response)
	if deadlineErr := controller.SetWriteDeadline(
		time.Now().Add(displayWriteTimeout),
	); deadlineErr != nil {
		return deadlineErr
	}
	defer func() {
		err = errors.Join(err, controller.SetWriteDeadline(time.Time{}))
	}()
	if _, err = fmt.Fprint(response, payload); err != nil {
		return err
	}
	return controller.Flush()
}
