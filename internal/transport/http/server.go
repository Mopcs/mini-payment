package http

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/Mopcs/mini-payment-system/internal/transport/http/handlers"
)

func NewServer(addr string, postgres, redis handlers.Dependency, logger *slog.Logger) *http.Server {
	mux := http.NewServeMux()
	handlers.NewHealth(postgres, redis, logger).Register(mux)
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
}
