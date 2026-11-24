package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Mopcs/mini-payment-system/internal/dbutil"
	"github.com/Mopcs/mini-payment-system/internal/domain"
)

type TransferRepository struct {
	pool   *pgxpool.Pool
	outbox *OutboxRepository
}

func NewTransferRepository(pool *pgxpool.Pool) *TransferRepository {
	return &TransferRepository{pool: pool, outbox: NewOutboxRepository(pool)}
}

// Conditions shared by the history queries. $1 is always the user ID.
const (
	transferVisibleToUser = `(EXISTS (SELECT 1 FROM wallets sw WHERE sw.id = lt.wallet_id AND sw.user_id = $1)
		OR EXISTS (SELECT 1 FROM wallets dw WHERE dw.id = lt.destination_wallet_id AND dw.user_id = $1))`
	transferOutgoingFromUser = `EXISTS (SELECT 1 FROM wallets sw WHERE sw.id = lt.wallet_id AND sw.user_id = $1)`
	transferIncomingToUser   = `EXISTS (SELECT 1 FROM wallets dw WHERE dw.id = lt.destination_wallet_id AND dw.user_id = $1)`
)

func (r *TransferRepository) List(ctx context.Context, userID uuid.UUID, filter domain.TransferHistoryFilter) ([]domain.TransferHistoryItem, int64, error) {
	// One snapshot for both the count and the page, so total always matches items.
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

	items, err := selectTransferHistory(ctx, tx, where, args, filter)
	if err != nil {
		return nil, 0, err
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

func selectTransferHistory(ctx context.Context, tx pgx.Tx, where string, args []any, filter domain.TransferHistoryFilter) ([]domain.TransferHistoryItem, error) {
	limitArg := len(args) + 1
	offsetArg := limitArg + 1
	args = append(args, filter.Limit, filter.Offset)

	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT lt.id, lt.wallet_id, lt.destination_wallet_id, lt.amount,
		       lt.currency, lt.created_at
		FROM ledger_transactions lt
		%s
		ORDER BY lt.created_at DESC, lt.id DESC
		LIMIT $%d OFFSET $%d`, where, limitArg, offsetArg), args...)
	if err != nil {
		return nil, translateError("list transfer history", err)
	}
	defer rows.Close()

	items := make([]domain.TransferHistoryItem, 0)
	for rows.Next() {
		var item domain.TransferHistoryItem
		item.Status = "completed"
		if err := rows.Scan(
			&item.ID, &item.SourceWalletID, &item.DestinationWalletID,
			&item.Amount, &item.Currency, &item.CreatedAt,
		); err != nil {
			return nil, translateError("scan transfer history", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, translateError("iterate transfer history", err)
	}
	return items, nil
}

func transferHistoryWhere(userID uuid.UUID, filter domain.TransferHistoryFilter) (string, []any) {
	conditions := []string{
		"lt.transaction_type = 'transfer'",
		transferVisibleToUser,
	}
	args := []any{userID}

	switch filter.Direction {
	case "outgoing":
		conditions = append(conditions, transferOutgoingFromUser)
	case "incoming":
		conditions = append(conditions, transferIncomingToUser)
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
	defer dbutil.Rollback(ctx, tx, &returnedErr)

	plan := transferPlan{
		userID:        userID,
		sourceID:      sourceID,
		destinationID: destinationID,
		amount:        amount,
		currency:      currency,
	}

	transactionID, createdAt, replay, err := claimLedgerTransaction(ctx, tx, ledgerTransaction{
		id:                  uuid.New(),
		userID:              userID,
		transactionType:     "transfer",
		idempotencyKey:      key,
		requestHash:         requestHash,
		sourceWalletID:      sourceID,
		destinationWalletID: &destinationID,
		currency:            currency,
		amount:              amount,
	})
	if err != nil {
		return domain.Transfer{}, false, fmt.Errorf("claim transfer idempotency key: %w", err)
	}
	if replay {
		return replayTransfer(ctx, tx, transactionID)
	}

	source, destination, err := lockTransferWallets(ctx, tx, plan)
	if err != nil {
		return domain.Transfer{}, false, err
	}

	sourceAfter, destinationAfter, err := applyTransfer(ctx, tx, transactionID, plan, source, destination)
	if err != nil {
		return domain.Transfer{}, false, err
	}

	if err := verifyLedgerBalanced(ctx, tx, transactionID, 2); err != nil {
		return domain.Transfer{}, false, err
	}

	result = domain.Transfer{
		TransactionID:           transactionID,
		SourceWalletID:          sourceID,
		DestinationWalletID:     destinationID,
		Amount:                  amount,
		Currency:                currency,
		SourceBalanceAfter:      sourceAfter,
		DestinationBalanceAfter: destinationAfter,
		CreatedAt:               createdAt,
	}

	if err := r.recordCompleted(ctx, tx, result); err != nil {
		return domain.Transfer{}, false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Transfer{}, false, translateError("commit transfer transaction", err)
	}
	return result, false, nil
}

// replayTransfer loads the result of a request that was already applied.
func replayTransfer(ctx context.Context, tx pgx.Tx, transactionID uuid.UUID) (domain.Transfer, bool, error) {
	var existing domain.Transfer
	err := tx.QueryRow(ctx, `
		SELECT id, wallet_id, destination_wallet_id, amount, currency,
		       balance_after, destination_balance_after, created_at
		FROM ledger_transactions
		WHERE id = $1`,
		transactionID,
	).Scan(
		&existing.TransactionID, &existing.SourceWalletID, &existing.DestinationWalletID,
		&existing.Amount, &existing.Currency, &existing.SourceBalanceAfter,
		&existing.DestinationBalanceAfter, &existing.CreatedAt,
	)
	if err != nil {
		return domain.Transfer{}, false, translateError("load transfer result", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Transfer{}, false, fmt.Errorf("commit idempotent transfer lookup: %w", err)
	}
	return existing, true, nil
}

// transferPlan is the validated request. Every step below works on the same
// values, and passing them as one struct keeps the helpers short.
type transferPlan struct {
	userID        uuid.UUID
	sourceID      uuid.UUID
	destinationID uuid.UUID
	amount        int64
	currency      string
}

type lockedTransferWallet struct {
	userID   uuid.UUID
	currency string
	balance  int64
}

// lockTransferWallets locks both wallet rows in UUID order so A->B and B->A
// cannot deadlock each other, then checks that the transfer is allowed.
func lockTransferWallets(ctx context.Context, tx pgx.Tx, plan transferPlan) (lockedTransferWallet, lockedTransferWallet, error) {
	first, second := plan.sourceID, plan.destinationID
	if bytes.Compare(first[:], second[:]) > 0 {
		first, second = second, first
	}

	locked := make(map[uuid.UUID]lockedTransferWallet, 2)
	for _, walletID := range []uuid.UUID{first, second} {
		wallet, found, err := lockTransferWallet(ctx, tx, walletID)
		if err != nil {
			return lockedTransferWallet{}, lockedTransferWallet{}, err
		}
		if found {
			locked[walletID] = wallet
		}
	}

	source, sourceExists := locked[plan.sourceID]
	destination, destinationExists := locked[plan.destinationID]

	// A missing wallet and somebody else's wallet report the same error, so the
	// caller cannot probe for wallets they do not own.
	if !sourceExists || !destinationExists {
		return lockedTransferWallet{}, lockedTransferWallet{}, ErrTransferWalletNotFound
	}
	if source.userID != plan.userID {
		return lockedTransferWallet{}, lockedTransferWallet{}, ErrTransferSourceNotOwned
	}
	if source.currency != plan.currency || destination.currency != plan.currency {
		return lockedTransferWallet{}, lockedTransferWallet{}, ErrTransferCurrencyMismatch
	}
	if plan.amount > source.balance {
		return lockedTransferWallet{}, lockedTransferWallet{}, ErrInsufficientFunds
	}
	if plan.amount > math.MaxInt64-destination.balance { // would wrap the destination
		return lockedTransferWallet{}, lockedTransferWallet{}, ErrBalanceOverflow
	}
	return source, destination, nil
}

func lockTransferWallet(ctx context.Context, tx pgx.Tx, walletID uuid.UUID) (lockedTransferWallet, bool, error) {
	var wallet lockedTransferWallet
	err := tx.QueryRow(ctx, `
		SELECT user_id, currency, balance
		FROM wallets
		WHERE id = $1
		FOR UPDATE`,
		walletID,
	).Scan(&wallet.userID, &wallet.currency, &wallet.balance)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedTransferWallet{}, false, nil
	}
	if err != nil {
		return lockedTransferWallet{}, false, translateError("lock transfer wallet", err)
	}
	return wallet, true, nil
}

// applyTransfer moves the money: both wallet balances, the transaction row, and
// the two ledger entries.
func applyTransfer(ctx context.Context, tx pgx.Tx, transactionID uuid.UUID, plan transferPlan, source, destination lockedTransferWallet) (int64, int64, error) {
	sourceAccountID, err := walletLedgerAccount(ctx, tx, plan.sourceID, plan.currency)
	if err != nil {
		return 0, 0, err
	}
	destinationAccountID, err := walletLedgerAccount(ctx, tx, plan.destinationID, plan.currency)
	if err != nil {
		return 0, 0, err
	}

	sourceAfter := source.balance - plan.amount
	destinationAfter := destination.balance + plan.amount

	if err := updateWalletBalance(ctx, tx, plan.sourceID, source.balance, sourceAfter); err != nil {
		return 0, 0, fmt.Errorf("debit transfer source: %w", err)
	}
	if err := updateWalletBalance(ctx, tx, plan.destinationID, destination.balance, destinationAfter); err != nil {
		return 0, 0, fmt.Errorf("credit transfer destination: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE ledger_transactions
		SET balance_after = $1, destination_balance_after = $2
		WHERE id = $3`,
		sourceAfter, destinationAfter, transactionID,
	); err != nil {
		return 0, 0, translateError("store transfer result", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO ledger_entries (transaction_id, account_id, amount)
		VALUES ($1, $2, $3), ($1, $4, $5)`,
		transactionID, sourceAccountID, -plan.amount, destinationAccountID, plan.amount,
	); err != nil {
		return 0, 0, translateError("write transfer ledger entries", err)
	}

	return sourceAfter, destinationAfter, nil
}

// recordCompleted queues the transfer.completed event in the same transaction,
// so the outbox worker can never publish a transfer that was rolled back.
func (r *TransferRepository) recordCompleted(ctx context.Context, tx pgx.Tx, transfer domain.Transfer) error {
	return r.outbox.Create(ctx, tx, domain.OutboxEvent{
		ID:            uuid.New(),
		AggregateID:   transfer.TransactionID,
		AggregateType: "transfer",
		EventType:     domain.EventTypeTransferCompleted,
		Payload: domain.TransferCompletedPayload{
			TransferID:          transfer.TransactionID,
			SourceWalletID:      transfer.SourceWalletID,
			DestinationWalletID: transfer.DestinationWalletID,
			Amount:              transfer.Amount,
			Currency:            transfer.Currency,
			CompletedAt:         transfer.CreatedAt,
		},
	})
}
