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

// FailSessionRunUpdates installs a test-only trigger that forces target adjustment rollback.
func FailSessionRunUpdates(ctx context.Context, path string) error {
	return mutateSchema(path, func(database *sql.DB) error {
		const statement = `CREATE TRIGGER fail_session_run_update
BEFORE UPDATE OF target_adjustment_seconds, target_adjusted_at ON session_runs
BEGIN
	SELECT RAISE(FAIL, 'forced Session Run update failure');
END`
		if _, err := database.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("install Session Run failure trigger: %w", err)
		}
		return nil
	})
}

// AllowSessionRunUpdates removes the test-only target adjustment failure trigger.
func AllowSessionRunUpdates(ctx context.Context, path string) error {
	return mutateSchema(path, func(database *sql.DB) error {
		if _, err := database.ExecContext(ctx, "DROP TRIGGER fail_session_run_update"); err != nil {
			return fmt.Errorf("remove Session Run failure trigger: %w", err)
		}
		return nil
	})
}

// FailSessionForecastUpdate installs a test-only trigger for one Session.
func FailSessionForecastUpdate(ctx context.Context, path string, sessionID int64) error {
	if sessionID <= 0 {
		return errors.New("session ID must be positive")
	}
	return mutateSchema(path, func(database *sql.DB) error {
		statement := fmt.Sprintf(`CREATE TRIGGER fail_session_forecast_update
BEFORE UPDATE OF forecast_start, forecast_end ON sessions
WHEN OLD.id = %d
BEGIN
	SELECT RAISE(FAIL, 'forced Session Forecast update failure');
END`, sessionID)
		if _, err := database.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("install Session Forecast failure trigger: %w", err)
		}
		return nil
	})
}

// AllowSessionForecastUpdates removes the test-only Forecast failure trigger.
func AllowSessionForecastUpdates(ctx context.Context, path string) error {
	return mutateSchema(path, func(database *sql.DB) error {
		if _, err := database.ExecContext(ctx, "DROP TRIGGER fail_session_forecast_update"); err != nil {
			return fmt.Errorf("remove Session Forecast failure trigger: %w", err)
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
