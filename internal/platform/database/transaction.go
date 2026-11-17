package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// WithinTx commits only if fn succeeds. Errors, cancellation and panics roll back
// all changes. fn must use the supplied transaction, not db, and must not commit
// or roll it back itself. Transactions are not implicitly nested or retried.
func WithinTx(ctx context.Context, db *sql.DB, fn func(context.Context, *sql.Tx) error) (err error) {
	if fn == nil {
		return fmt.Errorf("transaction callback is required")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rollback transaction: %w", rollbackErr))
		}
	}()
	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}
