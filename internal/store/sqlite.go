package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"entgo.io/ent/privacy"
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

const minimumManagedUpgradeVersion = 1

//go:embed migrations/*.sql
var migrationFiles embed.FS

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

type migration struct {
	version  int
	name     string
	checksum string
	sql      string
}

// MigrationSafety classifies the data-loss risk of one committed migration.
type MigrationSafety string

const (
	// MigrationNonDestructive is an attested data-preserving migration.
	MigrationNonDestructive MigrationSafety = "NonDestructive"
	// MigrationUnclassified has no matching committed attestation.
	MigrationUnclassified MigrationSafety = "Unclassified"
)

// MigrationStep is one exact committed migration in an upgrade plan.
type MigrationStep struct {
	Version                    int             `json:"version"`
	Name                       string          `json:"name"`
	Checksum                   string          `json:"checksum"`
	Safety                     MigrationSafety `json:"safety"`
	MinimumReaderSchemaVersion int             `json:"minimum_reader_schema_version"`
	MinimumWriterSchemaVersion int             `json:"minimum_writer_schema_version"`
	Consequence                string          `json:"consequence"`
}

// MigrationPlan is an exact validated schema-prefix transition.
type MigrationPlan struct {
	FromVersion                int             `json:"from_version"`
	ToVersion                  int             `json:"to_version"`
	Safety                     MigrationSafety `json:"safety"`
	MinimumReaderSchemaVersion int             `json:"minimum_reader_schema_version"`
	MinimumWriterSchemaVersion int             `json:"minimum_writer_schema_version"`
	Migrations                 []MigrationStep `json:"migrations"`
}

// BackupAttachment identifies one immutable file referenced by installation state.
type BackupAttachment struct {
	StorageKey string
	SHA256     string
	SizeBytes  int64
}

// UnsupportedSnapshotInspection describes a structurally valid Beamers database
// that this executable cannot safely open.
type UnsupportedSnapshotInspection struct {
	SchemaVersion         int
	UnknownSchemaElements []string
}

// systemContext is the store-only boundary for narrowly isolated internal work.
func systemContext(ctx context.Context) context.Context {
	return privacy.DecisionContext(ctx, privacy.Allow)
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
	count, err := installation.client.Installation.Query().Count(systemContext(ctx))
	if err != nil {
		return fmt.Errorf("read installation identity: %w", err)
	}
	if count != 1 {
		return fmt.Errorf("%w: found %d installation identity records", ErrUnsupportedSchema, count)
	}
	return nil
}

// Snapshot writes one consistent compact database copy without replacing an
// existing destination.
func (installation *SQLite) Snapshot(ctx context.Context, destination string) error {
	if installation == nil || installation.database == nil {
		return ErrUninitialized
	}
	if destination == "" {
		return errors.New("snapshot destination is required")
	}
	if _, err := os.Stat(destination); err == nil {
		return errors.New("snapshot destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect snapshot destination: %w", err)
	}
	if _, err := installation.database.ExecContext(
		ctx,
		"VACUUM main INTO ?",
		destination,
	); err != nil {
		return fmt.Errorf("snapshot installation database: %w", err)
	}
	return syncFile(destination)
}

// SanitizeSnapshot removes authentication material from a closed snapshot.
func SanitizeSnapshot(ctx context.Context, path string) (returnErr error) {
	database, err := openDatabase(ctx, path)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, database.Close())
	}()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Backup sanitization: %w", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	for _, statement := range []string{
		"DELETE FROM account_sessions",
		"DELETE FROM bootstrap_credentials",
		"DELETE FROM password_credentials",
		"DELETE FROM web_authn_credentials",
		"DELETE FROM federated_identities",
		"DELETE FROM recovery_codes",
		"DELETE FROM recovery_tokens",
		"DELETE FROM display_credentials",
		"DELETE FROM display_enrollments",
		"DELETE FROM voting_keys",
	} {
		if _, err = transaction.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("sanitize Backup authentication material: %w", err)
		}
	}
	if err = transaction.Commit(); err != nil {
		return fmt.Errorf("commit Backup sanitization: %w", err)
	}
	return syncFile(path)
}

// ValidateSnapshot proves that a closed database matches the committed schema.
func ValidateSnapshot(ctx context.Context, path string) (returnErr error) {
	migrations, err := loadMigrations()
	if err != nil {
		return fmt.Errorf("load committed migrations: %w", err)
	}
	database, err := openValidationDatabase(ctx, path)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, database.Close())
	}()
	return validateStorage(ctx, database, migrations)
}

