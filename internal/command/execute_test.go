package command

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	_ "github.com/dotwaffle/beamers/ent/runtime"

	"github.com/dotwaffle/beamers/internal/authz"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/systemactor"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestExecuteOwnsRetryConflictRejectionAndAuditOrdering(t *testing.T) {
	storage, actorID, ctx, _ := openExecutionTestStore(t)
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	identity := store.CommandIdentity{
		ActorAccountID: actorID, CommandID: "executor-success", PayloadHash: strings.Repeat("a", 64),
		Action: "UpdateAccountProfile", TargetType: "Account", TargetID: "1", Now: now,
	}
	applications := 0
	plan := Plan[string]{
		Storage: storage, Identity: identity,
		Authorization: Authorization{Facts: authz.Installation()},
		Replay:        func(outcome string) (string, error) { return outcome, nil },
		Apply: func(*store.CommandTx) (Execution[string], error) {
			applications++
			return Success("first", `"first"`), nil
		},
	}
	first, err := Execute(ctx, plan)
	if err != nil || first != "first" {
		t.Fatalf("first execution = %q, %v", first, err)
	}
	retried, err := Execute(ctx, plan)
	if err != nil || retried != `"first"` || applications != 1 {
		t.Fatalf("retry = %q, %v after %d applications", retried, err, applications)
	}

	conflict := plan
	conflict.Identity.PayloadHash = strings.Repeat("b", 64)
	if _, conflictErr := Execute(ctx, conflict); !errors.Is(conflictErr, store.ErrCommandConflict) {
		t.Fatalf("conflict error = %v, want %v", conflictErr, store.ErrCommandConflict)
	}

	rejectedReason := errors.New("not allowed")
	rejectedPlan := Plan[struct{}]{
		Storage:       storage,
		Authorization: Authorization{Facts: authz.Installation()},
		Identity: store.CommandIdentity{
			ActorAccountID: actorID, CommandID: "executor-rejected", PayloadHash: strings.Repeat("c", 64),
			Action: "RecoverAccount", TargetType: "Account", TargetID: "1", Now: now,
		},
		Replay: func(outcome string) (struct{}, error) {
			var result struct{}
			decodeErr := store.DecodeCommandReceipt(outcome, &result)
			return result, decodeErr
		},
		Apply: func(*store.CommandTx) (Execution[struct{}], error) {
			return Reject(struct{}{}, store.CommandRejection{Code: "not_allowed"}, rejectedReason), nil
		},
	}
	if _, rejectionErr := Execute(ctx, rejectedPlan); !errors.Is(rejectionErr, rejectedReason) {
		t.Fatalf("rejection error = %v, want %v", rejectionErr, rejectedReason)
	}
	if _, retryErr := Execute(ctx, rejectedPlan); retryErr == nil {
		t.Fatal("rejected retry returned no error")
	}

	audits, err := storage.ListAuditEntries(ctx)
	if err != nil {
		t.Fatalf("list Audit Entries: %v", err)
	}
	if len(audits) != 3 {
		t.Fatalf("Audit Entry count = %d, want success, conflict, and rejection", len(audits))
	}
}

