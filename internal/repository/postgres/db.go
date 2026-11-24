package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// DBTX is the slice of pgx the repositories actually need, which lets them run
// against either a pool or an already open transaction.
type DBTX interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// rowScanner covers both pgx.Row and pgx.Rows, so a row mapper can be reused
// for single lookups and list queries.
type rowScanner interface {
	Scan(dest ...any) error
}
