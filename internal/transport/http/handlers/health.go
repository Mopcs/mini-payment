package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

type Dependency interface {
	Ping(context.Context) error
}

type Health struct {
	postgres Dependency
	redis    Dependency
	logger   *slog.Logger
}

func NewHealth(postgres, redis Dependency, logger *slog.Logger) *Health {
	return &Health{postgres: postgres, redis: redis, logger: logger}
}

func (h *Health) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", h.live)
	mux.HandleFunc("GET /ready", h.ready)
}

func (h *Health) live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Health) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if err := h.postgres.Ping(ctx); err != nil {
		h.logger.Error("readiness check failed", slog.String("dependency", "postgres"), slog.Any("error", err))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	if err := h.redis.Ping(ctx); err != nil {
		h.logger.Error("readiness check failed", slog.String("dependency", "redis"), slog.Any("error", err))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func writeJSON(w http.ResponseWriter, status int, body map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
