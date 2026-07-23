package operations

import (
	"context"
	"errors"
	"time"

	"github.com/dotwaffle/beamers/internal/activation"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/competition"
	"github.com/dotwaffle/beamers/internal/displays"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/schedule"
	"github.com/dotwaffle/beamers/internal/sessioncontrol"
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
	storage         *store.SQLite
	authentication  *auth.Service
	displays        *displays.Service
	activation      *activation.Service
	competition     *competition.Service
	events          *events.Service
	rundownCommands *rundown.Commands
	rundownQueries  *rundown.Queries
	schedule        *schedule.Service
	sessionControl  *sessioncontrol.Service
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
	installation := &Installation{storage: storage}
	startupErr := storage.StartupError()
	if startupErr != nil {
		// Startup storage failures deliberately produce a recovery-mode handle.
		return installation, nil //nolint:nilerr // The caller reads StartupError to select recovery mode.
	}
	authentication, err := auth.New(storage, auth.DefaultConfig())
	if err != nil {
		return nil, errors.Join(err, storage.Close())
	}
	installation.authentication = authentication
	displayService, err := displays.New(storage, displays.DefaultConfig())
	if err != nil {
		return nil, errors.Join(err, storage.Close())
	}
	installation.displays = displayService
	activationService, err := activation.New(storage, time.Now)
	if err != nil {
		return nil, errors.Join(err, storage.Close())
	}
	installation.activation = activationService
	eventService, err := events.New(storage, time.Now)
	if err != nil {
		return nil, errors.Join(err, storage.Close())
	}
	installation.events = eventService
	competitionService, err := competition.New(storage, time.Now)
	if err != nil {
		return nil, errors.Join(err, storage.Close())
	}
	installation.competition = competitionService
	rundownCommands, err := rundown.NewCommands(storage, time.Now)
	if err != nil {
		return nil, errors.Join(err, storage.Close())
	}
	installation.rundownCommands = rundownCommands
	rundownQueries, err := rundown.NewQueries(storage)
	if err != nil {
		return nil, errors.Join(err, storage.Close())
	}
	installation.rundownQueries = rundownQueries
	scheduleService, err := schedule.New(storage, time.Now)
	if err != nil {
		return nil, errors.Join(err, storage.Close())
	}
	installation.schedule = scheduleService
	sessionControlService, err := sessioncontrol.New(storage, time.Now)
	if err != nil {
		return nil, errors.Join(err, storage.Close())
	}
	installation.sessionControl = sessionControlService
	return installation, nil
}

// Activation returns the Active Event application service.
// It is nil only while the installation is restricted to recovery mode.
func (installation *Installation) Activation() *activation.Service {
	return installation.activation
}

// IssueAdministratorBootstrap creates a short-lived credential while holding
// exclusive host access to an initialized installation.
func IssueAdministratorBootstrap(
	ctx context.Context,
	dataDir string,
) (token string, returnErr error) {
	storage, err := store.Open(ctx, dataDir)
	if err != nil {
		return "", err
	}
	defer func() {
		returnErr = errors.Join(returnErr, storage.Close())
	}()
	startupErr := storage.StartupError()
	if startupErr != nil {
		return "", startupErr
	}
	authentication, err := auth.New(storage, auth.DefaultConfig())
	if err != nil {
		return "", err
	}
	return authentication.IssueBootstrap(ctx)
}

// StartupError reports why the installation must remain in recovery mode.
func (installation *Installation) StartupError() error {
	return installation.storage.StartupError()
}

// Ready reports whether storage is usable and on the supported schema.
func (installation *Installation) Ready(ctx context.Context) error {
	return installation.storage.Ready(ctx)
}

// Authentication returns the Account authentication application service.
// It is nil only while the installation is restricted to recovery mode.
func (installation *Installation) Authentication() *auth.Service {
	return installation.authentication
}

// Events returns the Event application service.
// It is nil only while the installation is restricted to recovery mode.
func (installation *Installation) Events() *events.Service {
	return installation.events
}

// Competition returns the Competition Entry application service.
// It is nil only while the installation is restricted to recovery mode.
func (installation *Installation) Competition() *competition.Service {
	return installation.competition
}

// Displays returns the Display Enrollment and Assignment service.
// It is nil only while the installation is restricted to recovery mode.
func (installation *Installation) Displays() *displays.Service {
	return installation.displays
}

// RundownCommands returns the Rundown command application service.
// It is nil only while the installation is restricted to recovery mode.
func (installation *Installation) RundownCommands() *rundown.Commands {
	return installation.rundownCommands
}

// RundownQueries returns the Rundown query application service.
// It is nil only while the installation is restricted to recovery mode.
func (installation *Installation) RundownQueries() *rundown.Queries {
	return installation.rundownQueries
}

// Schedule returns the public Schedule query service.
// It is nil only while the installation is restricted to recovery mode.
func (installation *Installation) Schedule() *schedule.Service {
	return installation.schedule
}

// SessionControl returns the live Session command service.
// It is nil only while the installation is restricted to recovery mode.
func (installation *Installation) SessionControl() *sessioncontrol.Service {
	return installation.sessionControl
}

// Close closes storage and releases the installation lock.
func (installation *Installation) Close() error {
	return installation.storage.Close()
}
