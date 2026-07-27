package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dotwaffle/beamers/internal/displaystream"
	"github.com/dotwaffle/beamers/internal/schedule"
)

func TestScheduleStreamPositionResumesLastDeliveredEvent(t *testing.T) {
	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/schedule/events?after=2",
		http.NoBody,
	)
	request.Header.Set("Last-Event-ID", "7")
	position, err := scheduleStreamPosition(request)
	if err != nil || position != 7 {
		t.Fatalf("Schedule stream position = %d, %v; want 7", position, err)
	}

	request.Header.Set("Last-Event-ID", "invalid")
	if _, err = scheduleStreamPosition(request); err == nil {
		t.Fatal("invalid Last-Event-ID was accepted")
	}
}

func TestScheduleSnapshotCursorPrecedesSnapshotRead(t *testing.T) {
	stream, err := displaystream.New("schedule-test", 1)
	if err != nil {
		t.Fatalf("create Schedule stream: %v", err)
	}
	snapshot, err := currentScheduleSnapshot(t.Context(), stream, func(context.Context) (schedule.Snapshot, error) {
		stream.Notify()
		return schedule.Snapshot{}, nil
	})
	if err != nil {
		t.Fatalf("read Schedule snapshot: %v", err)
	}
	if snapshot.StreamPosition != 0 {
		t.Fatalf("Schedule snapshot position = %d, want 0", snapshot.StreamPosition)
	}
	subscription := stream.Subscribe(snapshot.StreamPosition)
	t.Cleanup(subscription.Close)
	if notification := <-subscription.Notifications; notification.Position != 1 {
		t.Fatalf("Schedule notification position = %d, want 1", notification.Position)
	}
}
