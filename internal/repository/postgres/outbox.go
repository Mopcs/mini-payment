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
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/Mopcs/mini-payment-system/internal/domain"
	"github.com/Mopcs/mini-payment-system/internal/observability"
)

type OutboxRepository struct {
	pool *pgxpool.Pool
}

var ErrOutboxLeaseLost = errors.New("outbox event lease is no longer owned by worker")

func NewOutboxRepository(pool *pgxpool.Pool) *OutboxRepository {
	return &OutboxRepository{pool: pool}
}

func (r *OutboxRepository) Create(ctx context.Context, tx pgx.Tx, event domain.OutboxEvent) error {
	ctx, span := observability.Tracer().Start(ctx, "outbox.enqueue")
	defer span.End()

	payload, err := json.Marshal(event.Payload)
	if err != nil {
		observability.RecordError(span, err)
		return fmt.Errorf("marshal outbox event payload: %w", err)
	}

	// The trace context is persisted next to the row, because the worker runs in
	// another process and cannot inherit the API's in-memory context.
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)

	_, err = tx.Exec(ctx, `
		INSERT INTO outbox_events
			(id, aggregate_id, aggregate_type, event_type, payload, trace_parent, trace_state)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7)`,
		event.ID, event.AggregateID, event.AggregateType, event.EventType,
		string(payload), carrier.Get("traceparent"), carrier.Get("tracestate"),
	)
	if err != nil {
		observability.RecordError(span, err)
		return translateError("create outbox event", err)
	}
	return nil
}

// claimOutboxEventsQuery takes due events and stamps a lease on them in one
// statement. $1 is the batch size, $2 the worker ID, $3 the lease in
// microseconds.
const claimOutboxEventsQuery = `
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
	RETURNING e.id, e.aggregate_id, e.aggregate_type, e.event_type, e.payload, e.attempts,
	          e.trace_parent, e.trace_state`

// ClaimBatch locks a batch of due events for this worker. SKIP LOCKED lets
// several workers pull from the same table without queueing behind each other.
func (r *OutboxRepository) ClaimBatch(
	ctx context.Context,
	workerID uuid.UUID,
	batchSize int,
	lease time.Duration,
) ([]domain.OutboxEvent, error) {
	if workerID == uuid.Nil || batchSize < 1 || batchSize > 1000 || lease.Microseconds() < 1 {
		return nil, errors.New("worker ID, positive batch size, and positive lease are required")
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin outbox claim: %w", err)
	}
	defer rollbackQuietly(ctx, tx)

	rows, err := tx.Query(
		ctx, claimOutboxEventsQuery, batchSize, workerID.String(), lease.Microseconds(),
	)
	if err != nil {
		return nil, translateError("claim outbox events", err)
	}

	events, err := collectClaimedEvents(rows, batchSize)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, translateError("commit outbox claim", err)
	}
	return events, nil
}

// rollbackQuietly releases the transaction when the caller is already returning
// an error, so the rollback failure would only hide the original one.
func rollbackQuietly(ctx context.Context, tx pgx.Tx) {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(rollbackCtx)
}

func collectClaimedEvents(rows pgx.Rows, capacity int) ([]domain.OutboxEvent, error) {
	defer rows.Close()

	events, err := scanClaimedEvents(rows, capacity)
	if err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, translateError("iterate claimed outbox events", err)
	}
	return events, nil
}

func scanClaimedEvents(rows pgx.Rows, capacity int) ([]domain.OutboxEvent, error) {
	events := make([]domain.OutboxEvent, 0, capacity)
	for rows.Next() {
		var event domain.OutboxEvent
		var payload []byte
		err := rows.Scan(
			&event.ID,
			&event.AggregateID,
			&event.AggregateType,
			&event.EventType,
			&payload,
			&event.Attempts,
			&event.TraceParent,
			&event.TraceState,
		)
		if err != nil {
			return nil, translateError("scan claimed outbox event", err)
		}
		if err := json.Unmarshal(payload, &event.Payload); err != nil {
			return nil, fmt.Errorf("decode claimed outbox event %s: invalid payload", event.ID)
		}
		events = append(events, event)
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
	// No row updated means the lease ran out and another worker took the event.
	if tag.RowsAffected() != 1 {
		return ErrOutboxLeaseLost
	}
	return nil
}

func (r *OutboxRepository) OldestPendingAge(ctx context.Context) (time.Duration, error) {
	var seconds float64
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(EXTRACT(EPOCH FROM CURRENT_TIMESTAMP - MIN(created_at)), 0)
		FROM outbox_events
		WHERE published_at IS NULL`).Scan(&seconds)
	if err != nil {
		return 0, translateError("read oldest pending outbox event", err)
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

func (r *OutboxRepository) ExtendLease(
	ctx context.Context,
	eventID, workerID uuid.UUID,
	lease time.Duration,
) error {
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

func (r *OutboxRepository) ScheduleRetry(
	ctx context.Context,
	eventID, workerID uuid.UUID,
	message string,
	delay time.Duration,
) error {
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
		WHERE id = $1 AND locked_by = $2 AND published_at IS NULL`,
		eventID, workerID.String(), message, delay.Microseconds(),
	)
	if err != nil {
		return translateError("schedule outbox retry", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrOutboxLeaseLost
	}
	return nil
}

// truncateError cuts at a rune boundary. Postgres will not accept half a UTF-8
// sequence in last_error, and error messages do get cut in the middle.
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
