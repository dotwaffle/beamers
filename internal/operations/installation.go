package operations

import (
	"context"

	"github.com/dotwaffle/beamers/internal/store"
)

var (
	// ErrAlreadyInitialized means initialization found existing installation data.
	ErrAlreadyInitialized = store.ErrAlreadyInitialized
	// ErrInstallationInUse means another Beamers process holds the installation lock.
	ErrInstallationInUse = store.ErrInstallationInUse
	// ErrUninitialized means no initialized Beamers database exists at the data path.
	ErrUninitialized = store.ErrUninitialized
	// ErrUnsupportedSchema means the database is not a supported committed schema.
	ErrUnsupportedSchema = store.ErrUnsupportedSchema
)

// Installation coordinates access to installation persistence.
type Installation struct {
	storage *store.SQLite
}

// Initialize creates a new installation with the committed schema.
func Initialize(ctx context.Context, dataDir string) error {
	return store.Initialize(ctx, dataDir)
}

// OpenInstallation opens storage for normal service or local recovery mode.
func OpenInstallation(ctx context.Context, dataDir string) (*Installation, error) {
	storage, err := store.Open(ctx, dataDir)
	if err != nil {
		return nil, err
	}
	return &Installation{storage: storage}, nil
}

// StartupError reports why the installation must remain in recovery mode.
func (installation *Installation) StartupError() error {
	return installation.storage.StartupError()
}

// Ready reports whether storage is usable and on the supported schema.
func (installation *Installation) Ready(ctx context.Context) error {
	return installation.storage.Ready(ctx)
}

// Close closes storage and releases the installation lock.
func (installation *Installation) Close() error {
	return installation.storage.Close()
}
