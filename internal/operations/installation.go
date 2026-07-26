package operations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

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

var (
	// ErrAlreadyInitialized means initialization found existing installation data.
	ErrAlreadyInitialized = store.ErrAlreadyInitialized
	// ErrInstallationInUse means another Beamers process holds the installation lock.
	ErrInstallationInUse = store.ErrInstallationInUse
	// ErrUninitialized means no initialized Beamers database exists at the data path.
	ErrUninitialized = store.ErrUninitialized
	// ErrUnsupportedSchema means the database is not a supported committed schema.
	ErrUnsupportedSchema = store.ErrUnsupportedSchema
	// ErrNoUpgrade means the installation already uses the current schema.
	ErrNoUpgrade = store.ErrNoMigration
)

// Installation coordinates access to installation persistence.
type Installation struct {
	storage          *store.SQLite
	releaseAccess    func() error
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

// Capacity is the durable installation shape compared with the tested envelope.
type Capacity = store.Capacity

// OpenConfig identifies an installation's database and Attachment Store roots.
type OpenConfig struct {
	DataDir        string
	AttachmentsDir string
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
}

// Initialize creates a new installation with the committed schema.
func Initialize(ctx context.Context, dataDir string) error {
	return InitializeWithConfig(ctx, OpenConfig{DataDir: dataDir})
}

// InitializeWithConfig creates an installation only when all configured roots are unused.
func InitializeWithConfig(ctx context.Context, config OpenConfig) (returnErr error) {
	if config.DataDir == "" {
		return store.Initialize(ctx, config.DataDir)
	}
	if config.AttachmentsDir == "" {
		config.AttachmentsDir = filepath.Join(config.DataDir, "attachments")
	}
	roots := configuredAccessRoots(config)
	for _, root := range roots {
		marker, err := store.ExclusiveAccessMarker(root)
		if err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
			return fmt.Errorf("create exclusive access marker directory: %w", err)
		}
	}
	release, err := store.HoldExclusiveAccess(roots...)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, release())
	}()

	if _, err = os.Lstat(config.DataDir + ".beamers-restore.json"); err == nil {
		return fmt.Errorf("%w: Restore journal exists", ErrAlreadyInitialized)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect Restore journal: %w", err)
	}
	if _, err = os.Lstat(config.DataDir + ".beamers-upgrade.json"); err == nil {
		return fmt.Errorf("%w: upgrade journal exists", ErrAlreadyInitialized)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect upgrade journal: %w", err)
	}
	if store.UsesExternalAttachments(config.DataDir, config.AttachmentsDir) {
		entries, readErr := os.ReadDir(config.AttachmentsDir)
		if readErr == nil && len(entries) != 0 {
			return fmt.Errorf("%w: Attachment Store contains state", ErrAlreadyInitialized)
		}
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return fmt.Errorf("inspect Attachment Store: %w", readErr)
		}
	}
	return store.Initialize(ctx, config.DataDir)
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
	if config.DataDir != "" {
		if err := backup.RecoverRestore(config.DataDir); err != nil {
			return &Installation{storage: store.RecoverySQLite(
				fmt.Errorf("recover interrupted Restore: %w", err),
			)}, nil
		}
		if err := RecoverUpgrade(config.DataDir); err != nil {
			return &Installation{storage: store.RecoverySQLite(
				fmt.Errorf("recover interrupted upgrade: %w", err),
			)}, nil
		}
	}
	ownedState, err := configuredInstallationHasState(config)
	if err != nil {
		return &Installation{storage: store.RecoverySQLite(
			fmt.Errorf("inspect configured installation state: %w", err),
		)}, nil
	}
	var releaseAccess func() error
	if ownedState {
		releaseAccess, err = store.HoldExclusiveAccess(configuredAccessRoots(config)...)
		if err != nil {
			if errors.Is(err, ErrInstallationInUse) {
				return nil, err
			}
			return &Installation{storage: store.RecoverySQLite(
				fmt.Errorf("lock configured installation state: %w", err),
			)}, nil
		}
	}
	var storage *store.SQLite
	if config.TracerProvider != nil || config.MeterProvider != nil {
		storage, err = store.OpenWithTelemetry(
			ctx,
			config.DataDir,
			config.TracerProvider,
			config.MeterProvider,
		)
	} else {
		storage, err = store.Open(ctx, config.DataDir)
	}
	if err != nil {
		if errors.Is(err, ErrInstallationInUse) {
			return nil, errors.Join(err, closeAccess(releaseAccess))
		}
		return &Installation{
			storage:       store.RecoverySQLite(err),
			releaseAccess: releaseAccess,
		}, nil
	}
	installation := &Installation{storage: storage, releaseAccess: releaseAccess}
	startupErr := storage.StartupError()
	if startupErr != nil {
		// Startup storage failures deliberately produce a recovery-mode handle.
		return installation, nil //nolint:nilerr // The caller reads StartupError to select recovery mode.
	}
	overrideService, err := overrides.New(ctx, storage, time.Now)
	if err != nil {
		return nil, errors.Join(err, installation.Close())
	}
	installation.overrides = overrideService
	authConfig := auth.DefaultConfig()
	authConfig.StorageState = overrideService
	authentication, err := auth.New(storage, authConfig)
	if err != nil {
		return nil, errors.Join(err, installation.Close())
	}
	installation.authentication = authentication
	activationService, err := activation.New(storage, time.Now)
	if err != nil {
		return nil, errors.Join(err, installation.Close())
	}
	installation.activation = activationService
	attachmentService, err := attachments.New(ctx, storage, config.AttachmentsDir, time.Now)
	if err != nil {
		return nil, errors.Join(err, installation.Close())
	}
	installation.attachments = attachmentService
	eventService, err := events.New(storage, time.Now)
	if err != nil {
		return nil, errors.Join(err, installation.Close())
	}
	installation.events = eventService
	displayConfig := displays.DefaultConfig()
	displayConfig.Emergency = overrideService
	displayService, err := displays.New(storage, displayConfig)
	if err != nil {
		return nil, errors.Join(err, installation.Close())
	}
	installation.displays = displayService
	competitionService, err := competition.New(storage, time.Now)
	if err != nil {
		return nil, errors.Join(err, installation.Close())
	}
	installation.competition = competitionService
	programControlService, err := programcontrol.New(storage, time.Now)
	if err != nil {
		return nil, errors.Join(err, installation.Close())
	}
	installation.programControl = programControlService
	resultsService, err := results.New(storage, time.Now)
	if err != nil {
		return nil, errors.Join(err, installation.Close())
	}
	installation.results = resultsService
	rundownCommands, err := rundown.NewCommands(storage, time.Now)
	if err != nil {
		return nil, errors.Join(err, installation.Close())
	}
	installation.rundownCommands = rundownCommands
	rundownQueries, err := rundown.NewQueries(storage)
	if err != nil {
		return nil, errors.Join(err, installation.Close())
	}
	installation.rundownQueries = rundownQueries
	scheduleService, err := schedule.New(storage, time.Now)
	if err != nil {
		return nil, errors.Join(err, installation.Close())
	}
	installation.schedule = scheduleService
	baselineCommands, err := schedulebaseline.NewCommands(storage, time.Now)
	if err != nil {
		return nil, errors.Join(err, installation.Close())
	}
	installation.baselineCommands = baselineCommands
	baselineQueries, err := schedulebaseline.NewQueries(storage)
	if err != nil {
		return nil, errors.Join(err, installation.Close())
	}
	installation.baselineQueries = baselineQueries
	sessionControlService, err := sessioncontrol.New(storage, time.Now)
	if err != nil {
		return nil, errors.Join(err, installation.Close())
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

// CreateBackup writes a verified archive through this live installation.
func (installation *Installation) CreateBackup(
	ctx context.Context,
	input backup.CreateInput,
) (backup.Manifest, error) {
	return backup.CreateWithStorage(ctx, installation.storage, input)
}

// IssueAdministratorBootstrap creates a short-lived credential while holding
// exclusive host access to an initialized installation.
func IssueAdministratorBootstrap(
	ctx context.Context,
	dataDir string,
) (token string, returnErr error) {
	releaseAccess, err := store.HoldExclusiveAccess(dataDir)
	if err != nil {
		return "", err
	}
	defer func() {
		returnErr = errors.Join(returnErr, releaseAccess())
	}()
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

// RecordInterfaceModes persists host-authorized insecure mode transitions.
func (installation *Installation) RecordInterfaceModes(
	ctx context.Context,
	insecureCrew bool,
	insecureDisplay bool,
) error {
	if err := installation.storage.RecordHostInterfaceMode(ctx, "Crew", insecureCrew); err != nil {
		return err
	}
	return installation.storage.RecordHostInterfaceMode(ctx, "Display", insecureDisplay)
}

// Ready reports whether storage is usable and on the supported schema.
func (installation *Installation) Ready(ctx context.Context) error {
	return installation.storage.Ready(ctx)
}

// Capacity returns durable counts used for operational warnings.
func (installation *Installation) Capacity(ctx context.Context) (Capacity, error) {
	return installation.storage.Capacity(ctx)
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
	if installation == nil {
		return nil
	}
	return errors.Join(installation.storage.Close(), closeAccess(installation.releaseAccess))
}

func configuredAccessRoots(config OpenConfig) []string {
	roots := []string{config.DataDir}
	if store.UsesExternalAttachments(config.DataDir, config.AttachmentsDir) {
		roots = append(roots, config.AttachmentsDir)
	}
	return roots
}

func configuredInstallationHasState(config OpenConfig) (bool, error) {
	for _, path := range append(
		configuredAccessRoots(config),
		config.DataDir+".beamers-restore.json",
		config.DataDir+".beamers-upgrade.json",
	) {
		found, err := pathHasState(path)
		if err != nil {
			return false, err
		}
		if found {
			return true, nil
		}
	}
	for _, root := range configuredAccessRoots(config) {
		marker, err := store.ExclusiveAccessMarker(root)
		if err != nil {
			return false, err
		}
		found, err := pathHasState(marker)
		if err != nil {
			return false, err
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}

func pathHasState(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return true, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	return len(entries) != 0, nil
}

func closeAccess(release func() error) error {
	if release == nil {
		return nil
	}
	return release()
}
