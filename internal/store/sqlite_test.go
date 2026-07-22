package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCommittedMigrationsReplayIntoCleanSQLite(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize clean database: %v", err)
	}

	installation, err := Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open migrated database: %v", err)
	}
	defer func() {
		if closeErr := installation.Close(); closeErr != nil {
			t.Errorf("close migrated database: %v", closeErr)
		}
	}()
	if err := installation.StartupError(); err != nil {
		t.Fatalf("validate migrated database: %v", err)
	}
	if err := installation.Ready(t.Context()); err != nil {
		t.Fatalf("query migrated database through Ent: %v", err)
	}
}

func TestAuthenticationCredentialsExpire(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize authentication database: %v", err)
	}
	installation, err := Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open authentication database: %v", err)
	}
	defer func() {
		if closeErr := installation.Close(); closeErr != nil {
			t.Errorf("close authentication database: %v", closeErr)
		}
	}()

	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	expiredBootstrapHash := strings.Repeat("a", 64)
	issueErr := installation.IssueBootstrap(
		t.Context(),
		expiredBootstrapHash,
		now,
		now.Add(time.Minute),
	)
	if issueErr != nil {
		t.Fatalf("issue expiring bootstrap credential: %v", issueErr)
	}
	_, err = installation.BootstrapAdministrator(
		t.Context(),
		BootstrapAdministratorParams{
			BootstrapHash:  expiredBootstrapHash,
			Name:           "Ada Admin",
			NormalizedName: "ada admin",
			PasswordHash:   "password hash",
			SessionHash:    strings.Repeat("b", 64),
			Now:            now.Add(2 * time.Minute),
			SessionExpiry:  now.Add(time.Hour),
		},
	)
	if !errors.Is(err, ErrInvalidBootstrap) {
		t.Fatalf("expired bootstrap error = %v, want %v", err, ErrInvalidBootstrap)
	}

	validBootstrapHash := strings.Repeat("c", 64)
	bootstrapTime := now.Add(2 * time.Minute)
	issueErr = installation.IssueBootstrap(
		t.Context(),
		validBootstrapHash,
		bootstrapTime,
		bootstrapTime.Add(time.Minute),
	)
	if issueErr != nil {
		t.Fatalf("replace expired bootstrap credential: %v", issueErr)
	}
	sessionHash := strings.Repeat("d", 64)
	created, err := installation.BootstrapAdministrator(
		t.Context(),
		BootstrapAdministratorParams{
			BootstrapHash:  validBootstrapHash,
			Name:           "Ada Admin",
			NormalizedName: "ada admin",
			PasswordHash:   "password hash",
			SessionHash:    sessionHash,
			Now:            bootstrapTime,
			SessionExpiry:  bootstrapTime.Add(time.Minute),
		},
	)
	if err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	if created.Name != "Ada Admin" || !created.Administrator {
		t.Errorf("created Account = %+v, want Ada Admin Administrator", created)
	}
	_, err = installation.FindAccountSession(
		t.Context(),
		sessionHash,
		bootstrapTime.Add(2*time.Minute),
	)
	if !errors.Is(err, ErrInvalidSession) {
		t.Errorf("expired session error = %v, want %v", err, ErrInvalidSession)
	}
}
