package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Mopcs/mini-payment-system/internal/config"
	"github.com/Mopcs/mini-payment-system/internal/infrastructure/postgres"
	redisinfra "github.com/Mopcs/mini-payment-system/internal/infrastructure/redis"
	authservice "github.com/Mopcs/mini-payment-system/internal/service/auth"
	transferservice "github.com/Mopcs/mini-payment-system/internal/service/transfer"
	walletservice "github.com/Mopcs/mini-payment-system/internal/service/wallet"
	httpserver "github.com/Mopcs/mini-payment-system/internal/transport/http"
)

func main() {
	cfg, err := config.Load()
	level := slog.LevelInfo
	if cfg.LogLevel == "debug" {
		level = slog.LevelDebug
	} else if cfg.LogLevel == "warn" {
		level = slog.LevelWarn
	} else if cfg.LogLevel == "error" {
		level = slog.LevelError
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	if err != nil {
		logger.Error("load configuration", slog.Any("error", err))
		os.Exit(1)
	}
	if err := run(cfg, logger); err != nil {
		logger.Error("application stopped with error", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(cfg config.Config, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tokens, err := authservice.NewTokenManager(cfg.JWTSecret, cfg.JWTIssuer, cfg.JWTAccessTokenTTL)
	if err != nil {
		return fmt.Errorf("initialize JWT signing: %w", err)
	}

	pool, err := postgres.NewPool(ctx, cfg)
	if err != nil {
		return fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	defer pool.Close()

	redisClient := redisinfra.NewClient(cfg)
	defer func() {
		if err := redisClient.Close(); err != nil {
			logger.Error("close Redis client", slog.Any("error", err))
		}
	}()

	service := authservice.NewService(pool, tokens)
	wallets := walletservice.NewService(pool)
	transfers := transferservice.NewService(pool)
	server := httpserver.NewServer(cfg.AppAddr, pool, redisinfra.NewPinger(redisClient), service, wallets, transfers, tokens, logger)
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("HTTP server started", slog.String("address", cfg.AppAddr))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
		close(serverErrors)
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-serverErrors:
		if err != nil {
			logger.Error("HTTP server failed", slog.Any("error", err))
			return fmt.Errorf("HTTP server: %w", err)
		}
		return nil
	}

	// Give in-flight requests a moment to finish, but don't hang forever.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", slog.Any("error", err))
		return fmt.Errorf("shut down HTTP server: %w", err)
	}
	logger.Info("HTTP server stopped")
	return nil
}
