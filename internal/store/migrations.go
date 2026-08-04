package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

const minimumManagedUpgradeVersion = 1

//go:embed migrations/*.sql
var migrationFiles embed.FS

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
