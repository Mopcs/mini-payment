package outbox

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Mopcs/mini-payment-system/internal/domain"
	"github.com/Mopcs/mini-payment-system/internal/observability"
	"github.com/Mopcs/mini-payment-system/internal/repository/postgres"
)

type Settings struct {
	BatchSize     int
	PollInterval  time.Duration
	LeaseDuration time.Duration
	MaxBackoff    time.Duration
	Metrics       *observability.OutboxMetrics
}

type Publisher interface {
	Publish(context.Context, domain.OutboxEvent) error
	Close() error
}

type PublisherFactory func(context.Context) (Publisher, error)

type Worker struct {
	repository *postgres.OutboxRepository
	settings   Settings
	factory    PublisherFactory
	logger     *slog.Logger
	workerID   uuid.UUID
	metrics    *observability.OutboxMetrics
}

func New(
	pool *pgxpool.Pool,
	settings Settings,
	factory PublisherFactory,
	logger *slog.Logger,
) (*Worker, error) {
	if settings.BatchSize < 1 || settings.BatchSize > 1000 ||
		settings.PollInterval.Microseconds() < 1 ||
		settings.LeaseDuration.Microseconds() < 1 ||
		settings.MaxBackoff.Microseconds() < 1 {
		return nil, errors.New("outbox worker settings must be positive")
	}
	if factory == nil {
		return nil, errors.New("outbox publisher factory is required")
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &Worker{
		repository: postgres.NewOutboxRepository(pool),
		settings:   settings,
		factory:    factory,
		logger:     logger,
		workerID:   uuid.New(),
		metrics:    settings.Metrics,
	}, nil
}

// Run polls the outbox until the context is cancelled. A dead RabbitMQ
// connection is not fatal: the worker drops it and reconnects on the next tick.
func (w *Worker) Run(ctx context.Context) error {
	w.logger.Info("outbox worker started", slog.String("worker_id", w.workerID.String()))
	defer w.logger.Info("outbox worker stopped", slog.String("worker_id", w.workerID.String()))

	ticker := time.NewTicker(w.settings.PollInterval)
	defer ticker.Stop()

	var publisher Publisher
	defer func() {
		if publisher != nil {
			if err := publisher.Close(); err != nil {
				w.logger.Warn("close RabbitMQ publisher failed", slog.Any("error", err))
			}
		}
	}()

	for ctx.Err() == nil {
		if publisher == nil {
			publisher = w.connect(ctx)
		}

		if publisher != nil && w.processBatch(ctx, publisher) {
			w.disconnect(publisher)
			publisher = nil
		}
		w.updateOldestPendingAge(ctx)

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
	return nil
}

// connect opens a publisher, or returns nil when the broker is not reachable
// yet. The caller simply tries again on the next tick.
func (w *Worker) connect(ctx context.Context) Publisher {
	publisher, err := w.factory(ctx)
	if err != nil || publisher == nil {
		w.logger.Warn("RabbitMQ connection unavailable; will retry",
			slog.String("worker_id", w.workerID.String()))
		return nil
	}
	w.logger.Info("RabbitMQ connected", slog.String("worker_id", w.workerID.String()))
	return publisher
}

func (w *Worker) disconnect(publisher Publisher) {
	if err := publisher.Close(); err != nil {
		w.logger.Warn("close RabbitMQ publisher after publish failure", slog.Any("error", err))
	}
	w.logger.Warn("RabbitMQ publisher will reconnect",
		slog.String("worker_id", w.workerID.String()))
}

// processBatch publishes one claimed batch and reports whether the connection
// looks broken.
func (w *Worker) processBatch(ctx context.Context, publisher Publisher) (reconnect bool) {
	events, err := w.repository.ClaimBatch(
		ctx, w.workerID, w.settings.BatchSize, w.settings.LeaseDuration,
	)
	if err != nil {
		w.logger.Error("claim outbox events failed", slog.Any("error", err))
		return false
	}

	w.logger.Info(
		"claimed outbox events",
		slog.Int("count", len(events)),
		slog.String("worker_id", w.workerID.String()),
	)
	w.metrics.RecordClaimed(len(events))

	for _, event := range events {
		if w.publishEvent(ctx, publisher, event) {
			reconnect = true
		}
	}
	return reconnect
}

// updateOldestPendingAge keeps the backlog age fresh; it is the metric that
// reveals a stalled publisher.
func (w *Worker) updateOldestPendingAge(ctx context.Context) {
	if w.metrics == nil {
		return
	}
	age, err := w.repository.OldestPendingAge(ctx)
	if err != nil {
		w.logger.Warn("read oldest pending outbox age failed", slog.Any("error", err))
		return
	}
	w.metrics.SetOldestPending(age)
}

// publishEvent publishes a single event and reports whether the failure looks
// like a broken channel. Lease and bookkeeping errors are local, so they do not
// ask for a reconnect.
func (w *Worker) publishEvent(
	ctx context.Context,
	publisher Publisher,
	event domain.OutboxEvent,
) (connectionFailed bool) {
	w.metrics.StartJob()
	defer w.metrics.FinishJob()

	// Keep the lease alive while RabbitMQ confirms publication.
	if err := w.repository.ExtendLease(
		ctx, event.ID, w.workerID, w.settings.LeaseDuration,
	); err != nil {
		w.logger.Warn("outbox event lease is no longer owned",
			slog.String("event_id", event.ID.String()))
		return false
	}

	if err := w.publish(ctx, publisher, event); err != nil {
		return w.schedulePublishRetry(ctx, event, err)
	}
	w.metrics.RecordPublished()

	return w.markEventPublished(ctx, event)
}

func (w *Worker) publish(ctx context.Context, publisher Publisher, event domain.OutboxEvent) error {
	publishCtx, cancel := context.WithTimeout(ctx, w.publishTimeout())
	defer cancel()
	return publisher.Publish(publishCtx, event)
}

func (w *Worker) schedulePublishRetry(
	ctx context.Context,
	event domain.OutboxEvent,
	publishErr error,
) bool {
	w.metrics.RecordPublishFailure()

	delay := RetryDelay(event.Attempts+1, w.settings.MaxBackoff)
	if err := w.repository.ScheduleRetry(
		ctx, event.ID, w.workerID, publishErr.Error(), delay,
	); err != nil {
		w.logger.Error("schedule outbox retry failed",
			slog.String("event_id", event.ID.String()),
			slog.Any("error", err))
		return true
	}
	w.metrics.RecordRetry()

	w.logger.Warn("outbox event publication failed",
		slog.String("event_id", event.ID.String()),
		slog.Int("attempt", event.Attempts+1),
		slog.Duration("retry_after", delay))
	return true
}

func (w *Worker) markEventPublished(ctx context.Context, event domain.OutboxEvent) bool {
	if err := w.repository.MarkPublished(ctx, event.ID, w.workerID); err != nil {
		w.logger.Error("mark outbox event published failed",
			slog.String("event_id", event.ID.String()),
			slog.Any("error", err))
		return false
	}

	w.logger.Info("outbox event published",
		slog.String("event_id", event.ID.String()),
		slog.String("event_type", event.EventType))
	return false
}

// publishTimeout keeps a single publish inside the lease, so the worker still
// owns the event when the broker answers.
func (w *Worker) publishTimeout() time.Duration {
	timeout := w.settings.LeaseDuration / 2
	if timeout < time.Microsecond {
		return w.settings.LeaseDuration
	}
	return timeout
}

// RetryDelay doubles the wait starting at one second, capped at maximum.
func RetryDelay(attempt int, maximum time.Duration) time.Duration {
	if maximum <= 0 {
		return 0
	}
	delay := time.Second
	if delay > maximum {
		return maximum
	}
	for i := 1; i < attempt; i++ {
		if delay > maximum/2 {
			return maximum
		}
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
}
