package middleware

import (
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/Mopcs/mini-payment-system/internal/observability"
	"github.com/Mopcs/mini-payment-system/internal/ratelimit"
)

// RateLimitConfig wires the limiter to the routes it protects.
type RateLimitConfig struct {
	Limiter  ratelimit.Limiter
	Resolver ClientIPResolver
	Policies map[string]ratelimit.Policy
	Metrics  *observability.RateLimitMetrics
	Auditor  *observability.Auditor
	Logger   *slog.Logger
}

// RateLimit protects the routes listed in Policies. It resolves the route
// pattern before dispatch, so the table below is the single place that decides
// what is limited and how.
type RateLimit struct {
	config RateLimitConfig
}

func NewRateLimit(config RateLimitConfig) *RateLimit {
	return &RateLimit{config: config}
}

// Wrap returns the mux unchanged when no limiter is configured, which is how
// RATE_LIMIT_ENABLED=false behaves.
func (l *RateLimit) Wrap(mux *http.ServeMux) http.Handler {
	if l == nil || l.config.Limiter == nil {
		return mux
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		policy, ok := l.config.Policies[pattern]
		if !ok {
			mux.ServeHTTP(w, r)
			return
		}

		clientIP := l.config.Resolver.ClientIP(r)
		decision, err := l.config.Limiter.Allow(r.Context(), clientIP, policy)
		if err != nil {
			l.rejectUnavailable(w, r, policy, clientIP, err)
			return
		}
		if !decision.Allowed {
			l.rejectLimited(w, r, policy, clientIP, decision.RetryAfter)
			return
		}

		l.config.Metrics.Record(policy.Name, observability.RateLimitAllowed)
		mux.ServeHTTP(w, r)
	})
}

// rejectUnavailable fails closed. Redis is not a source of truth for money, but
// without it the limit cannot be enforced, so the request is refused instead of
// being let through unaccounted.
func (l *RateLimit) rejectUnavailable(
	w http.ResponseWriter,
	r *http.Request,
	policy ratelimit.Policy,
	clientIP string,
	err error,
) {
	l.config.Metrics.Record(policy.Name, observability.RateLimitUnavailable)
	l.config.Auditor.Event(r.Context(), "security.rate_limit_unavailable",
		slog.String("policy", policy.Name),
		slog.String("client_ip", clientIP),
		slog.String("error", err.Error()),
	)
	if l.config.Logger != nil {
		l.config.Logger.WarnContext(r.Context(), "rate limit store unavailable",
			slog.String("policy", policy.Name),
			slog.Any("error", err))
	}

	w.Header().Set("Retry-After", "1")
	writeJSONError(w, http.StatusServiceUnavailable,
		"rate_limit_unavailable", "request could not be accepted right now")
}

func (l *RateLimit) rejectLimited(
	w http.ResponseWriter,
	r *http.Request,
	policy ratelimit.Policy,
	clientIP string,
	retryAfter time.Duration,
) {
	l.config.Metrics.Record(policy.Name, observability.RateLimitRejected)
	l.config.Auditor.Event(r.Context(), "security.rate_limit_exceeded",
		slog.String("policy", policy.Name),
		slog.String("client_ip", clientIP),
		slog.String("path", r.URL.Path),
	)
	trace.SpanFromContext(r.Context()).SetAttributes(
		attribute.String("ratelimit.policy", policy.Name),
		attribute.String("ratelimit.outcome", observability.RateLimitRejected),
	)

	w.Header().Set("Retry-After", retryAfterSeconds(retryAfter))
	writeJSONError(w, http.StatusTooManyRequests,
		"rate_limit_exceeded", "too many requests, retry later")
}

// retryAfterSeconds rounds up, so a client never retries before the window ends.
func retryAfterSeconds(retryAfter time.Duration) string {
	seconds := int(math.Ceil(retryAfter.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	return strconv.Itoa(seconds)
}
