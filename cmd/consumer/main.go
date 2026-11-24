package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Mopcs/mini-payment-system/internal/config"
	"github.com/Mopcs/mini-payment-system/internal/consumer/transfer"
	"github.com/Mopcs/mini-payment-system/internal/infrastructure/postgres"
)

func main() {
	cfg, err := config.Load()
	logger := newLogger(cfg.LogLevel)
	if err != nil {
		logger.Error("load configuration", slog.Any("error", err))
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.NewPool(ctx, cfg)
	if err != nil {
		logger.Error("connect to PostgreSQL", slog.Any("error", err))
		os.Exit(1)
	}
	defer pool.Close()

	if err := pingPool(ctx, pool); err != nil {
		logger.Error("ping PostgreSQL", slog.Any("error", err))
		os.Exit(1)
	}

	consumer, err := transfer.New(pool, consumerSettings(cfg), logger)
	if err != nil {
		logger.Error("configure consumer", slog.Any("error", err))
		os.Exit(1)
	}

	if err := consumer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("consumer stopped", slog.Any("error", err))
		os.Exit(1)
	}
}

// consumerSettings copies the broker topology from the environment config, so
// the consumer package does not depend on config.
func consumerSettings(cfg config.Config) transfer.Settings {
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
	}
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
