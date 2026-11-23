package wallet

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Mopcs/mini-payment-system/internal/domain"
	"github.com/Mopcs/mini-payment-system/internal/repository/postgres"
)

func TestWalletServiceIntegration(t *testing.T) {
	pool := walletTestPool(t)
	service := NewService(pool)
	user := &domain.User{Email: fmt.Sprintf("wallet-%s@example.test", uuid.NewString()), PasswordHash: "test-only-hash"}
	if err := postgres.NewUserRepository(pool).Create(context.Background(), user); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, `DELETE FROM wallets WHERE user_id = $1`, user.ID); err != nil {
			t.Errorf("delete test wallets: %v", err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, user.ID); err != nil {
			t.Errorf("delete test user: %v", err)
		}
	})

	for _, currency := range []string{" usd ", "eur", "JPY"} {
		wallet, err := service.Create(context.Background(), user.ID, currency)
		if err != nil {
			t.Fatalf("create wallet %s: %v", currency, err)
		}
		if wallet.Currency != strings.ToUpper(strings.TrimSpace(currency)) || wallet.Balance != 0 || wallet.ID == uuid.Nil {
			t.Fatalf("created wallet = %+v", wallet)
		}
	}
	list, err := service.List(context.Background(), user.ID)
	if err != nil || len(list) != 3 {
		t.Fatalf("List() = %d wallets, %v", len(list), err)
	}
	if list[0].Currency != "EUR" || list[1].Currency != "JPY" || list[2].Currency != "USD" {
		t.Fatalf("wallet order = %v, %v, %v", list[0].Currency, list[1].Currency, list[2].Currency)
	}
	if _, err := service.Create(context.Background(), user.ID, "CAD"); !errors.Is(err, ErrInvalidCurrency) {
		t.Fatalf("unsupported currency error = %v", err)
	}
	if _, err := service.Create(context.Background(), user.ID, "usd"); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate currency error = %v", err)
	}

	otherUser := uuid.New()
	if _, err := service.Get(context.Background(), otherUser, list[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign wallet lookup error = %v, want ErrNotFound", err)
	}
	if _, err := service.Get(context.Background(), user.ID, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing wallet lookup error = %v, want ErrNotFound", err)
	}

	concurrentUser := &domain.User{Email: fmt.Sprintf("wallet-race-%s@example.test", uuid.NewString()), PasswordHash: "test-only-hash"}
	if err := postgres.NewUserRepository(pool).Create(context.Background(), concurrentUser); err != nil {
		t.Fatalf("create concurrent user: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, `DELETE FROM wallets WHERE user_id = $1`, concurrentUser.ID); err != nil {
			t.Errorf("delete concurrent wallets: %v", err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, concurrentUser.ID); err != nil {
			t.Errorf("delete concurrent user: %v", err)
		}
	})
	const attempts = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes, conflicts := 0, 0
	unexpected := make([]error, 0)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.Create(context.Background(), concurrentUser.ID, "EUR")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				successes++
			case errors.Is(err, ErrAlreadyExists):
				conflicts++
			default:
				unexpected = append(unexpected, err)
			}
		}()
	}
	wg.Wait()
	if successes != 1 || conflicts != attempts-1 || len(unexpected) != 0 {
		t.Fatalf("concurrent creates: successes=%d conflicts=%d unexpected=%v", successes, conflicts, unexpected)
	}
}

func walletTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to a dedicated PostgreSQL test database with migrations applied")
	}
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	if !strings.HasSuffix(cfg.ConnConfig.Database, "_test") {
		t.Fatal("TEST_DATABASE_URL must point to a dedicated database whose name ends in _test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping test database: %v", err)
	}
	var schemaReady bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.users') IS NOT NULL AND to_regclass('public.wallets') IS NOT NULL`).Scan(&schemaReady); err != nil || !schemaReady {
		pool.Close()
		if err != nil {
			t.Fatalf("check test schema: %v", err)
		}
		t.Fatal("test database schema is missing; apply migrations before running integration tests")
	}
	t.Cleanup(pool.Close)
	return pool
}
