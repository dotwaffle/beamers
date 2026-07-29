package themes_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/dotwaffle/beamers/ent/runtime"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/themes"
	"github.com/dotwaffle/beamers/internal/themevalue"
)

func TestAdministratorPreviewsActivatesAndRollsBackImmutableThemeRevisions(t *testing.T) {
	storage, administrator := openThemeTest(t)
	now := time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC)
	notifications := 0
	service, err := themes.New(storage, func() time.Time { return now }, func() {
		notifications++
	})
	if err != nil {
		t.Fatalf("create Theme service: %v", err)
	}

	initial, err := service.Active(t.Context())
	if err != nil || initial.ID != 0 || initial.Config != themevalue.DefaultConfig() {
		t.Fatalf("initial Theme = %+v, %v", initial, err)
	}

	firstConfig := themevalue.DefaultConfig()
	firstConfig.BackgroundColor = "#101828"
	first, err := service.CreateDraft(t.Context(), administrator, themes.CreateDraftInput{
		Config: firstConfig, CommandID: "create-first-theme",
	})
	if err != nil {
		t.Fatalf("create first Draft Theme Revision: %v", err)
	}
	if first.ID == 0 || first.Config != firstConfig || first.CreatedByAccountID != administrator.ID {
		t.Fatalf("first Draft Theme Revision = %+v", first)
	}
	if active, activeErr := service.Active(t.Context()); activeErr != nil || active.ID != 0 {
		t.Fatalf("active Theme after preview-only edit = %+v, %v", active, activeErr)
	}
	preview, err := service.Preview(t.Context(), administrator, first.ID)
	if err != nil || len(preview.Findings) != 0 || preview.Revision != first {
		t.Fatalf("first Theme preview = %+v, %v", preview, err)
	}

	activated, err := service.Activate(t.Context(), administrator, themes.ActivateInput{
		RevisionID: first.ID, ExpectedActiveRevisionID: 0, CommandID: "activate-first-theme",
	})
	if err != nil || activated != first {
		t.Fatalf("activate first Theme Revision = %+v, %v", activated, err)
	}

	blockedConfig := firstConfig
	blockedConfig.TextColor = "#777777"
	blockedConfig.SurfaceColor = "#ffffff"
	blockedConfig.BackgroundColor = "#ffffff"
	blocked, err := service.CreateDraft(t.Context(), administrator, themes.CreateDraftInput{
		Config: blockedConfig, CommandID: "create-blocked-theme",
	})
	if err != nil {
		t.Fatalf("create inaccessible Draft Theme Revision: %v", err)
	}
	if _, err = service.Activate(t.Context(), administrator, themes.ActivateInput{
		RevisionID: blocked.ID, ExpectedActiveRevisionID: first.ID,
		CommandID: "activate-blocked-theme",
	}); !errors.Is(err, themes.ErrActivationBlocked) {
		t.Fatalf("inaccessible Theme activation error = %v, want %v", err, themes.ErrActivationBlocked)
	}
	if active, activeErr := service.Active(t.Context()); activeErr != nil || active.ID != first.ID {
		t.Fatalf("active Theme after blocked activation = %+v, %v", active, activeErr)
	}

	secondConfig := firstConfig
	secondConfig.AccentColor = "#7fffd4"
	second, err := service.CreateDraft(t.Context(), administrator, themes.CreateDraftInput{
		Config: secondConfig, CommandID: "create-second-theme",
	})
	if err != nil {
		t.Fatalf("create second Draft Theme Revision: %v", err)
	}
	if _, err = service.Activate(t.Context(), administrator, themes.ActivateInput{
		RevisionID: second.ID, ExpectedActiveRevisionID: first.ID,
		CommandID: "activate-second-theme",
	}); err != nil {
		t.Fatalf("activate second Theme Revision: %v", err)
	}
	rolledBack, err := service.Activate(t.Context(), administrator, themes.ActivateInput{
		RevisionID: first.ID, ExpectedActiveRevisionID: second.ID,
		CommandID: "roll-back-theme",
	})
	if err != nil || rolledBack != first {
		t.Fatalf("roll back exact Theme Revision = %+v, %v; want %+v", rolledBack, err, first)
	}

	if _, err = service.Activate(t.Context(), administrator, themes.ActivateInput{
		RevisionID: second.ID, ExpectedActiveRevisionID: 0,
		CommandID: "stale-theme-activation",
	}); !errors.Is(err, themes.ErrStaleActiveRevision) {
		t.Fatalf("stale Theme activation error = %v, want %v", err, themes.ErrStaleActiveRevision)
	}
	replayed, err := service.Activate(t.Context(), auth.Account{ID: administrator.ID}, themes.ActivateInput{
		RevisionID: first.ID, ExpectedActiveRevisionID: second.ID,
		CommandID: "roll-back-theme",
	})
	if err != nil || replayed != first {
		t.Fatalf("rollback replay = %+v, %v; want %+v", replayed, err, first)
	}
	if notifications != 4 {
		t.Errorf("Theme notifications = %d, want 4 successful commits and replays", notifications)
	}
}

func openThemeTest(t *testing.T) (*store.SQLite, auth.Account) {
	t.Helper()

	dataDir := t.TempDir()
	if err := store.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize storage: %v", err)
	}
	storage, err := store.Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := storage.Close(); closeErr != nil {
			t.Errorf("close storage: %v", closeErr)
		}
	})
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	bootstrapHash := strings.Repeat("b", 64)
	if err = storage.IssueBootstrap(t.Context(), bootstrapHash, now, now.Add(time.Hour)); err != nil {
		t.Fatalf("issue bootstrap: %v", err)
	}
	created, err := storage.BootstrapAdministrator(t.Context(), store.BootstrapAdministratorParams{
		BootstrapHash:  bootstrapHash,
		Name:           "Administrator",
		NormalizedName: "administrator",
		PasswordHash:   "test-password-hash",
		SessionHash:    strings.Repeat("s", 64),
		Now:            now,
		SessionExpiry:  now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	return storage, auth.Account{ID: created.ID, Name: created.Name, Administrator: true}
}
