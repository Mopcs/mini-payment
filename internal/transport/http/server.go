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

// Timeouts are the four net/http deadlines, passed through from configuration.
type Timeouts struct {
	ReadHeader time.Duration
	Read       time.Duration
	Write      time.Duration
	Idle       time.Duration
}

type ServerConfig struct {
	Address         string
	Postgres        handlers.Dependency
	Redis           handlers.Dependency
	AuthService     handlers.AuthService
	WalletService   handlers.WalletService
	TransferService handlers.TransferService
	Tokens          middleware.TokenValidator
	Logger          *slog.Logger
	Auditor         *observability.Auditor
	Registry        *prometheus.Registry
	Metrics         *observability.HTTPMetrics
	RateLimit       *middleware.RateLimit
	CORS            middleware.CORSConfig
	Timeouts        Timeouts
}

func NewServer(config ServerConfig) *http.Server {
	mux := http.NewServeMux()
	registerRoutes(mux, config)
	mux.Handle("GET /metrics", promhttp.HandlerFor(config.Registry, promhttp.HandlerOpts{}))

	return &http.Server{
		Addr:              config.Address,
		Handler:           middlewareChain(mux, config),
		ReadHeaderTimeout: config.Timeouts.ReadHeader,
		ReadTimeout:       config.Timeouts.Read,
		WriteTimeout:      config.Timeouts.Write,
		IdleTimeout:       config.Timeouts.Idle,
	}
}

// middlewareChain spells the order out once, from the outside in:
//
//	tracing    - a rejected request still belongs to a trace
//	metrics    - records the status the client actually receives
//	security   - headers apply to every response, including errors
//	cors       - answers preflight before any route matching
//	rate limit - rejects before a handler reads a body
//	mux        - the routes themselves
func middlewareChain(mux *http.ServeMux, config ServerConfig) http.Handler {
	handler := config.RateLimit.Wrap(mux)
	handler = middleware.CORS(config.CORS)(handler)
	handler = middleware.SecurityHeaders(handler)
	handler = config.Metrics.Instrument(handler)
	return middleware.Tracing(handler)
}

func registerRoutes(mux *http.ServeMux, config ServerConfig) {
	handlers.NewHealth(config.Postgres, config.Redis, config.Logger).Register(mux)
	handlers.NewAuth(config.AuthService, config.Logger, config.Auditor).Register(mux)
	handlers.NewWallet(
		config.WalletService, config.Logger, config.Auditor,
	).Register(mux, config.Tokens)
	handlers.NewTransfers(
		config.TransferService, config.Logger, config.Auditor,
	).Register(mux, config.Tokens)
}
