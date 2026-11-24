// Package dbutil holds the transaction plumbing shared by the repositories and
// services, so the same rollback rules are applied everywhere.
package dbutil

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const rollbackTimeout = 5 * time.Second

// Rollback is meant to be deferred right after Begin. It does nothing once the
// transaction has been committed, and joins any real rollback failure into
// returnedErr: a failed rollback usually means the connection is unusable, so
// hiding it would be worse than the original error.
func Rollback(ctx context.Context, tx pgx.Tx, returnedErr *error) {
	if err := rollback(ctx, tx); err != nil {
		*returnedErr = errors.Join(*returnedErr, err)
	}
}

func rollback(ctx context.Context, tx pgx.Tx) error {
	// The caller's context may already be cancelled, and then the rollback would
	// never reach the server. Detach from cancellation but keep the deadline.
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()

	if err := tx.Rollback(rollbackCtx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		return fmt.Errorf("rollback transaction: %w", err)
	}
	return nil
}
