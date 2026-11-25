package http

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/Mopcs/mini-payment-system/internal/observability"
	"github.com/Mopcs/mini-payment-system/internal/transport/http/handlers"
	"github.com/Mopcs/mini-payment-system/internal/transport/http/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type ServerConfig struct {
	Address         string
	Postgres        handlers.Dependency
	Redis           handlers.Dependency
	AuthService     handlers.AuthService
	WalletService   handlers.WalletService
	TransferService handlers.TransferService
	Tokens          middleware.TokenValidator
	Logger          *slog.Logger
	Registry        *prometheus.Registry
	Metrics         *observability.HTTPMetrics
}

func NewServer(config ServerConfig) *http.Server {
	mux := http.NewServeMux()
	registerRoutes(mux, config)
	mux.Handle("GET /metrics", promhttp.HandlerFor(config.Registry, promhttp.HandlerOpts{}))

	return &http.Server{
		Addr:              config.Address,
		Handler:           middleware.Tracing(config.Metrics.Instrument(mux)),
		ReadHeaderTimeout: 5 * time.Second,
	}
}

func registerRoutes(mux *http.ServeMux, config ServerConfig) {
	handlers.NewHealth(config.Postgres, config.Redis, config.Logger).Register(mux)
	handlers.NewAuth(config.AuthService, config.Logger).Register(mux)
	handlers.NewWallet(config.WalletService, config.Logger).Register(mux, config.Tokens)
	handlers.NewTransfers(config.TransferService, config.Logger).Register(mux, config.Tokens)
}