// ValidateSanitizedSnapshot proves that a closed snapshot contains no authentication material.
func ValidateSanitizedSnapshot(ctx context.Context, path string) (returnErr error) {
	database, err := openValidationDatabase(ctx, path)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, database.Close())
	}()
	for _, query := range []string{
		"SELECT EXISTS(SELECT 1 FROM account_sessions)",
		"SELECT EXISTS(SELECT 1 FROM bootstrap_credentials)",
		"SELECT EXISTS(SELECT 1 FROM password_credentials)",
		"SELECT EXISTS(SELECT 1 FROM web_authn_credentials)",
		"SELECT EXISTS(SELECT 1 FROM federated_identities)",
		"SELECT EXISTS(SELECT 1 FROM recovery_codes)",
		"SELECT EXISTS(SELECT 1 FROM recovery_tokens)",
		"SELECT EXISTS(SELECT 1 FROM display_credentials)",
		"SELECT EXISTS(SELECT 1 FROM display_enrollments)",
		"SELECT EXISTS(SELECT 1 FROM voting_keys)",
	} {
		var found bool
		if err = database.QueryRowContext(ctx, query).Scan(&found); err != nil {
			return fmt.Errorf("inspect sanitized Backup: %w", err)
		}
		if found {
			return errors.New("sanitized Backup contains authentication material")
		}
	}
	return nil
}

// InspectUnsupportedSnapshot checks integrity and reports schema elements not
// understood by this executable without mutating the supplied copy.
func InspectUnsupportedSnapshot(
	ctx context.Context,
	path string,
) (_ UnsupportedSnapshotInspection, returnErr error) {
	database, err := openValidationDatabase(ctx, path)
	if err != nil {
		return UnsupportedSnapshotInspection{}, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, database.Close())
	}()
	if integrityErr := verifySQLiteIntegrity(ctx, database); integrityErr != nil {
		return UnsupportedSnapshotInspection{}, integrityErr
	}
	var foundApplicationID, schemaVersion int
	if err = database.QueryRowContext(ctx, "PRAGMA application_id").Scan(
		&foundApplicationID,
	); err != nil {
		return UnsupportedSnapshotInspection{}, fmt.Errorf(
			"read unsupported Restore application identifier: %w",
			err,
		)
	}
	if foundApplicationID != applicationID {
		return UnsupportedSnapshotInspection{}, fmt.Errorf(
			"%w: unknown application identifier",
			ErrUnsupportedSchema,
		)
	}
	if err = database.QueryRowContext(ctx, "PRAGMA user_version").Scan(
		&schemaVersion,
	); err != nil {
		return UnsupportedSnapshotInspection{}, fmt.Errorf(
			"read unsupported Restore schema version: %w",
			err,
		)
	}
	foundSchema, err := sqliteSchema(ctx, database)
	if err != nil {
		return UnsupportedSnapshotInspection{}, err
	}

	migrations, err := loadMigrations()
	if err != nil {
		return UnsupportedSnapshotInspection{}, fmt.Errorf("load committed migrations: %w", err)
	}
	current, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return UnsupportedSnapshotInspection{}, fmt.Errorf("open current schema model: %w", err)
	}
	current.SetMaxOpenConns(1)
	defer func() {
		returnErr = errors.Join(returnErr, current.Close())
	}()
	if err = initializeSchema(ctx, current, migrations); err != nil {
		return UnsupportedSnapshotInspection{}, fmt.Errorf("build current schema model: %w", err)
	}
	currentSchema, err := sqliteSchema(ctx, current)
	if err != nil {
		return UnsupportedSnapshotInspection{}, err
	}
	unknown := make([]string, 0)
	for name, found := range foundSchema {
		expected, exists := currentSchema[name]
		if !exists {
			unknown = append(unknown, name)
		} else if found != expected {
			unknown = append(unknown, "changed "+name)
		}
	}
	sort.Strings(unknown)
	return UnsupportedSnapshotInspection{
		SchemaVersion:         schemaVersion,
		UnknownSchemaElements: unknown,
	}, nil
}

