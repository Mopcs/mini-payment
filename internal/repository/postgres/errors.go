package postgres

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// These errors let callers handle repository outcomes without depending on PostgreSQL details.
var (
	ErrNotFound                 = errors.New("record not found")
	ErrInvalidInput             = errors.New("invalid input")
	ErrUniqueViolation          = errors.New("unique constraint violation")
	ErrEmailAlreadyExists       = errors.New("email already exists")
	ErrWalletAlreadyExists      = errors.New("wallet already exists")
	ErrForeignKeyViolation      = errors.New("referenced record does not exist")
	ErrCheckViolation           = errors.New("value violates a constraint")
	ErrInvalidEmail             = errors.New("email must be lowercase and trimmed")
	ErrInvalidBalance           = errors.New("wallet balance cannot be negative")
	ErrUnsupportedCurrency      = errors.New("currency is not supported")
	ErrIdempotencyConflict      = errors.New("idempotency key was reused with a different request")
	ErrDepositWalletNotFound    = errors.New("deposit wallet not found")
	ErrDepositCurrencyMismatch  = errors.New("deposit currency does not match wallet")
	ErrLedgerAccountMissing     = errors.New("ledger account is missing")
	ErrBalanceOverflow          = errors.New("wallet balance would overflow")
	ErrTransferWalletNotFound   = errors.New("transfer wallet not found")
	ErrTransferSourceNotOwned   = errors.New("source wallet is not owned by user")
	ErrTransferCurrencyMismatch = errors.New("transfer wallet currency mismatch")
	ErrInsufficientFunds        = errors.New("insufficient wallet funds")
	ErrWalletBalanceChanged     = errors.New("wallet balance changed while the transaction was in flight")
)

type databaseOperationError struct {
	operation string
	cause     error
}

func (e *databaseOperationError) Error() string {
	return e.operation + ": database operation failed"
}

func (e *databaseOperationError) Unwrap() error {
	return e.cause
}

// translateError turns driver errors into the sentinels above, so callers can
// use errors.Is and never have to match PostgreSQL error codes themselves.
func translateError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", operation, ErrNotFound)
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return &databaseOperationError{operation: operation, cause: err}
	}

	kind, known := translateSQLState(pgErr)
	if !known {
		return &databaseOperationError{operation: operation, cause: err}
	}
	return fmt.Errorf("%s: %w", operation, kind)
}

// translateSQLState maps the SQLSTATE codes we care about. Where a code is too
// broad, the constraint name picks the specific sentinel.
func translateSQLState(pgErr *pgconn.PgError) (error, bool) {
	switch pgErr.Code {
	case "23505": // unique violation
		switch pgErr.ConstraintName {
		case "users_email_lower_unique_idx":
			return ErrEmailAlreadyExists, true
		case "wallets_user_currency_unique":
			return ErrWalletAlreadyExists, true
		default:
			return ErrUniqueViolation, true
		}
	case "23503": // foreign key violation
		return ErrForeignKeyViolation, true
	case "23514": // check violation
		switch pgErr.ConstraintName {
		case "users_email_normalized_check":
			return ErrInvalidEmail, true
		case "wallets_balance_nonnegative_check":
			return ErrInvalidBalance, true
		case "wallets_currency_check":
			return ErrUnsupportedCurrency, true
		default:
			return ErrCheckViolation, true
		}
	default:
		return nil, false
	}
}
