package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Mopcs/mini-payment-system/internal/domain"
)

type TransferRepository struct {
	pool   *pgxpool.Pool
	outbox *OutboxRepository
}

func NewTransferRepository(pool *pgxpool.Pool) *TransferRepository {
	return &TransferRepository{pool: pool, outbox: NewOutboxRepository(pool)}
}

func (r *TransferRepository) List(ctx context.Context, userID uuid.UUID, filter domain.TransferHistoryFilter) ([]domain.TransferHistoryItem, int64, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, 0, fmt.Errorf("begin transfer history transaction: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	where, args := transferHistoryWhere(userID, filter)
	total, err := countTransferHistory(ctx, tx, where, args)
	if err != nil {
		return nil, 0, err
	}
	args = append(args, filter.Limit, filter.Offset)
	rows, err := tx.Query(ctx, `
		SELECT lt.id, lt.wallet_id, lt.destination_wallet_id, lt.amount,
		       lt.currency, lt.created_at
		FROM ledger_transactions lt
		`+where+`
		ORDER BY lt.created_at DESC, lt.id DESC
		LIMIT $`+fmt.Sprint(len(args)-1)+` OFFSET $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, 0, translateError("list transfer history", err)
	}
	defer rows.Close()
	items := make([]domain.TransferHistoryItem, 0)
	for rows.Next() {
		var item domain.TransferHistoryItem
		item.Status = "completed"
		if err := rows.Scan(&item.ID, &item.SourceWalletID, &item.DestinationWalletID,
			&item.Amount, &item.Currency, &item.CreatedAt); err != nil {
			return nil, 0, translateError("scan transfer history", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, translateError("iterate transfer history", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, translateError("commit transfer history transaction", err)
	}
	return items, total, nil
}

func (r *TransferRepository) Count(ctx context.Context, userID uuid.UUID, filter domain.TransferHistoryFilter) (int64, error) {
	where, args := transferHistoryWhere(userID, filter)
	return countTransferHistory(ctx, r.pool, where, args)
}

type transferHistoryQuery interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func countTransferHistory(ctx context.Context, query transferHistoryQuery, where string, args []any) (int64, error) {
	var total int64
	if err := query.QueryRow(ctx, `SELECT count(*) FROM ledger_transactions lt `+where, args...).Scan(&total); err != nil {
		return 0, translateError("count transfer history", err)
	}
	return total, nil
}

func transferHistoryWhere(userID uuid.UUID, filter domain.TransferHistoryFilter) (string, []any) {
	conditions := []string{
		"lt.transaction_type = 'transfer'",
		"(EXISTS (SELECT 1 FROM wallets sw WHERE sw.id = lt.wallet_id AND sw.user_id = $1) OR EXISTS (SELECT 1 FROM wallets dw WHERE dw.id = lt.destination_wallet_id AND dw.user_id = $1))",
	}
	args := []any{userID}
	if filter.Direction == "outgoing" {
		conditions = append(conditions, "EXISTS (SELECT 1 FROM wallets sw WHERE sw.id = lt.wallet_id AND sw.user_id = $1)")
	} else if filter.Direction == "incoming" {
		conditions = append(conditions, "EXISTS (SELECT 1 FROM wallets dw WHERE dw.id = lt.destination_wallet_id AND dw.user_id = $1)")
	}
	if filter.Currency != "" {
		args = append(args, filter.Currency)
		conditions = append(conditions, fmt.Sprintf("lt.currency = $%d", len(args)))
	}
	return "WHERE " + strings.Join(conditions, " AND "), args
}

func (r *TransferRepository) Create(ctx context.Context, userID, sourceID, destinationID uuid.UUID, amount int64, currency, key string, requestHash []byte) (result domain.Transfer, replay bool, returnedErr error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Transfer{}, false, fmt.Errorf("begin transfer transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if rollbackErr := tx.Rollback(rollbackCtx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			returnedErr = errors.Join(returnedErr, fmt.Errorf("rollback transfer transaction: %w", rollbackErr))
		}
	}()

	transactionID := uuid.New()
	destinationPointer := &destinationID
	transactionID, createdAt, replay, err := claimLedgerTransaction(ctx, tx, userID, transactionID, "transfer", key, requestHash, sourceID, destinationPointer, currency, amount)
	if err != nil {
		return domain.Transfer{}, false, fmt.Errorf("claim transfer idempotency key: %w", err)
	}
	if replay {
		var existing domain.Transfer
		err = tx.QueryRow(ctx, `
			SELECT id, wallet_id, destination_wallet_id, amount, currency,
			       balance_after, destination_balance_after, created_at
			FROM ledger_transactions
			WHERE id = $1`, transactionID,
		).Scan(&existing.TransactionID, &existing.SourceWalletID, &existing.DestinationWalletID,
			&existing.Amount, &existing.Currency, &existing.SourceBalanceAfter,
			&existing.DestinationBalanceAfter, &existing.CreatedAt)
		if err != nil {
			return domain.Transfer{}, false, translateError("load transfer result", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.Transfer{}, false, fmt.Errorf("commit idempotent transfer lookup: %w", err)
		}
		committed = true
		return existing, true, nil
	}

	// Lock wallets in UUID order so opposite-direction transfers use the same order.
	lockOrder := []uuid.UUID{sourceID, destinationID}
	if bytes.Compare(lockOrder[0][:], lockOrder[1][:]) > 0 {
		lockOrder[0], lockOrder[1] = lockOrder[1], lockOrder[0]
	}
	locked := make(map[uuid.UUID]lockedTransferWallet, 2)
	for _, walletID := range lockOrder {
		var wallet lockedTransferWallet
		err := tx.QueryRow(ctx, `
			SELECT user_id, currency, balance
			FROM wallets
			WHERE id = $1
			FOR UPDATE`, walletID,
		).Scan(&wallet.userID, &wallet.currency, &wallet.balance)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return domain.Transfer{}, false, translateError("lock transfer wallet", err)
		}
		locked[walletID] = wallet
	}
	source, sourceExists := locked[sourceID]
	destination, destinationExists := locked[destinationID]
	if !sourceExists || !destinationExists {
		return domain.Transfer{}, false, ErrTransferWalletNotFound
	}
	if source.userID != userID {
		return domain.Transfer{}, false, ErrTransferSourceNotOwned
	}
	if source.currency != currency || destination.currency != currency {
		return domain.Transfer{}, false, ErrTransferCurrencyMismatch
	}
	if amount > source.balance {
		return domain.Transfer{}, false, ErrInsufficientFunds
	}
	if amount > math.MaxInt64-destination.balance {
		return domain.Transfer{}, false, ErrBalanceOverflow
	}

	sourceAccountID, err := transferWalletAccount(ctx, tx, sourceID, currency)
	if err != nil {
		return domain.Transfer{}, false, err
	}
	destinationAccountID, err := transferWalletAccount(ctx, tx, destinationID, currency)
	if err != nil {
		return domain.Transfer{}, false, err
	}

	sourceBalanceAfter := source.balance - amount
	destinationBalanceAfter := destination.balance + amount
	if err := updateTransferBalance(ctx, tx, sourceID, source.balance, sourceBalanceAfter); err != nil {
		return domain.Transfer{}, false, fmt.Errorf("debit transfer source: %w", err)
	}
	if err := updateTransferBalance(ctx, tx, destinationID, destination.balance, destinationBalanceAfter); err != nil {
		return domain.Transfer{}, false, fmt.Errorf("credit transfer destination: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE ledger_transactions
		SET balance_after = $1, destination_balance_after = $2
		WHERE id = $3`, sourceBalanceAfter, destinationBalanceAfter, transactionID); err != nil {
		return domain.Transfer{}, false, translateError("store transfer result", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ledger_entries (transaction_id, account_id, amount)
		VALUES ($1, $2, $3), ($1, $4, $5)`, transactionID, sourceAccountID, -amount, destinationAccountID, amount,
	); err != nil {
		return domain.Transfer{}, false, translateError("write transfer ledger entries", err)
	}

	var balanced bool
	if err := tx.QueryRow(ctx, `
		SELECT count(*) = 2 AND COALESCE(sum(le.amount), 0) = 0
		   AND count(DISTINCT la.currency) = 1
		FROM ledger_entries le
		JOIN ledger_accounts la ON la.id = le.account_id
		WHERE le.transaction_id = $1`, transactionID,
	).Scan(&balanced); err != nil {
		return domain.Transfer{}, false, translateError("verify transfer ledger", err)
	}
	if !balanced {
		return domain.Transfer{}, false, fmt.Errorf("verify transfer ledger: %w", ErrCheckViolation)
	}

	result = domain.Transfer{
		TransactionID:           transactionID,
		SourceWalletID:          sourceID,
		DestinationWalletID:     destinationID,
		Amount:                  amount,
		Currency:                currency,
		SourceBalanceAfter:      sourceBalanceAfter,
		DestinationBalanceAfter: destinationBalanceAfter,
		CreatedAt:               createdAt,
	}
	if err := r.outbox.Create(ctx, tx, domain.OutboxEvent{
		ID:            uuid.New(),
		AggregateID:   transactionID,
		AggregateType: "transfer",
		EventType:     domain.EventTypeTransferCompleted,
		Payload: domain.TransferCompletedPayload{
			TransferID:          transactionID,
			SourceWalletID:      sourceID,
			DestinationWalletID: destinationID,
			Amount:              amount,
			Currency:            currency,
			CompletedAt:         createdAt,
		},
	}); err != nil {
		return domain.Transfer{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Transfer{}, false, translateError("commit transfer transaction", err)
	}
	committed = true
	return result, false, nil
}

type lockedTransferWallet struct {
	userID   uuid.UUID
	currency string
	balance  int64
}

func transferWalletAccount(ctx context.Context, tx pgx.Tx, walletID uuid.UUID, currency string) (uuid.UUID, error) {
	var accountID uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT id FROM ledger_accounts
		WHERE account_type = 'wallet' AND wallet_id = $1 AND currency = $2`, walletID, currency,
	).Scan(&accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrLedgerAccountMissing
	}
	if err != nil {
		return uuid.Nil, translateError("load transfer wallet ledger account", err)
	}
	return accountID, nil
}

func updateTransferBalance(ctx context.Context, tx pgx.Tx, walletID uuid.UUID, previous, updated int64) error {
	tag, err := tx.Exec(ctx, `
		UPDATE wallets SET balance = $1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $2 AND balance = $3`, updated, walletID, previous)
	if err != nil {
		return translateError("update transfer wallet balance", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrTransferWalletNotFound
	}
	return nil
}
