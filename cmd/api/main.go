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
	"github.com/Mopcs/mini-payment-system/internal/observability"
	authservice "github.com/Mopcs/mini-payment-system/internal/service/auth"
	transferservice "github.com/Mopcs/mini-payment-system/internal/service/transfer"
	walletservice "github.com/Mopcs/mini-payment-system/internal/service/wallet"
	httpserver "github.com/Mopcs/mini-payment-system/internal/transport/http"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	redis "github.com/redis/go-redis/v9"
)

type apiRuntime struct {
	pool          *pgxpool.Pool
	redisClient   *redis.Client
	metricsServer *observability.MetricsServer
	server        *http.Server
}

type apiServerDependencies struct {
	config      config.Config
	pool        *pgxpool.Pool
	redisClient *redis.Client
	tokens      *authservice.TokenManager
	registry    *prometheus.Registry
	metrics     *observability.HTTPMetrics
	logger      *slog.Logger
}

func main() {
	cfg, err := config.Load()
	logger := newLogger(cfg.LogLevel)
	if err != nil {
		logger.Error("load configuration", slog.Any("error", err))
		os.Exit(1)
	}
	if err := run(cfg, logger); err != nil {
		logger.Error("application stopped with error", slog.Any("error", err))
		os.Exit(1)
	}
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

func run(cfg config.Config, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app, err := newRuntime(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer app.close(logger)

	return serveAPI(ctx, app.server, logger)
}

func newRuntime(ctx context.Context, cfg config.Config, logger *slog.Logger) (*apiRuntime, error) {
	tokens, err := authservice.NewTokenManager(cfg.JWTSecret, cfg.JWTIssuer, cfg.JWTAccessTokenTTL)
	if err != nil {
		return nil, fmt.Errorf("initialize JWT signing: %w", err)
	}

	pool, err := postgres.NewPool(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	redisClient := redisinfra.NewClient(cfg)
	registry := observability.NewRegistry()
	registry.MustRegister(observability.NewPGXPoolCollector(pool))
	httpMetrics := observability.NewHTTPMetrics(registry)
	metricsServer, err := observability.StartMetricsServer(cfg.APIMetricsAddr, registry, logger)
	if err != nil {
		_ = redisClient.Close()
		pool.Close()
		return nil, err
	}
	server := newHTTPServer(apiServerDependencies{
		config:      cfg,
		pool:        pool,
		redisClient: redisClient,
		tokens:      tokens,
		registry:    registry,
		metrics:     httpMetrics,
		logger:      logger,
	})
	return &apiRuntime{
		pool:          pool,
		redisClient:   redisClient,
		metricsServer: metricsServer,
		server:        server,
	}, nil
}

func newHTTPServer(dependencies apiServerDependencies) *http.Server {
	cfg := dependencies.config
	return httpserver.NewServer(httpserver.ServerConfig{
		Address:         cfg.AppAddr,
		Postgres:        dependencies.pool,
		Redis:           redisinfra.NewPinger(dependencies.redisClient),
		AuthService:     authservice.NewService(dependencies.pool, dependencies.tokens),
		WalletService:   walletservice.NewService(dependencies.pool),
		TransferService: transferservice.NewService(dependencies.pool),
		Tokens:          dependencies.tokens,
		Logger:          dependencies.logger,
		Registry:        dependencies.registry,
		Metrics:         dependencies.metrics,
	})
}

func (r *apiRuntime) close(logger *slog.Logger) {
	if err := r.redisClient.Close(); err != nil {
		logger.Error("close Redis client", slog.Any("error", err))
	}
	r.metricsServer.Shutdown()
	r.pool.Close()
}

func serveAPI(ctx context.Context, server *http.Server, logger *slog.Logger) error {
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("HTTP server started", slog.String("address", server.Addr))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
		close(serverErrors)
	}()

	if err := waitForAPI(ctx, serverErrors, logger); err != nil {
		return err
	}
	return shutdownAPI(server, logger)
}

func waitForAPI(ctx context.Context, serverErrors <-chan error, logger *slog.Logger) error {
	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
		return nil
	case err := <-serverErrors:
		if err != nil {
			return fmt.Errorf("HTTP server: %w", err)
		}
		return nil
	}
}

func shutdownAPI(server *http.Server, logger *slog.Logger) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", slog.Any("error", err))
		return fmt.Errorf("shut down HTTP server: %w", err)
	}
	logger.Info("HTTP server stopped")
	return nil
}
