package revisioncache_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dotwaffle/beamers/internal/revisioncache"
)

func TestCacheRebuildsOnlyWhenTheRevisionMoves(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		revisions []int
		rebuilds  int64
	}{
		{name: "repeat requests at one revision", revisions: []int{7, 7, 7}, rebuilds: 1},
		{name: "each advance rebuilds", revisions: []int{7, 8, 9}, rebuilds: 3},
		{name: "returning to a revision rebuilds", revisions: []int{7, 8, 7}, rebuilds: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var cache revisioncache.Cache[int, string]
			var rebuilds atomic.Int64
			for _, revision := range test.revisions {
				value, err := cache.Load(
					t.Context(),
					revision,
					func(context.Context) (string, error) {
						rebuilds.Add(1)
						return string(rune('a' + revision)), nil
					},
				)
				if err != nil {
					t.Fatalf("load at revision %d: %v", revision, err)
				}
				if value != string(rune('a'+revision)) {
					t.Fatalf("value at revision %d = %q", revision, value)
				}
			}
			if rebuilds.Load() != test.rebuilds {
				t.Fatalf("rebuilds = %d, want %d", rebuilds.Load(), test.rebuilds)
			}
		})
	}
}

func TestCacheDoesNotHoldFailures(t *testing.T) {
	t.Parallel()
	var cache revisioncache.Cache[int, string]
	failure := errors.New("rebuild failed")
	if _, err := cache.Load(
		t.Context(),
		3,
		func(context.Context) (string, error) { return "", failure },
	); !errors.Is(err, failure) {
		t.Fatalf("failed load error = %v, want %v", err, failure)
	}
	value, err := cache.Load(
		t.Context(),
		3,
		func(context.Context) (string, error) { return "rebuilt", nil },
	)
	if err != nil || value != "rebuilt" {
		t.Fatalf("load after failure = %q, %v", value, err)
	}
}

// TestCacheCoalescesConcurrentRebuilds covers the wave that arrives right after
// a Publish: every page reloads at the new revision at once, and they must not
// each start their own database cascade.
func TestCacheCoalescesConcurrentRebuilds(t *testing.T) {
	t.Parallel()
	var cache revisioncache.Cache[int, string]
	var rebuilds atomic.Int64
	var requests sync.WaitGroup
	for range 32 {
		requests.Go(func() {
			if _, err := cache.Load(
				t.Context(),
				5,
				func(context.Context) (string, error) {
					rebuilds.Add(1)
					return "built", nil
				},
			); err != nil {
				t.Errorf("concurrent load: %v", err)
			}
		})
	}
	requests.Wait()
	if rebuilds.Load() != 1 {
		t.Fatalf("concurrent rebuilds = %d, want 1", rebuilds.Load())
	}
}