func verifySQLiteIntegrity(ctx context.Context, database *sql.DB) (returnErr error) {
	rows, err := database.QueryContext(ctx, "PRAGMA quick_check")
	if err != nil {
		return fmt.Errorf("check unsupported Restore copy: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, rows.Close())
	}()
	for rows.Next() {
		var result string
		if err = rows.Scan(&result); err != nil {
			return fmt.Errorf("read unsupported Restore integrity result: %w", err)
		}
		if result != "ok" {
			return errors.New("unsupported Restore copy failed SQLite integrity check")
		}
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("finish unsupported Restore integrity check: %w", err)
	}
	return nil
}

func sqliteSchema(ctx context.Context, database *sql.DB) (_ map[string]string, returnErr error) {
	rows, err := database.QueryContext(
		ctx,
		"SELECT type, name, coalesce(sql, '') FROM sqlite_schema "+
			"WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name",
	)
	if err != nil {
		return nil, fmt.Errorf("read SQLite schema elements: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, rows.Close())
	}()
	elements := make(map[string]string)
	for rows.Next() {
		var kind, name, definition string
		if err = rows.Scan(&kind, &name, &definition); err != nil {
			return nil, fmt.Errorf("read SQLite schema element: %w", err)
		}
		elements[kind+" "+name] = definition
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("finish SQLite schema inspection: %w", err)
	}
	return elements, nil
}

// BackupAttachmentsFromSnapshot lists immutable files referenced by a snapshot.
func BackupAttachmentsFromSnapshot(
	ctx context.Context,
	path string,
) (_ []BackupAttachment, returnErr error) {
	database, err := openValidationDatabase(ctx, path)
	if err != nil {
		return nil, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, database.Close())
	}()
	rows, err := database.QueryContext(
		ctx,
		"SELECT storage_key, sha256, size_bytes "+
			"FROM attachment_versions ORDER BY storage_key, id",
	)
	if err != nil {
		return nil, fmt.Errorf("list Backup Attachments: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, rows.Close())
	}()
	attachments := make([]BackupAttachment, 0)
	for rows.Next() {
		var found BackupAttachment
		if err = rows.Scan(&found.StorageKey, &found.SHA256, &found.SizeBytes); err != nil {
			return nil, fmt.Errorf("read Backup Attachment inventory: %w", err)
		}
		if len(attachments) != 0 &&
			attachments[len(attachments)-1].StorageKey == found.StorageKey {
			if attachments[len(attachments)-1] != found {
				return nil, errors.New("attachment storage key has conflicting metadata")
			}
			continue
		}
		attachments = append(attachments, found)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("read Backup Attachment inventory: %w", err)
	}
	return attachments, nil
}

// SchemaVersion returns the latest committed schema understood by this binary.
func (installation *SQLite) SchemaVersion() int {
	if installation == nil {
		return 0
	}
	return installation.applied
}

// CurrentSchemaVersion returns the latest committed schema understood by this binary.
func CurrentSchemaVersion() (int, error) {
	migrations, err := loadMigrations()
	if err != nil {
		return 0, fmt.Errorf("load committed migrations: %w", err)
	}
	if len(migrations) == 0 {
		return 0, ErrUnsupportedSchema
	}
	return migrations[len(migrations)-1].version, nil
}

// PlanMigrations validates one known database prefix and returns its exact
// transition to the latest committed schema.
func PlanMigrations(
	ctx context.Context,
	databasePath string,
) (_ MigrationPlan, returnErr error) {
	migrations, err := loadMigrations()
	if err != nil {
		return MigrationPlan{}, fmt.Errorf("load committed migrations: %w", err)
	}
	database, err := openValidationDatabase(ctx, databasePath)
	if err != nil {
		return MigrationPlan{}, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, database.Close())
	}()
	applied, err := validateSchemaPrefix(ctx, database, migrations)
	if err != nil {
		return MigrationPlan{}, err
	}
	if applied > len(migrations) {
		return MigrationPlan{}, fmt.Errorf(
			"%w: schema version %d is newer than this executable",
			ErrUnsupportedSchema,
			applied,
		)
	}
	if applied != len(migrations) && applied < minimumManagedUpgradeVersion {
		return MigrationPlan{}, fmt.Errorf(
			"%w: schema version %d predates managed upgrades",
			ErrUnsupportedSchema,
			applied,
		)
	}
	plan := MigrationPlan{
		FromVersion: applied,
		ToVersion:   len(migrations),
		Safety:      MigrationNonDestructive,
		Migrations:  make([]MigrationStep, 0, len(migrations)-applied),
	}
	for _, migration := range migrations[applied:] {
		plan.Safety = MigrationUnclassified
		plan.MinimumReaderSchemaVersion = migration.version
		plan.MinimumWriterSchemaVersion = migration.version
		plan.Migrations = append(plan.Migrations, MigrationStep{
			Version: migration.version, Name: migration.name,
			Checksum: migration.checksum, Safety: MigrationUnclassified,
			MinimumReaderSchemaVersion: migration.version,
			MinimumWriterSchemaVersion: migration.version,
			Consequence: "migration has no matching committed safety " +
				"attestation",
		})
	}
	if len(plan.Migrations) == 0 {
		plan.MinimumReaderSchemaVersion = applied
		plan.MinimumWriterSchemaVersion = applied
	}
	return plan, nil
}

// MigrateSnapshot applies one exact plan to a closed staged database.
func MigrateSnapshot(ctx context.Context, databasePath string, plan MigrationPlan) error {
	found, err := PlanMigrations(ctx, databasePath)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(found, plan) {
		return errors.New("staged migration plan no longer matches its preview")
	}
	if len(plan.Migrations) == 0 {
		return nil
	}
	migrations, err := loadMigrations()
	if err != nil {
		return fmt.Errorf("load committed migrations: %w", err)
	}
	database, err := openDatabase(ctx, databasePath)
	if err != nil {
		return err
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return errors.Join(fmt.Errorf("begin staged migration: %w", err), database.Close())
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	for _, step := range plan.Migrations {
		committedMigration := migrations[step.Version-1]
		if _, err = transaction.ExecContext(ctx, committedMigration.sql); err != nil {
			return errors.Join(
				fmt.Errorf(
					"apply migration %04d_%s: %w",
					committedMigration.version,
					committedMigration.name,
					err,
				),
				database.Close(),
			)
		}
		if err = recordMigration(ctx, transaction, committedMigration); err != nil {
			return errors.Join(err, database.Close())
		}
		if _, err = transaction.ExecContext(
			ctx,
			fmt.Sprintf("PRAGMA user_version = %d", committedMigration.version),
		); err != nil {
			return errors.Join(fmt.Errorf("set schema version: %w", err), database.Close())
		}
	}
	if err = transaction.Commit(); err != nil {
		return errors.Join(fmt.Errorf("commit staged migration: %w", err), database.Close())
	}
	if err = database.Close(); err != nil {
		return fmt.Errorf("close migrated database: %w", err)
	}
	return ValidateSnapshot(ctx, databasePath)
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

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].Name() < entries[right].Name()
	})

	migrations := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		loaded, err := readMigration(entry.Name())
		if err != nil {
			return nil, err
		}
		expectedVersion := len(migrations) + 1
		if loaded.version != expectedVersion {
			return nil, fmt.Errorf("migration %q is version %d, want %d", entry.Name(), loaded.version, expectedVersion)
		}
		migrations = append(migrations, loaded)
	}
	if len(migrations) == 0 {
		return nil, errors.New("no committed migrations")
	}
	return migrations, nil
}

