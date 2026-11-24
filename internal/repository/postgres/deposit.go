package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Mopcs/mini-payment-system/internal/dbutil"
	"github.com/Mopcs/mini-payment-system/internal/domain"
)

type DepositRepository struct {
	pool *pgxpool.Pool
}

func NewDepositRepository(pool *pgxpool.Pool) *DepositRepository {
	return &DepositRepository{pool: pool}
}

// Create atomically claims an idempotency key, updates the wallet, and writes
// both ledger entries. A repeated request returns the original result instead
// of moving the money twice.
func (r *DepositRepository) Create(ctx context.Context, userID, walletID uuid.UUID, amount int64, currency, key string, requestHash []byte) (result domain.Deposit, replay bool, returnedErr error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Deposit{}, false, fmt.Errorf("begin deposit transaction: %w", err)
	}
	defer dbutil.Rollback(ctx, tx, &returnedErr)

	transactionID, createdAt, replay, err := claimLedgerTransaction(ctx, tx, ledgerTransaction{
		id:              uuid.New(),
		userID:          userID,
		transactionType: "deposit",
		idempotencyKey:  key,
		requestHash:     requestHash,
		sourceWalletID:  walletID,
		currency:        currency,
		amount:          amount,
	})
	if err != nil {
		return domain.Deposit{}, false, fmt.Errorf("claim deposit idempotency key: %w", err)
	}
	if replay {
		return replayDeposit(ctx, tx, transactionID)
	}

	balance, err := lockDepositWallet(ctx, tx, userID, walletID, currency, amount)
	if err != nil {
		return domain.Deposit{}, false, err
	}

	balanceAfter, err := applyDeposit(ctx, tx, transactionID, walletID, balance, amount, currency)
	if err != nil {
		return domain.Deposit{}, false, err
	}

	if err := verifyLedgerBalanced(ctx, tx, transactionID, 2); err != nil {
		return domain.Deposit{}, false, err
	}

	result = domain.Deposit{
		TransactionID: transactionID,
		WalletID:      walletID,
		Amount:        amount,
		Currency:      currency,
		BalanceAfter:  balanceAfter,
		CreatedAt:     createdAt,
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Deposit{}, false, translateError("commit deposit transaction", err)
	}
	return result, false, nil
}

// replayDeposit loads the result of a request that was already applied.
func replayDeposit(ctx context.Context, tx pgx.Tx, transactionID uuid.UUID) (domain.Deposit, bool, error) {
	var existing domain.Deposit
	err := tx.QueryRow(ctx, `
		SELECT id, wallet_id, amount, currency, balance_after, created_at
		FROM ledger_transactions
		WHERE id = $1`,
		transactionID,
	).Scan(
		&existing.TransactionID, &existing.WalletID, &existing.Amount,
		&existing.Currency, &existing.BalanceAfter, &existing.CreatedAt,
	)
	if err != nil {
		return domain.Deposit{}, false, translateError("load idempotent deposit", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Deposit{}, false, fmt.Errorf("commit idempotent deposit lookup: %w", err)
	}
	return existing, true, nil
}

// lockDepositWallet holds the wallet row until commit, then checks that the
// deposit is allowed. Returns the balance read under the lock.
func lockDepositWallet(ctx context.Context, tx pgx.Tx, userID, walletID uuid.UUID, currency string, amount int64) (int64, error) {
	var walletCurrency string
	var balance int64

	err := tx.QueryRow(ctx, `
		SELECT currency, balance
		FROM wallets
		WHERE id = $1 AND user_id = $2
		FOR UPDATE`,
		walletID, userID,
	).Scan(&walletCurrency, &balance)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrDepositWalletNotFound
	}
	if err != nil {
		return 0, translateError("lock deposit wallet", err)
	}

	if walletCurrency != currency {
		return 0, ErrDepositCurrencyMismatch
	}
	if amount > math.MaxInt64-balance { // would wrap into a negative balance
		return 0, ErrBalanceOverflow
	}
	return balance, nil
}

// applyDeposit credits the wallet, stores the resulting balance on the
// transaction row, and writes the two ledger entries that keep the books even.
func applyDeposit(ctx context.Context, tx pgx.Tx, transactionID, walletID uuid.UUID, balance, amount int64, currency string) (int64, error) {
	walletAccountID, err := walletLedgerAccount(ctx, tx, walletID, currency)
	if err != nil {
		return 0, err
	}
	fundingAccountID, err := systemFundingAccount(ctx, tx, currency)
	if err != nil {
		return 0, err
	}

	balanceAfter := balance + amount

	if err := updateWalletBalance(ctx, tx, walletID, balance, balanceAfter); err != nil {
		return 0, fmt.Errorf("credit deposit wallet: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE ledger_transactions SET balance_after = $1 WHERE id = $2`,
		balanceAfter, transactionID,
	); err != nil {
		return 0, translateError("store deposit result", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ledger_entries (transaction_id, account_id, amount)
		VALUES ($1, $2, $3), ($1, $4, $5)`,
		transactionID, walletAccountID, amount, fundingAccountID, -amount,
	); err != nil {
		return 0, translateError("write deposit ledger entries", err)
	}

	return balanceAfter, nil
}
