package operations

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/backup"
	"github.com/dotwaffle/beamers/internal/store"
)

const upgradeJournalVersion = 1

type upgradePhase string

const (
	upgradePrepared            upgradePhase = "prepared"
	upgradeOriginalQuarantined upgradePhase = "original_quarantined"
	upgradeInstalled           upgradePhase = "installed"
	upgradeCommitted           upgradePhase = "committed"
)

// UpgradePlan describes one exact guarded schema transition.
type UpgradePlan struct {
	Migration            store.MigrationPlan `json:"migration"`
	LiveBlockers         []string            `json:"live_blockers,omitempty"`
	RollbackIncompatible bool                `json:"rollback_incompatible"`
	RequiresApproval     bool                `json:"requires_approval"`
	Consequences         []string            `json:"consequences"`
	PreviewDigest        string              `json:"preview_digest"`
}

// UpgradeApproval carries explicit unsafe or force-live safeguards.
type UpgradeApproval struct {
	ApproveConsequences        bool
	AcknowledgeNoDownMigration bool
	ForceLive                  bool
	Reason                     string
	ActorAccountID             int
	HostAuthority              bool
	PreviewDigest              string
}

// UpgradeResult identifies the preserved rollback material and installed schema.
type UpgradeResult struct {
	FromVersion int             `json:"from_version"`
	ToVersion   int             `json:"to_version"`
	BackupPath  string          `json:"backup_path"`
	Manifest    backup.Manifest `json:"manifest"`
}

// Upgrade owns exclusive access to one migration source.
type Upgrade struct {
	config         OpenConfig
	plan           UpgradePlan
	source         *store.SQLite
	authentication *auth.Service
	releaseAccess  func() error
}

// Plan returns the exact immutable preview held by this coordinator.
func (upgrade *Upgrade) Plan() UpgradePlan {
	if upgrade == nil {
		return UpgradePlan{}
	}
	found := upgrade.plan
	found.Migration.Migrations = slices.Clone(found.Migration.Migrations)
	found.LiveBlockers = slices.Clone(found.LiveBlockers)
	found.Consequences = slices.Clone(found.Consequences)
	return found
}

// PrepareUpgrade validates and exclusively opens one known older installation.
func PrepareUpgrade(
	ctx context.Context,
	config OpenConfig,
) (_ *Upgrade, returnErr error) {
	if config.DataDir == "" {
		return nil, errors.New("upgrade data directory is required")
	}
	absoluteDataDir, err := filepath.Abs(config.DataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve upgrade data directory: %w", err)
	}
	config.DataDir = absoluteDataDir
	if config.AttachmentsDir == "" {
		config.AttachmentsDir = filepath.Join(config.DataDir, "attachments")
	} else {
		absoluteAttachments, pathErr := filepath.Abs(config.AttachmentsDir)
		if pathErr != nil {
			return nil, fmt.Errorf("resolve upgrade Attachment Store: %w", pathErr)
		}
		config.AttachmentsDir = absoluteAttachments
	}
	if recoverErr := RecoverUpgrade(config.DataDir); recoverErr != nil {
		return nil, fmt.Errorf("recover interrupted upgrade: %w", recoverErr)
	}
	releaseAccess, err := store.HoldExclusiveAccess(configuredAccessRoots(config)...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, releaseAccess())
		}
	}()
	source, migration, err := store.OpenMigrationSource(ctx, config.DataDir)
	if err != nil {
		return nil, err
	}
	blockers, err := source.UpgradeBlockers(ctx, time.Now())
	if err != nil {
		return nil, errors.Join(err, source.Close())
	}
	authentication, err := auth.New(source, auth.DefaultConfig())
	if err != nil {
		return nil, errors.Join(err, source.Close())
	}
	rollbackIncompatible := migration.MinimumReaderSchemaVersion > migration.FromVersion ||
		migration.MinimumWriterSchemaVersion > migration.FromVersion
	requiresApproval := migration.Safety != store.MigrationNonDestructive ||
		rollbackIncompatible ||
		len(blockers) != 0
	backupConsequence := "creates and preserves a Sanitized Backup"
	if requiresApproval {
		backupConsequence = "creates and preserves a Full-Fidelity Backup containing credentials"
	}
	consequences := []string{backupConsequence, "version one provides no down migrations"}
	for _, step := range migration.Migrations {
		consequences = append(consequences, step.Consequence)
	}
	plan := UpgradePlan{
		Migration: migration, LiveBlockers: blockers,
		RollbackIncompatible: rollbackIncompatible,
		RequiresApproval:     requiresApproval,
		Consequences:         consequences,
	}
	plan.PreviewDigest, err = upgradePlanDigest(plan)
	if err != nil {
		return nil, errors.Join(err, source.Close())
	}
	return &Upgrade{
		config:         config,
		plan:           plan,
		source:         source,
		authentication: authentication,
		releaseAccess:  releaseAccess,
	}, nil
}

