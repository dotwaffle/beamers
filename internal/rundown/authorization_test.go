package rundown_test

import (
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/store"
)

// TestRevokedAuthorityReplaysReceiptAndRefusesFreshCommands states the two
// halves of the rule the Capability Table has to keep once it judges commands
// inside the command path.
//
// A Command Receipt is durable truth about something that already happened, so
// a retry of a command that already succeeded returns its recorded outcome even
// after the actor's Event Grant is gone. The table is never asked about it
// again, because asking would let a later change of authority rewrite history.
//
// A fresh command from the same actor is a new question, so the table answers
// it, and the refusal leaves the same evidence any other rejection leaves: a
// Command Receipt that makes the refusal idempotent and a Rejected Audit Entry
// that makes it visible. Discarding draft changes had no such evidence before
// the table judged it, because its refusal happened without a command ever
// being recorded.
func TestRevokedAuthorityReplaysReceiptAndRefusesFreshCommands(t *testing.T) {
	storage, actor, eventID := openRundownTest(t)
	commands, err := rundown.NewCommands(storage, time.Now, nil, nil)
	if err != nil {
		t.Fatalf("create Rundown Commands: %v", err)
	}

	queries, err := rundown.NewQueries(storage, nil)
	if err != nil {
		t.Fatalf("create Rundown Queries: %v", err)
	}
	created, err := commands.EditDraft(t.Context(), actor, rundown.EditDraftInput{
		EventID: eventID, CommandID: "authorization-create", ExpectedDraftRevision: 0,
		Locations: []rundown.LocationDraftInput{{Ref: "authorization", Name: "Stage Door"}},
	})
	if err != nil {
		t.Fatalf("create draft Location: %v", err)
	}
	preview, err := queries.PublishPreview(t.Context(), actor, rundown.PublishPreviewInput{
		EventID: eventID, ChangeIDs: []int{created.Changes[0].ID},
	})
	if err != nil {
		t.Fatalf("preview publish baseline: %v", err)
	}
	published, err := commands.Publish(t.Context(), actor, rundown.PublishInput{
		EventID: eventID, CommandID: "authorization-publish",
		Confirmation: rundown.PublishConfirmation{
			DraftRevision: preview.DraftRevision, PublishedRevision: preview.PublishedRevision,
			ChangeIDs: preview.ChangeIDs, Fingerprint: preview.Fingerprint,
		},
	})
	if err != nil {
		t.Fatalf("publish baseline: %v", err)
	}
	edited, err := commands.EditDraft(t.Context(), actor, rundown.EditDraftInput{
		EventID: eventID, CommandID: "authorization-edit",
		ExpectedDraftRevision: published.DraftRevision,
		Locations: []rundown.LocationDraftInput{{
			ID: created.Changes[0].TargetID, Name: "Stage Door Rear", UpdateFields: []string{"name"},
		}},
	})
	if err != nil {
		t.Fatalf("edit draft before revocation: %v", err)
	}
	discard := rundown.DraftHistoryInput{
		EventID: eventID, CommandID: "authorization-discard",
		ExpectedDraftRevision: edited.DraftRevision,
		ChangeIDs:             []int{edited.Changes[0].ID},
	}
	discarded, err := commands.DiscardDraftChanges(t.Context(), actor, discard)
	if err != nil {
		t.Fatalf("discard draft change while granted: %v", err)
	}

	revoked := actor
	revoked.EventRoles = nil
	revoked.EventScopes = nil

	replayed, replayErr := commands.DiscardDraftChanges(t.Context(), revoked, discard)
	if replayErr != nil || replayed.DraftRevision != discarded.DraftRevision {
		t.Fatalf(
			"replay after revocation = %+v, %v, want the recorded outcome %+v",
			replayed, replayErr, discarded,
		)
	}

	fresh := discard
	fresh.CommandID = "authorization-discard-after-revocation"
	fresh.ExpectedDraftRevision = discarded.DraftRevision
	if _, refusalErr := commands.DiscardDraftChanges(
		t.Context(), revoked, fresh,
	); !errors.Is(refusalErr, rundown.ErrEventAccessDenied) {
		t.Fatalf("fresh command after revocation = %v, want %v", refusalErr, rundown.ErrEventAccessDenied)
	}
	if _, retryErr := commands.DiscardDraftChanges(
		t.Context(), revoked, fresh,
	); !errors.Is(retryErr, rundown.ErrEventAccessDenied) {
		t.Fatalf("retry of the refused command = %v, want the recorded refusal %v", retryErr, rundown.ErrEventAccessDenied)
	}

	rejections := rejectedAuditEntries(t, storage, actor, "DiscardDraftChanges")
	if len(rejections) != 1 {
		t.Fatalf(
			"Rejected Audit Entries for the refused Discard = %d, want exactly one; "+
				"the refusal must be recorded once and its retry answered from the receipt",
			len(rejections),
		)
	}
	if rejections[0].Reason != "event_access_denied" {
		t.Errorf(
			"Rejected Audit Entry reason = %q, want the durable rejection code the imperative check uses",
			rejections[0].Reason,
		)
	}

	revert := fresh
	revert.CommandID = "authorization-revert-after-revocation"
	if _, refusalErr := commands.RevertDraftChange(
		t.Context(), revoked, revert,
	); !errors.Is(refusalErr, rundown.ErrEventAccessDenied) {
		t.Fatalf("refused Revert after revocation = %v, want %v", refusalErr, rundown.ErrEventAccessDenied)
	}
	if reverts := rejectedAuditEntries(t, storage, actor, "RevertDraftChange"); len(reverts) != 1 {
		t.Errorf("Rejected Audit Entries for the refused Revert = %d, want exactly one", len(reverts))
	}
}

// rejectedAuditEntries returns the Rejected Audit Entries recorded for one
// command action.
func rejectedAuditEntries(
	t *testing.T,
	storage *store.SQLite,
	reader auth.Account,
	action string,
) []store.AuditEntry {
	t.Helper()

	entries, err := storage.ListAuditEntries(reader.Context(t.Context()))
	if err != nil {
		t.Fatalf("list Audit Entries: %v", err)
	}
	rejected := make([]store.AuditEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Action == action && entry.Outcome == "Rejected" {
			rejected = append(rejected, entry)
		}
	}
	return rejected
}
