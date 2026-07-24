package operations

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/dotwaffle/beamers/internal/activation"
	"github.com/dotwaffle/beamers/internal/attachments"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/backup"
	"github.com/dotwaffle/beamers/internal/competition"
	"github.com/dotwaffle/beamers/internal/displays"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/overrides"
	"github.com/dotwaffle/beamers/internal/programcontrol"
	"github.com/dotwaffle/beamers/internal/results"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/schedule"
	"github.com/dotwaffle/beamers/internal/schedulebaseline"
	"github.com/dotwaffle/beamers/internal/sessioncontrol"
	"github.com/dotwaffle/beamers/internal/store"
)

// CreateBackup writes one verified installation archive.
func CreateBackup(
	ctx context.Context,
	input backup.CreateInput,
) (backup.Manifest, error) {
	return backup.Create(ctx, input)
}

// VerifyBackup validates one installation archive without applying it.
func VerifyBackup(path string) (backup.Manifest, error) {
	return backup.Verify(path)
}

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
	storage          *store.SQLite
	authentication   *auth.Service
	displays         *displays.Service
	activation       *activation.Service
	attachments      *attachments.Service
	competition      *competition.Service
	events           *events.Service
	overrides        *overrides.Service
	programControl   *programcontrol.Service
	results          *results.Service
	rundownCommands  *rundown.Commands
	rundownQueries   *rundown.Queries
	schedule         *schedule.Service
	baselineCommands *schedulebaseline.Commands
	baselineQueries  *schedulebaseline.Queries
	sessionControl   *sessioncontrol.Service
}

// OpenConfig identifies an installation's database and Attachment Store roots.
type OpenConfig struct {
	DataDir        string
	AttachmentsDir string
}

// Initialize creates a new installation with the committed schema.
func Initialize(ctx context.Context, dataDir string) error {
	return store.Initialize(ctx, dataDir)
}

// OpenInstallation opens storage for normal service or local recovery mode.
func OpenInstallation(ctx context.Context, dataDir string) (*Installation, error) {
	return OpenInstallationWithConfig(ctx, OpenConfig{DataDir: dataDir})
}

// OpenInstallationWithConfig opens storage with explicit local roots.
func OpenInstallationWithConfig(
	ctx context.Context,
	config OpenConfig,
) (*Installation, error) {
	if config.AttachmentsDir == "" {
		config.AttachmentsDir = filepath.Join(config.DataDir, "attachments")
	}
	storage, err := store.Open(ctx, config.DataDir)
	if err != nil {
		return nil, err
	}
	installation := &Installation{storage: storage}
	startupErr := storage.StartupError()
	if startupErr != nil {
		// Startup storage failures deliberately produce a recovery-mode handle.
		return installation, nil //nolint:nilerr // The caller reads StartupError to select recovery mode.
	}
	overrideService, err := overrides.New(ctx, storage, time.Now)
	if err != nil {
		return nil, errors.Join(err, storage.Close())
	}
	installation.overrides = overrideService
	authConfig := auth.DefaultConfig()
	authConfig.StorageState = overrideService
	authentication, err := auth.New(storage, authConfig)
	if err != nil {
		return nil, errors.Join(err, storage.Close())
	}
	installation.authentication = authentication
	activationService, err := activation.New(storage, time.Now)
	if err != nil {
		return nil, errors.Join(err, storage.Close())
	}
	installation.activation = activationService
	attachmentService, err := attachments.New(storage, config.AttachmentsDir, time.Now)
	if err != nil {
		return nil, errors.Join(err, storage.Close())
	}
	installation.attachments = attachmentService
	eventService, err := events.New(storage, time.Now)
	if err != nil {
		return nil, errors.Join(err, storage.Close())
	}
	installation.events = eventService
	displayConfig := displays.DefaultConfig()
	displayConfig.Emergency = overrideService
	displayService, err := displays.New(storage, displayConfig)
	if err != nil {
		return nil, errors.Join(err, storage.Close())
	}
	installation.displays = displayService
	competitionService, err := competition.New(storage, time.Now)
	if err != nil {
		return nil, errors.Join(err, storage.Close())
	}
	installation.competition = competitionService
	programControlService, err := programcontrol.New(storage, time.Now)
	if err != nil {
		return nil, errors.Join(err, storage.Close())
	}
	installation.programControl = programControlService
	resultsService, err := results.New(storage, time.Now)
	if err != nil {
		return nil, errors.Join(err, storage.Close())
	}
	installation.results = resultsService
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
	baselineCommands, err := schedulebaseline.NewCommands(storage, time.Now)
	if err != nil {
		return nil, errors.Join(err, storage.Close())
	}
	installation.baselineCommands = baselineCommands
	baselineQueries, err := schedulebaseline.NewQueries(storage)
	if err != nil {
		return nil, errors.Join(err, storage.Close())
	}
	installation.baselineQueries = baselineQueries
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

// Attachments returns the scoped upload application service.
// It is nil only while the installation is restricted to recovery mode.
func (installation *Installation) Attachments() *attachments.Service {
	return installation.attachments
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

// Recover persists process-owned degraded state after storage becomes writable.
func (installation *Installation) Recover(ctx context.Context) (bool, error) {
	if installation.overrides == nil {
		return false, nil
	}
	return installation.overrides.Recover(ctx)
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

// Overrides returns temporary Display Override control.
// It is nil only while the installation is restricted to recovery mode.
func (installation *Installation) Overrides() *overrides.Service {
	return installation.overrides
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

// ProgramControl returns volatile ownership and durable Program Output control.
// It is nil only while the installation is restricted to recovery mode.
func (installation *Installation) ProgramControl() *programcontrol.Service {
	return installation.programControl
}

// Results returns unreleased Competition Results Draft control.
// It is nil only while the installation is restricted to recovery mode.
func (installation *Installation) Results() *results.Service {
	return installation.results
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

// ScheduleBaselineCommands returns immutable baseline capture commands.
// It is nil only while the installation is restricted to recovery mode.
func (installation *Installation) ScheduleBaselineCommands() *schedulebaseline.Commands {
	return installation.baselineCommands
}

// ScheduleBaselineQueries returns revision-bound baseline previews.
// It is nil only while the installation is restricted to recovery mode.
func (installation *Installation) ScheduleBaselineQueries() *schedulebaseline.Queries {
	return installation.baselineQueries
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
