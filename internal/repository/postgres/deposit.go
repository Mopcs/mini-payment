package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Mopcs/mini-payment-system/internal/domain"
)

type DepositRepository struct {
	pool *pgxpool.Pool
}

func NewDepositRepository(pool *pgxpool.Pool) *DepositRepository {
	return &DepositRepository{pool: pool}
}

// Create atomically claims an idempotency key, updates the wallet, and writes both ledger entries.
func (r *DepositRepository) Create(ctx context.Context, userID, walletID uuid.UUID, amount int64, currency, key string, requestHash []byte) (result domain.Deposit, replay bool, returnedErr error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Deposit{}, false, fmt.Errorf("begin deposit transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if rollbackErr := tx.Rollback(rollbackCtx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			returnedErr = errors.Join(returnedErr, fmt.Errorf("rollback deposit transaction: %w", rollbackErr))
		}
	}()

	transactionID := uuid.New()
	var createdAt time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO ledger_transactions
			(id, transaction_type, user_id, idempotency_key, request_hash, wallet_id, currency, amount)
		VALUES ($1, 'deposit', $2, $3, $4, $5, $6, $7)
		ON CONFLICT (user_id, idempotency_key) DO NOTHING
		RETURNING id, created_at`,
		transactionID, userID, key, requestHash, walletID, currency, amount,
	).Scan(&transactionID, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		var existing domain.Deposit
		var existingHash []byte
		err = tx.QueryRow(ctx, `
			SELECT id, wallet_id, amount, currency, balance_after, created_at, request_hash
			FROM ledger_transactions
			WHERE user_id = $1 AND idempotency_key = $2`, userID, key,
		).Scan(&existing.TransactionID, &existing.WalletID, &existing.Amount, &existing.Currency, &existing.BalanceAfter, &existing.CreatedAt, &existingHash)
		if err != nil {
			return domain.Deposit{}, false, translateError("load idempotent deposit", err)
		}
		if !bytes.Equal(existingHash, requestHash) {
			return domain.Deposit{}, false, ErrIdempotencyConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.Deposit{}, false, fmt.Errorf("commit idempotent deposit lookup: %w", err)
		}
		committed = true
		return existing, true, nil
	}
	if err != nil {
		return domain.Deposit{}, false, translateError("claim deposit idempotency key", err)
	}

	var walletCurrency string
	var balance int64
	err = tx.QueryRow(ctx, `
		SELECT currency, balance
		FROM wallets
		WHERE id = $1 AND user_id = $2
		FOR UPDATE`, walletID, userID,
	).Scan(&walletCurrency, &balance)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Deposit{}, false, ErrDepositWalletNotFound
	}
	if err != nil {
		return domain.Deposit{}, false, translateError("lock deposit wallet", err)
	}
	if walletCurrency != currency {
		return domain.Deposit{}, false, ErrDepositCurrencyMismatch
	}
	if amount > math.MaxInt64-balance {
		return domain.Deposit{}, false, ErrBalanceOverflow
	}

	var walletAccountID, fundingAccountID uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT id FROM ledger_accounts
		WHERE account_type = 'wallet' AND wallet_id = $1 AND currency = $2`, walletID, currency,
	).Scan(&walletAccountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Deposit{}, false, ErrLedgerAccountMissing
	}
	if err != nil {
		return domain.Deposit{}, false, translateError("load wallet ledger account", err)
	}
	err = tx.QueryRow(ctx, `
		SELECT id FROM ledger_accounts
		WHERE account_type = 'system_funding' AND currency = $1`, currency,
	).Scan(&fundingAccountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Deposit{}, false, ErrLedgerAccountMissing
	}
	if err != nil {
		return domain.Deposit{}, false, translateError("load system funding account", err)
	}

	newBalance := balance + amount
	err = tx.QueryRow(ctx, `
		UPDATE wallets SET balance = $1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $2 AND user_id = $3
		RETURNING updated_at`, newBalance, walletID, userID,
	).Scan(new(time.Time))
	if err != nil {
		return domain.Deposit{}, false, translateError("update deposit wallet balance", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE ledger_transactions SET balance_after = $1 WHERE id = $2`, newBalance, transactionID); err != nil {
		return domain.Deposit{}, false, translateError("store deposit result", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ledger_entries (transaction_id, account_id, amount)
		VALUES ($1, $2, $3), ($1, $4, $5)`, transactionID, walletAccountID, amount, fundingAccountID, -amount,
	); err != nil {
		return domain.Deposit{}, false, translateError("write deposit ledger entries", err)
	}

	var balanced bool
	if err := tx.QueryRow(ctx, `
		SELECT count(*) >= 2 AND COALESCE(sum(le.amount), 0) = 0
		   AND count(DISTINCT la.currency) = 1
		FROM ledger_entries le
		JOIN ledger_accounts la ON la.id = le.account_id
		WHERE le.transaction_id = $1`, transactionID,
	).Scan(&balanced); err != nil {
		return domain.Deposit{}, false, translateError("verify deposit ledger", err)
	}
	if !balanced {
		return domain.Deposit{}, false, fmt.Errorf("verify deposit ledger: %w", ErrCheckViolation)
	}

	result = domain.Deposit{
		TransactionID: transactionID,
		WalletID:      walletID,
		Amount:        amount,
		Currency:      currency,
		BalanceAfter:  newBalance,
		CreatedAt:     createdAt,
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Deposit{}, false, translateError("commit deposit transaction", err)
	}
	committed = true
	return result, false, nil
}
