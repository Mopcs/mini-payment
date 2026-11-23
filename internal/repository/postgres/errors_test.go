package postgres

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestTranslateErrorMapsPostgresCodes(t *testing.T) {
	tests := []struct {
		name       string
		code       string
		constraint string
		want       error
	}{
		{name: "no rows", want: ErrNotFound},
		{name: "email duplicate", code: "23505", constraint: "users_email_lower_unique_idx", want: ErrEmailAlreadyExists},
		{name: "wallet duplicate", code: "23505", constraint: "wallets_user_currency_unique", want: ErrWalletAlreadyExists},
		{name: "other unique", code: "23505", want: ErrUniqueViolation},
		{name: "foreign key", code: "23503", want: ErrForeignKeyViolation},
		{name: "email normalization", code: "23514", constraint: "users_email_normalized_check", want: ErrInvalidEmail},
		{name: "negative balance", code: "23514", constraint: "wallets_balance_nonnegative_check", want: ErrInvalidBalance},
		{name: "currency", code: "23514", constraint: "wallets_currency_check", want: ErrUnsupportedCurrency},
		{name: "other check", code: "23514", want: ErrCheckViolation},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cause error
			if tt.name == "no rows" {
				cause = pgx.ErrNoRows
			} else {
				cause = &pgconn.PgError{Code: tt.code, ConstraintName: tt.constraint, Message: "private database detail"}
			}
			got := translateError("test operation", cause)
			if !errors.Is(got, tt.want) {
				t.Fatalf("translateError() = %v, want errors.Is(_, %v)", got, tt.want)
			}
			if got.Error() == "" || strings.Contains(got.Error(), "private database detail") {
				t.Fatalf("error message is empty or exposes PostgreSQL details: %q", got)
			}
		})
	}
}

func TestNormalizeEmail(t *testing.T) {
	if got, want := normalizeEmail("  User.Name@Example.COM  "), "user.name@example.com"; got != want {
		t.Fatalf("normalizeEmail() = %q, want %q", got, want)
	}
}
