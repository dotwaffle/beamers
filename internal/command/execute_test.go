package command

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/dotwaffle/beamers/ent/runtime"

	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestExecuteOwnsRetryConflictRejectionAndAuditOrdering(t *testing.T) {
	storage, actorID, ctx := openExecutionTestStore(t)
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	identity := store.CommandIdentity{
		ActorAccountID: actorID, CommandID: "executor-success", PayloadHash: strings.Repeat("a", 64),
		Action: "TestCommand", TargetType: "Account", TargetID: "1", Now: now,
	}
	applications := 0
	plan := Plan[string]{
		Storage: storage, Identity: identity,
		Replay: func(outcome string) (string, error) { return outcome, nil },
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
		Storage: storage,
		Identity: store.CommandIdentity{
			ActorAccountID: actorID, CommandID: "executor-rejected", PayloadHash: strings.Repeat("c", 64),
			Action: "TestRejectedCommand", TargetType: "Account", TargetID: "1", Now: now,
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

func openExecutionTestStore(t *testing.T) (*store.SQLite, int, context.Context) {
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
	now := time.Date(2026, time.July, 22, 11, 0, 0, 0, time.UTC)
	bootstrapHash := strings.Repeat("d", 64)
	if issueErr := storage.IssueBootstrap(t.Context(), bootstrapHash, now, now.Add(time.Minute)); issueErr != nil {
		t.Fatalf("issue bootstrap: %v", issueErr)
	}
	actor, err := storage.BootstrapAdministrator(t.Context(), store.BootstrapAdministratorParams{
		BootstrapHash: bootstrapHash, Name: "Ada Admin", NormalizedName: "ada admin",
		PasswordHash: "password hash", SessionHash: strings.Repeat("e", 64),
		Now: now, SessionExpiry: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	ctx := viewer.NewContext(t.Context(), viewer.Identity{AccountID: actor.ID, Administrator: true})
	return storage, actor.ID, ctx
}
