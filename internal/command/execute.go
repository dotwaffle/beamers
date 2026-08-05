package command

import (
	"context"
	"errors"

	"github.com/dotwaffle/beamers/internal/authz"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/systemactor"
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
// Set exactly one of Facts and LoadFacts. Use Facts when the scope is known
// before the transaction opens. Use LoadFacts when the target determines the
// scope. LoadFacts runs inside the command transaction.
type Authorization struct {
	// Facts are the scope facts a row is judged against.
	Facts authz.Facts
	// LoadFacts loads the scope facts from the target, inside the command
	// transaction and before Apply runs. An error it returns is classified
	// through Refusals exactly as the same error would be from Apply, so
	// loading the target early does not change what a failure to load
	// produces.
	LoadFacts func(context.Context, *store.CommandTx) (authz.Facts, error)
	// EventID names the Event a LoadFacts command acts within, which its input
	// states before anything is loaded. It lets the capability half of the row
	// be judged before the target is looked up, so an actor with no authority
	// over the Event is refused for that rather than for what the lookup found.
	EventID int
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

	// The Command Receipt lifecycle acts as the command replay System Actor:
	// receipt lookup and outcome recording run whether or not the command's
	// own caller carries a viewer identity.
	receiptContext := systemactor.NewContext(ctx, systemactor.CommandReplay)
	transaction, err := plan.Storage.BeginCommand(receiptContext)
	if err != nil {
		return zero, err
	}
	defer func() {
		_ = transaction.Rollback()
	}()

	original, retry, err := transaction.LookupReceipt(receiptContext, plan.Identity)
	if errors.Is(err, store.ErrCommandConflict) {
		if commitErr := transaction.CommitConflict(receiptContext, plan.Identity); commitErr != nil {
			return zero, commitErr
		}
		return zero, store.ErrCommandConflict
	}
	if err != nil {
		return zero, err
	}
	if retry {
		if refusal := plan.replayRefusal(original); refusal != nil {
			_ = transaction.Rollback()
			return zero, refusal
		}
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
		err = transaction.RecordRejection(receiptContext, identity, *execution.rejection)
	case execution.outcomeJSON == "":
		err = errors.New("command outcome is required")
	default:
		err = transaction.RecordOutcomeWithAudit(
			receiptContext,
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

// replayRefusal returns the domain error behind a refusal the Capability Table
// committed for this command, or nil if this plan does not recognize the
// receipt as one of its own refusals.
//
// Execute replays a table refusal rather than the plan, because the plan never
// saw it: the evaluator refuses before the application runs, so the receipt
// carries the shared rejection envelope rather than whatever outcome this
// command records for itself. Without this a retry would decode the envelope as
// an empty outcome and report success for a command that was refused.
//
// A rejection this plan's table does not name is left to the plan, which is the
// only thing that knows what its own rejection envelopes mean.
func (plan Plan[T]) replayRefusal(original string) error {
	var ignored struct{}
	err := store.DecodeCommandReceipt(original, &ignored)
	var rejected *store.RejectedCommandError
	if !errors.As(err, &rejected) {
		return nil
	}
	return plan.Authorization.Refusals.Sentinel(rejected.Rejection.Code)
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
	if plan.Authorization.Facts.Supplied() == (plan.Authorization.LoadFacts != nil) {
		return true, errors.New("command authorization must set exactly one of Facts and LoadFacts")
	}

	identity, authenticated := viewer.FromContext(ctx)
	if plan.Authorization.LoadFacts != nil {
		capabilities := authz.EvaluateCapabilities(authz.CapabilityRequest{
			Identity:      identity,
			Authenticated: authenticated,
			Action:        plan.Identity.Action,
			EventID:       plan.Authorization.EventID,
		})
		if capabilities != nil {
			return plan.refuse(ctx, transaction, capabilities)
		}
	}

	facts, factsErr := plan.loadFacts(ctx, transaction)
	if factsErr != nil {
		return plan.commitClassified(ctx, transaction, factsErr)
	}

	evaluation := authz.Evaluate(authz.Request{
		Identity:      identity,
		Authenticated: authenticated,
		Action:        plan.Identity.Action,
		Facts:         facts,
	})
	if evaluation == nil {
		return false, nil
	}
	return plan.refuse(ctx, transaction, evaluation)
}

// refuse commits the table's refusal and returns the domain error this
// package's callers expect. An evaluation that is not a refusal is a wrong
// declaration rather than a decision, and fails the command outright.
func (plan Plan[T]) refuse(
	ctx context.Context,
	transaction *store.CommandTx,
	evaluation error,
) (handled bool, err error) {
	var refusal *authz.RefusalError
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