// Authentication provides the constrained old-schema Administrator boundary.
func (upgrade *Upgrade) Authentication() *auth.Service {
	if upgrade == nil {
		return nil
	}
	return upgrade.authentication
}

// Apply creates rollback material, migrates staged state, and atomically
// installs it after all required approvals.
func (upgrade *Upgrade) Apply(
	ctx context.Context,
	approval UpgradeApproval,
) (result UpgradeResult, returnErr error) {
	if upgrade == nil || upgrade.source == nil {
		return UpgradeResult{}, errors.New("prepared upgrade is required")
	}
	defer func() {
		returnErr = errors.Join(returnErr, upgrade.Close())
	}()
	approval.Reason = strings.TrimSpace(approval.Reason)
	if err := validateUpgradeApproval(upgrade.plan, approval); err != nil {
		return UpgradeResult{}, err
	}

	backupRoot, err := os.MkdirTemp(
		filepath.Dir(upgrade.config.DataDir),
		"."+filepath.Base(upgrade.config.DataDir)+".beamers-upgrade-backup-*",
	)
	if err != nil {
		return UpgradeResult{}, fmt.Errorf("reserve upgrade Backup location: %w", err)
	}
	backupPath := filepath.Join(backupRoot, "backup.zip")
	backupMode := backup.Sanitized
	if upgrade.plan.RequiresApproval {
		backupMode = backup.FullFidelity
	}
	manifest, err := backup.CreateWithStorage(ctx, upgrade.source, backup.CreateInput{
		DataDir: upgrade.config.DataDir, AttachmentsDir: upgrade.config.AttachmentsDir,
		OutputPath: backupPath, Mode: backupMode,
	})
	if err != nil {
		return UpgradeResult{}, errors.Join(err, os.RemoveAll(backupRoot))
	}
	result = UpgradeResult{
		FromVersion: upgrade.plan.Migration.FromVersion,
		ToVersion:   upgrade.plan.Migration.ToVersion,
		BackupPath:  backupPath,
		Manifest:    manifest,
	}
	if verified, verifyErr := backup.Verify(backupPath); verifyErr != nil ||
		!reflect.DeepEqual(verified, manifest) {
		return result, errors.Join(
			verifyErr,
			errors.New("upgrade Backup verification changed after creation"),
		)
	}
	if upgrade.plan.RequiresApproval {
		if auditErr := upgrade.source.RecordUpgradeAudit(ctx, store.UpgradeAudit{
			ActorAccountID: approval.ActorAccountID,
			HostAuthority:  approval.HostAuthority,
			FromVersion:    result.FromVersion,
			ToVersion:      result.ToVersion,
			BackupPath:     backupPath,
			Reason:         approval.Reason,
			ForcedLive:     approval.ForceLive,
			Now:            time.Now(),
		}); auditErr != nil {
			return result, auditErr
		}
	}

	stagingRoot, err := os.MkdirTemp(
		filepath.Dir(upgrade.config.DataDir),
		"."+filepath.Base(upgrade.config.DataDir)+".beamers-upgrade-*",
	)
	if err != nil {
		return result, fmt.Errorf("create upgrade staging directory: %w", err)
	}
	stagedDatabase := filepath.Join(stagingRoot, "beamers.db")
	if err = upgrade.source.Snapshot(ctx, stagedDatabase); err != nil {
		return result, errors.Join(err, os.RemoveAll(stagingRoot))
	}
	if err = store.MigrateSnapshot(ctx, stagedDatabase, upgrade.plan.Migration); err != nil {
		return result, errors.Join(err, os.RemoveAll(stagingRoot))
	}
	quarantineRoot, err := os.MkdirTemp(
		filepath.Dir(upgrade.config.DataDir),
		"."+filepath.Base(upgrade.config.DataDir)+".beamers-upgrade-quarantine-*",
	)
	if err != nil {
		return result, errors.Join(
			fmt.Errorf("reserve upgrade quarantine: %w", err),
			os.RemoveAll(stagingRoot),
		)
	}
	journal := upgradeJournal{
		Version:          upgradeJournalVersion,
		Phase:            upgradePrepared,
		DataDir:          upgrade.config.DataDir,
		AttachmentsDir:   upgrade.config.AttachmentsDir,
		Plan:             upgrade.plan,
		BackupPath:       backupPath,
		StagingRoot:      stagingRoot,
		StagedDatabase:   stagedDatabase,
		QuarantineRoot:   quarantineRoot,
		OriginalDatabase: filepath.Join(quarantineRoot, "original.db"),
		FailedDatabase:   filepath.Join(quarantineRoot, "failed.db"),
	}
	journalPath := upgradeJournalPath(upgrade.config.DataDir)
	if err = createUpgradeJournal(journalPath, journal); err != nil {
		return result, errors.Join(
			err,
			os.RemoveAll(stagingRoot),
			os.RemoveAll(quarantineRoot),
		)
	}
	if err = upgrade.source.Close(); err != nil {
		upgrade.source = nil
		return result, err
	}
	upgrade.source = nil

	if err = renameUpgradePath(
		filepath.Join(upgrade.config.DataDir, "beamers.db"),
		journal.OriginalDatabase,
	); err != nil {
		return result, rollbackUpgrade(journalPath, journal, err)
	}
	if err = advanceUpgradeJournal(journalPath, &journal, upgradeOriginalQuarantined); err != nil {
		return result, rollbackUpgrade(journalPath, journal, err)
	}
	if err = renameUpgradePath(journal.StagedDatabase, filepath.Join(
		upgrade.config.DataDir,
		"beamers.db",
	)); err != nil {
		return result, rollbackUpgrade(journalPath, journal, err)
	}
	if err = advanceUpgradeJournal(journalPath, &journal, upgradeInstalled); err != nil {
		return result, rollbackUpgrade(journalPath, journal, err)
	}
	if err = validateInstalledUpgrade(ctx, upgrade.config, backupPath); err != nil {
		return result, rollbackUpgrade(journalPath, journal, err)
	}
	if err = advanceUpgradeJournal(journalPath, &journal, upgradeCommitted); err != nil {
		return result, rollbackUpgrade(journalPath, journal, err)
	}
	if cleanupErr := cleanupCommittedUpgrade(journalPath, journal); cleanupErr != nil {
		return result, cleanupErr
	}
	return result, nil
}

