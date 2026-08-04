package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/auditentry"
)

var (
	// ErrDraftRevisionConflict means a concurrent Draft Edit won the revision update.
	ErrDraftRevisionConflict = errors.New("draft revision conflict")
	// ErrDraftReference means a Draft entity references unknown structural identity.
	ErrDraftReference = errors.New("invalid Draft reference")
)

// DraftRevisionConflictError describes the current state that overlaps a stale edit.
type DraftRevisionConflictError struct {
	CurrentDraftRevision int
	OverlappingChanges   []DraftChangeResult
}

func (conflict *DraftRevisionConflictError) Error() string { return ErrDraftRevisionConflict.Error() }

func (conflict *DraftRevisionConflictError) Unwrap() error { return ErrDraftRevisionConflict }

// CommandIdentity contains one command's durable replay identity.
type CommandIdentity struct {
	ActorAccountID int
	CommandID      string
	PayloadHash    string
	Action         string
	TargetType     string
	TargetID       string
	Now            time.Time
}

// CommandTx owns one command's persistence transaction.
type CommandTx struct {
	transaction *ent.Tx
	committed   bool
}

// AuditDetails contains optional domain-required evidence for one outcome.
type AuditDetails struct {
	Reason string
	Note   string
}

// BeginCommand starts one concrete command transaction.
func (installation *SQLite) BeginCommand(ctx context.Context) (*CommandTx, error) {
	transaction, err := installation.client.Tx(ctx)
	if err != nil {
		return nil, opaqueError("begin command", err)
	}
	return &CommandTx{transaction: transaction}, nil
}

// ProbeCommandEvidence verifies that both durable command evidence tables are
// writable without retaining probe rows.
func (installation *SQLite) ProbeCommandEvidence(
	ctx context.Context,
	now time.Time,
) (returnErr error) {
	transaction, err := installation.client.Tx(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return opaqueError("begin command evidence probe", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, transaction.Rollback())
	}()
	internalContext := systemContext(ctx)
	const receiptInsert = `
INSERT INTO command_receipts (
	command_id, payload_hash, action, target_type, target_id,
	outcome_json, created_at
) VALUES ('emergency-alert-storage-probe', ?, ?, ?, ?, '{}', ?)`
	if _, err = transaction.ExecContext(
		internalContext,
		receiptInsert,
		strings.Repeat("0", 64),
		"ProbeEmergencyAlertStorage",
		"Installation",
		"1",
		now.UTC(),
	); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return opaqueError("probe Command Receipt storage", err)
	}
	const auditInsert = `
INSERT INTO audit_entries (
	actor_kind, created_at, action, target_type, target_id, result
) VALUES ('Account', ?, ?, ?, ?, 'Succeeded')`
	if _, err = transaction.ExecContext(
		internalContext,
		auditInsert,
		now.UTC(),
		"ProbeEmergencyAlertStorage",
		"Installation",
		"1",
	); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return opaqueError("probe Audit Entry storage", err)
	}
	return nil
}

// LookupReceipt returns the original outcome for an exact retry.
func (transaction *CommandTx) LookupReceipt(
	ctx context.Context,
	identity CommandIdentity,
) (string, bool, error) {
	return findCommandReceipt(ctx, transaction.transaction, commandReceiptParams{
		ActorAccountID: identity.ActorAccountID, CommandID: identity.CommandID,
		PayloadHash: identity.PayloadHash, Action: identity.Action,
	})
}

// RecordOutcome appends the Command Receipt and Audit Entry without committing.
func (transaction *CommandTx) RecordOutcome(
	ctx context.Context,
	identity CommandIdentity,
	outcomeJSON string,
	rejected bool,
) error {
	return transaction.RecordOutcomeWithAudit(ctx, identity, outcomeJSON, rejected, AuditDetails{})
}

// RecordOutcomeWithAudit appends a receipt and detailed Audit Entry without committing.
func (transaction *CommandTx) RecordOutcomeWithAudit(
	ctx context.Context,
	identity CommandIdentity,
	outcomeJSON string,
	rejected bool,
	details AuditDetails,
) error {
	result := auditentry.ResultSucceeded
	if rejected {
		result = auditentry.ResultRejected
	}
	if err := createCommandReceipt(ctx, transaction.transaction, commandReceiptParams{
		ActorAccountID: identity.ActorAccountID, CommandID: identity.CommandID,
		PayloadHash: identity.PayloadHash, Action: identity.Action,
		TargetType: identity.TargetType, TargetID: identity.TargetID,
		OutcomeJSON: outcomeJSON, Now: identity.Now,
	}); err != nil {
		return opaqueError("record Command Receipt", err)
	}
	audit := transaction.transaction.AuditEntry.Create().
		SetCreatedAt(identity.Now).
		SetAction(identity.Action).
		SetTargetType(identity.TargetType).
		SetTargetID(identity.TargetID).
		SetResult(result).
		SetReason(auditReason(outcomeJSON, rejected, details.Reason)).
		SetNote(details.Note)
	audit.SetActorKind(auditentry.ActorKindAccount).
		SetActorAccountID(identity.ActorAccountID)
	if _, err := audit.Save(systemContext(ctx)); err != nil {
		return opaqueError("record command Audit Entry", err)
	}
	return nil
}

func auditReason(outcomeJSON string, rejected bool, reason string) string {
	if reason != "" {
		return reason
	}
	if !rejected {
		return ""
	}
	var outcome commandOutcome
	if err := json.Unmarshal([]byte(outcomeJSON), &outcome); err != nil || outcome.Rejected == nil {
		return ""
	}
	return outcome.Rejected.Code
}

// RecordRejection appends one stable rejected outcome and its Audit Entry.
func (transaction *CommandTx) RecordRejection(
	ctx context.Context,
	identity CommandIdentity,
	rejection CommandRejection,
) error {
	encoded, err := json.Marshal(commandOutcome{Rejected: &rejection})
	if err != nil {
		return opaqueError("encode rejected command outcome", err)
	}
	return transaction.RecordOutcome(ctx, identity, string(encoded), true)
}

// CommitConflict records one conflicting Command ID reuse without altering its receipt.
func (transaction *CommandTx) CommitConflict(ctx context.Context, identity CommandIdentity) error {
	if err := auditRejectedCommand(
		systemContext(ctx),
		transaction.transaction.AuditEntry,
		identity,
	); err != nil {
		return opaqueError("audit conflicting Command ID", err)
	}
	return transaction.Commit()
}

// Commit makes the command state and evidence durable together.
func (transaction *CommandTx) Commit() error {
	if err := transaction.transaction.Commit(); err != nil {
		return opaqueError("commit command", err)
	}
	transaction.committed = true
	return nil
}

// Rollback safely releases an unfinished transaction and is harmless after commit.
func (transaction *CommandTx) Rollback() error {
	if transaction == nil || transaction.committed {
		return nil
	}
	return transaction.transaction.Rollback()
}
