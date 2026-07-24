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
	"sort"
	"strconv"
	"strings"
	"syscall"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"entgo.io/ent/privacy"

	"github.com/dotwaffle/beamers/ent"

	_ "modernc.org/sqlite" // Register the pure-Go SQLite database/sql driver.
)

const (
	databaseFilename = "beamers.db"
	lockFilename     = ".beamers.lock"
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
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// SQLite is an open Beamers installation, including a recovery-mode handle
// when startup validation finds unsafe storage.
type SQLite struct {
	database   *sql.DB
	client     *ent.Client
	lock       *installationLock
	migrations []migration
	startupErr error
}

type migration struct {
	version  int
	name     string
	checksum string
	sql      string
}

// BackupAttachment identifies one immutable file referenced by installation state.
type BackupAttachment struct {
	StorageKey string
	SHA256     string
	SizeBytes  int64
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

	database, err := openDatabase(ctx, databasePath)
	if err != nil {
		return installation.withStartupError(err)
	}
	if err := validateStorage(ctx, database, migrations); err != nil {
		return installation.withStartupError(errors.Join(err, database.Close()))
	}

	driver := entsql.OpenDB(dialect.SQLite, database)
	installation.database = database
	installation.client = ent.NewClient(ent.Driver(driver))
	installation.migrations = migrations
	return installation, nil
}

func recoverySQLite(startupErr error) (*SQLite, error) {
	return (&SQLite{}).withStartupError(startupErr)
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
		"DELETE FROM display_credentials",
		"DELETE FROM display_enrollments",
		"DELETE FROM upload_links",
		"DELETE FROM command_receipts WHERE action = 'CreateAccount'",
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
	if installation == nil || len(installation.migrations) == 0 {
		return 0
	}
	return installation.migrations[len(installation.migrations)-1].version
}

// Close closes storage and releases the installation's process lock.
func (installation *SQLite) Close() error {
	if installation == nil {
		return nil
	}
	var databaseErr error
	if installation.client != nil {
		databaseErr = installation.client.Close()
	} else if installation.database != nil {
		databaseErr = installation.database.Close()
	}
	return errors.Join(databaseErr, installation.lock.close())
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

func openValidationDatabase(ctx context.Context, path string) (*sql.DB, error) {
	return openSQLite(ctx, path, true)
}

func openSQLite(ctx context.Context, path string, readOnly bool) (*sql.DB, error) {
	database, err := sql.Open("sqlite", sqliteDataSource(path, readOnly))
	action := "open installation database"
	if readOnly {
		action += " for validation"
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", action, err)
	}
	database.SetMaxOpenConns(1)
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
		if _, err := transaction.ExecContext(
			ctx,
			"INSERT INTO beamers_schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, CURRENT_TIMESTAMP)",
			migration.version,
			migration.name,
			migration.checksum,
		); err != nil {
			return fmt.Errorf("record migration %04d_%s: %w", migration.version, migration.name, err)
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

func validateCurrentSchema(ctx context.Context, database *sql.DB, migrations []migration) (returnErr error) {
	var foundApplicationID int
	if err := database.QueryRowContext(ctx, "PRAGMA application_id").Scan(&foundApplicationID); err != nil {
		return errors.Join(ErrUnsupportedSchema, fmt.Errorf("read application identifier: %w", err))
	}
	if foundApplicationID == 0 {
		return ErrUninitialized
	}
	if foundApplicationID != applicationID {
		return fmt.Errorf("%w: unknown application identifier", ErrUnsupportedSchema)
	}

	rows, err := database.QueryContext(ctx, "SELECT version, name, checksum FROM beamers_schema_migrations ORDER BY version")
	if err != nil {
		return errors.Join(ErrUnsupportedSchema, fmt.Errorf("read migration history: %w", err))
	}
	defer func() {
		returnErr = errors.Join(returnErr, rows.Close())
	}()
	applied := 0
	for rows.Next() {
		var version int
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum); err != nil {
			return errors.Join(ErrUnsupportedSchema, fmt.Errorf("read migration record: %w", err))
		}
		if applied >= len(migrations) {
			return fmt.Errorf("%w: schema version %d is newer than this executable", ErrUnsupportedSchema, version)
		}
		expected := migrations[applied]
		if version != expected.version || name != expected.name || checksum != expected.checksum {
			return fmt.Errorf("%w: migration %d does not match committed history", ErrUnsupportedSchema, version)
		}
		applied++
	}
	if err := rows.Err(); err != nil {
		return errors.Join(ErrUnsupportedSchema, fmt.Errorf("read migration history: %w", err))
	}

	var userVersion int
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&userVersion); err != nil {
		return errors.Join(ErrUnsupportedSchema, fmt.Errorf("read schema version: %w", err))
	}
	if userVersion > len(migrations) {
		return fmt.Errorf("%w: schema version %d is newer than this executable", ErrUnsupportedSchema, userVersion)
	}
	if applied != len(migrations) || userVersion != applied {
		return fmt.Errorf("%w: schema version %d is not current", ErrUnsupportedSchema, userVersion)
	}
	return nil
}

type installationLock struct {
	file *os.File
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
