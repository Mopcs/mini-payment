package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Mopcs/mini-payment-system/internal/domain"
)

type OutboxRepository struct {
	pool *pgxpool.Pool
}

var ErrOutboxLeaseLost = errors.New("outbox event lease is no longer owned by worker")

func NewOutboxRepository(pool *pgxpool.Pool) *OutboxRepository {
	return &OutboxRepository{pool: pool}
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

func (r *OutboxRepository) ClaimBatch(ctx context.Context, workerID uuid.UUID, batchSize int, lease time.Duration) ([]domain.OutboxEvent, error) {
	if workerID == uuid.Nil || batchSize < 1 || batchSize > 1000 || lease.Microseconds() < 1 {
		return nil, errors.New("worker ID, positive batch size, and positive lease are required")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin outbox claim: %w", err)
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()

	rows, err := tx.Query(ctx, `
		WITH claimable AS (
			SELECT id
			FROM outbox_events
			WHERE published_at IS NULL
			  AND event_type = 'transfer.completed'
			  AND next_attempt_at <= CURRENT_TIMESTAMP
			  AND (locked_until IS NULL OR locked_until <= CURRENT_TIMESTAMP)
			ORDER BY next_attempt_at, created_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE outbox_events AS e
		SET locked_by = $2,
		    locked_until = clock_timestamp() + ($3 * INTERVAL '1 microsecond')
		FROM claimable AS c
		WHERE e.id = c.id
		RETURNING e.id, e.aggregate_id, e.aggregate_type, e.event_type, e.payload, e.attempts`,
		batchSize, workerID.String(), lease.Microseconds())
	if err != nil {
		return nil, translateError("claim outbox events", err)
	}
	events := make([]domain.OutboxEvent, 0, batchSize)
	for rows.Next() {
		var event domain.OutboxEvent
		var payload []byte
		if err := rows.Scan(&event.ID, &event.AggregateID, &event.AggregateType, &event.EventType, &payload, &event.Attempts); err != nil {
			rows.Close()
			return nil, translateError("scan claimed outbox event", err)
		}
		if err := json.Unmarshal(payload, &event.Payload); err != nil {
			rows.Close()
			return nil, fmt.Errorf("decode claimed outbox event %s: invalid payload", event.ID)
		}
		events = append(events, event)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, translateError("iterate claimed outbox events", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, translateError("commit outbox claim", err)
	}
	return events, nil
}

func (r *OutboxRepository) MarkPublished(ctx context.Context, eventID, workerID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE outbox_events
		SET published_at = CURRENT_TIMESTAMP,
		    locked_by = NULL,
		    locked_until = NULL,
		    last_error = NULL
		WHERE id = $1 AND locked_by = $2 AND published_at IS NULL`, eventID, workerID.String())
	if err != nil {
		return translateError("mark outbox event published", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrOutboxLeaseLost
	}
	return nil
}

func (r *OutboxRepository) ExtendLease(ctx context.Context, eventID, workerID uuid.UUID, lease time.Duration) error {
	if lease.Microseconds() < 1 {
		return errors.New("lease must be positive")
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE outbox_events
		SET locked_until = clock_timestamp() + ($3 * INTERVAL '1 microsecond')
		WHERE id = $1 AND locked_by = $2 AND published_at IS NULL AND locked_until > clock_timestamp()`,
		eventID, workerID.String(), lease.Microseconds())
	if err != nil {
		return translateError("extend outbox event lease", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrOutboxLeaseLost
	}
	return nil
}

func (r *OutboxRepository) ScheduleRetry(ctx context.Context, eventID, workerID uuid.UUID, message string, delay time.Duration) error {
	if delay.Microseconds() < 1 {
		return errors.New("retry delay must be positive")
	}
	message = truncateError(message, 2048)
	tag, err := r.pool.Exec(ctx, `
		UPDATE outbox_events
		SET attempts = attempts + 1,
		    last_error = $3,
		    next_attempt_at = CURRENT_TIMESTAMP + ($4 * INTERVAL '1 microsecond'),
		    locked_by = NULL,
		    locked_until = NULL
		WHERE id = $1 AND locked_by = $2 AND published_at IS NULL`, eventID, workerID.String(), message, delay.Microseconds())
	if err != nil {
		return translateError("schedule outbox retry", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrOutboxLeaseLost
	}
	return nil
}

func truncateError(message string, maximum int) string {
	if len(message) <= maximum {
		return message
	}
	message = message[:maximum]
	for len(message) > 0 && !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return message
}
