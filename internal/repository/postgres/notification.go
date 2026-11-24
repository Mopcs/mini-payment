package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Mopcs/mini-payment-system/internal/dbutil"
	"github.com/Mopcs/mini-payment-system/internal/domain"
)

var ErrInvalidTransferEvent = errors.New("invalid transfer event")

type NotificationRepository struct{ pool *pgxpool.Pool }

func NewNotificationRepository(pool *pgxpool.Pool) *NotificationRepository {
	return &NotificationRepository{pool: pool}
}

// ProcessTransferCompleted records a transfer event and its notifications
// atomically. The bool result reports a duplicate delivery, which is not an
// error: the message was already handled by an earlier consumer run.
func (r *NotificationRepository) ProcessTransferCompleted(ctx context.Context, eventID uuid.UUID, eventType string, payload domain.TransferCompletedPayload) (duplicate bool, returnedErr error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin event transaction: %w", err)
	}
	defer dbutil.Rollback(ctx, tx, &returnedErr)

	alreadyProcessed, err := claimEvent(ctx, tx, eventID, eventType)
	if err != nil {
		return false, err
	}
	if alreadyProcessed {
		if err := tx.Commit(ctx); err != nil {
			return false, translateError("commit duplicate event", err)
		}
		return true, nil
	}

	if eventType != domain.EventTypeTransferCompleted {
		return false, fmt.Errorf("%w: unsupported event type", ErrInvalidTransferEvent)
	}

	if err := verifySourceEvent(ctx, tx, eventID, eventType, payload); err != nil {
		return false, err
	}

	record, err := loadTransferRecord(ctx, tx, payload.TransferID)
	if err != nil {
		return false, err
	}
	if !record.matches(payload) {
		return false, fmt.Errorf("%w: transfer fields do not match", ErrInvalidTransferEvent)
	}

	for _, recipient := range record.recipients() {
		if err := insertNotification(ctx, tx, eventID, payload.TransferID, recipient, record); err != nil {
			return false, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, translateError("commit event transaction", err)
	}
	return false, nil
}

// claimEvent marks the event as processed and reports whether it was already
// there. The unique key on event_id makes this safe across consumers.
func claimEvent(ctx context.Context, tx pgx.Tx, eventID uuid.UUID, eventType string) (bool, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO processed_events (event_id, event_type)
		VALUES ($1, $2)
		ON CONFLICT (event_id) DO NOTHING`,
		eventID, eventType)
	if err != nil {
		return false, translateError("record processed event", err)
	}
	return tag.RowsAffected() == 0, nil
}

// verifySourceEvent compares the message with the outbox row that produced it,
// so a forged or edited payload cannot create notifications.
func verifySourceEvent(ctx context.Context, tx pgx.Tx, eventID uuid.UUID, eventType string, payload domain.TransferCompletedPayload) error {
	var aggregateID uuid.UUID
	var aggregateType, storedEventType string
	var raw []byte

	err := tx.QueryRow(ctx, `
		SELECT aggregate_id, aggregate_type, event_type, payload
		FROM outbox_events
		WHERE id = $1`,
		eventID,
	).Scan(&aggregateID, &aggregateType, &storedEventType, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: source event is missing", ErrInvalidTransferEvent)
	}
	if err != nil {
		return translateError("load source event", err)
	}

	var stored domain.TransferCompletedPayload
	if err := json.Unmarshal(raw, &stored); err != nil {
		return fmt.Errorf("%w: stored payload is malformed", ErrInvalidTransferEvent)
	}
	if aggregateType != "transfer" || storedEventType != eventType ||
		aggregateID != payload.TransferID || stored != payload {
		return fmt.Errorf("%w: event does not match persisted outbox record", ErrInvalidTransferEvent)
	}
	return nil
}

// transferRecord is the authoritative transfer row, joined with the owners of
// both wallets.
type transferRecord struct {
	sourceUser          uuid.UUID
	destinationUser     uuid.UUID
	sourceWalletID      uuid.UUID
	destinationWalletID uuid.UUID
	amount              int64
	currency            string
	completedAt         time.Time
}

func loadTransferRecord(ctx context.Context, tx pgx.Tx, transferID uuid.UUID) (transferRecord, error) {
	var record transferRecord

	err := tx.QueryRow(ctx, `
		SELECT sw.user_id, dw.user_id, lt.wallet_id, lt.destination_wallet_id,
		       lt.amount, lt.currency, lt.created_at
		FROM ledger_transactions lt
		JOIN wallets sw ON sw.id = lt.wallet_id
		JOIN wallets dw ON dw.id = lt.destination_wallet_id
		WHERE lt.id = $1 AND lt.transaction_type = 'transfer'`,
		transferID,
	).Scan(
		&record.sourceUser, &record.destinationUser,
		&record.sourceWalletID, &record.destinationWalletID,
		&record.amount, &record.currency, &record.completedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return transferRecord{}, fmt.Errorf("%w: transfer record is missing", ErrInvalidTransferEvent)
	}
	if err != nil {
		return transferRecord{}, translateError("load transfer record", err)
	}
	return record, nil
}

func (t transferRecord) matches(payload domain.TransferCompletedPayload) bool {
	return t.sourceWalletID == payload.SourceWalletID &&
		t.destinationWalletID == payload.DestinationWalletID &&
		t.amount == payload.Amount &&
		t.currency == payload.Currency &&
		t.completedAt.Equal(payload.CompletedAt)
}

type notificationRecipient struct {
	userID    uuid.UUID
	direction string
}

// recipients is derived from the persisted wallet owners, not from the message,
// so a self-transfer produces a single "both" notification.
func (t transferRecord) recipients() []notificationRecipient {
	if t.destinationUser == t.sourceUser {
		return []notificationRecipient{{userID: t.sourceUser, direction: "both"}}
	}
	return []notificationRecipient{
		{userID: t.sourceUser, direction: "outgoing"},
		{userID: t.destinationUser, direction: "incoming"},
	}
}

func insertNotification(ctx context.Context, tx pgx.Tx, eventID, transferID uuid.UUID, recipient notificationRecipient, record transferRecord) error {
	body, err := json.Marshal(domain.TransferNotificationPayload{
		TransferID: transferID,
		Amount:     record.amount,
		Currency:   record.currency,
		Direction:  recipient.direction,
	})
	if err != nil {
		return fmt.Errorf("encode notification payload: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO notifications (id, user_id, transfer_id, event_id, notification_type, payload)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		uuid.New(), recipient.userID, transferID, eventID,
		domain.NotificationTypeTransferCompleted, body,
	); err != nil {
		return translateError("create transfer notification", err)
	}
	return nil
}
