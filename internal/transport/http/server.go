package http

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/Mopcs/mini-payment-system/internal/transport/http/handlers"
	"github.com/Mopcs/mini-payment-system/internal/transport/http/middleware"
)

func NewServer(addr string, postgres, redis handlers.Dependency, authService handlers.AuthService, walletService handlers.WalletService, tokens middleware.TokenValidator, logger *slog.Logger) *http.Server {
	mux := http.NewServeMux()
	handlers.NewHealth(postgres, redis, logger).Register(mux)
	handlers.NewAuth(authService, logger).Register(mux)
	handlers.NewWallet(walletService, logger).Register(mux, tokens)
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
}