func validateUpgradeApproval(plan UpgradePlan, approval UpgradeApproval) error {
	if len(plan.LiveBlockers) == 0 && approval.ForceLive {
		return errors.New("force-live is not permitted without live blockers")
	}
	if !plan.RequiresApproval {
		return nil
	}
	if !approval.ApproveConsequences || !approval.AcknowledgeNoDownMigration {
		return errors.New(
			"upgrade requires approval of its consequences and acknowledgment that no down migration exists",
		)
	}
	if approval.PreviewDigest != plan.PreviewDigest {
		return errors.New("upgrade approval does not match the exact preview")
	}
	if len(plan.LiveBlockers) != 0 &&
		(!approval.ForceLive || strings.TrimSpace(approval.Reason) == "") {
		return errors.New("force-live upgrade requires a mandatory reason")
	}
	if !approval.HostAuthority && approval.ActorAccountID <= 0 {
		return errors.New("upgrade approval requires an Administrator or host authority")
	}
	return nil
}

func upgradePlanDigest(plan UpgradePlan) (string, error) {
	plan.PreviewDigest = ""
	encoded, err := json.Marshal(plan)
	if err != nil {
		return "", fmt.Errorf("encode upgrade preview: %w", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded)), nil
}

func validateInstalledUpgrade(
	ctx context.Context,
	config OpenConfig,
	backupPath string,
) (returnErr error) {
	if _, err := backup.Verify(backupPath); err != nil {
		return err
	}
	installation, err := store.Open(ctx, config.DataDir)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, installation.Close())
	}()
	if startupErr := installation.StartupError(); startupErr != nil {
		return startupErr
	}
	if readyErr := installation.Ready(ctx); readyErr != nil {
		return readyErr
	}
	if evidenceErr := installation.ProbeCommandEvidence(ctx, time.Now()); evidenceErr != nil {
		return evidenceErr
	}
	return nil
}

