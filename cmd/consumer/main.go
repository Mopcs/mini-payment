package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Mopcs/mini-payment-system/internal/config"
	"github.com/Mopcs/mini-payment-system/internal/consumer/transfer"
	"github.com/Mopcs/mini-payment-system/internal/infrastructure/postgres"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load configuration", slog.Any("error", err))
		os.Exit(1)
	}
	level := slog.LevelInfo
	if cfg.LogLevel == "debug" {
		level = slog.LevelDebug
	} else if cfg.LogLevel == "warn" {
		level = slog.LevelWarn
	} else if cfg.LogLevel == "error" {
		level = slog.LevelError
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := postgres.NewPool(ctx, cfg)
	if err != nil {
		logger.Error("connect to PostgreSQL", slog.Any("error", err))
		os.Exit(1)
	}
	defer pool.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = pool.Ping(pingCtx)
	cancel()
	if err != nil {
		logger.Error("ping PostgreSQL", slog.Any("error", err))
		os.Exit(1)
	}
	consumer, err := transfer.New(pool, transfer.Settings{URL: cfg.RabbitMQURL, Exchange: cfg.RabbitMQExchange, Queue: cfg.RabbitMQQueue, RoutingKey: cfg.RabbitMQRoutingKey, RetryExchange: cfg.RabbitMQRetryExchange, RetryQueue: cfg.RabbitMQRetryQueue, DeadLetterExchange: cfg.RabbitMQDeadLetterExchange, DeadLetterQueue: cfg.RabbitMQDeadLetterQueue, DeadLetterRoutingKey: cfg.RabbitMQDeadLetterRoutingKey, RetryDelay: cfg.RabbitMQRetryDelay, MaxRetries: cfg.ConsumerMaxRetries, Prefetch: cfg.ConsumerPrefetchCount, ReconnectInterval: cfg.ConsumerReconnectInterval}, logger)
	if err != nil {
		logger.Error("configure consumer", slog.Any("error", err))
		os.Exit(1)
	}
	if err := consumer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("consumer stopped", slog.Any("error", err))
		os.Exit(1)
	}
}
