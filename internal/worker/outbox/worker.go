package outbox

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Mopcs/mini-payment-system/internal/domain"
	"github.com/Mopcs/mini-payment-system/internal/repository/postgres"
)

type Settings struct {
	BatchSize     int
	PollInterval  time.Duration
	LeaseDuration time.Duration
	MaxBackoff    time.Duration
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
}

func New(pool *pgxpool.Pool, settings Settings, factory PublisherFactory, logger *slog.Logger) (*Worker, error) {
	if settings.BatchSize < 1 || settings.BatchSize > 1000 || settings.PollInterval.Microseconds() < 1 || settings.LeaseDuration.Microseconds() < 1 || settings.MaxBackoff.Microseconds() < 1 {
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
	}, nil
}

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

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if publisher == nil {
			connected, err := w.factory(ctx)
			if err != nil || connected == nil {
				w.logger.Warn("RabbitMQ connection unavailable; will retry", slog.String("worker_id", w.workerID.String()))
			} else {
				publisher = connected
				w.logger.Info("RabbitMQ connected", slog.String("worker_id", w.workerID.String()))
			}
		}
		if publisher != nil && w.processBatch(ctx, publisher) {
			if err := publisher.Close(); err != nil {
				w.logger.Warn("close RabbitMQ publisher after publish failure", slog.Any("error", err))
			}
			publisher = nil
			w.logger.Warn("RabbitMQ publisher will reconnect", slog.String("worker_id", w.workerID.String()))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (w *Worker) processBatch(ctx context.Context, publisher Publisher) bool {
	events, err := w.repository.ClaimBatch(ctx, w.workerID, w.settings.BatchSize, w.settings.LeaseDuration)
	if err != nil {
		w.logger.Error("claim outbox events failed", slog.Any("error", err))
		return false
	}
	w.logger.Info("claimed outbox events", slog.Int("count", len(events)), slog.String("worker_id", w.workerID.String()))
	connectionFailed := false
	for _, event := range events {
		if err := w.repository.ExtendLease(ctx, event.ID, w.workerID, w.settings.LeaseDuration); err != nil {
			w.logger.Warn("outbox event lease is no longer owned", slog.String("event_id", event.ID.String()))
			continue
		}
		publishTimeout := w.settings.LeaseDuration / 2
		if publishTimeout < time.Microsecond {
			publishTimeout = w.settings.LeaseDuration
		}
		publishCtx, cancelPublish := context.WithTimeout(ctx, publishTimeout)
		publishErr := publisher.Publish(publishCtx, event)
		cancelPublish()
		if publishErr != nil {
			connectionFailed = true
			delay := RetryDelay(event.Attempts+1, w.settings.MaxBackoff)
			if retryErr := w.repository.ScheduleRetry(ctx, event.ID, w.workerID, publishErr.Error(), delay); retryErr != nil {
				w.logger.Error("schedule outbox retry failed", slog.String("event_id", event.ID.String()), slog.Any("error", retryErr))
				continue
			}
			w.logger.Warn("outbox event publication failed", slog.String("event_id", event.ID.String()),
				slog.Int("attempt", event.Attempts+1), slog.Duration("retry_after", delay))
			continue
		}
		if err := w.repository.MarkPublished(ctx, event.ID, w.workerID); err != nil {
			w.logger.Error("mark outbox event published failed", slog.String("event_id", event.ID.String()), slog.Any("error", err))
			continue
		}
		w.logger.Info("outbox event published", slog.String("event_id", event.ID.String()), slog.String("event_type", event.EventType))
	}
	return connectionFailed
}

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
