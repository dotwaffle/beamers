package displaystream

import (
	"testing"
	"time"
)

func TestHubDisconnectsSlowSubscriberWithoutBlockingPublisher(t *testing.T) {
	hub, err := New("test-stream", 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	subscription := hub.Subscribe(0)
	t.Cleanup(subscription.Close)

	hub.Publish()
	published := make(chan Cursor, 1)
	go func() {
		published <- hub.Publish()
	}()

	select {
	case cursor := <-published:
		if cursor.StreamID != "test-stream" || cursor.Position != 2 {
			t.Errorf("Publish cursor = %+v, want test-stream position 2", cursor)
		}
	case <-time.After(time.Second):
		t.Fatal("Publish blocked behind slow subscriber")
	}

	first, ok := <-subscription.Notifications
	if !ok || first.Position != 1 {
		t.Fatalf("first notification = %+v, %t, want position 1", first, ok)
	}
	if _, ok := <-subscription.Notifications; ok {
		t.Fatal("slow subscriber remained connected")
	}
}

func TestHubBridgesSnapshotToStreamWithoutHistory(t *testing.T) {
	hub, err := New("test-stream", 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	hub.Publish()
	cursor := hub.Cursor()
	hub.Publish()

	subscription := hub.Subscribe(cursor.Position)
	t.Cleanup(subscription.Close)
	notification := <-subscription.Notifications
	if notification.StreamID != cursor.StreamID || notification.Position != 2 {
		t.Fatalf("bridged notification = %+v, want test-stream position 2", notification)
	}
}

func TestHubAuthenticatesExactSnapshotTuple(t *testing.T) {
	hub, err := New("test-stream", 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	state := SnapshotState{
		Cursor:    Cursor{StreamID: "test-stream", Position: 3},
		DisplayID: 7, ProtocolVersion: "beamers.display.v1",
		ActiveEventID: 11, ActivationGeneration: 2, PublishedRevision: 5,
	}
	token := hub.SnapshotToken(state)
	if !hub.ValidSnapshotToken(token, state) {
		t.Fatal("issued snapshot token did not validate")
	}
	state.PublishedRevision++
	if hub.ValidSnapshotToken(token, state) {
		t.Fatal("snapshot token validated a different revision tuple")
	}
}
