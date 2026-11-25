package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Mopcs/mini-payment-system/internal/ratelimit"
)

const transferPattern = "POST /api/v1/transfers"

type stubLimiter struct {
	decision ratelimit.Decision
	err      error

	calls  int
	key    string
	policy ratelimit.Policy
}

func (s *stubLimiter) Allow(
	_ context.Context,
	key string,
	policy ratelimit.Policy,
) (ratelimit.Decision, error) {
	s.calls++
	s.key = key
	s.policy = policy
	return s.decision, s.err
}

func newProtectedHandler(t *testing.T, limiter ratelimit.Limiter, handled *bool) http.Handler {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc(transferPattern, func(w http.ResponseWriter, _ *http.Request) {
		if handled != nil {
			*handled = true
		}
		w.WriteHeader(http.StatusCreated)
	})

	rateLimit := NewRateLimit(RateLimitConfig{
		Limiter:  limiter,
		Resolver: NewClientIPResolver(nil),
		Policies: map[string]ratelimit.Policy{
			transferPattern: {Name: "financial", Limit: 2, Window: time.Minute},
		},
	})
	return rateLimit.Wrap(mux)
}

func transferRequest() *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/transfers", nil)
	request.RemoteAddr = "198.51.100.9:443"
	return request
}

func decodeErrorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return body.Error.Code
}

func TestRateLimitAllowsRequestWithinLimit(t *testing.T) {
	limiter := &stubLimiter{decision: ratelimit.Decision{Allowed: true}}
	handler := newProtectedHandler(t, limiter, nil)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, transferRequest())

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusCreated)
	}
	if limiter.calls != 1 {
		t.Fatalf("limiter calls = %d, want 1", limiter.calls)
	}
	// The key is the client address, never a password, token, or idempotency key.
	if limiter.key != "198.51.100.9" {
		t.Fatalf("rate limit key = %q, want the client IP", limiter.key)
	}
	if limiter.policy.Name != "financial" {
		t.Fatalf("policy = %q, want financial", limiter.policy.Name)
	}
}

func TestRateLimitRejectsWithRetryAfter(t *testing.T) {
	limiter := &stubLimiter{
		decision: ratelimit.Decision{Allowed: false, RetryAfter: 1500 * time.Millisecond},
	}
	handled := false
	handler := newProtectedHandler(t, limiter, &handled)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, transferRequest())

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if got := recorder.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After = %q, want 2 rounded up from 1.5s", got)
	}
	if code := decodeErrorCode(t, recorder); code != "rate_limit_exceeded" {
		t.Fatalf("error code = %q", code)
	}
	if handled {
		t.Fatal("handler ran for a rejected request")
	}
}

// A Redis outage must not turn into an open door, so the request is refused.
func TestRateLimitFailsClosedWhenStoreIsUnavailable(t *testing.T) {
	limiter := &stubLimiter{err: ratelimit.ErrUnavailable}
	handled := false
	handler := newProtectedHandler(t, limiter, &handled)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, transferRequest())

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	if got := recorder.Header().Get("Retry-After"); got == "" {
		t.Fatal("Retry-After is missing on the unavailable response")
	}
	if code := decodeErrorCode(t, recorder); code != "rate_limit_unavailable" {
		t.Fatalf("error code = %q", code)
	}
	if handled {
		t.Fatal("handler ran while the limiter was unavailable")
	}
}

func TestRateLimitIgnoresUnprotectedRoutes(t *testing.T) {
	limiter := &stubLimiter{decision: ratelimit.Decision{Allowed: true}}
	handler := newProtectedHandler(t, limiter, nil)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/wallets", nil)
	request.RemoteAddr = "198.51.100.9:443"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if limiter.calls != 0 {
		t.Fatalf("limiter calls = %d, want 0 for an unprotected route", limiter.calls)
	}
}

func TestRateLimitWithoutLimiterIsDisabled(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(transferPattern, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	var rateLimit *RateLimit
	handler := rateLimit.Wrap(mux)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, transferRequest())

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want the request to pass through", recorder.Code)
	}
}
