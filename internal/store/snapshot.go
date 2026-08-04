package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
)

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

// Snapshot writes one consistent compact database copy without replacing an
// existing destination.
func (installation *SQLite) Snapshot(ctx context.Context, destination string) error {
	if err := requireActor(ctx, "SQLite.Snapshot"); err != nil {
		return err
	}

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
