package operations

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/dotwaffle/beamers/ent/runtime"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/backup"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/themes"
	"github.com/dotwaffle/beamers/internal/themevalue"
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

func TestThemeActivationUsesInstallationDisplayNotification(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	dataDir := t.TempDir()
	if err := Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	notifications := 0
	installation, err := OpenInstallationWithConfig(t.Context(), OpenConfig{
		DataDir: dataDir,
		Now:     func() time.Time { return now },
		NotifyDisplays: func() {
			notifications++
		},
	})
	if err != nil {
		t.Fatalf("open installation: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := installation.Close(); closeErr != nil {
			t.Errorf("close installation: %v", closeErr)
		}
	})

	bootstrapHash := strings.Repeat("b", 64)
	if err = installation.storage.IssueBootstrap(
		t.Context(),
		bootstrapHash,
		now,
		now.Add(time.Hour),
	); err != nil {
		t.Fatalf("issue bootstrap: %v", err)
	}
	account, err := installation.storage.BootstrapAdministrator(
		t.Context(),
		store.BootstrapAdministratorParams{
			BootstrapHash: bootstrapHash, Name: "Administrator",
			NormalizedName: "administrator", PasswordHash: "test-password-hash",
			SessionHash: strings.Repeat("s", 64), Now: now, SessionExpiry: now.Add(time.Hour),
		},
	)
	if err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	administrator := auth.Account{ID: account.ID, Name: account.Name, Administrator: true}
	draft, err := installation.Themes().CreateDraft(
		t.Context(),
		administrator,
		themes.CreateDraftInput{
			Config: themevalue.DefaultConfig(), CommandID: "create-installation-theme",
		},
	)
	if err != nil {
		t.Fatalf("create Theme Draft: %v", err)
	}
	if _, err = installation.Themes().Activate(
		t.Context(),
		administrator,
		themes.ActivateInput{
			RevisionID: draft.ID, CommandID: "activate-installation-theme",
		},
	); err != nil {
		t.Fatalf("activate Theme Revision: %v", err)
	}
	if notifications != 1 {
		t.Errorf("Display notifications = %d, want 1", notifications)
	}
}
