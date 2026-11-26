package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/Mopcs/mini-payment-system/internal/config"
	"github.com/Mopcs/mini-payment-system/internal/infrastructure/postgres"
	redisinfra "github.com/Mopcs/mini-payment-system/internal/infrastructure/redis"
	"github.com/Mopcs/mini-payment-system/internal/observability"
	"github.com/Mopcs/mini-payment-system/internal/ratelimit"
	authservice "github.com/Mopcs/mini-payment-system/internal/service/auth"
	transferservice "github.com/Mopcs/mini-payment-system/internal/service/transfer"
	walletservice "github.com/Mopcs/mini-payment-system/internal/service/wallet"
	"github.com/Mopcs/mini-payment-system/internal/shutdown"
	httpserver "github.com/Mopcs/mini-payment-system/internal/transport/http"
	"github.com/Mopcs/mini-payment-system/internal/transport/http/middleware"
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
	auditor     *observability.Auditor
	rateLimit   *middleware.RateLimit
	logger      *slog.Logger
}

func main() {
	cfg, err := config.Load()
	logger := observability.NewLogger(cfg.LogLevel)
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
	ctx, stop := shutdown.NotifyContext()
	defer stop()

	finish := shutdown.NewWatchdog(cfg.ShutdownTimeout, logger).Start(ctx)
	defer finish()

	shutdownTracing, err := observability.StartTracing(
		ctx, cfg.TracingSettings("mini-payment-api"), logger,
	)
	if err != nil {
		return fmt.Errorf("start tracing: %w", err)
	}
	defer observability.ShutdownTracing(shutdownTracing, logger)

	app, err := newRuntime(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer app.close(logger)

	return serveAPI(ctx, app.server, cfg.ShutdownTimeout, logger)
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
	rateLimitMetrics := observability.NewRateLimitMetrics(registry)
	auditor := observability.NewAuditor(logger)
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
		auditor:     auditor,
		rateLimit:   newRateLimit(cfg, redisClient, rateLimitMetrics, auditor, logger),
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
		Auditor:         dependencies.auditor,
		Registry:        dependencies.registry,
		Metrics:         dependencies.metrics,
		RateLimit:       dependencies.rateLimit,
		CORS: middleware.CORSConfig{
			AllowedOrigins:   dependencies.config.CORSAllowedOrigins,
			AllowCredentials: dependencies.config.CORSAllowCredentials,
		},
		Timeouts: httpserver.Timeouts{
			ReadHeader: dependencies.config.HTTPReadHeaderTimeout,
			Read:       dependencies.config.HTTPReadTimeout,
			Write:      dependencies.config.HTTPWriteTimeout,
			Idle:       dependencies.config.HTTPIdleTimeout,
		},
	})
}

func (r *apiRuntime) close(logger *slog.Logger) {
	if err := r.redisClient.Close(); err != nil {
		logger.Error("close Redis client", slog.Any("error", err))
	}
	r.metricsServer.Shutdown()
	r.pool.Close()
}

func serveAPI(
	ctx context.Context,
	server *http.Server,
	shutdownTimeout time.Duration,
	logger *slog.Logger,
) error {
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
	return shutdownAPI(server, shutdownTimeout, logger)
}

func waitForAPI(ctx context.Context, serverErrors <-chan error, logger *slog.Logger) error {
	select {
	case <-ctx.Done():
		// The watchdog logs the signal; this records that draining started.
		return nil
	case err := <-serverErrors:
		if err != nil {
			return fmt.Errorf("HTTP server: %w", err)
		}
		return nil
	}
}

// shutdownAPI stops accepting new connections and waits for in-flight requests
// to finish. A request that outlives the budget is abandoned: the client sees a
// dropped connection rather than a partial write.
func shutdownAPI(server *http.Server, timeout time.Duration, logger *slog.Logger) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", slog.Any("error", err))
		return fmt.Errorf("shut down HTTP server: %w", err)
	}
	logger.Info("HTTP server stopped")
	return nil
}

// newRateLimit builds the limiter. A nil result means the middleware is
// skipped entirely.
func newRateLimit(
	cfg config.Config,
	client *redis.Client,
	metrics *observability.RateLimitMetrics,
	auditor *observability.Auditor,
	logger *slog.Logger,
) *middleware.RateLimit {
	if !cfg.RateLimitEnabled {
		logger.Info("rate limiting disabled")
		return nil
	}

	policies := rateLimitPolicies(cfg)
	logRateLimitPolicies(cfg, policies, logger)

	return middleware.NewRateLimit(middleware.RateLimitConfig{
		Limiter:  ratelimit.NewRedisLimiter(client),
		Resolver: middleware.NewClientIPResolver(cfg.TrustedProxies),
		Policies: policies,
		Metrics:  metrics,
		Auditor:  auditor,
		Logger:   logger,
	})
}

// rateLimitPolicies maps the exact ServeMux patterns to their budget.
func rateLimitPolicies(cfg config.Config) map[string]ratelimit.Policy {
	authPolicy := ratelimit.Policy{
		Name:   "auth",
		Limit:  cfg.RateLimitAuthLimit,
		Window: cfg.RateLimitAuthWindow,
	}
	financialPolicy := ratelimit.Policy{
		Name:   "financial",
		Limit:  cfg.RateLimitFinancialLimit,
		Window: cfg.RateLimitFinancialWindow,
	}

	return map[string]ratelimit.Policy{
		"POST /api/v1/auth/register":         authPolicy,
		"POST /api/v1/auth/login":            authPolicy,
		"POST /api/v1/wallets/{id}/deposits": financialPolicy,
		"POST /api/v1/transfers":             financialPolicy,
	}
}

func logRateLimitPolicies(
	cfg config.Config,
	policies map[string]ratelimit.Policy,
	logger *slog.Logger,
) {
	auth := policies["POST /api/v1/auth/login"]
	financial := policies["POST /api/v1/transfers"]
	logger.Info("rate limiting enabled",
		slog.Int("auth_limit", auth.Limit),
		slog.Duration("auth_window", auth.Window),
		slog.Int("financial_limit", financial.Limit),
		slog.Duration("financial_window", financial.Window),
		slog.Int("trusted_proxies", len(cfg.TrustedProxies)),
	)
}