func readMigration(filename string) (migration, error) {
	stem := strings.TrimSuffix(filename, filepath.Ext(filename))
	versionText, name, found := strings.Cut(stem, "_")
	if !found || name == "" {
		return migration{}, fmt.Errorf("invalid migration filename %q", filename)
	}
	version, err := strconv.Atoi(versionText)
	if err != nil {
		return migration{}, fmt.Errorf("parse migration version %q: %w", filename, err)
	}
	contents, err := migrationFiles.ReadFile(filepath.Join("migrations", filename))
	if err != nil {
		return migration{}, fmt.Errorf("read migration %q: %w", filename, err)
	}
	checksum := sha256.Sum256(contents)
	return migration{
		version:  version,
		name:     name,
		checksum: fmt.Sprintf("%x", checksum),
		sql:      string(contents),
	}, nil
}

func initializeSchema(ctx context.Context, database *sql.DB, migrations []migration) error {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schema initialization: %w", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()

	for _, migration := range migrations {
		if _, err := transaction.ExecContext(ctx, migration.sql); err != nil {
			return fmt.Errorf("apply migration %04d_%s: %w", migration.version, migration.name, err)
		}
		if err := recordMigration(ctx, transaction, migration); err != nil {
			return err
		}
	}
	if _, err := transaction.ExecContext(
		ctx,
		"INSERT INTO installations (created_at) VALUES (CURRENT_TIMESTAMP)",
	); err != nil {
		return fmt.Errorf("record installation identity: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id = %d", applicationID)); err != nil {
		return fmt.Errorf("set application identifier: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", len(migrations))); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit schema initialization: %w", err)
	}
	return nil
}