// Close releases a prepared upgrade without changing storage.
func (upgrade *Upgrade) Close() error {
	if upgrade == nil {
		return nil
	}
	var sourceErr error
	if upgrade.source != nil {
		sourceErr = upgrade.source.Close()
		upgrade.source = nil
	}
	upgrade.authentication = nil
	releaseErr := closeAccess(upgrade.releaseAccess)
	upgrade.releaseAccess = nil
	return errors.Join(sourceErr, releaseErr)
}

type upgradeJournal struct {
	Version          int          `json:"version"`
	Phase            upgradePhase `json:"phase"`
	DataDir          string       `json:"data_dir"`
	AttachmentsDir   string       `json:"attachments_dir"`
	Plan             UpgradePlan  `json:"plan"`
	BackupPath       string       `json:"backup_path"`
	StagingRoot      string       `json:"staging_root"`
	StagedDatabase   string       `json:"staged_database"`
	QuarantineRoot   string       `json:"quarantine_root"`
	OriginalDatabase string       `json:"original_database"`
	FailedDatabase   string       `json:"failed_database"`
}

func upgradeJournalPath(dataDir string) string {
	return dataDir + ".beamers-upgrade.json"
}

func createUpgradeJournal(path string, journal upgradeJournal) error {
	if _, err := os.Lstat(path); err == nil {
		return errors.New("upgrade journal already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect upgrade journal: %w", err)
	}
	return writeUpgradeJournal(path, journal, true)
}

func advanceUpgradeJournal(
	path string,
	journal *upgradeJournal,
	phase upgradePhase,
) error {
	journal.Phase = phase
	return writeUpgradeJournal(path, *journal, false)
}

func writeUpgradeJournal(path string, journal upgradeJournal, exclusive bool) (returnErr error) {
	parent := filepath.Dir(path)
	temporary, err := os.CreateTemp(parent, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create upgrade journal: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		if removeErr := os.Remove(temporaryPath); removeErr != nil &&
			!errors.Is(removeErr, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, removeErr)
		}
	}()
	if err = temporary.Chmod(0o600); err != nil {
		return errors.Join(err, temporary.Close())
	}
	if err = json.NewEncoder(temporary).Encode(journal); err != nil {
		return errors.Join(fmt.Errorf("encode upgrade journal: %w", err), temporary.Close())
	}
	if err = temporary.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync upgrade journal: %w", err), temporary.Close())
	}
	if err = temporary.Close(); err != nil {
		return fmt.Errorf("close upgrade journal: %w", err)
	}
	if exclusive {
		if err = os.Link(temporaryPath, path); err != nil {
			return fmt.Errorf("install upgrade journal: %w", err)
		}
	} else if err = os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace upgrade journal: %w", err)
	}
	return syncUpgradeDirectory(parent)
}

