// Package ratelimit counts requests per client and policy in Redis. The
// counters are deliberately the only state kept there: balances, transfers and
// idempotency stay in PostgreSQL.
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Policy is one limit applied over one window.
type Policy struct {
	Name   string
	Limit  int
	Window time.Duration
}

// Decision reports whether the request may proceed and, when it may not, how
// long the client should wait.
type Decision struct {
	Allowed    bool
	RetryAfter time.Duration
}

// ErrUnavailable means the counter store could not be reached. Callers must
// decide the failure policy explicitly instead of treating it as allowed.
var ErrUnavailable = errors.New("rate limit store unavailable")

// Limiter counts requests for a key.
type Limiter interface {
	Allow(ctx context.Context, key string, policy Policy) (Decision, error)
}

// incrementScript creates the counter and its expiry in one round trip, so two
// API instances cannot both decide they are the first hit.
var incrementScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then
	redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return count
`)

// RedisLimiter keeps counters in Redis so every API instance sees the same
// budget.
type RedisLimiter struct {
	client *redis.Client
	clock  func() time.Time
}

func NewRedisLimiter(client *redis.Client) *RedisLimiter {
	return &RedisLimiter{client: client, clock: time.Now}
}

// WithClock replaces the time source. Tests use it to move between windows
// without sleeping.
func (l *RedisLimiter) WithClock(clock func() time.Time) *RedisLimiter {
	l.clock = clock
	return l
}

func (l *RedisLimiter) Allow(
	ctx context.Context,
	key string,
	policy Policy,
) (Decision, error) {
	if key == "" {
		return Decision{}, errors.New("rate limit key must not be empty")
	}
	if policy.Limit < 1 || policy.Window < time.Second {
		return Decision{}, errors.New("rate limit policy must be positive")
	}

	window := policy.Window.Nanoseconds()
	now := l.clock().UnixNano()
	// The bucket is part of the key, so a new window starts on a fresh counter
	// and the decision never depends on when the first request arrived.
	bucket := now / window
	counterKey := fmt.Sprintf("ratelimit:%s:%s:%d", policy.Name, key, bucket)
	expiry := policy.Window + time.Second

	count, err := incrementScript.Run(ctx, l.client, []string{counterKey}, expiry.Milliseconds()).Int()
	if err != nil {
		return Decision{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	if count > policy.Limit {
		return Decision{Allowed: false, RetryAfter: windowLeft(now, window)}, nil
	}
	return Decision{Allowed: true}, nil
}

func windowLeft(now, window int64) time.Duration {
	remaining := window - now%window
	if remaining <= 0 {
		remaining = window
	}
	return time.Duration(remaining)
}
