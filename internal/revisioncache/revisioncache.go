// Package revisioncache memoizes one expensive read against the exact revision
// that invalidates it.
//
// Beamers already identifies every projection by a revision or a stream cursor,
// because Displays and browsers resume from one. A read path that rebuilds the
// same projection for every request at an unchanged revision is repeating work
// the installation has already declared unchanged, and it repeats it most
// exactly when it can least afford to: the reload wave after a Publish.
package revisioncache

import (
	"context"
	"sync"
)

// Cache holds the most recent build and the revision it was built at. It holds
// one entry, not a map: read paths here are whole-Event projections whose old
// revisions can never be asked for again.
//
// Rebuilds run under the write lock, so a burst arriving at a new revision
// coalesces into one rebuild instead of stampeding the database. Failures are
// never cached.
type Cache[Key comparable, Value any] struct {
	mutex sync.RWMutex
	key   Key
	value Value
	valid bool
}

// Load returns the build for key, calling rebuild only when the cache does not
// already hold that key.
func (cache *Cache[Key, Value]) Load(
	ctx context.Context,
	key Key,
	rebuild func(context.Context) (Value, error),
) (Value, error) {
	cache.mutex.RLock()
	if cache.valid && cache.key == key {
		value := cache.value
		cache.mutex.RUnlock()
		return value, nil
	}
	cache.mutex.RUnlock()
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	if cache.valid && cache.key == key {
		return cache.value, nil
	}
	value, err := rebuild(ctx)
	if err != nil {
		var zero Value
		return zero, err
	}
	cache.key = key
	cache.value = value
	cache.valid = true
	return value, nil
}
