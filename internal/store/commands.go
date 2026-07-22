package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/auditentry"
	"github.com/dotwaffle/beamers/ent/commandreceipt"
	"github.com/dotwaffle/beamers/internal/viewer"
)

var (
	// ErrCommandConflict means a Command ID was reused for different work.
	ErrCommandConflict = errors.New("command_id was already used with a different payload")
	// ErrRevisionConflict means a mutation expected an outdated Event revision.
	ErrRevisionConflict = errors.New("Event revision conflict")
)

// CommandRejection is the stable, non-sensitive outcome of a rejected command.
type CommandRejection struct {
	Code    string `json:"code"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message,omitempty"`
}

// RejectedCommandError replays a Command's original rejected outcome.
type RejectedCommandError struct {
	Rejection CommandRejection
}

// Error implements error.
func (err *RejectedCommandError) Error() string {
	if err.Rejection.Message != "" {
		return err.Rejection.Message
	}
	return "command was rejected: " + err.Rejection.Code
}

type commandOutcome struct {
	Rejected *CommandRejection `json:"rejected,omitempty"`
}

type commandReceiptParams struct {
	ActorAccountID int
	CommandID      string
	PayloadHash    string
	Action         string
	TargetType     string
	TargetID       string
	OutcomeJSON    string
	Now            time.Time
}

func findCommandReceipt(
	ctx context.Context,
	transaction *ent.Tx,
	params commandReceiptParams,
) (outcomeJSON string, retry bool, err error) {
	found, err := transaction.CommandReceipt.Query().
		Where(commandreceipt.CommandIDEQ(params.CommandID)).
		Only(viewer.SystemContext(ctx))
	if ent.IsNotFound(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, opaqueError("read Command Receipt", err)
	}
	if found.ActorAccountID != params.ActorAccountID ||
		found.PayloadHash != params.PayloadHash || found.Action != params.Action {
		return "", false, ErrCommandConflict
	}
	return found.OutcomeJSON, true, nil
}

func createCommandReceipt(
	ctx context.Context,
	transaction *ent.Tx,
	params commandReceiptParams,
) error {
	_, err := transaction.CommandReceipt.Create().
		SetActorAccountID(params.ActorAccountID).
		SetCommandID(params.CommandID).
		SetPayloadHash(params.PayloadHash).
		SetAction(params.Action).
		SetTargetType(params.TargetType).
		SetTargetID(params.TargetID).
		SetOutcomeJSON(params.OutcomeJSON).
		SetCreatedAt(params.Now).
		Save(viewer.SystemContext(ctx))
	return err
}

func decodeCommandReceipt(outcome string, target any, description string) error {
	var envelope commandOutcome
	if err := json.Unmarshal([]byte(outcome), &envelope); err == nil && envelope.Rejected != nil {
		return &RejectedCommandError{Rejection: *envelope.Rejected}
	}
	if err := json.Unmarshal([]byte(outcome), target); err != nil {
		return opaqueError(description, err)
	}
	return nil
}

func rejectCommandConflict(
	ctx context.Context,
	transaction *ent.Tx,
	params commandReceiptParams,
) error {
	if err := auditRejectedCommand(
		ctx, transaction.AuditEntry, params.ActorAccountID, params.Action,
		"Command", params.CommandID, params.Now,
	); err != nil {
		return opaqueError("audit conflicting Command ID", err)
	}
	if err := transaction.Commit(); err != nil {
		return opaqueError("commit conflicting Command ID audit", err)
	}
	return ErrCommandConflict
}

func auditRejectedCommand(
	ctx context.Context,
	audits *ent.AuditEntryClient,
	actorAccountID int,
	action string,
	targetType string,
	targetID string,
	now time.Time,
) error {
	_, err := audits.Create().
		SetActorAccountID(actorAccountID).
		SetCreatedAt(now).
		SetAction(action).
		SetTargetType(targetType).
		SetTargetID(targetID).
		SetResult(auditentry.ResultRejected).
		Save(ctx)
	return err
}

// RecordRejectedCommand atomically records one rejected command and makes exact
// retries return the same rejection without another Audit Entry.
func (installation *SQLite) RecordRejectedCommand(
	ctx context.Context,
	actorAccountID int,
	commandID string,
	payloadHash string,
	action string,
	targetType string,
	targetID string,
	rejection CommandRejection,
	now time.Time,
) (bool, error) {
	if targetID == "" {
		targetID = "unidentified"
	}
	transaction, err := installation.client.Tx(ctx)
	if err != nil {
		return false, opaqueError("begin rejected command", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	outcomeJSON, err := json.Marshal(commandOutcome{Rejected: &rejection})
	if err != nil {
		return false, opaqueError("encode rejected command outcome", err)
	}
	receipt := commandReceiptParams{
		ActorAccountID: actorAccountID, CommandID: commandID, PayloadHash: payloadHash,
		Action: action, TargetType: targetType, TargetID: targetID,
		OutcomeJSON: string(outcomeJSON), Now: now,
	}
	_, retry, err := findCommandReceipt(ctx, transaction, receipt)
	if errors.Is(err, ErrCommandConflict) {
		return false, rejectCommandConflict(ctx, transaction, receipt)
	}
	if err != nil || retry {
		return retry, err
	}
	if err := createCommandReceipt(ctx, transaction, receipt); err != nil {
		return false, opaqueError("record rejected Command Receipt", err)
	}
	if err := auditRejectedCommand(
		ctx, transaction.AuditEntry, actorAccountID, action, targetType, targetID, now,
	); err != nil {
		return false, opaqueError("audit rejected command", err)
	}
	if err := transaction.Commit(); err != nil {
		return false, opaqueError("commit rejected command", err)
	}
	return false, nil
}
