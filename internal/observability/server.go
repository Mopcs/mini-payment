package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type MetricsServer struct {
	server   *http.Server
	listener net.Listener
	logger   *slog.Logger
}

func StartMetricsServer(
	address string,
	registry *prometheus.Registry,
	logger *slog.Logger,
) (*MetricsServer, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listen on metrics address: %w", err)
	}
	server := &http.Server{
		Handler:           promhttp.HandlerFor(registry, promhttp.HandlerOpts{}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	metrics := &MetricsServer{server: server, listener: listener, logger: logger}
	go metrics.serve()
	return metrics, nil
}

func (s *MetricsServer) serve() {
	s.logger.Info("metrics server started", slog.String("address", s.listener.Addr().String()))
	if err := s.server.Serve(s.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		s.logger.Error("metrics server failed", slog.Any("error", err))
	}
}

// Shutdown is safe on a nil server so callers can defer it unconditionally.
func (s *MetricsServer) Shutdown() {
	if s == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.logger.Info("metrics server shutting down")
	if err := s.server.Shutdown(ctx); err != nil {
		s.logger.Warn("metrics server shutdown failed", slog.Any("error", err))
	}
	s.logger.Info("metrics server stopped")
}
