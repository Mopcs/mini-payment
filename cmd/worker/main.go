package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Mopcs/mini-payment-system/internal/config"
	"github.com/Mopcs/mini-payment-system/internal/infrastructure/postgres"
	"github.com/Mopcs/mini-payment-system/internal/observability"
	"github.com/Mopcs/mini-payment-system/internal/worker/outbox"
)

func main() {
	cfg, err := config.Load()
	logger := observability.NewLogger(cfg.LogLevel)
	if err != nil {
		logger.Error("load configuration", slog.Any("error", err))
		os.Exit(1)
	}
	if err := run(cfg, logger); err != nil {
		logger.Error("outbox worker stopped with error", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(cfg config.Config, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := observability.StartTracing(
		ctx, cfg.TracingSettings("mini-payment-worker"), logger,
	)
	if err != nil {
		return fmt.Errorf("start tracing: %w", err)
	}
	defer observability.ShutdownTracing(shutdownTracing, logger)

	pool, err := postgres.NewPool(ctx, cfg)
	if err != nil {
		return fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	defer pool.Close()

	if err := pingPool(ctx, pool); err != nil {
		return fmt.Errorf("connect to PostgreSQL: %w", err)
	}

	workerMetrics, metricsServer, err := startWorkerMetrics(cfg, pool, logger)
	if err != nil {
		return err
	}
	defer metricsServer.Shutdown()

	worker, err := newWorker(cfg, pool, logger, workerMetrics)
	if err != nil {
		return err
	}
	if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("run outbox worker: %w", err)
	}
	return nil
}

func startWorkerMetrics(
	cfg config.Config,
	pool *pgxpool.Pool,
	logger *slog.Logger,
) (*observability.OutboxMetrics, *observability.MetricsServer, error) {
	registry := observability.NewRegistry()
	registry.MustRegister(observability.NewPGXPoolCollector(pool))
	workerMetrics := observability.NewOutboxMetrics(registry)
	metricsServer, err := observability.StartMetricsServer(cfg.WorkerMetricsAddr, registry, logger)
	if err != nil {
		return nil, nil, err
	}
	return workerMetrics, metricsServer, nil
}

func newWorker(
	cfg config.Config,
	pool *pgxpool.Pool,
	logger *slog.Logger,
	metrics *observability.OutboxMetrics,
) (*outbox.Worker, error) {
	settings := outbox.Settings{
		BatchSize:     cfg.OutboxBatchSize,
		PollInterval:  cfg.OutboxPollInterval,
		LeaseDuration: cfg.OutboxLeaseDuration,
		MaxBackoff:    cfg.OutboxMaxBackoff,
		Metrics:       metrics,
	}
	return outbox.New(pool, settings, func(ctx context.Context) (outbox.Publisher, error) {
		return outbox.NewRabbitPublisher(ctx, outbox.RabbitSettings{
			URL:        cfg.RabbitMQURL,
			Exchange:   cfg.RabbitMQExchange,
			Queue:      cfg.RabbitMQQueue,
			RoutingKey: cfg.RabbitMQRoutingKey,
		})
	}, logger)
}

func pingPool(ctx context.Context, pool *pgxpool.Pool) error {
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return pool.Ping(pingCtx)
}
