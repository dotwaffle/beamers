package store

import (
	"context"

	"github.com/dotwaffle/beamers/ent"
)

// withTx runs fn inside one fresh Ent transaction, committing its result on
// success and rolling back otherwise. label names the operation for the
// wrapped error returned when the transaction cannot begin or commit,
// mirroring the begin/defer-rollback/commit shape every hand-rolled store
// transaction repeated.
func withTx[T any](
	ctx context.Context,
	client *ent.Client,
	label string,
	fn func(*ent.Tx) (T, error),
) (T, error) {
	var zero T
	transaction, err := client.Tx(ctx)
	if err != nil {
		return zero, opaqueError("begin "+label, err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	result, err := fn(transaction)
	if err != nil {
		return zero, err
	}
	if err := transaction.Commit(); err != nil {
		return zero, opaqueError("commit "+label, err)
	}
	return result, nil
}

// withVoidTx is withTx for the store methods that report only an error.
func withVoidTx(
	ctx context.Context,
	client *ent.Client,
	label string,
	fn func(*ent.Tx) error,
) error {
	_, err := withTx(ctx, client, label, func(transaction *ent.Tx) (struct{}, error) {
		return struct{}{}, fn(transaction)
	})
	return err
}

// withReadTx runs fn inside one fresh Ent transaction that is always rolled
// back and never committed, for the store methods that only preview state.
func withReadTx[T any](
	ctx context.Context,
	client *ent.Client,
	label string,
	fn func(*ent.Tx) (T, error),
) (T, error) {
	var zero T
	transaction, err := client.Tx(ctx)
	if err != nil {
		return zero, opaqueError("begin "+label, err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	return fn(transaction)
}
