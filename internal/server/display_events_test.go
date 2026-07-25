package server

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dotwaffle/beamers/internal/displays"
	"github.com/dotwaffle/beamers/internal/displaystream"
)

func TestDisplayInvalidationProjectionIsShared(t *testing.T) {
	cache := &displayInvalidationCache{}
	cursor := displaystream.Cursor{StreamID: "test", Position: 1}
	var loads atomic.Int32
	var wait sync.WaitGroup
	for range 500 {
		wait.Go(func() {
			_, err := cache.current(t.Context(), cursor, func(context.Context) (displays.Snapshot, error) {
				loads.Add(1)
				return displays.Snapshot{PublishedRevision: 7}, nil
			})
			if err != nil {
				t.Errorf("load invalidation projection: %v", err)
			}
		})
	}
	wait.Wait()
	if got := loads.Load(); got != 1 {
		t.Fatalf("invalidation projection loads = %d, want 1", got)
	}
}
