// Package storetest prepares deliberately unsupported SQLite fixtures for
// executable-level tests without exposing raw schema manipulation there.
package storetest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"time"

	_ "modernc.org/sqlite" // Register the pure-Go SQLite fixture driver.
)

// SetDisplayOverrideExpiry deterministically changes one test Override deadline.
func SetDisplayOverrideExpiry(
	ctx context.Context,
	path string,
	overrideID int,
	expiresAt time.Time,
) error {
	if overrideID <= 0 {
		return errors.New("display Override ID must be positive")
	}
	return mutateSchema(path, func(database *sql.DB) error {
		const statement = "UPDATE display_overrides SET expires_at = ? WHERE id = ?"
		result, err := database.ExecContext(ctx, statement, expiresAt.UTC(), overrideID)
		if err != nil {
			return fmt.Errorf("set Display Override expiry: %w", err)
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read Display Override expiry update count: %w", err)
		}
		if updated != 1 {
			return fmt.Errorf("set Display Override expiry: updated %d rows", updated)
		}
		return nil
	})
}

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

// AddReferencedAttachment adds the smallest committed Attachment Version fixture.
func AddReferencedAttachment(
	ctx context.Context,
	path, storageKey, digest string,
	size int64,
) error {
	return mutateSchema(path, func(database *sql.DB) error {
		const statement = `
INSERT INTO attachments (
	event_id, owner_type, owner_id, name, created_at
) VALUES (1, 'Presentation', 1, 'fixture', CURRENT_TIMESTAMP);
INSERT INTO attachment_versions (
	attachment_id, version, original_filename, media_type, size_bytes, sha256,
	storage_key, uploader_type, uploader_id, created_at
) VALUES (
	last_insert_rowid(), 1, 'fixture.bin', 'application/octet-stream', ?, ?, ?,
	'Crew', 1, CURRENT_TIMESTAMP
);`
		if _, err := database.ExecContext(ctx, statement, size, digest, storageKey); err != nil {
			return fmt.Errorf("add referenced Attachment fixture: %w", err)
		}
		return nil
	})
}

// MarkAttachmentVersionFinal prepares one uploaded test Version for release.
func MarkAttachmentVersionFinal(ctx context.Context, path string, versionID int) error {
	if versionID <= 0 {
		return errors.New("attachment Version ID must be positive")
	}
	return mutateSchema(path, func(database *sql.DB) error {
		result, err := database.ExecContext(
			ctx,
			"UPDATE attachment_versions "+
				"SET final = true, readiness_revision = readiness_revision + 1 WHERE id = ?",
			versionID,
		)
		if err != nil {
			return fmt.Errorf("mark Attachment Version Final: %w", err)
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read Attachment Version update count: %w", err)
		}
		if updated != 1 {
			return fmt.Errorf("mark Attachment Version Final: updated %d rows", updated)
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

// FailCommandEvidence installs a test-only trigger that makes every durable
// command transaction fail when it reaches its Command Receipt.
func FailCommandEvidence(ctx context.Context, path string) error {
	return mutateSchema(path, func(database *sql.DB) error {
		const statement = `CREATE TRIGGER fail_command_evidence
BEFORE INSERT ON command_receipts
BEGIN
	SELECT RAISE(FAIL, 'forced Command Receipt failure');
END`
		if _, err := database.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("install Command Receipt failure trigger: %w", err)
		}
		return nil
	})
}

// AllowCommandEvidence removes the test-only Command Receipt failure trigger.
func AllowCommandEvidence(ctx context.Context, path string) error {
	return mutateSchema(path, func(database *sql.DB) error {
		if _, err := database.ExecContext(ctx, "DROP TRIGGER fail_command_evidence"); err != nil {
			return fmt.Errorf("remove Command Receipt failure trigger: %w", err)
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