func TestExecuteNotifiesOnlyAfterSuccess(t *testing.T) {
	storage, actorID, ctx, dataDir := openExecutionTestStore(t)
	now := time.Date(2026, time.July, 22, 13, 0, 0, 0, time.UTC)
	identity := func(commandID string) store.CommandIdentity {
		return store.CommandIdentity{
			ActorAccountID: actorID, CommandID: commandID, PayloadHash: strings.Repeat("a", 64),
			Action: "UpdateAccountProfile", TargetType: "Account", TargetID: "1", Now: now,
		}
	}

	var sequence []string
	plan := Plan[string]{
		Storage: storage, Identity: identity("executor-notification-order"),
		Authorization: Authorization{Facts: authz.Installation()},
		Replay: func(outcome string) (string, error) {
			sequence = append(sequence, "replay")
			return outcome, nil
		},
		Apply: func(*store.CommandTx) (Execution[string], error) {
			sequence = append(sequence, "apply")
			return Success("first", `"first"`), nil
		},
		Notify: func() {
			audits, err := storage.ListAuditEntries(ctx)
			if err != nil {
				t.Errorf("list Audit Entries during notification: %v", err)
			}
			if len(audits) != 1 {
				t.Errorf("Audit Entry count during notification = %d, want committed entry", len(audits))
			}
			sequence = append(sequence, "notify")
		},
	}
	if _, err := Execute(ctx, plan); err != nil {
		t.Fatalf("first execution: %v", err)
	}
	if want := []string{"apply", "notify"}; !slices.Equal(sequence, want) {
		t.Fatalf("first execution sequence = %v, want %v", sequence, want)
	}
	sequence = nil
	if _, err := Execute(ctx, plan); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if want := []string{"replay", "notify"}; !slices.Equal(sequence, want) {
		t.Fatalf("replay sequence = %v, want %v", sequence, want)
	}

	notifications := 0
	notify := func() { notifications++ }

	conflict := plan
	conflict.Identity.PayloadHash = strings.Repeat("b", 64)
	conflict.Notify = notify
	if _, err := Execute(ctx, conflict); !errors.Is(err, store.ErrCommandConflict) {
		t.Fatalf("conflict error = %v, want %v", err, store.ErrCommandConflict)
	}

	rejectionErr := errors.New("not allowed")
	rejection := Plan[string]{
		Storage: storage, Identity: identity("executor-notification-rejection"),
		Authorization: Authorization{Facts: authz.Installation()},
		Replay:        func(string) (string, error) { return "", rejectionErr },
		Apply: func(*store.CommandTx) (Execution[string], error) {
			return Reject("", store.CommandRejection{Code: "not_allowed"}, rejectionErr), nil
		},
		Notify: notify,
	}
	if _, err := Execute(ctx, rejection); !errors.Is(err, rejectionErr) {
		t.Fatalf("rejection error = %v, want %v", err, rejectionErr)
	}
	if _, err := Execute(ctx, rejection); !errors.Is(err, rejectionErr) {
		t.Fatalf("rejected replay error = %v, want %v", err, rejectionErr)
	}

	applicationErr := errors.New("application failed")
	application := plan
	application.Identity = identity("executor-notification-application-failure")
	application.Apply = func(*store.CommandTx) (Execution[string], error) {
		return Execution[string]{}, applicationErr
	}
	application.Notify = notify
	if _, err := Execute(ctx, application); !errors.Is(err, applicationErr) {
		t.Fatalf("application error = %v, want %v", err, applicationErr)
	}

	installCommandFailures(t, dataDir)
	persistence := plan
	persistence.Identity = identity("executor-notification-persistence-failure")
	persistence.Notify = notify
	if _, err := Execute(ctx, persistence); err == nil {
		t.Fatal("persistence failure returned no error")
	}

	replayFailure := plan
	replayFailure.Identity = identity("executor-notification-replay-failure")
	replayFailure.Notify = nil
	if _, err := Execute(ctx, replayFailure); err != nil {
		t.Fatalf("seed replay failure receipt: %v", err)
	}
	replayErr := errors.New("replay failed")
	replayFailure.Replay = func(string) (string, error) { return "", replayErr }
	replayFailure.Notify = notify
	if _, err := Execute(ctx, replayFailure); !errors.Is(err, replayErr) {
		t.Fatalf("replay error = %v, want %v", err, replayErr)
	}

	commit := plan
	commit.Identity = identity("executor-notification-commit-failure")
	commit.Notify = notify
	if _, err := Execute(ctx, commit); err == nil {
		t.Fatal("commit failure returned no error")
	}

	if notifications != 0 {
		t.Fatalf("failure notifications = %d, want 0", notifications)
	}
}

func TestExecuteSignalsApplicationSeparatelyFromReplay(t *testing.T) {
	storage, actorID, ctx, _ := openExecutionTestStore(t)
	now := time.Date(2026, time.July, 22, 14, 0, 0, 0, time.UTC)
	identity := func(commandID string) store.CommandIdentity {
		return store.CommandIdentity{
			ActorAccountID: actorID, CommandID: commandID, PayloadHash: strings.Repeat("a", 64),
			Action: "UpdateAccountProfile", TargetType: "Account", TargetID: "1", Now: now,
		}
	}

	var sequence []string
	plan := Plan[string]{
		Storage: storage, Identity: identity("executor-applied-signal"),
		Authorization: Authorization{Facts: authz.Installation()},
		Replay:        func(outcome string) (string, error) { return outcome, nil },
		Apply: func(*store.CommandTx) (Execution[string], error) {
			return Success("first", `"first"`), nil
		},
		Applied: func() { sequence = append(sequence, "applied") },
		Notify:  func() { sequence = append(sequence, "notify") },
	}
	if _, err := Execute(ctx, plan); err != nil {
		t.Fatalf("first execution: %v", err)
	}
	if want := []string{"applied", "notify"}; !slices.Equal(sequence, want) {
		t.Fatalf("first execution sequence = %v, want %v", sequence, want)
	}

	sequence = nil
	if _, err := Execute(ctx, plan); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if want := []string{"notify"}; !slices.Equal(sequence, want) {
		t.Fatalf("replay sequence = %v, want %v", sequence, want)
	}

	sequence = nil
	rejectionErr := errors.New("not allowed")
	rejection := plan
	rejection.Identity = identity("executor-applied-rejection")
	rejection.Apply = func(*store.CommandTx) (Execution[string], error) {
		return Reject("", store.CommandRejection{Code: "not_allowed"}, rejectionErr), nil
	}
	if _, err := Execute(ctx, rejection); !errors.Is(err, rejectionErr) {
		t.Fatalf("rejection error = %v, want %v", err, rejectionErr)
	}
	if len(sequence) != 0 {
		t.Fatalf("rejected sequence = %v, want no signals", sequence)
	}
}

