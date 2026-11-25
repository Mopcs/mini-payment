package ratelimit

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func testClient(t *testing.T) *redis.Client {
	t.Helper()

	address := os.Getenv("TEST_REDIS_ADDR")
	if address == "" {
		t.Skip("set TEST_REDIS_ADDR to a dedicated Redis instance")
	}

	client := redis.NewClient(&redis.Options{
		Addr:     address,
		Password: os.Getenv("TEST_REDIS_PASSWORD"),
	})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("connect to test Redis: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// testKey keeps each test on its own counter, so parallel runs never share a
// window.
func testKey(t *testing.T) string {
	t.Helper()
	return "test-" + uuid.NewString()
}

func TestAllowPermitsUpToTheLimit(t *testing.T) {
	client := testClient(t)
	now := time.Unix(1700000000, 0)
	limiter := NewRedisLimiter(client).WithClock(func() time.Time { return now })
	policy := Policy{Name: "unit", Limit: 2, Window: time.Minute}
	key := testKey(t)

	for attempt := 1; attempt <= 2; attempt++ {
		decision, err := limiter.Allow(context.Background(), key, policy)
		if err != nil {
			t.Fatalf("attempt %d error = %v", attempt, err)
		}
		if !decision.Allowed {
			t.Fatalf("attempt %d was rejected, want allowed", attempt)
		}
	}

	decision, err := limiter.Allow(context.Background(), key, policy)
	if err != nil {
		t.Fatalf("third attempt error = %v", err)
	}
	if decision.Allowed {
		t.Fatal("third attempt was allowed, want rejected")
	}
	if decision.RetryAfter <= 0 {
		t.Fatalf("RetryAfter = %v, want a positive window remainder", decision.RetryAfter)
	}
}

// Advancing the clock is what a new window looks like, so the test never sleeps.
func TestAllowResetsWhenTheWindowRolls(t *testing.T) {
	client := testClient(t)
	now := time.Unix(1700000000, 0)
	limiter := NewRedisLimiter(client).WithClock(func() time.Time { return now })
	policy := Policy{Name: "unit", Limit: 1, Window: time.Minute}
	key := testKey(t)

	if decision, err := limiter.Allow(context.Background(), key, policy); err != nil || !decision.Allowed {
		t.Fatalf("first attempt = %+v, %v", decision, err)
	}
	if decision, err := limiter.Allow(context.Background(), key, policy); err != nil || decision.Allowed {
		t.Fatalf("second attempt = %+v, %v, want rejected", decision, err)
	}

	now = now.Add(policy.Window)

	decision, err := limiter.Allow(context.Background(), key, policy)
	if err != nil {
		t.Fatalf("next window error = %v", err)
	}
	if !decision.Allowed {
		t.Fatal("next window was rejected, want a fresh counter")
	}
}

func TestAllowKeepsClientsIndependent(t *testing.T) {
	client := testClient(t)
	now := time.Unix(1700000000, 0)
	limiter := NewRedisLimiter(client).WithClock(func() time.Time { return now })
	policy := Policy{Name: "unit", Limit: 1, Window: time.Minute}

	first, second := testKey(t), testKey(t)
	if decision, _ := limiter.Allow(context.Background(), first, policy); !decision.Allowed {
		t.Fatal("first client was rejected")
	}
	if decision, _ := limiter.Allow(context.Background(), second, policy); !decision.Allowed {
		t.Fatal("second client was rejected, want its own budget")
	}
}

func TestAllowReportsUnavailableStore(t *testing.T) {
	// Nothing listens on port 1, so the client fails fast instead of waiting.
	client := redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:1",
		DialTimeout: 100 * time.Millisecond,
		MaxRetries:  -1,
	})
	t.Cleanup(func() { _ = client.Close() })

	limiter := NewRedisLimiter(client)
	_, err := limiter.Allow(
		context.Background(),
		testKey(t),
		Policy{Name: "unit", Limit: 1, Window: time.Minute},
	)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}

func TestAllowValidatesInput(t *testing.T) {
	limiter := NewRedisLimiter(redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}))
	t.Cleanup(func() { _ = limiter.client.Close() })

	if _, err := limiter.Allow(context.Background(), "", Policy{Limit: 1, Window: time.Minute}); err == nil {
		t.Fatal("empty key was accepted")
	}
	if _, err := limiter.Allow(context.Background(), "key", Policy{Limit: 0, Window: time.Minute}); err == nil {
		t.Fatal("zero limit was accepted")
	}
	if _, err := limiter.Allow(context.Background(), "key", Policy{Limit: 1, Window: 0}); err == nil {
		t.Fatal("zero window was accepted")
	}
}
