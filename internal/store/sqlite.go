package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/XSAM/otelsql"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/dotwaffle/beamers/ent"

	_ "modernc.org/sqlite" // Register the pure-Go SQLite database/sql driver.
)

const (
	databaseFilename = "beamers.db"
	lockFilename     = ".beamers.lock"
	accessLockSuffix = ".beamers-access.lock"
	applicationID    = 0x424D5253
)

var (
	// ErrAlreadyInitialized means initialization found existing installation data.
	ErrAlreadyInitialized = errors.New("installation is already initialized")
	// ErrInstallationInUse means another Beamers process holds the installation lock.
	ErrInstallationInUse = errors.New("installation is in use")
	// ErrUninitialized means no initialized Beamers database exists at the data path.
	ErrUninitialized = errors.New("installation is uninitialized")
	// ErrUnsupportedSchema means the database is not a supported committed schema.
	ErrUnsupportedSchema = errors.New("installation schema is unsupported")
	// ErrNoMigration means the installation already uses the current schema.
	ErrNoMigration = errors.New("installation does not require migration")
)

// SQLite is an open Beamers installation, including a recovery-mode handle
// when startup validation finds unsafe storage.
type SQLite struct {
	database   *sql.DB
	client     *ent.Client
	reader     *ent.Client
	lock       *installationLock
	migrations []migration
	applied    int
	startupErr error
	dbMetrics  []otelmetric.Registration

	displayRundownMu     sync.Mutex
	displayRundownKey    displayRundownCacheKey
	displayRundown       displayRundownState
	displayRundownCached bool
}

// Initialize creates a new installation and atomically installs its committed
// schema. Existing data is never replaced.
func Initialize(ctx context.Context, dataDir string) (returnErr error) {
	if err := ensureDataDirectory(dataDir); err != nil {
		return err
	}
	unused, err := directoryIsUnused(dataDir)
	if err != nil {
		return err
	}
	if !unused {
		return ErrAlreadyInitialized
	}

	lock, err := createInstallationLock(dataDir)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, lock.close())
	}()
	if syncErr := lock.sync(); syncErr != nil {
		return syncErr
	}
	if syncErr := syncDirectory(dataDir); syncErr != nil {
		return syncErr
	}

	unused, err = directoryIsUnused(dataDir)
	if err != nil {
		return err
	}
	if !unused {
		return ErrAlreadyInitialized
	}

	migrations, err := loadMigrations()
	if err != nil {
		return fmt.Errorf("load committed migrations: %w", err)
	}

	temporary, err := os.CreateTemp(dataDir, ".beamers-init-*.db")
	if err != nil {
		return fmt.Errorf("create installation database: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		returnErr = errors.Join(returnErr, removeIfPresent(temporaryPath))
	}()
	if closeErr := temporary.Close(); closeErr != nil {
		return fmt.Errorf("close new installation database: %w", closeErr)
	}

	database, err := openDatabase(ctx, temporaryPath)
	if err != nil {
		return err
	}
	if err := initializeSchema(ctx, database, migrations); err != nil {
		return errors.Join(err, database.Close())
	}
	if err := database.Close(); err != nil {
		return fmt.Errorf("close initialized database: %w", err)
	}
	if err := syncFile(temporaryPath); err != nil {
		return err
	}

	databasePath := filepath.Join(dataDir, databaseFilename)
	if err := installWithoutReplacement(temporaryPath, databasePath); err != nil {
		return err
	}
	if err := syncDirectory(dataDir); err != nil {
		return err
	}
	return nil
}

// Open opens an installation without changing its schema. Unsafe startup
// storage is retained under an exclusive lock and exposed through StartupError
// so the caller can enter local-only recovery mode.
func Open(ctx context.Context, dataDir string) (*SQLite, error) {
	return open(ctx, dataDir, nil, nil)
}

// OpenWithTelemetry opens an installation with bounded database instrumentation.
func OpenWithTelemetry(
	ctx context.Context,
	dataDir string,
	tracerProvider trace.TracerProvider,
	meterProvider otelmetric.MeterProvider,
) (*SQLite, error) {
	if tracerProvider == nil || meterProvider == nil {
		return nil, errors.New("database telemetry providers are required")
	}
	return open(ctx, dataDir, tracerProvider, meterProvider)
}

