package command

import (
	"context"
	"errors"

	"github.com/dotwaffle/beamers/internal/authz"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/viewer"
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

// Value returns the outcome this execution carries: the applied result of a
// successful command, or the unchanged current state of a rejected one.
func (execution Execution[T]) Value() T {
	return execution.value
}

// Rejection returns the durable rejection this execution commits, if any.
func (execution Execution[T]) Rejection() (store.CommandRejection, bool) {
	if execution.rejection == nil {
		return store.CommandRejection{}, false
	}
	return *execution.rejection, true
}

// ReturnError returns the error this execution returns to its caller once its
// outcome has committed.
func (execution Execution[T]) ReturnError() error {
	return execution.returnError
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

// Authorization is the plan phase that judges one command against its
// Capability Table row.
//
// Exactly one of Facts and LoadFacts must be set. Facts serves the rows whose
// scope is known before the transaction opens; LoadFacts serves the rows whose
// scope depends on the target, and runs inside the command transaction so that
// the facts are the ones the application will act on. Supplying neither leaves
// the facts unset, which every row refuses: a command that declares nothing
// fails closed rather than passing unjudged.
type Authorization struct {
	// Facts are the scope facts a row is judged against.
	Facts authz.Facts
	// LoadFacts loads the scope facts from the target, inside the command
	// transaction and before Apply runs. An error it returns is classified
	// through Refusals exactly as the same error would be from Apply, so
	// loading the target early does not change what a failure to load
	// produces.
	LoadFacts func(context.Context, *store.CommandTx) (authz.Facts, error)
	// Refusals translates a table refusal code into the domain error this
	// package's callers expect, so a refusal the evaluator raises is
	// indistinguishable from the imperative refusal it mirrors.
	Refusals RejectionTable
}

// Plan supplies the command-specific work around the shared durable lifecycle.
type Plan[T any] struct {
	Storage  *store.SQLite
	Identity store.CommandIdentity
	Replay   func(string) (T, error)
	Apply    func(*store.CommandTx) (Execution[T], error)
	// Authorization judges this command against the Capability Table, after
	// the Command Receipt lookup and before Apply. A replayed command returns
	// its recorded outcome without being judged again.
	Authorization Authorization
	// Applied runs after a fresh application commits and never after a replay,
	// so a caller can adopt in-memory state that only the applying execution
	// produced. It runs before Notify.
	Applied func()
	// Notify synchronously publishes a best-effort freshness hint after success.
	Notify func()
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
		value, replayErr := plan.Replay(original)
		if replayErr != nil {
			return zero, replayErr
		}
		_ = transaction.Rollback()
		if plan.Notify != nil {
			plan.Notify()
		}
		return value, nil
	}

	if handled, authorizationErr := plan.authorize(ctx, transaction); handled {
		return zero, authorizationErr
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
	if execution.returnError == nil && execution.rejection == nil && !execution.rejected {
		if plan.Applied != nil {
			plan.Applied()
		}
		if plan.Notify != nil {
			plan.Notify()
		}
	}
	return execution.value, execution.returnError
}

// authorize applies this command's Capability Table row, inside the transaction
// and before the application runs. It reports whether it has decided the
// command's outcome: a refusal commits its rejection and returns the domain
// error the caller expects, so an evaluated refusal is indistinguishable from
// the imperative refusal it mirrors.
func (plan Plan[T]) authorize(
	ctx context.Context,
	transaction *store.CommandTx,
) (handled bool, err error) {
	facts, factsErr := plan.loadFacts(ctx, transaction)
	if factsErr != nil {
		return plan.commitClassified(ctx, transaction, factsErr)
	}

	identity, authenticated := viewer.FromContext(ctx)
	evaluation := authz.Evaluate(authz.Request{
		Identity:      identity,
		Authenticated: authenticated,
		Action:        plan.Identity.Action,
		Facts:         facts,
	})
	if evaluation == nil {
		return false, nil
	}
	var refusal *authz.Refusal
	if !errors.As(evaluation, &refusal) {
		return true, evaluation
	}
	if sentinel := plan.Authorization.Refusals.Sentinel(refusal.Code); sentinel != nil {
		return plan.commitClassified(ctx, transaction, sentinel)
	}
	return plan.commitRejection(
		ctx, transaction, store.CommandRejection{Code: refusal.Code}, refusal,
	)
}

func (plan Plan[T]) loadFacts(
	ctx context.Context,
	transaction *store.CommandTx,
) (authz.Facts, error) {
	if plan.Authorization.LoadFacts != nil {
		return plan.Authorization.LoadFacts(ctx, transaction)
	}
	return plan.Authorization.Facts, nil
}

// commitClassified commits err as a durable rejection when this command's table
// classifies it, and otherwise fails the command outright, which is what the
// same error would have done had it come from the application.
func (plan Plan[T]) commitClassified(
	ctx context.Context,
	transaction *store.CommandTx,
	err error,
) (handled bool, returned error) {
	rejection, classified := plan.Authorization.Refusals.Rejection(err)
	if !classified {
		return true, err
	}
	return plan.commitRejection(
		ctx, transaction, rejection, plan.Authorization.Refusals.Return(err),
	)
}

func (plan Plan[T]) commitRejection(
	ctx context.Context,
	transaction *store.CommandTx,
	rejection store.CommandRejection,
	returned error,
) (handled bool, err error) {
	if recordErr := transaction.RecordRejection(ctx, plan.Identity, rejection); recordErr != nil {
		return true, errors.Join(returned, recordErr)
	}
	if commitErr := transaction.Commit(); commitErr != nil {
		return true, errors.Join(returned, commitErr)
	}
	return true, returned
}