func openExecutionTestStore(t *testing.T) (*store.SQLite, int, context.Context, string) {
	t.Helper()
	dataDir := t.TempDir()
	if err := store.Initialize(systemactor.NewContext(t.Context(), systemactor.HostMaintenance), dataDir); err != nil {
		t.Fatalf("initialize storage: %v", err)
	}
	storage, err := store.Open(systemactor.NewContext(t.Context(), systemactor.HostMaintenance), dataDir)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := storage.Close(); closeErr != nil {
			t.Errorf("close storage: %v", closeErr)
		}
	})
	now := time.Date(2026, time.July, 22, 11, 0, 0, 0, time.UTC)
	bootstrapHash := strings.Repeat("d", 64)
	if issueErr := storage.IssueBootstrap(systemactor.NewContext(t.Context(), systemactor.HostMaintenance), bootstrapHash, now, now.Add(time.Minute)); issueErr != nil {
		t.Fatalf("issue bootstrap: %v", issueErr)
	}
	actor, err := storage.BootstrapAdministrator(systemactor.NewContext(t.Context(), systemactor.HostMaintenance), store.BootstrapAdministratorParams{
		BootstrapHash: bootstrapHash, Name: "Ada Admin", NormalizedName: "ada admin",
		PasswordHash: "password hash", SessionHash: strings.Repeat("e", 64),
		Now: now, SessionExpiry: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	ctx := viewer.NewContext(t.Context(), viewer.Identity{AccountID: actor.ID, Administrator: true})
	return storage, actor.ID, ctx, dataDir
}

func installCommandFailures(t *testing.T, dataDir string) {
	t.Helper()
	database, err := sql.Open(
		"sqlite",
		"file:"+filepath.Join(dataDir, "beamers.db")+"?mode=rw&_pragma=busy_timeout(5000)",
	)
	if err != nil {
		t.Fatalf("open raw SQLite database: %v", err)
	}
	defer func() {
		if closeErr := database.Close(); closeErr != nil {
			t.Errorf("close raw SQLite database: %v", closeErr)
		}
	}()
	const schema = `
CREATE TABLE deferred_command_failure (
	account_id INTEGER NOT NULL
		REFERENCES accounts(id) DEFERRABLE INITIALLY DEFERRED
);
CREATE TRIGGER fail_selected_command_persistence
BEFORE INSERT ON command_receipts
WHEN NEW.command_id = 'executor-notification-persistence-failure'
BEGIN
	SELECT RAISE(FAIL, 'forced persistence failure');
END;
CREATE TRIGGER fail_selected_command_commit
AFTER INSERT ON command_receipts
WHEN NEW.command_id = 'executor-notification-commit-failure'
BEGIN
	INSERT INTO deferred_command_failure (account_id) VALUES (-1);
END;`
	if _, err := database.ExecContext(t.Context(), schema); err != nil {
		t.Fatalf("install command failures: %v", err)
	}
}

// TestCapabilityRefusalPrecedesLoadingTheTarget states the order the imperative
// checks keep today and the table has to keep as well.
//
// A scoped command has to load its target before its scope can be judged, but
// an actor with no authority over the Event must never be the reason that
// lookup happens. Loading first would answer with what the lookup found, so a
// Crew Member with no grant at all would be told a Session does not exist, or
// have that fact recorded against them, instead of being refused for the
// authority they lack.
func TestCapabilityRefusalPrecedesLoadingTheTarget(t *testing.T) {
	storage, actorID, _, _ := openExecutionTestStore(t)
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	stranger := viewer.NewContext(t.Context(), viewer.Identity{AccountID: actorID})
	refused := errors.New("operator required")
	loads := 0

	plan := Plan[string]{
		Storage: storage,
		Identity: store.CommandIdentity{
			ActorAccountID: actorID, CommandID: "scoped-refusal",
			PayloadHash: strings.Repeat("f", 64), Action: "StartSession",
			TargetType: "Session", TargetID: "1", Now: now,
		},
		Authorization: Authorization{
			EventID: 1,
			LoadFacts: func(context.Context, *store.CommandTx) (authz.Facts, error) {
				loads++
				return authz.Facts{}, errors.New("target lookup must not happen")
			},
			Refusals: RejectionTable{
				Rejections: []Rejection{{Err: refused, Code: "operator_required"}},
			},
		},
		Replay: func(string) (string, error) { return "", nil },
		Apply: func(*store.CommandTx) (Execution[string], error) {
			return Execution[string]{}, errors.New("application must not run")
		},
	}
	if _, err := Execute(stranger, plan); !errors.Is(err, refused) {
		t.Fatalf("refusal for an actor with no grant = %v, want %v", err, refused)
	}
	if loads != 0 {
		t.Errorf("target was loaded %d times for a command the table already refused", loads)
	}
}
