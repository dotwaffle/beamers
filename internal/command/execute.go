package command

import (
	"context"
	"errors"

	"github.com/dotwaffle/beamers/internal/store"
)

// Execution describes the command-specific work performed inside one durable
// command lifecycle.
type Execution[T any] struct {
	value        T
	outcomeJSON  string
	rejection    *store.CommandRejection
	rejected     bool
	auditDetails store.AuditDetails
	targetID     string
	returnError  error
}

// Success returns a successful durable command outcome.
func Success[T any](value T, outcomeJSON string) Execution[T] {
	return Execution[T]{value: value, outcomeJSON: outcomeJSON}
}

// Reject returns a stable rejection to commit before returning its error.
func Reject[T any](value T, rejection store.CommandRejection, returnError error) Execution[T] {
	return Execution[T]{value: value, rejection: &rejection, returnError: returnError}
}

// RejectEncoded returns a domain-specific rejection envelope to commit before
// returning its error.
func RejectEncoded[T any](value T, outcomeJSON string, returnError error) Execution[T] {
	return Execution[T]{value: value, outcomeJSON: outcomeJSON, rejected: true, returnError: returnError}
}

// WithAudit attaches domain-required evidence to a successful outcome.
func (execution Execution[T]) WithAudit(details store.AuditDetails) Execution[T] {
	execution.auditDetails = details
	return execution
}

// WithTargetID replaces an initially unidentified durable target.
func (execution Execution[T]) WithTargetID(targetID string) Execution[T] {
	execution.targetID = targetID
	return execution
}

// Plan supplies the command-specific work around the shared durable lifecycle.
type Plan[T any] struct {
	Storage  *store.SQLite
	Identity store.CommandIdentity
	Replay   func(string) (T, error)
	Apply    func(*store.CommandTx) (Execution[T], error)
}

// Execute owns transaction, retry, conflict, evidence, and commit ordering for
// one durable command.
func Execute[T any](ctx context.Context, plan Plan[T]) (T, error) {
	var zero T
	if plan.Storage == nil {
		return zero, errors.New("command storage is required")
	}
	if plan.Replay == nil {
		return zero, errors.New("command replay is required")
	}
	if plan.Apply == nil {
		return zero, errors.New("command application is required")
	}

	transaction, err := plan.Storage.BeginCommand(ctx)
	if err != nil {
		return zero, err
	}
	defer func() {
		_ = transaction.Rollback()
	}()

	original, retry, err := transaction.LookupReceipt(ctx, plan.Identity)
	if errors.Is(err, store.ErrCommandConflict) {
		if commitErr := transaction.CommitConflict(ctx, plan.Identity); commitErr != nil {
			return zero, commitErr
		}
		return zero, store.ErrCommandConflict
	}
	if err != nil {
		return zero, err
	}
	if retry {
		return plan.Replay(original)
	}

	execution, err := plan.Apply(transaction)
	if err != nil {
		return zero, err
	}
	identity := plan.Identity
	if execution.targetID != "" {
		identity.TargetID = execution.targetID
	}
	switch {
	case execution.rejection != nil:
		err = transaction.RecordRejection(ctx, identity, *execution.rejection)
	case execution.outcomeJSON == "":
		err = errors.New("command outcome is required")
	default:
		err = transaction.RecordOutcomeWithAudit(
			ctx,
			identity,
			execution.outcomeJSON,
			execution.rejected,
			execution.auditDetails,
		)
	}
	if err != nil {
		return zero, errors.Join(execution.returnError, err)
	}
	if err := transaction.Commit(); err != nil {
		return zero, errors.Join(execution.returnError, err)
	}
	return execution.value, execution.returnError
}