func readUpgradeJournal(path string) (upgradeJournal, error) {
	input, err := os.Open(path) //nolint:gosec // Fixed adjacent recovery journal.
	if err != nil {
		return upgradeJournal{}, err
	}
	defer func() {
		_ = input.Close()
	}()
	var journal upgradeJournal
	decoder := json.NewDecoder(input)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&journal); err != nil {
		return upgradeJournal{}, errors.New("decode upgrade journal")
	}
	if journal.Version != upgradeJournalVersion ||
		journal.DataDir == "" ||
		journal.BackupPath == "" ||
		journal.StagedDatabase == "" ||
		journal.OriginalDatabase == "" {
		return upgradeJournal{}, errors.New("upgrade journal is invalid")
	}
	return journal, nil
}

// RecoverUpgrade rolls back an interrupted database cutover before startup.
func RecoverUpgrade(dataDir string) (returnErr error) {
	absoluteDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("resolve upgrade recovery path: %w", err)
	}
	journalPath := upgradeJournalPath(absoluteDataDir)
	journal, err := readUpgradeJournal(journalPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if journal.DataDir != absoluteDataDir {
		return errors.New("upgrade journal does not match its installation")
	}
	roots := []string{journal.DataDir}
	if store.UsesExternalAttachments(journal.DataDir, journal.AttachmentsDir) {
		roots = append(roots, journal.AttachmentsDir)
	}
	releaseAccess, err := store.HoldExclusiveAccess(roots...)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, releaseAccess())
	}()
	if journal.Phase == upgradeCommitted {
		return cleanupCommittedUpgrade(journalPath, journal)
	}
	return rollbackUpgrade(journalPath, journal, nil)
}

func rollbackUpgrade(path string, journal upgradeJournal, cause error) error {
	databasePath := filepath.Join(journal.DataDir, "beamers.db")
	originalExists, err := upgradePathExists(journal.OriginalDatabase)
	if err != nil {
		return errors.Join(cause, err)
	}
	databaseExists, err := upgradePathExists(databasePath)
	if err != nil {
		return errors.Join(cause, err)
	}
	if originalExists {
		if databaseExists {
			if err = renameUpgradePath(databasePath, journal.FailedDatabase); err != nil {
				return errors.Join(cause, err)
			}
		}
		if err = renameUpgradePath(journal.OriginalDatabase, databasePath); err != nil {
			return errors.Join(cause, err)
		}
	}
	cleanupErr := errors.Join(os.RemoveAll(journal.StagingRoot), os.Remove(path))
	if syncErr := syncUpgradeDirectory(filepath.Dir(path)); syncErr != nil {
		cleanupErr = errors.Join(cleanupErr, syncErr)
	}
	return errors.Join(cause, cleanupErr)
}

func cleanupCommittedUpgrade(path string, journal upgradeJournal) error {
	err := errors.Join(
		os.RemoveAll(journal.StagingRoot),
		os.RemoveAll(journal.QuarantineRoot),
		os.Remove(path),
	)
	return errors.Join(err, syncUpgradeDirectory(filepath.Dir(path)))
}

func renameUpgradePath(source, destination string) error {
	if err := os.Rename(source, destination); err != nil {
		return fmt.Errorf("rename upgrade state: %w", err)
	}
	return errors.Join(
		syncUpgradeDirectory(filepath.Dir(source)),
		syncUpgradeDirectory(filepath.Dir(destination)),
	)
}

func upgradePathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func syncUpgradeDirectory(path string) error {
	directory, err := os.Open(path) //nolint:gosec // Host-configured local state parent.
	if err != nil {
		return fmt.Errorf("open upgrade directory for sync: %w", err)
	}
	return errors.Join(directory.Sync(), directory.Close())
}
