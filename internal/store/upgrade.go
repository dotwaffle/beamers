package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/auditentry"
)

// OpenMigrationSource opens one attested older schema for bounded
// authentication, Backup, live-state inspection, and upgrade evidence only.
func OpenMigrationSource(
	ctx context.Context,
	dataDir string,
) (*SQLite, MigrationPlan, error) {
	if err := requireDataDirectory(dataDir); err != nil {
		return nil, MigrationPlan{}, err
	}
	databasePath := filepath.Join(dataDir, databaseFilename)
	if err := requireInstallationMarker(dataDir, databasePath); err != nil {
		return nil, MigrationPlan{}, err
	}
	lock, err := openInstallationLock(dataDir)
	if err != nil {
		return nil, MigrationPlan{}, err
	}
	plan, err := PlanMigrations(ctx, databasePath)
	if err != nil {
		return nil, MigrationPlan{}, errors.Join(err, lock.close())
	}
	if len(plan.Migrations) == 0 {
		return nil, MigrationPlan{}, errors.Join(
			ErrNoMigration,
			lock.close(),
		)
	}
	migrations, err := loadMigrations()
	if err != nil {
		return nil, MigrationPlan{}, errors.Join(err, lock.close())
	}
	database, err := openDatabase(ctx, databasePath)
	if err != nil {
		return nil, MigrationPlan{}, errors.Join(err, lock.close())
	}
	applied, err := validateSchemaPrefix(ctx, database, migrations)
	if err != nil || applied != plan.FromVersion {
		return nil, MigrationPlan{}, errors.Join(
			err,
			errors.New("migration source changed after planning"),
			database.Close(),
			lock.close(),
		)
	}
	driver := entsql.OpenDB(dialect.SQLite, database)
	return &SQLite{
		database: database,
		client:   ent.NewClient(ent.Driver(driver)),
		lock:     lock,
		// The source is current relative to the exact schema prefix it exposes.
		migrations: migrations[:applied],
		applied:    applied,
	}, plan, nil
}

// UpgradeBlockers reports persisted live operation that migration must not
// silently end or clear.
func (installation *SQLite) UpgradeBlockers(
	ctx context.Context,
	now time.Time,
) ([]string, error) {
	if err := requireActor(ctx, "SQLite.UpgradeBlockers"); err != nil {
		return []string(nil), err
	}

	if installation == nil || installation.database == nil {
		return nil, ErrUninitialized
	}
	checks := []struct {
		label string
		query string
		args  []any
	}{
		{
			label: "Live Session",
			query: "SELECT EXISTS(SELECT 1 FROM sessions WHERE lifecycle = 'Live')",
		},
		{
			label: "Competition presentation",
			query: "SELECT EXISTS(SELECT 1 FROM sessions WHERE program_output_kind " +
				"IN ('Upcoming', 'Starting', 'Entry', 'Ending'))",
		},
		{
			label: "Result Reveal",
			query: "SELECT EXISTS(SELECT 1 FROM sessions WHERE " +
				"json_extract(program_output_result, '$.status') = 'Revealing')",
		},
		{
			label: "Emergency Alert",
			query: "SELECT EXISTS(SELECT 1 FROM display_overrides " +
				"WHERE kind = 'EmergencyAlert' AND cleared_at IS NULL AND " +
				"(until_cleared = 1 OR expires_at > ?))",
			args: []any{now.UTC()},
		},
	}
	blockers := make([]string, 0, len(checks))
	for _, check := range checks {
		var found bool
		if err := installation.database.QueryRowContext(
			ctx,
			check.query,
			check.args...,
		).Scan(&found); err != nil {
			return nil, fmt.Errorf("inspect %s before migration: %w", check.label, err)
		}
		if found {
			blockers = append(blockers, check.label)
		}
	}
	return blockers, nil
}

// UpgradeAudit records explicit Account or host authority before storage closes.
type UpgradeAudit struct {
	ActorAccountID int
	HostAuthority  bool
	FromVersion    int
	ToVersion      int
	BackupPath     string
	Reason         string
	ForcedLive     bool
	Now            time.Time
}

// RecordUpgradeAudit appends the approval evidence required before migration.
func (installation *SQLite) RecordUpgradeAudit(
	ctx context.Context,
	evidence UpgradeAudit,
) error {
	if err := requireActor(ctx, "SQLite.RecordUpgradeAudit"); err != nil {
		return err
	}

	if installation == nil || installation.client == nil {
		return ErrUninitialized
	}
	if !evidence.HostAuthority && evidence.ActorAccountID <= 0 {
		return errors.New("upgrade Audit actor is required")
	}
	if evidence.Now.IsZero() {
		evidence.Now = time.Now().UTC()
	}
	action := "ApproveStorageUpgrade"
	if evidence.ForcedLive {
		action = "ForceLiveStorageUpgrade"
	}
	entry := installation.client.AuditEntry.Create().
		SetCreatedAt(evidence.Now.UTC()).
		SetAction(action).
		SetTargetType("Installation").
		SetTargetID("1").
		SetResult(auditentry.ResultSucceeded).
		SetReason(strings.TrimSpace(evidence.Reason)).
		SetNote(fmt.Sprintf(
			"schema %d to %d; verified Backup %s",
			evidence.FromVersion,
			evidence.ToVersion,
			filepath.Base(evidence.BackupPath),
		))
	if evidence.HostAuthority {
		entry.SetActorKind(auditentry.ActorKindHost)
	} else {
		entry.SetActorKind(auditentry.ActorKindAccount).
			SetActorAccountID(evidence.ActorAccountID)
	}
	if _, err := entry.Save(ctx); err != nil {
		return opaqueError("record storage upgrade Audit Entry", err)
	}
	return nil
}
