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
	"github.com/Mopcs/mini-payment-system/internal/consumer/transfer"
	"github.com/Mopcs/mini-payment-system/internal/infrastructure/postgres"
	"github.com/Mopcs/mini-payment-system/internal/observability"
)

func main() {
	cfg, err := config.Load()
	logger := newLogger(cfg.LogLevel)
	if err != nil {
		logger.Error("load configuration", slog.Any("error", err))
		os.Exit(1)
	}
	if err := run(cfg, logger); err != nil {
		logger.Error("consumer stopped", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(cfg config.Config, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.NewPool(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	defer pool.Close()

	if err := pingPool(ctx, pool); err != nil {
		return fmt.Errorf("ping PostgreSQL: %w", err)
	}

	consumerMetrics, metricsServer, err := startConsumerMetrics(cfg, pool, logger)
	if err != nil {
		return fmt.Errorf("start metrics server: %w", err)
	}
	defer metricsServer.Shutdown()

	consumer, err := transfer.New(pool, consumerSettings(cfg, consumerMetrics), logger)
	if err != nil {
		return fmt.Errorf("configure consumer: %w", err)
	}

	if err := consumer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("run transfer consumer: %w", err)
	}
	return nil
}

// consumerSettings copies the broker topology from the environment config, so
// the consumer package does not depend on config.
func consumerSettings(
	cfg config.Config,
	metrics *observability.ConsumerMetrics,
) transfer.Settings {
	return transfer.Settings{
		URL:        cfg.RabbitMQURL,
		Exchange:   cfg.RabbitMQExchange,
		Queue:      cfg.RabbitMQQueue,
		RoutingKey: cfg.RabbitMQRoutingKey,

		RetryExchange: cfg.RabbitMQRetryExchange,
		RetryQueue:    cfg.RabbitMQRetryQueue,

		DeadLetterExchange:   cfg.RabbitMQDeadLetterExchange,
		DeadLetterQueue:      cfg.RabbitMQDeadLetterQueue,
		DeadLetterRoutingKey: cfg.RabbitMQDeadLetterRoutingKey,

		RetryDelay:        cfg.RabbitMQRetryDelay,
		MaxRetries:        cfg.ConsumerMaxRetries,
		Prefetch:          cfg.ConsumerPrefetchCount,
		ReconnectInterval: cfg.ConsumerReconnectInterval,
		Metrics:           metrics,
	}
}

func startConsumerMetrics(
	cfg config.Config,
	pool *pgxpool.Pool,
	logger *slog.Logger,
) (*observability.ConsumerMetrics, *observability.MetricsServer, error) {
	registry := observability.NewRegistry()
	registry.MustRegister(observability.NewPGXPoolCollector(pool))
	consumerMetrics := observability.NewConsumerMetrics(registry)
	metricsServer, err := observability.StartMetricsServer(cfg.ConsumerMetricsAddr, registry, logger)
	if err != nil {
		return nil, nil, err
	}
	return consumerMetrics, metricsServer, nil
}

func pingPool(ctx context.Context, pool *pgxpool.Pool) error {
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return pool.Ping(pingCtx)
}

func newLogger(levelName string) *slog.Logger {
	level := slog.LevelInfo
	switch levelName {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}
