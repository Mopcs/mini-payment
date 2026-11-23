package handlers

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type pingFunc func(context.Context) error

func (f pingFunc) Ping(ctx context.Context) error { return f(ctx) }

func TestHealthReturnsOK(t *testing.T) {
	handler := newTestHandler(func(context.Context) error { return nil }, func(context.Context) error { return nil })
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"ok"`) {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
}

func TestReadyReturnsOKWhenDependenciesRespond(t *testing.T) {
	handler := newTestHandler(func(context.Context) error { return nil }, func(context.Context) error { return nil })
	request := httptest.NewRequest(http.MethodGet, "/ready", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"ready"`) {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
	}
}

func TestReadyReturnsUnavailableWhenDependencyFails(t *testing.T) {
	handler := newTestHandler(func(context.Context) error { return errors.New("connection failed") }, func(context.Context) error { return nil })
	request := httptest.NewRequest(http.MethodGet, "/ready", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "connection failed") {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
	}
}

func newTestHandler(postgres, redis pingFunc) http.Handler {
	mux := http.NewServeMux()
	NewHealth(postgres, redis, slog.New(slog.NewTextHandler(io.Discard, nil))).Register(mux)
	return mux
}
