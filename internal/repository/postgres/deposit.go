package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Mopcs/mini-payment-system/internal/dbutil"
	"github.com/Mopcs/mini-payment-system/internal/domain"
)

// DepositRequest carries one validated deposit attempt.
type DepositRequest struct {
	UserID         uuid.UUID
	WalletID       uuid.UUID
	Amount         int64
	Currency       string
	IdempotencyKey string
	RequestHash    []byte
}

type DepositRepository struct {
	pool *pgxpool.Pool
}

func NewDepositRepository(pool *pgxpool.Pool) *DepositRepository {
	return &DepositRepository{pool: pool}
}

// Create atomically claims an idempotency key, updates the wallet, and writes
// both ledger entries. A repeated request returns the original result instead
// of moving the money twice.
func (r *DepositRepository) Create(
	ctx context.Context,
	request DepositRequest,
) (result domain.Deposit, replay bool, returnedErr error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Deposit{}, false, fmt.Errorf("begin deposit transaction: %w", err)
	}
	defer dbutil.Rollback(ctx, tx, &returnedErr)

	transactionID, createdAt, replay, err := claimDeposit(ctx, tx, request)
	if err != nil {
		return domain.Deposit{}, false, err
	}
	if replay {
		return replayDeposit(ctx, tx, transactionID)
	}

	balanceAfter, err := applyDeposit(ctx, tx, transactionID, request)
	if err != nil {
		return domain.Deposit{}, false, err
	}
	if err := verifyLedgerBalanced(ctx, tx, transactionID, 2); err != nil {
		return domain.Deposit{}, false, err
	}

	result = domain.Deposit{
		TransactionID: transactionID,
		WalletID:      request.WalletID,
		Amount:        request.Amount,
		Currency:      request.Currency,
		BalanceAfter:  balanceAfter,
		CreatedAt:     createdAt,
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Deposit{}, false, translateError("commit deposit transaction", err)
	}
	return result, false, nil
}

func claimDeposit(
	ctx context.Context,
	tx pgx.Tx,
	request DepositRequest,
) (uuid.UUID, time.Time, bool, error) {
	transactionID, createdAt, replay, err := claimLedgerTransaction(ctx, tx, ledgerTransaction{
		id:              uuid.New(),
		userID:          request.UserID,
		transactionType: "deposit",
		idempotencyKey:  request.IdempotencyKey,
		requestHash:     request.RequestHash,
		sourceWalletID:  request.WalletID,
		currency:        request.Currency,
		amount:          request.Amount,
	})
	if err != nil {
		return uuid.Nil, time.Time{}, false,
			fmt.Errorf("claim deposit idempotency key: %w", err)
	}
	return transactionID, createdAt, replay, nil
}

// replayDeposit loads the result of a request that was already applied.
func replayDeposit(
	ctx context.Context,
	tx pgx.Tx,
	transactionID uuid.UUID,
) (domain.Deposit, bool, error) {
	var existing domain.Deposit
	err := tx.QueryRow(ctx, `
		SELECT id, wallet_id, amount, currency, balance_after, created_at
		FROM ledger_transactions
		WHERE id = $1`,
		transactionID,
	).Scan(
		&existing.TransactionID,
		&existing.WalletID,
		&existing.Amount,
		&existing.Currency,
		&existing.BalanceAfter,
		&existing.CreatedAt,
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
func lockDepositWallet(
	ctx context.Context,
	tx pgx.Tx,
	request DepositRequest,
) (int64, error) {
	var walletCurrency string
	var balance int64

	err := tx.QueryRow(ctx, `
		SELECT currency, balance
		FROM wallets
		WHERE id = $1 AND user_id = $2
		FOR UPDATE`,
		request.WalletID, request.UserID,
	).Scan(&walletCurrency, &balance)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrDepositWalletNotFound
	}
	if err != nil {
		return 0, translateError("lock deposit wallet", err)
	}

	if walletCurrency != request.Currency {
		return 0, ErrDepositCurrencyMismatch
	}
	if request.Amount > math.MaxInt64-balance { // would wrap into a negative balance
		return 0, ErrBalanceOverflow
	}
	return balance, nil
}

// applyDeposit credits the wallet, stores the resulting balance on the
// transaction row, and writes the two ledger entries that keep the books even.
func applyDeposit(
	ctx context.Context,
	tx pgx.Tx,
	transactionID uuid.UUID,
	request DepositRequest,
) (int64, error) {
	balance, err := lockDepositWallet(ctx, tx, request)
	if err != nil {
		return 0, err
	}
	balanceAfter := balance + request.Amount

	walletAccountID, err := walletLedgerAccount(ctx, tx, request.WalletID, request.Currency)
	if err != nil {
		return 0, err
	}
	fundingAccountID, err := systemFundingAccount(ctx, tx, request.Currency)
	if err != nil {
		return 0, err
	}

	if err := updateWalletBalance(ctx, tx, request.WalletID, balance, balanceAfter); err != nil {
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
		transactionID, walletAccountID, request.Amount, fundingAccountID, -request.Amount,
	); err != nil {
		return 0, translateError("write deposit ledger entries", err)
	}

	return balanceAfter, nil
}