func recordMigration(
	ctx context.Context,
	transaction *sql.Tx,
	migration migration,
) error {
	if migration.version > 1 {
		if _, err := transaction.ExecContext(
			ctx,
			"INSERT INTO beamers_schema_migrations "+
				"(version, name, checksum, safety, minimum_reader_schema_version, "+
				"minimum_writer_schema_version, applied_at) "+
				"VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)",
			migration.version,
			migration.name,
			migration.checksum,
			MigrationUnclassified,
			migration.version,
			migration.version,
		); err != nil {
			return fmt.Errorf(
				"record migration %04d_%s: %w",
				migration.version,
				migration.name,
				err,
			)
		}
		return nil
	}
	if _, err := transaction.ExecContext(
		ctx,
		"INSERT INTO beamers_schema_migrations "+
			"(version, name, checksum, applied_at) VALUES (?, ?, ?, CURRENT_TIMESTAMP)",
		migration.version,
		migration.name,
		migration.checksum,
	); err != nil {
		return fmt.Errorf(
			"record migration %04d_%s: %w",
			migration.version,
			migration.name,
			err,
		)
	}
	return nil
}

func validateStorage(ctx context.Context, database *sql.DB, migrations []migration) error {
	if err := validateCurrentSchema(ctx, database, migrations); err != nil {
		return err
	}
	var installationCount int
	if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM installations").Scan(&installationCount); err != nil {
		return errors.Join(ErrUnsupportedSchema, fmt.Errorf("read installation identity: %w", err))
	}
	if installationCount != 1 {
		return fmt.Errorf(
			"%w: found %d installation identity records",
			ErrUnsupportedSchema,
			installationCount,
		)
	}
	return nil
}

func validateCurrentSchema(ctx context.Context, database *sql.DB, migrations []migration) error {
	applied, err := validateSchemaPrefix(ctx, database, migrations)
	if err != nil {
		return err
	}
	if applied <= len(migrations) {
		if applied != len(migrations) {
			return fmt.Errorf(
				"%w: schema version %d is not current",
				ErrUnsupportedSchema,
				applied,
			)
		}
		return nil
	}
	var schemaVersion, minimumReader, minimumWriter int
	if err := database.QueryRowContext(
		ctx,
		"SELECT version, minimum_reader_schema_version, "+
			"minimum_writer_schema_version FROM beamers_schema_migrations "+
			"ORDER BY version DESC LIMIT 1",
	).Scan(&schemaVersion, &minimumReader, &minimumWriter); err != nil {
		return errors.Join(
			ErrUnsupportedSchema,
			fmt.Errorf("read forward compatibility contract: %w", err),
		)
	}
	if schemaVersion != applied ||
		len(migrations) < minimumReader ||
		len(migrations) < minimumWriter {
		return fmt.Errorf(
			"%w: schema version %d requires reader %d and writer %d",
			ErrUnsupportedSchema,
			applied,
			minimumReader,
			minimumWriter,
		)
	}
	return nil
}

