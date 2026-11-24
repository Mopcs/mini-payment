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

	"github.com/Mopcs/mini-payment-system/internal/config"
	"github.com/Mopcs/mini-payment-system/internal/infrastructure/postgres"
	"github.com/Mopcs/mini-payment-system/internal/worker/outbox"
)

func main() {
	cfg, err := config.Load()
	logger := newLogger(cfg.LogLevel)
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

	pool, err := postgres.NewPool(ctx, cfg)
	if err != nil {
		return fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	defer pool.Close()
	pingCtx, cancelPing := context.WithTimeout(ctx, 5*time.Second)
	if err := pool.Ping(pingCtx); err != nil {
		cancelPing()
		return fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	cancelPing()

	settings := outbox.Settings{
		BatchSize:     cfg.OutboxBatchSize,
		PollInterval:  cfg.OutboxPollInterval,
		LeaseDuration: cfg.OutboxLeaseDuration,
		MaxBackoff:    cfg.OutboxMaxBackoff,
	}
	worker, err := outbox.New(pool, settings, func(ctx context.Context) (outbox.Publisher, error) {
		return outbox.NewRabbitPublisher(ctx, outbox.RabbitSettings{
			URL:        cfg.RabbitMQURL,
			Exchange:   cfg.RabbitMQExchange,
			Queue:      cfg.RabbitMQQueue,
			RoutingKey: cfg.RabbitMQRoutingKey,
		})
	}, logger)
	if err != nil {
		return fmt.Errorf("configure outbox worker: %w", err)
	}
	if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("run outbox worker: %w", err)
	}
	return nil
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
