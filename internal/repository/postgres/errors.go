package postgres

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// These errors let callers handle repository outcomes without depending on PostgreSQL details.
var (
	ErrNotFound            = errors.New("record not found")
	ErrInvalidInput        = errors.New("invalid input")
	ErrUniqueViolation     = errors.New("unique constraint violation")
	ErrEmailAlreadyExists  = errors.New("email already exists")
	ErrWalletAlreadyExists = errors.New("wallet already exists")
	ErrForeignKeyViolation = errors.New("referenced record does not exist")
	ErrCheckViolation      = errors.New("value violates a constraint")
	ErrInvalidEmail        = errors.New("email must be lowercase and trimmed")
	ErrInvalidBalance      = errors.New("wallet balance cannot be negative")
	ErrUnsupportedCurrency = errors.New("currency is not supported")
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

	var kind error
	switch pgErr.Code {
	case "23505":
		switch pgErr.ConstraintName {
		case "users_email_lower_unique_idx":
			kind = ErrEmailAlreadyExists
		case "wallets_user_currency_unique":
			kind = ErrWalletAlreadyExists
		default:
			kind = ErrUniqueViolation
		}
	case "23503":
		kind = ErrForeignKeyViolation
	case "23514":
		switch pgErr.ConstraintName {
		case "users_email_normalized_check":
			kind = ErrInvalidEmail
		case "wallets_balance_nonnegative_check":
			kind = ErrInvalidBalance
		case "wallets_currency_check":
			kind = ErrUnsupportedCurrency
		default:
			kind = ErrCheckViolation
		}
	default:
		return &databaseOperationError{operation: operation, cause: err}
	}

	return fmt.Errorf("%s: %w", operation, kind)
}
