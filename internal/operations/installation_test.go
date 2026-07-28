package operations

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/dotwaffle/beamers/ent/runtime"
	"github.com/dotwaffle/beamers/internal/backup"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/store/storetest"
)

func TestOpenInstallationRollsBackInterruptedRestoreBeforeReadiness(t *testing.T) {
	ctx := t.Context()
	sourceDataDir := filepath.Join(t.TempDir(), "source")
	if err := Initialize(ctx, sourceDataDir); err != nil {
		t.Fatalf("initialize source: %v", err)
	}
	sourceConfiguration, configurationErr := backup.LoadConfiguration(sourceDataDir, "")
	if configurationErr != nil {
		t.Fatalf("load source configuration: %v", configurationErr)
	}
	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	if _, err := backup.Create(ctx, backup.CreateInput{
		DataDir: sourceDataDir, OutputPath: archivePath,
		Configuration: sourceConfiguration,
	}); err != nil {
		t.Fatalf("create Backup: %v", err)
	}

	targetDataDir := filepath.Join(t.TempDir(), "target")
	if err := Initialize(ctx, targetDataDir); err != nil {
		t.Fatalf("initialize target: %v", err)
	}
	targetConfiguration, configurationErr := backup.LoadConfiguration(targetDataDir, "")
	if configurationErr != nil {
		t.Fatalf("load target configuration: %v", configurationErr)
	}
	oldMarker := filepath.Join(targetDataDir, "old-generation")
	if err := os.WriteFile(oldMarker, []byte("old"), 0o600); err != nil {
		t.Fatalf("write old-generation marker: %v", err)
	}
	plan, err := backup.PrepareRestore(ctx, backup.RestoreInput{
		InputPath:     archivePath,
		DataDir:       targetDataDir,
		Replace:       true,
		Configuration: targetConfiguration,
	})
	if err != nil {
		t.Fatalf("prepare Restore: %v", err)
	}
	if err = os.Rename(targetDataDir, plan.DataQuarantine); err != nil {
		t.Fatalf("simulate interrupted quarantine: %v", err)
	}

	installation, err := OpenInstallation(ctx, targetDataDir)
	if err != nil {
		t.Fatalf("open after interrupted Restore: %v", err)
	}
	if err = errors.Join(installation.Ready(ctx), installation.Close()); err != nil {
		t.Fatalf("recovered installation readiness: %v", err)
	}
	if content, readErr := os.ReadFile(oldMarker); readErr != nil || string(content) != "old" {
		t.Fatalf("recovered old generation = %q, %v", content, readErr)
	}
	if _, err = os.Stat(plan.JournalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered Restore journal remains: %v", err)
	}
}

func TestApprovedUpgradeInstallsValidatedStagedSchema(t *testing.T) {
	ctx := t.Context()
	dataDir := filepath.Join(t.TempDir(), "installation")
	if err := Initialize(ctx, dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	databasePath := filepath.Join(dataDir, "beamers.db")
	if err := storetest.DowngradeBeforeUpgradeContracts(ctx, databasePath); err != nil {
		t.Fatalf("prepare schema 47 fixture: %v", err)
	}

	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("read working directory: %v", err)
	}
	relativeDataDir, err := filepath.Rel(workingDirectory, dataDir)
	if err != nil {
		t.Fatalf("make relative data directory: %v", err)
	}
	upgrade, err := PrepareUpgrade(ctx, OpenConfig{DataDir: relativeDataDir})
	if err != nil {
		t.Fatalf("prepare upgrade: %v", err)
	}
	if !upgrade.Plan().RequiresApproval ||
		upgrade.Plan().Migration.FromVersion != 47 ||
		upgrade.Plan().Migration.ToVersion != 64 {
		t.Fatalf("upgrade plan = %+v", upgrade.Plan())
	}
	result, err := upgrade.Apply(ctx, UpgradeApproval{
		ApproveConsequences:        true,
		AcknowledgeNoDownMigration: true,
		HostAuthority:              true,
		PreviewDigest:              upgrade.Plan().PreviewDigest,
	})
	if err != nil {
		t.Fatalf("apply approved upgrade: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(filepath.Dir(result.BackupPath))
	})
	if result.BackupPath == "" || result.Manifest.Mode != backup.FullFidelity {
		t.Fatalf("upgrade result = %+v", result)
	}
	if !filepath.IsAbs(result.BackupPath) {
		t.Fatalf("upgrade Backup path is not durable: %q", result.BackupPath)
	}
	if _, err = backup.Verify(ctx, result.BackupPath); err != nil {
		t.Fatalf("verify rollback Backup: %v", err)
	}

	installation, err := OpenInstallation(ctx, dataDir)
	if err != nil {
		t.Fatalf("open upgraded installation: %v", err)
	}
	defer func() {
		if closeErr := installation.Close(); closeErr != nil {
			t.Errorf("close upgraded installation: %v", closeErr)
		}
	}()
	if err = installation.Ready(ctx); err != nil {
		t.Fatalf("upgraded installation readiness: %v", err)
	}
	current, err := store.CurrentSchemaVersion()
	if err != nil {
		t.Fatalf("read current schema: %v", err)
	}
	if result.ToVersion != current {
		t.Fatalf("upgraded schema = %d, want %d", result.ToVersion, current)
	}
	if _, err = os.Stat(dataDir + ".beamers-upgrade.json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed upgrade journal remains: %v", err)
	}
}

