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

	"github.com/Mopcs/mini-payment-system/internal/dbutil"
	"github.com/Mopcs/mini-payment-system/internal/domain"
)

// TransferRequest carries one validated transfer attempt.
type TransferRequest struct {
	UserID         uuid.UUID
	SourceID       uuid.UUID
	DestinationID  uuid.UUID
	Amount         int64
	Currency       string
	IdempotencyKey string
	RequestHash    []byte
}

type TransferRepository struct {
	pool   *pgxpool.Pool
	outbox *OutboxRepository
}

func NewTransferRepository(pool *pgxpool.Pool) *TransferRepository {
	return &TransferRepository{pool: pool, outbox: NewOutboxRepository(pool)}
}

// Conditions shared by the history queries. $1 is always the user ID.
const (
	transferVisibleToUser = `(EXISTS (
			SELECT 1 FROM wallets sw WHERE sw.id = lt.wallet_id AND sw.user_id = $1
		) OR EXISTS (
			SELECT 1 FROM wallets dw
			WHERE dw.id = lt.destination_wallet_id AND dw.user_id = $1
		))`
	transferOutgoingFromUser = `EXISTS (
		SELECT 1 FROM wallets sw WHERE sw.id = lt.wallet_id AND sw.user_id = $1)`
	transferIncomingToUser = `EXISTS (
		SELECT 1 FROM wallets dw
		WHERE dw.id = lt.destination_wallet_id AND dw.user_id = $1)`
)

