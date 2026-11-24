package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Mopcs/mini-payment-system/internal/domain"
)

type OutboxRepository struct{}

func NewOutboxRepository() *OutboxRepository {
	return &OutboxRepository{}
}

func (r *OutboxRepository) Create(ctx context.Context, tx pgx.Tx, event domain.OutboxEvent) error {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return fmt.Errorf("marshal outbox event payload: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO outbox_events (id, aggregate_id, aggregate_type, event_type, payload)
		VALUES ($1, $2, $3, $4, $5::jsonb)`,
		event.ID, event.AggregateID, event.AggregateType, event.EventType, string(payload)); err != nil {
		return translateError("create outbox event", err)
	}
	return nil
}