func open(
	ctx context.Context,
	dataDir string,
	tracerProvider trace.TracerProvider,
	meterProvider otelmetric.MeterProvider,
) (*SQLite, error) {
	if dataDir == "" {
		return nil, errors.New("data directory is required")
	}
	if err := requireDataDirectory(dataDir); err != nil {
		return recoverySQLite(err)
	}

	databasePath := filepath.Join(dataDir, databaseFilename)
	if err := requireInstallationMarker(dataDir, databasePath); err != nil {
		return recoverySQLite(err)
	}
	lock, err := openInstallationLock(dataDir)
	if err != nil {
		return nil, err
	}
	installation := &SQLite{lock: lock}
	if validationErr := requireRegularDatabase(databasePath); validationErr != nil {
		return installation.withStartupError(validationErr)
	}

	migrations, err := loadMigrations()
	if err != nil {
		return installation.withStartupError(fmt.Errorf("load committed migrations: %w", err))
	}
	validationDatabase, err := openValidationDatabase(ctx, databasePath)
	if err != nil {
		return installation.withStartupError(err)
	}
	validationErr := validateStorage(ctx, validationDatabase, migrations)
	if combinedErr := errors.Join(validationErr, validationDatabase.Close()); combinedErr != nil {
		return installation.withStartupError(combinedErr)
	}

	var database *sql.DB
	var dbMetrics otelmetric.Registration
	if tracerProvider == nil {
		database, err = openDatabase(ctx, databasePath)
	} else {
		database, dbMetrics, err = openTelemetryDatabase(
			ctx,
			databasePath,
			tracerProvider,
			meterProvider,
		)
	}
	if err != nil {
		return installation.withStartupError(err)
	}
	if storageErr := validateStorage(ctx, database, migrations); storageErr != nil {
		return installation.withStartupError(errors.Join(
			storageErr,
			unregister(dbMetrics),
			database.Close(),
		))
	}
	var appliedVersion int
	if versionErr := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&appliedVersion); versionErr != nil {
		return installation.withStartupError(errors.Join(
			fmt.Errorf("read opened schema version: %w", versionErr),
			unregister(dbMetrics),
			database.Close(),
		))
	}

	var readerDatabase *sql.DB
	var readerMetrics otelmetric.Registration
	if tracerProvider == nil {
		readerDatabase, err = openReadDatabase(ctx, databasePath)
	} else {
		readerDatabase, readerMetrics, err = openTelemetryReadDatabase(
			ctx,
			databasePath,
			tracerProvider,
			meterProvider,
		)
	}
	if err != nil {
		return installation.withStartupError(errors.Join(
			err,
			unregister(dbMetrics),
			database.Close(),
		))
	}
	driver := entsql.OpenDB(dialect.SQLite, database)
	readerDriver := entsql.OpenDB(dialect.SQLite, readerDatabase)
	installation.database = database
	installation.client = ent.NewClient(ent.Driver(driver))
	installation.reader = ent.NewClient(ent.Driver(readerDriver))
	installation.migrations = migrations
	installation.applied = appliedVersion
	installation.dbMetrics = []otelmetric.Registration{dbMetrics, readerMetrics}
	return installation, nil
}

func recoverySQLite(startupErr error) (*SQLite, error) {
	return RecoverySQLite(startupErr), nil
}

// RecoverySQLite creates a storage handle that can only report one startup failure.
func RecoverySQLite(startupErr error) *SQLite {
	return &SQLite{startupErr: startupErr}
}

func (installation *SQLite) withStartupError(startupErr error) (*SQLite, error) {
	installation.startupErr = startupErr
	return installation, nil
}

// StartupError reports why an installation is restricted to recovery mode.
func (installation *SQLite) StartupError() error {
	return installation.startupErr
}