func TestFailedUpgradeRestoresOriginalAndPreservesRollbackBackup(t *testing.T) {
	ctx := t.Context()
	dataDir := filepath.Join(t.TempDir(), "installation")
	if err := Initialize(ctx, dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	databasePath := filepath.Join(dataDir, "beamers.db")
	if err := storetest.FailCommandEvidence(ctx, databasePath); err != nil {
		t.Fatalf("install post-upgrade validation failure: %v", err)
	}
	if err := storetest.DowngradeBeforeUpgradeContracts(ctx, databasePath); err != nil {
		t.Fatalf("prepare schema 47 fixture: %v", err)
	}

	upgrade, err := PrepareUpgrade(ctx, OpenConfig{DataDir: dataDir})
	if err != nil {
		t.Fatalf("prepare upgrade: %v", err)
	}
	result, err := upgrade.Apply(ctx, UpgradeApproval{
		ApproveConsequences:        true,
		AcknowledgeNoDownMigration: true,
		HostAuthority:              true,
		PreviewDigest:              upgrade.Plan().PreviewDigest,
	})
	if err == nil {
		t.Fatal("upgrade with failed command evidence succeeded")
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(filepath.Dir(result.BackupPath))
	})
	if _, verifyErr := backup.Verify(ctx, result.BackupPath); verifyErr != nil {
		t.Fatalf("verify preserved rollback Backup: %v", verifyErr)
	}
	if _, statErr := os.Stat(dataDir + ".beamers-upgrade.json"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed upgrade journal remains: %v", statErr)
	}

	retry, prepareErr := PrepareUpgrade(ctx, OpenConfig{DataDir: dataDir})
	if prepareErr != nil {
		t.Fatalf("original schema was not restored: %v", prepareErr)
	}
	defer func() {
		if closeErr := retry.Close(); closeErr != nil {
			t.Errorf("close retry upgrade: %v", closeErr)
		}
	}()
	if retry.Plan().Migration.FromVersion != 47 {
		t.Fatalf("restored schema = %d, want 47", retry.Plan().Migration.FromVersion)
	}
}

func TestLiveUpgradeRequiresExplicitForceAndReason(t *testing.T) {
	ctx := t.Context()
	dataDir := filepath.Join(t.TempDir(), "installation")
	if err := Initialize(ctx, dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	databasePath := filepath.Join(dataDir, "beamers.db")
	if err := storetest.AddLiveSession(ctx, databasePath); err != nil {
		t.Fatalf("add live state: %v", err)
	}
	if err := storetest.DowngradeBeforeUpgradeContracts(ctx, databasePath); err != nil {
		t.Fatalf("prepare schema 47 fixture: %v", err)
	}

	upgrade, err := PrepareUpgrade(ctx, OpenConfig{DataDir: dataDir})
	if err != nil {
		t.Fatalf("prepare upgrade: %v", err)
	}
	if _, err = upgrade.Apply(ctx, UpgradeApproval{
		ApproveConsequences:        true,
		AcknowledgeNoDownMigration: true,
		ForceLive:                  true,
		Reason:                     "urgent repair",
		HostAuthority:              true,
		PreviewDigest:              "different-preview",
	}); err == nil || !strings.Contains(err.Error(), "exact preview") {
		t.Fatalf("mismatched preview error = %v", err)
	}
	upgrade, err = PrepareUpgrade(ctx, OpenConfig{DataDir: dataDir})
	if err != nil {
		t.Fatalf("prepare upgrade after rejected preview: %v", err)
	}
	if _, err = upgrade.Apply(ctx, UpgradeApproval{
		ApproveConsequences:        true,
		AcknowledgeNoDownMigration: true,
		HostAuthority:              true,
		PreviewDigest:              upgrade.Plan().PreviewDigest,
	}); err == nil || !strings.Contains(err.Error(), "force-live") {
		t.Fatalf("unforced live upgrade error = %v", err)
	}
}

func TestForceLiveIsRejectedWithoutLiveState(t *testing.T) {
	plan := UpgradePlan{
		RequiresApproval: true,
		PreviewDigest:    "exact-preview",
	}
	err := validateUpgradeApproval(plan, UpgradeApproval{
		ApproveConsequences:        true,
		AcknowledgeNoDownMigration: true,
		ForceLive:                  true,
		Reason:                     "unnecessary force",
		HostAuthority:              true,
		PreviewDigest:              plan.PreviewDigest,
	})
	if err == nil || !strings.Contains(err.Error(), "without live blockers") {
		t.Fatalf("force-live without blockers error = %v", err)
	}
}