func validateSchemaPrefix(
	ctx context.Context,
	database *sql.DB,
	migrations []migration,
) (applied int, returnErr error) {
	var foundApplicationID int
	if err := database.QueryRowContext(ctx, "PRAGMA application_id").Scan(&foundApplicationID); err != nil {
		return 0, errors.Join(
			ErrUnsupportedSchema,
			fmt.Errorf("read application identifier: %w", err),
		)
	}
	if foundApplicationID == 0 {
		return 0, ErrUninitialized
	}
	if foundApplicationID != applicationID {
		return 0, fmt.Errorf("%w: unknown application identifier", ErrUnsupportedSchema)
	}

	rows, err := database.QueryContext(ctx, "SELECT version, name, checksum FROM beamers_schema_migrations ORDER BY version")
	if err != nil {
		return 0, errors.Join(
			ErrUnsupportedSchema,
			fmt.Errorf("read migration history: %w", err),
		)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			returnErr = errors.Join(
				returnErr,
				fmt.Errorf("close migration history: %w", closeErr),
			)
		}
	}()
	for rows.Next() {
		var version int
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum); err != nil {
			return 0, errors.Join(
				ErrUnsupportedSchema,
				fmt.Errorf("read migration record: %w", err),
			)
		}
		if version != applied+1 {
			return 0, errors.Join(
				fmt.Errorf("%w: migration history is not contiguous", ErrUnsupportedSchema),
			)
		}
		if applied < len(migrations) {
			expected := migrations[applied]
			if name != expected.name || checksum != expected.checksum {
				return 0, errors.Join(
					fmt.Errorf(
						"%w: migration %d does not match committed history",
						ErrUnsupportedSchema,
						version,
					),
				)
			}
		} else if name == "" || len(checksum) != sha256.Size*2 {
			return 0, errors.Join(
				fmt.Errorf(
					"%w: migration %d has invalid forward history",
					ErrUnsupportedSchema,
					version,
				),
			)
		}
		applied++
	}
	if err := rows.Err(); err != nil {
		return 0, errors.Join(
			ErrUnsupportedSchema,
			fmt.Errorf("read migration history: %w", err),
		)
	}
	if err := rows.Close(); err != nil {
		return 0, errors.Join(
			ErrUnsupportedSchema,
			fmt.Errorf("close migration history: %w", err),
		)
	}

	var userVersion int
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&userVersion); err != nil {
		return 0, errors.Join(
			ErrUnsupportedSchema,
			fmt.Errorf("read schema version: %w", err),
		)
	}
	if userVersion != applied {
		return 0, fmt.Errorf(
			"%w: schema version %d does not match %d migration records",
			ErrUnsupportedSchema,
			userVersion,
			applied,
		)
	}
	return applied, nil
}

type installationLock struct {
	file *os.File
}

// ExclusiveAccessMarker returns the stable lock path adjacent to one owned root.
func ExclusiveAccessMarker(root string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve exclusive access root: %w", err)
	}
	return absolute + accessLockSuffix, nil
}

// UsesExternalAttachments reports whether Attachments live outside their default root.
func UsesExternalAttachments(dataDir, attachmentsDir string) bool {
	return attachmentsDir != "" &&
		filepath.Clean(attachmentsDir) != filepath.Clean(filepath.Join(dataDir, "attachments"))
}

// HoldExclusiveAccess locks configured roots without placing markers inside them.
func HoldExclusiveAccess(roots ...string) (func() error, error) {
	markers := make([]string, 0, len(roots))
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		if root == "" {
			continue
		}
		marker, err := ExclusiveAccessMarker(root)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[marker]; duplicate {
			continue
		}
		seen[marker] = struct{}{}
		markers = append(markers, marker)
	}
	sort.Strings(markers)

	locks := make([]*installationLock, 0, len(markers))
	release := func() error {
		var releaseErr error
		for _, lock := range slices.Backward(locks) {
			releaseErr = errors.Join(releaseErr, lock.close())
		}
		return releaseErr
	}
	for _, marker := range markers {
		file, err := os.OpenFile( //nolint:gosec // Host-configured root selects its adjacent lock.
			marker,
			os.O_CREATE|os.O_RDWR,
			0o600,
		)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("open exclusive access marker: %w", err), release())
		}
		lock, err := lockInstallationFile(file)
		if err != nil {
			return nil, errors.Join(err, release())
		}
		locks = append(locks, lock)
	}
	return release, nil
}

func createInstallationLock(dataDir string) (*installationLock, error) {
	path := filepath.Join(dataDir, lockFilename)
	// The operator-selected data directory is the intended filesystem boundary.
	//nolint:gosec // Opening its fixed lock filename is required installation behavior.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, ErrAlreadyInitialized
	}
	if err != nil {
		return nil, fmt.Errorf("open installation lock: %w", err)
	}
	return lockInstallationFile(file)
}

func openInstallationLock(dataDir string) (*installationLock, error) {
	path := filepath.Join(dataDir, lockFilename)
	//nolint:gosec // requireInstallationMarker verified this fixed path immediately before use.
	file, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open installation lock: %w", err)
	}
	return lockInstallationFile(file)
}

func lockInstallationFile(file *os.File) (*installationLock, error) {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, errors.Join(ErrInstallationInUse, fmt.Errorf("lock installation: %w", err), file.Close())
	}
	return &installationLock{file: file}, nil
}

func (lock *installationLock) sync() error {
	if err := lock.file.Sync(); err != nil {
		return fmt.Errorf("sync installation marker: %w", err)
	}
	return nil
}

func (lock *installationLock) close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(lock.file.Fd()), syscall.LOCK_UN)
	closeErr := lock.file.Close()
	lock.file = nil
	return errors.Join(unlockErr, closeErr)
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