// Ready reports whether storage remains usable and on the supported schema.
func (installation *SQLite) Ready(ctx context.Context) error {
	if installation.startupErr != nil {
		return installation.startupErr
	}
	if err := installation.database.PingContext(ctx); err != nil {
		return fmt.Errorf("ping installation database: %w", err)
	}
	if err := validateCurrentSchema(ctx, installation.database, installation.migrations); err != nil {
		return err
	}
	count, err := installation.client.Installation.Query().Count(ctx)
	if err != nil {
		return fmt.Errorf("read installation identity: %w", err)
	}
	if count != 1 {
		return fmt.Errorf("%w: found %d installation identity records", ErrUnsupportedSchema, count)
	}
	return nil
}

// Close closes storage and releases the installation's process lock.
func (installation *SQLite) Close() error {
	if installation == nil {
		return nil
	}
	var databaseErr error
	var metricsErr error
	for _, registration := range installation.dbMetrics {
		metricsErr = errors.Join(metricsErr, unregister(registration))
	}
	var readerErr error
	if installation.reader != nil {
		readerErr = installation.reader.Close()
	}
	if installation.client != nil {
		databaseErr = installation.client.Close()
	} else if installation.database != nil {
		databaseErr = installation.database.Close()
	}
	return errors.Join(metricsErr, readerErr, databaseErr, installation.lock.close())
}

// readClient returns the pooled read-only handle that keeps attendee and crew
// read traffic off the serialized writer connection. Recovery-mode handles have
// no reader, so they fall back to whatever handle they were opened with.
func (installation *SQLite) readClient() *ent.Client {
	if installation.reader != nil {
		return installation.reader
	}
	return installation.client
}

func unregister(registration otelmetric.Registration) error {
	if registration == nil {
		return nil
	}
	return registration.Unregister()
}

func ensureDataDirectory(dataDir string) error {
	if dataDir == "" {
		return errors.New("data directory is required")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	return requireDataDirectory(dataDir)
}

func requireDataDirectory(dataDir string) error {
	if dataDir == "" {
		return errors.New("data directory is required")
	}
	info, err := os.Stat(dataDir)
	if errors.Is(err, os.ErrNotExist) {
		return ErrUninitialized
	}
	if err != nil {
		return fmt.Errorf("inspect data directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: data path is not a directory", ErrUninitialized)
	}
	return nil
}

func directoryIsUnused(dataDir string) (bool, error) {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return false, fmt.Errorf("inspect data directory contents: %w", err)
	}
	for _, entry := range entries {
		if entry.Name() != lockFilename {
			return false, nil
		}
	}
	return true, nil
}

func requireRegularDatabase(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrUninitialized
	}
	if err != nil {
		return fmt.Errorf("inspect installation database: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: database path is not a regular file", ErrUnsupportedSchema)
	}
	return nil
}

func requireInstallationMarker(dataDir, databasePath string) error {
	markerPath := filepath.Join(dataDir, lockFilename)
	info, err := os.Lstat(markerPath)
	if errors.Is(err, os.ErrNotExist) {
		if _, databaseErr := os.Lstat(databasePath); errors.Is(databaseErr, os.ErrNotExist) {
			return ErrUninitialized
		} else if databaseErr != nil {
			return fmt.Errorf("inspect installation database: %w", databaseErr)
		}
		return fmt.Errorf("%w: installation marker is missing", ErrUnsupportedSchema)
	}
	if err != nil {
		return fmt.Errorf("inspect installation marker: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: installation marker is not a regular file", ErrUnsupportedSchema)
	}
	return nil
}

func openDatabase(ctx context.Context, path string) (*sql.DB, error) {
	return openSQLite(ctx, path, false)
}

// Keep 500 concurrent Display snapshots in bounded SQLite read waves while the
// authoritative writer remains serialized.
const displayReadConnectionLimit = 64

func openReadDatabase(ctx context.Context, path string) (*sql.DB, error) {
	return openSQLitePool(ctx, path, true, displayReadConnectionLimit)
}

func openTelemetryDatabase(
	ctx context.Context,
	path string,
	tracerProvider trace.TracerProvider,
	meterProvider otelmetric.MeterProvider,
) (*sql.DB, otelmetric.Registration, error) {
	return openTelemetrySQLite(
		ctx,
		path,
		false,
		1,
		tracerProvider,
		meterProvider,
	)
}

