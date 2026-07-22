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
	Code    string          `json:"code"`
	Field   string          `json:"field,omitempty"`
	Message string          `json:"message,omitempty"`
	Details json.RawMessage `json:"details,omitempty"`
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

// DecodeCommandReceipt restores a successful projection or stable rejection.
func DecodeCommandReceipt(outcome string, target any) error {
	return decodeCommandReceipt(outcome, target, "decode Command Receipt")
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
		SetReason("command_id_conflict").
		Save(ctx)
	return err
}
