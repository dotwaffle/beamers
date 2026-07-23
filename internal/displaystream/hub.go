// Package displaystream provides bounded, revisioned Display invalidations.
package displaystream

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"sync"
)

const (
	processStreamIDBytes  = 16
	snapshotTokenKeyBytes = 32
)

// Cursor identifies one process-local stream position.
type Cursor struct {
	StreamID string
	Position uint64
}

// SnapshotState is the bounded revision tuple authenticated for acknowledgment.
type SnapshotState struct {
	Cursor
	DisplayID            int64
	ProtocolVersion      string
	ActiveEventID        int64
	ActivationGeneration int64
	PublishedRevision    int64
}

// Subscription is one bounded stream consumer.
type Subscription struct {
	Notifications <-chan Cursor
	closeOnce     sync.Once
	close         func()
}

// Close releases the subscription.
func (subscription *Subscription) Close() {
	subscription.closeOnce.Do(subscription.close)
}

// Hub fans bounded invalidations out without retaining event history.
type Hub struct {
	mu               sync.Mutex
	streamID         string
	position         uint64
	queueCapacity    int
	nextID           uint64
	subscribers      map[uint64]chan Cursor
	snapshotTokenKey [snapshotTokenKeyBytes]byte
}

// New creates one process-local Display stream.
func New(streamID string, queueCapacity int) (*Hub, error) {
	if streamID == "" {
		return nil, errors.New("display stream ID is required")
	}
	if queueCapacity <= 0 {
		return nil, errors.New("display stream queue capacity must be positive")
	}
	hub := &Hub{
		streamID: streamID, queueCapacity: queueCapacity,
		subscribers: make(map[uint64]chan Cursor),
	}
	if _, err := rand.Read(hub.snapshotTokenKey[:]); err != nil {
		return nil, errors.Join(errors.New("generate Display snapshot token key"), err)
	}
	return hub, nil
}

// SnapshotToken authenticates one issued snapshot revision tuple.
func (hub *Hub) SnapshotToken(state SnapshotState) string {
	signature := hmac.New(sha256.New, hub.snapshotTokenKey[:])
	_, _ = signature.Write(snapshotTokenPayload(state))
	return base64.RawURLEncoding.EncodeToString(signature.Sum(nil))
}

// ValidSnapshotToken reports whether a token authenticates the exact tuple.
func (hub *Hub) ValidSnapshotToken(token string, state SnapshotState) bool {
	provided, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return false
	}
	signature := hmac.New(sha256.New, hub.snapshotTokenKey[:])
	_, _ = signature.Write(snapshotTokenPayload(state))
	return hmac.Equal(provided, signature.Sum(nil))
}

func snapshotTokenPayload(state SnapshotState) []byte {
	payload := make([]byte, 0, 160)
	payload = strconv.AppendInt(payload, state.DisplayID, 10)
	payload = append(payload, 0)
	payload = append(payload, state.ProtocolVersion...)
	payload = append(payload, 0)
	payload = append(payload, state.StreamID...)
	payload = append(payload, 0)
	payload = strconv.AppendUint(payload, state.Position, 10)
	payload = append(payload, 0)
	payload = strconv.AppendInt(payload, state.ActiveEventID, 10)
	payload = append(payload, 0)
	payload = strconv.AppendInt(payload, state.ActivationGeneration, 10)
	payload = append(payload, 0)
	return strconv.AppendInt(payload, state.PublishedRevision, 10)
}

// NewProcess creates a stream identity that changes across process restarts.
func NewProcess(queueCapacity int) (*Hub, error) {
	var entropy [processStreamIDBytes]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return nil, errors.Join(errors.New("generate Display stream ID"), err)
	}
	return New(hex.EncodeToString(entropy[:]), queueCapacity)
}

// Cursor returns the current stream position.
func (hub *Hub) Cursor() Cursor {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	return Cursor{StreamID: hub.streamID, Position: hub.position}
}

// Publish advances the stream and drops subscribers whose queue is full.
func (hub *Hub) Publish() Cursor {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	hub.position++
	cursor := Cursor{StreamID: hub.streamID, Position: hub.position}
	for id, subscriber := range hub.subscribers {
		select {
		case subscriber <- cursor:
		default:
			delete(hub.subscribers, id)
			close(subscriber)
		}
	}
	return cursor
}

// Notify publishes one invalidation without exposing cursor management.
func (hub *Hub) Notify() {
	hub.Publish()
}

// Subscribe starts after a complete snapshot cursor and bridges any observed gap.
func (hub *Hub) Subscribe(after uint64) *Subscription {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	hub.nextID++
	id := hub.nextID
	notifications := make(chan Cursor, hub.queueCapacity)
	hub.subscribers[id] = notifications
	if hub.position > after {
		notifications <- Cursor{StreamID: hub.streamID, Position: hub.position}
	}
	return &Subscription{
		Notifications: notifications,
		close: func() {
			hub.mu.Lock()
			defer hub.mu.Unlock()
			if subscriber, ok := hub.subscribers[id]; ok {
				delete(hub.subscribers, id)
				close(subscriber)
			}
		},
	}
}
