package command

import (
	"errors"

	"github.com/dotwaffle/beamers/internal/store"
)

// Rejection pairs one sentinel domain error with the stable code recorded in a
// Command Receipt.
type Rejection struct {
	Err  error
	Code string
	// Restored is the error a replay of this code returns, for the few codes a
	// package classifies from a storage sentinel but reports to callers as its
	// own. Leave it nil when both directions use Err.
	Restored error
}

// RejectionTable is one package's single source for the durable rejection
// mapping in both directions. Packages previously wrote the two directions as
// separate switch statements, so a code added to one and forgotten in the other
// turned a replayed command into a different failure than the original.
type RejectionTable struct {
	// Rejections are matched in order with errors.Is, so a sentinel must
	// precede any sentinel that wraps it.
	Rejections []Rejection
	// RecordMessage stores the failing error's text alongside the code, for
	// packages whose Crew-facing surfaces read rejection detail.
	RecordMessage bool
}

// Code returns the durable rejection code classifying err.
func (table RejectionTable) Code(err error) (string, bool) {
	for _, known := range table.Rejections {
		if errors.Is(err, known.Err) {
			return known.Code, true
		}
	}
	return "", false
}

// Return picks the error a fresh command application should return for a
// failure this table classifies. It stops at the same first match Code
// would record, so it never returns a later entry's Restored error for one
// Code would classify under an earlier entry. That match's Restored error
// comes back when set, so the fresh outcome agrees with what a replay of
// the same rejection restores. Any other error, including one this table
// does not classify, comes back unchanged so callers keep whatever context
// they wrapped it with.
func (table RejectionTable) Return(err error) error {
	for _, known := range table.Rejections {
		if !errors.Is(err, known.Err) {
			continue
		}
		if known.Restored != nil {
			return known.Restored
		}
		return err
	}
	return err
}

// Sentinel returns the domain error a durable rejection code stands for, or
// nil when this table does not know the code.
func (table RejectionTable) Sentinel(code string) error {
	for _, known := range table.Rejections {
		if known.Code != code {
			continue
		}
		if known.Restored != nil {
			return known.Restored
		}
		return known.Err
	}
	return nil
}

// Rejection classifies err as a durable rejection to commit before returning.
func (table RejectionTable) Rejection(err error) (store.CommandRejection, bool) {
	code, known := table.Code(err)
	if !known {
		return store.CommandRejection{}, false
	}
	rejection := store.CommandRejection{Code: code}
	if table.RecordMessage {
		rejection.Message = err.Error()
	}
	return rejection, true
}

// Restore replaces a replayed rejection with the sentinel domain error behind
// its code. Any other error, and any code this table does not know, is returned
// unchanged so the caller can decide what an unrecognized receipt means.
func (table RejectionTable) Restore(err error) error {
	var rejected *store.RejectedCommandError
	if !errors.As(err, &rejected) {
		return err
	}
	if sentinel := table.Sentinel(rejected.Rejection.Code); sentinel != nil {
		return sentinel
	}
	return err
}

// RejectKnown wraps one command application so that table-known domain errors
// commit as durable rejections instead of failing the command outright.
func RejectKnown[T any](
	table RejectionTable,
	apply func(*store.CommandTx) (Execution[T], error),
) func(*store.CommandTx) (Execution[T], error) {
	return func(transaction *store.CommandTx) (Execution[T], error) {
		execution, err := apply(transaction)
		rejection, rejected := table.Rejection(err)
		if !rejected {
			return execution, err
		}
		var zero T
		return Reject(zero, rejection, err), nil
	}
}

// ReplayKnown decodes a stored Command Receipt, restoring the sentinel domain
// error behind any rejection it recorded.
func ReplayKnown[T any](table RejectionTable, outcome string) (T, error) {
	var replayed T
	err := store.DecodeCommandReceipt(outcome, &replayed)
	return replayed, table.Restore(err)
}