func openTelemetryReadDatabase(
	ctx context.Context,
	path string,
	tracerProvider trace.TracerProvider,
	meterProvider otelmetric.MeterProvider,
) (*sql.DB, otelmetric.Registration, error) {
	return openTelemetrySQLite(
		ctx,
		path,
		true,
		displayReadConnectionLimit,
		tracerProvider,
		meterProvider,
	)
}

func openTelemetrySQLite(
	ctx context.Context,
	path string,
	readOnly bool,
	maxConnections int,
	tracerProvider trace.TracerProvider,
	meterProvider otelmetric.MeterProvider,
) (*sql.DB, otelmetric.Registration, error) {
	attributes := []attribute.KeyValue{attribute.String("db.system.name", "sqlite")}
	options := []otelsql.Option{
		otelsql.WithTracerProvider(tracerProvider),
		otelsql.WithMeterProvider(meterProvider),
		otelsql.WithAttributes(attributes...),
		otelsql.WithSQLCommenter(false),
		otelsql.WithSpanOptions(otelsql.SpanOptions{
			DisableQuery:         true,
			OmitConnResetSession: true,
			OmitConnPrepare:      true,
			OmitRows:             true,
			OmitConnectorConnect: true,
		}),
	}
	database, err := otelsql.Open("sqlite", sqliteDataSource(path, readOnly), options...)
	if err != nil {
		return nil, nil, fmt.Errorf("open instrumented installation database: %w", err)
	}
	database.SetMaxOpenConns(maxConnections)
	database.SetMaxIdleConns(maxConnections)
	if err = database.PingContext(ctx); err != nil {
		return nil, nil, errors.Join(
			fmt.Errorf("open instrumented installation database: %w", err),
			database.Close(),
		)
	}
	registration, err := otelsql.RegisterDBStatsMetrics(database, options...)
	if err != nil {
		return nil, nil, errors.Join(
			fmt.Errorf("register database metrics: %w", err),
			database.Close(),
		)
	}
	return database, registration, nil
}

func openValidationDatabase(ctx context.Context, path string) (*sql.DB, error) {
	return openSQLite(ctx, path, true)
}

func openSQLite(ctx context.Context, path string, readOnly bool) (*sql.DB, error) {
	return openSQLitePool(ctx, path, readOnly, 1)
}

func openSQLitePool(
	ctx context.Context,
	path string,
	readOnly bool,
	maxConnections int,
) (*sql.DB, error) {
	database, err := sql.Open("sqlite", sqliteDataSource(path, readOnly))
	action := "open installation database"
	if readOnly {
		action += " read-only"
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", action, err)
	}
	database.SetMaxOpenConns(maxConnections)
	database.SetMaxIdleConns(maxConnections)
	if err := database.PingContext(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("%s: %w", action, err), database.Close())
	}
	return database, nil
}

func sqliteDataSource(path string, readOnly bool) string {
	location := &url.URL{Scheme: "file", Path: path}
	query := location.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Set("_dqs", "false")
	query.Set("_error_rc", "true")
	if readOnly {
		query.Set("mode", "ro")
	} else {
		query.Add("_pragma", "journal_mode(WAL)")
		query.Add("_pragma", "synchronous(FULL)")
		query.Set("_txlock", "immediate")
		query.Set("mode", "rw")
	}
	location.RawQuery = query.Encode()
	return location.String()
}

func installWithoutReplacement(temporaryPath, databasePath string) error {
	if err := os.Link(temporaryPath, databasePath); errors.Is(err, os.ErrExist) {
		return ErrAlreadyInitialized
	} else if err != nil {
		return fmt.Errorf("install initialized database: %w", err)
	}
	if err := os.Remove(temporaryPath); err != nil {
		return fmt.Errorf("remove temporary database link: %w", err)
	}
	return nil
}

func syncFile(path string) error {
	// path is the private temporary database created by Initialize.
	//nolint:gosec // Syncing that generated path is required before installation.
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open initialized database for sync: %w", err)
	}
	return errors.Join(file.Sync(), file.Close())
}

func syncDirectory(path string) error {
	// path is the operator-selected installation data directory.
	//nolint:gosec // Syncing the installation directory makes the new entry durable.
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open data directory for sync: %w", err)
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func removeIfPresent(path string) error {
	err := os.Remove(path)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("remove temporary installation database: %w", err)
}
