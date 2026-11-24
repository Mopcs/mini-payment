package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// walletLedgerAccount returns the ledger account that backs a wallet.
func walletLedgerAccount(
	ctx context.Context,
	tx pgx.Tx,
	walletID uuid.UUID,
	currency string,
) (uuid.UUID, error) {
	var accountID uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT id FROM ledger_accounts
		WHERE account_type = 'wallet' AND wallet_id = $1 AND currency = $2`,
		walletID, currency,
	).Scan(&accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrLedgerAccountMissing
	}
	if err != nil {
		return uuid.Nil, translateError("load wallet ledger account", err)
	}
	return accountID, nil
}

// systemFundingAccount returns the account that funds deposits for a currency.
func systemFundingAccount(ctx context.Context, tx pgx.Tx, currency string) (uuid.UUID, error) {
	var accountID uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT id FROM ledger_accounts
		WHERE account_type = 'system_funding' AND currency = $1`,
		currency,
	).Scan(&accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrLedgerAccountMissing
	}
	if err != nil {
		return uuid.Nil, translateError("load system funding account", err)
	}
	return accountID, nil
}

// verifyLedgerBalanced is the last check before commit: the transaction must
// have exactly the expected number of entries, they must cancel out, and they
// must all belong to one currency.
func verifyLedgerBalanced(
	ctx context.Context,
	tx pgx.Tx,
	transactionID uuid.UUID,
	expectedEntries int,
) error {
	var balanced bool
	err := tx.QueryRow(ctx, `
		SELECT count(*) = $2
		   AND COALESCE(sum(le.amount), 0) = 0
		   AND count(DISTINCT la.currency) = 1
		FROM ledger_entries le
		JOIN ledger_accounts la ON la.id = le.account_id
		WHERE le.transaction_id = $1`,
		transactionID, expectedEntries,
	).Scan(&balanced)
	if err != nil {
		return translateError("verify ledger balance", err)
	}
	if !balanced {
		return fmt.Errorf("verify ledger balance: %w", ErrCheckViolation)
	}
	return nil
}

// updateWalletBalance writes the new balance only if it still matches the value
// read under the lock. Zero rows means somebody else moved the wallet, so we
// back out instead of overwriting their change.
func updateWalletBalance(
	ctx context.Context,
	tx pgx.Tx,
	walletID uuid.UUID,
	previous, updated int64,
) error {
	tag, err := tx.Exec(ctx, `
		UPDATE wallets SET balance = $1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $2 AND balance = $3`,
		updated, walletID, previous)
	if err != nil {
		return translateError("update wallet balance", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrWalletBalanceChanged
	}
	return nil
}
