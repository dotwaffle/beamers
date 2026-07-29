package voting

import (
	"bytes"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/dotwaffle/beamers/ent/runtime"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestPrintableVotingKeyNormalizesWithoutChangingDigest(t *testing.T) {
	token, err := newVotingToken(bytes.NewReader(make([]byte, votingTokenBytes)))
	if err != nil {
		t.Fatalf("create Voting Key: %v", err)
	}
	if !strings.Contains(token, "-") || len(token) != 39 {
		t.Fatalf("printable Voting Key = %q", token)
	}
	normalized, err := normalizeVotingToken(strings.ToLower(strings.ReplaceAll(token, "-", " ")))
	if err != nil {
		t.Fatalf("normalize printable Voting Key: %v", err)
	}
	if tokenDigest(normalized) != tokenDigest(strings.ReplaceAll(token, "-", "")) {
		t.Fatal("printable formatting changed Voting Key digest")
	}
}

func TestVotingKeyIssueRetryAndRevocation(t *testing.T) {
	storage, producer, attendee, eventID, databasePath := votingFixture(t)
	now := time.Date(2026, time.August, 21, 10, 0, 0, 0, time.UTC)
	service, err := New(storage, bytes.NewReader(make([]byte, votingTokenBytes)), func() time.Time {
		return now
	})
	if err != nil {
		t.Fatalf("create Voting service: %v", err)
	}
	input := IssueInput{
		EventID: eventID, Count: 1, ExpiresAt: now.Add(time.Hour),
		CommandID: "issue-voting-keys",
	}
	issued, err := service.Issue(t.Context(), producer, input)
	if err != nil {
		t.Fatalf("issue Voting Key: %v", err)
	}
	if len(issued) != 1 || issued[0].Token == "" {
		t.Fatalf("issued Voting Keys = %+v", issued)
	}
	if _, err = service.Issue(t.Context(), producer, input); !errors.Is(err, ErrKeysAlreadyIssued) {
		t.Fatalf("retry error = %v, want %v", err, ErrKeysAlreadyIssued)
	}
	if _, err = service.Issue(t.Context(), attendee, IssueInput{
		EventID: eventID, Count: 1, ExpiresAt: now.Add(time.Hour),
		CommandID: "unauthorized-voting-keys",
	}); !errors.Is(err, ErrProducerRequired) {
		t.Fatalf("unauthorized issue error = %v, want %v", err, ErrProducerRequired)
	}

	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open Voting Key database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	var digest string
	if err = database.QueryRowContext(
		t.Context(), "SELECT token_hash FROM voting_keys WHERE id = ?", issued[0].ID,
	).Scan(&digest); err != nil {
		t.Fatalf("read protected Voting Key: %v", err)
	}
	if digest == issued[0].Token || len(digest) != 64 {
		t.Fatalf("stored Voting Key digest = %q", digest)
	}

	if _, err = service.Revoke(
		t.Context(), producer, eventID, issued[0].ID, "revoke-voting-key",
	); err != nil {
		t.Fatalf("revoke Voting Key: %v", err)
	}
	if err = service.Redeem(
		t.Context(), attendee, eventID, issued[0].Token, "redeem-revoked-voting-key",
	); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("revoked redemption error = %v, want %v", err, ErrKeyUnavailable)
	}
}

func votingFixture(
	t *testing.T,
) (*store.SQLite, auth.Account, auth.Account, int, string) {
	t.Helper()
	dataDir := t.TempDir()
	if err := store.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize Voting store: %v", err)
	}
	storage, err := store.Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open Voting store: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	now := time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC)
	if err = storage.IssueBootstrap(
		t.Context(), strings.Repeat("b", 64), now, now.Add(time.Hour),
	); err != nil {
		t.Fatalf("issue Voting bootstrap: %v", err)
	}
	created, err := storage.BootstrapAdministrator(
		t.Context(),
		store.BootstrapAdministratorParams{
			BootstrapHash: strings.Repeat("b", 64),
			Name:          "Producer", NormalizedName: "producer", PasswordHash: "password-hash",
			SessionHash: strings.Repeat("s", 64), Now: now, SessionExpiry: now.Add(time.Hour),
		},
	)
	if err != nil {
		t.Fatalf("bootstrap Voting Producer: %v", err)
	}
	producer := auth.Account{ID: created.ID, Name: created.Name, Administrator: true}
	eventService, err := events.New(storage, func() time.Time { return now }, nil, nil)
	if err != nil {
		t.Fatalf("create Event service: %v", err)
	}
	event, err := eventService.Create(t.Context(), producer, events.CreateInput{
		Name: "Voting Event", PlannedStartDate: "2026-08-21", PlannedEndDate: "2026-08-21",
		Timezone: "UTC", EventLocale: "en-US", CommandID: "create-voting-event",
	})
	if err != nil {
		t.Fatalf("create Voting Event: %v", err)
	}
	if _, err = eventService.GrantEventAccess(
		t.Context(), producer, event.ID, producer.ID, "Producer", "grant-voting-producer",
	); err != nil {
		t.Fatalf("grant Voting Producer: %v", err)
	}
	producer.EventRoles = map[int]viewer.Role{event.ID: viewer.Producer}
	transaction, err := storage.BeginCommand(producer.Context(t.Context()))
	if err != nil {
		t.Fatalf("begin Voting attendee creation: %v", err)
	}
	account, err := transaction.CreateAccount(
		producer.Context(t.Context()),
		store.CreateAccountParams{
			ActorAccountID: producer.ID, Name: "Attendee", NormalizedName: "attendee",
			PasswordHash: "password-hash", Now: now,
		},
	)
	if err != nil {
		t.Fatalf("create Voting attendee: %v", err)
	}
	if err = transaction.Commit(); err != nil {
		t.Fatalf("commit Voting attendee: %v", err)
	}
	return storage, producer, auth.Account{ID: account.ID, Name: account.Name},
		event.ID, filepath.Join(dataDir, "beamers.db")
}
