// Package storetest prepares deliberately unsupported SQLite fixtures for
// executable-level tests without exposing raw schema manipulation there.
package storetest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite" // Register the pure-Go SQLite fixture driver.
)

// MarkSchemaNewer makes an initialized fixture newer than the executable.
func MarkSchemaNewer(ctx context.Context, path string) error {
	return mutateSchema(path, func(database *sql.DB) error {
		var version int
		if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
			return fmt.Errorf("read fixture schema version: %w", err)
		}
		if _, err := database.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version+1)); err != nil {
			return fmt.Errorf("set fixture schema version: %w", err)
		}
		return nil
	})
}

// ReplaceMigrationChecksum makes committed migration history unknown.
func ReplaceMigrationChecksum(ctx context.Context, path string) error {
	return mutateSchema(path, func(database *sql.DB) error {
		const statement = "UPDATE beamers_schema_migrations SET checksum = printf('%064d', 0)"
		if _, err := database.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("replace fixture migration checksum: %w", err)
		}
		return nil
	})
}

func mutateSchema(
	path string,
	mutation func(*sql.DB) error,
) (returnErr error) {
	location := &url.URL{Scheme: "file", Path: path}
	database, err := sql.Open("sqlite", location.String())
	if err != nil {
		return fmt.Errorf("open SQLite fixture: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, database.Close())
	}()
	if err := mutation(database); err != nil {
		return err
	}
	return nil
}
