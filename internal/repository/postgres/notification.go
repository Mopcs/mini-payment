package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Mopcs/mini-payment-system/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrInvalidTransferEvent = errors.New("invalid transfer event")

type NotificationRepository struct{ pool *pgxpool.Pool }

func NewNotificationRepository(pool *pgxpool.Pool) *NotificationRepository {
	return &NotificationRepository{pool: pool}
}

// ProcessTransferCompleted records a transfer event and its notifications atomically.
func (r *NotificationRepository) ProcessTransferCompleted(ctx context.Context, eventID uuid.UUID, eventType string, payload domain.TransferCompletedPayload) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin event transaction: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	tag, err := tx.Exec(ctx, `INSERT INTO processed_events (event_id, event_type) VALUES ($1, $2) ON CONFLICT (event_id) DO NOTHING`, eventID, eventType)
	if err != nil {
		return false, translateError("record processed event", err)
	}
	if tag.RowsAffected() == 0 {
		if err := tx.Commit(ctx); err != nil {
			return false, translateError("commit duplicate event", err)
		}
		return true, nil
	}
	if eventType != domain.EventTypeTransferCompleted {
		return false, fmt.Errorf("%w: unsupported event type", ErrInvalidTransferEvent)
	}
	var aggregateID uuid.UUID
	var aggregateType string
	var storedEventType string
	var stored domain.TransferCompletedPayload
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT aggregate_id, aggregate_type, event_type, payload FROM outbox_events WHERE id = $1`, eventID).Scan(&aggregateID, &aggregateType, &storedEventType, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("%w: source event is missing", ErrInvalidTransferEvent)
	}
	if err != nil {
		return false, translateError("load source event", err)
	}
	if err := json.Unmarshal(raw, &stored); err != nil || aggregateType != "transfer" || storedEventType != eventType || aggregateID != payload.TransferID || stored != payload {
		return false, fmt.Errorf("%w: event does not match persisted outbox record", ErrInvalidTransferEvent)
	}
	var sourceUser, destinationUser uuid.UUID
	var sourceWalletID, destinationWalletID uuid.UUID
	var amount int64
	var currency string
	var completedAt time.Time
	err = tx.QueryRow(ctx, `SELECT sw.user_id, dw.user_id, lt.wallet_id, lt.destination_wallet_id, lt.amount, lt.currency, lt.created_at
		FROM ledger_transactions lt JOIN wallets sw ON sw.id = lt.wallet_id
		JOIN wallets dw ON dw.id = lt.destination_wallet_id
		WHERE lt.id = $1 AND lt.transaction_type = 'transfer'`, payload.TransferID).
		Scan(&sourceUser, &destinationUser, &sourceWalletID, &destinationWalletID, &amount, &currency, &completedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("%w: transfer record is missing", ErrInvalidTransferEvent)
	}
	if err != nil {
		return false, translateError("load transfer record", err)
	}
	if sourceWalletID != payload.SourceWalletID || destinationWalletID != payload.DestinationWalletID || amount != payload.Amount || currency != payload.Currency || !completedAt.Equal(payload.CompletedAt) {
		return false, fmt.Errorf("%w: transfer fields do not match", ErrInvalidTransferEvent)
	}
	// The transfer row is authoritative; wallet owners are derived from persisted wallet relations.
	recipients := []struct {
		user      uuid.UUID
		direction string
	}{{sourceUser, "outgoing"}}
	if destinationUser == sourceUser {
		recipients[0].direction = "both"
	} else {
		recipients = append(recipients, struct {
			user      uuid.UUID
			direction string
		}{destinationUser, "incoming"})
	}
	for _, recipient := range recipients {
		body, _ := json.Marshal(domain.TransferNotificationPayload{TransferID: payload.TransferID, Amount: amount, Currency: currency, Direction: recipient.direction})
		if _, err := tx.Exec(ctx, `INSERT INTO notifications (id, user_id, transfer_id, event_id, notification_type, payload) VALUES ($1,$2,$3,$4,$5,$6)`, uuid.New(), recipient.user, payload.TransferID, eventID, domain.NotificationTypeTransferCompleted, body); err != nil {
			return false, translateError("create transfer notification", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, translateError("commit event transaction", err)
	}
	return false, nil
}