func (r *TransferRepository) List(
	ctx context.Context,
	userID uuid.UUID,
	filter domain.TransferHistoryFilter,
) ([]domain.TransferHistoryItem, int64, error) {
	// One snapshot for both the count and the page, so total always matches items.
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
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

func (r *TransferRepository) Count(
	ctx context.Context,
	userID uuid.UUID,
	filter domain.TransferHistoryFilter,
) (int64, error) {
	where, args := transferHistoryWhere(userID, filter)
	return countTransferHistory(ctx, r.pool, where, args)
}

type transferHistoryQuery interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func countTransferHistory(
	ctx context.Context,
	query transferHistoryQuery,
	where string,
	args []any,
) (int64, error) {
	var total int64
	row := query.QueryRow(ctx, `SELECT count(*) FROM ledger_transactions lt `+where, args...)
	if err := row.Scan(&total); err != nil {
		return 0, translateError("count transfer history", err)
	}
	return total, nil
}

func selectTransferHistory(
	ctx context.Context,
	tx pgx.Tx,
	where string,
	args []any,
	filter domain.TransferHistoryFilter,
) ([]domain.TransferHistoryItem, error) {
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

func transferHistoryWhere(
	userID uuid.UUID,
	filter domain.TransferHistoryFilter,
) (string, []any) {
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

// Create moves the money and queues the outbox event in one transaction, so a
// rolled back transfer can never be published.
func (r *TransferRepository) Create(
	ctx context.Context,
	request TransferRequest,
) (result domain.Transfer, replay bool, returnedErr error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Transfer{}, false, fmt.Errorf("begin transfer transaction: %w", err)
	}
	defer dbutil.Rollback(ctx, tx, &returnedErr)

	transactionID, createdAt, replay, err := claimTransfer(ctx, tx, request)
	if err != nil {
		return domain.Transfer{}, false, err
	}
	if replay {
		return replayTransfer(ctx, tx, transactionID)
	}

	balances, err := settleTransfer(ctx, tx, transactionID, request)
	if err != nil {
		return domain.Transfer{}, false, err
	}

	result = domain.Transfer{
		TransactionID:           transactionID,
		SourceWalletID:          request.SourceID,
		DestinationWalletID:     request.DestinationID,
		Amount:                  request.Amount,
		Currency:                request.Currency,
		SourceBalanceAfter:      balances.source,
		DestinationBalanceAfter: balances.destination,
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

func claimTransfer(
	ctx context.Context,
	tx pgx.Tx,
	request TransferRequest,
) (uuid.UUID, time.Time, bool, error) {
	destinationID := request.DestinationID
	transactionID, createdAt, replay, err := claimLedgerTransaction(ctx, tx, ledgerTransaction{
		id:                  uuid.New(),
		userID:              request.UserID,
		transactionType:     "transfer",
		idempotencyKey:      request.IdempotencyKey,
		requestHash:         request.RequestHash,
		sourceWalletID:      request.SourceID,
		destinationWalletID: &destinationID,
		currency:            request.Currency,
		amount:              request.Amount,
	})
	if err != nil {
		return uuid.Nil, time.Time{}, false,
			fmt.Errorf("claim transfer idempotency key: %w", err)
	}
	return transactionID, createdAt, replay, nil
}

// replayTransfer loads the result of a request that was already applied.
func replayTransfer(
	ctx context.Context,
	tx pgx.Tx,
	transactionID uuid.UUID,
) (domain.Transfer, bool, error) {
	var existing domain.Transfer
	err := tx.QueryRow(ctx, `
		SELECT id, wallet_id, destination_wallet_id, amount, currency,
		       balance_after, destination_balance_after, created_at
		FROM ledger_transactions
		WHERE id = $1`,
		transactionID,
	).Scan(
		&existing.TransactionID,
		&existing.SourceWalletID,
		&existing.DestinationWalletID,
		&existing.Amount,
		&existing.Currency,
		&existing.SourceBalanceAfter,
		&existing.DestinationBalanceAfter,
		&existing.CreatedAt,
	)
	if err != nil {
		return domain.Transfer{}, false, translateError("load transfer result", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Transfer{}, false, fmt.Errorf("commit idempotent transfer lookup: %w", err)
	}
	return existing, true, nil
}

// transferPlan is the validated request in the shape the steps below need.
type transferPlan struct {
	userID        uuid.UUID
	sourceID      uuid.UUID
	destinationID uuid.UUID
	amount        int64
	currency      string
}

func (r TransferRequest) plan() transferPlan {
	return transferPlan{
		userID:        r.UserID,
		sourceID:      r.SourceID,
		destinationID: r.DestinationID,
		amount:        r.Amount,
		currency:      r.Currency,
	}
}

type lockedTransferWallet struct {
	userID   uuid.UUID
	currency string
	balance  int64
}

type transferBalances struct {
	source      int64
	destination int64
}

// settleTransfer locks both wallets, moves the money, and checks the books
// before the caller commits.
func settleTransfer(
	ctx context.Context,
	tx pgx.Tx,
	transactionID uuid.UUID,
	request TransferRequest,
) (transferBalances, error) {
	plan := request.plan()
	source, destination, err := lockTransferWallets(ctx, tx, plan)
	if err != nil {
		return transferBalances{}, err
	}

	balances, err := applyTransfer(ctx, tx, transactionID, plan, source, destination)
	if err != nil {
		return transferBalances{}, err
	}

	if err := verifyLedgerBalanced(ctx, tx, transactionID, 2); err != nil {
		return transferBalances{}, err
	}
	return balances, nil
}

// lockTransferWallets locks both wallet rows in UUID order so A->B and B->A
// cannot deadlock each other, then checks that the transfer is allowed.
func lockTransferWallets(
	ctx context.Context,
	tx pgx.Tx,
	plan transferPlan,
) (lockedTransferWallet, lockedTransferWallet, error) {
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

	err := checkTransferAllowed(plan, source, destination, sourceExists, destinationExists)
	if err != nil {
		return lockedTransferWallet{}, lockedTransferWallet{}, err
	}
	return source, destination, nil
}

// checkTransferAllowed reports the first rule the transfer breaks. A missing
// wallet and somebody else's wallet share one error, so the caller cannot probe
// for wallets they do not own.
func checkTransferAllowed(
	plan transferPlan,
	source, destination lockedTransferWallet,
	sourceExists, destinationExists bool,
) error {
	switch {
	case !sourceExists || !destinationExists:
		return ErrTransferWalletNotFound
	case source.userID != plan.userID:
		return ErrTransferSourceNotOwned
	case source.currency != plan.currency || destination.currency != plan.currency:
		return ErrTransferCurrencyMismatch
	case plan.amount > source.balance:
		return ErrInsufficientFunds
	case plan.amount > math.MaxInt64-destination.balance: // would wrap the destination
		return ErrBalanceOverflow
	}
	return nil
}

func lockTransferWallet(
	ctx context.Context,
	tx pgx.Tx,
	walletID uuid.UUID,
) (lockedTransferWallet, bool, error) {
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
func applyTransfer(
	ctx context.Context,
	tx pgx.Tx,
	transactionID uuid.UUID,
	plan transferPlan,
	source, destination lockedTransferWallet,
) (transferBalances, error) {
	sourceAccountID, err := walletLedgerAccount(ctx, tx, plan.sourceID, plan.currency)
	if err != nil {
		return transferBalances{}, err
	}
	destinationAccountID, err := walletLedgerAccount(ctx, tx, plan.destinationID, plan.currency)
	if err != nil {
		return transferBalances{}, err
	}

	balances := transferBalances{
		source:      source.balance - plan.amount,
		destination: destination.balance + plan.amount,
	}

	err = moveTransferBalances(ctx, tx, plan, source, destination, balances)
	if err != nil {
		return transferBalances{}, err
	}

	err = storeTransferResult(ctx, tx, transactionID, plan, balances)
	if err != nil {
		return transferBalances{}, err
	}

	err = writeTransferEntries(
		ctx, tx, transactionID, plan, sourceAccountID, destinationAccountID,
	)
	if err != nil {
		return transferBalances{}, err
	}
	return balances, nil
}

func moveTransferBalances(
	ctx context.Context,
	tx pgx.Tx,
	plan transferPlan,
	source, destination lockedTransferWallet,
	balances transferBalances,
) error {
	err := updateWalletBalance(ctx, tx, plan.sourceID, source.balance, balances.source)
	if err != nil {
		return fmt.Errorf("debit transfer source: %w", err)
	}

	err = updateWalletBalance(ctx, tx, plan.destinationID, destination.balance, balances.destination)
	if err != nil {
		return fmt.Errorf("credit transfer destination: %w", err)
	}
	return nil
}

func storeTransferResult(
	ctx context.Context,
	tx pgx.Tx,
	transactionID uuid.UUID,
	plan transferPlan,
	balances transferBalances,
) error {
	_, err := tx.Exec(ctx, `
		UPDATE ledger_transactions
		SET balance_after = $1, destination_balance_after = $2
		WHERE id = $3`,
		balances.source, balances.destination, transactionID,
	)
	if err != nil {
		return translateError("store transfer result", err)
	}
	return nil
}

func writeTransferEntries(
	ctx context.Context,
	tx pgx.Tx,
	transactionID uuid.UUID,
	plan transferPlan,
	sourceAccountID, destinationAccountID uuid.UUID,
) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO ledger_entries (transaction_id, account_id, amount)
		VALUES ($1, $2, $3), ($1, $4, $5)`,
		transactionID, sourceAccountID, -plan.amount, destinationAccountID, plan.amount,
	)
	if err != nil {
		return translateError("write transfer ledger entries", err)
	}
	return nil
}

// recordCompleted queues the transfer.completed event in the same transaction,
// so the outbox worker can never publish a transfer that was rolled back.
func (r *TransferRepository) recordCompleted(
	ctx context.Context,
	tx pgx.Tx,
	transfer domain.Transfer,
) error {
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
