package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Mopcs/mini-payment-system/internal/domain"
)

func TestRepositoriesIntegration(t *testing.T) {
	pool := integrationPool(t)
	users := NewUserRepository(pool)
	wallets := NewWalletRepository(pool)

	t.Run("create and retrieve user", func(t *testing.T) {
		email := fmt.Sprintf("  Repo.%s@Example.Test  ", uuid.NewString())
		user := createTestUser(t, pool, users, email)
		if user.Email != strings.ToLower(strings.TrimSpace(email)) {
			t.Fatalf("stored email = %q, want normalized email", user.Email)
		}
		if user.ID == uuid.Nil || user.CreatedAt.IsZero() || user.UpdatedAt.IsZero() {
			t.Fatalf("database fields were not returned: %+v", user)
		}

		byID, err := users.GetByID(context.Background(), user.ID)
		if err != nil || byID.Email != user.Email || byID.PasswordHash != user.PasswordHash {
			t.Fatalf("GetByID() = %+v, %v", byID, err)
		}
		byEmail, err := users.GetByEmail(context.Background(), strings.ToUpper(email))
		if err != nil || byEmail.ID != user.ID {
			t.Fatalf("GetByEmail() = %+v, %v", byEmail, err)
		}
	})

	t.Run("duplicate email", func(t *testing.T) {
		email := fmt.Sprintf("duplicate-%s@example.test", uuid.NewString())
		user := createTestUser(t, pool, users, email)
		duplicate := &domain.User{Email: " " + strings.ToUpper(email) + " ", PasswordHash: "test-hash"}
		if err := users.Create(context.Background(), duplicate); !errors.Is(err, ErrEmailAlreadyExists) {
			t.Fatalf("Create() error = %v, want ErrEmailAlreadyExists (user %s)", err, user.ID)
		}
	})

	t.Run("wallet persistence and constraints", func(t *testing.T) {
		user := createTestUser(t, pool, users, "")
		for _, currency := range []string{"USD", "EUR", "JPY"} {
			wallet := &domain.Wallet{UserID: user.ID, Currency: currency, Balance: 125}
			if err := wallets.Create(context.Background(), wallet); err != nil {
				t.Fatalf("Create(%s) error = %v", currency, err)
			}
			if wallet.ID == uuid.Nil || wallet.Currency != currency || wallet.CreatedAt.IsZero() || wallet.UpdatedAt.IsZero() {
				t.Fatalf("database fields were not returned: %+v", wallet)
			}
		}

		list, err := wallets.GetByUserID(context.Background(), user.ID)
		if err != nil {
			t.Fatalf("GetByUserID() error = %v", err)
		}
		if got, want := currencies(list), []string{"EUR", "JPY", "USD"}; !equalStrings(got, want) {
			t.Fatalf("wallet order = %v, want %v", got, want)
		}

		byCurrency, err := wallets.GetByUserIDAndCurrency(context.Background(), user.ID, " jpy ")
		if err != nil || byCurrency.Balance != 125 {
			t.Fatalf("GetByUserIDAndCurrency() = %+v, %v", byCurrency, err)
		}
		byID, err := wallets.GetByID(context.Background(), byCurrency.ID)
		if err != nil || byID.ID != byCurrency.ID {
			t.Fatalf("GetByID() = %+v, %v", byID, err)
		}

		duplicate := &domain.Wallet{UserID: user.ID, Currency: "usd"}
		if err := wallets.Create(context.Background(), duplicate); !errors.Is(err, ErrWalletAlreadyExists) {
			t.Fatalf("duplicate Create() error = %v, want ErrWalletAlreadyExists", err)
		}
		constraintOwner := createTestUser(t, pool, users, "")
		negative := &domain.Wallet{UserID: constraintOwner.ID, Currency: "USD", Balance: -1}
		if err := wallets.Create(context.Background(), negative); !errors.Is(err, ErrInvalidBalance) {
			t.Fatalf("negative balance error = %v, want ErrInvalidBalance", err)
		}
		unsupported := &domain.Wallet{UserID: constraintOwner.ID, Currency: "CAD"}
		if err := wallets.Create(context.Background(), unsupported); !errors.Is(err, ErrUnsupportedCurrency) {
			t.Fatalf("unsupported currency error = %v, want ErrUnsupportedCurrency", err)
		}
	})

	t.Run("missing rows and missing owner", func(t *testing.T) {
		if _, err := users.GetByID(context.Background(), uuid.New()); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetByID() error = %v, want ErrNotFound", err)
		}
		missingEmail := fmt.Sprintf("missing-%s@example.test", uuid.NewString())
		if _, err := users.GetByEmail(context.Background(), missingEmail); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetByEmail() error = %v, want ErrNotFound", err)
		}
		if _, err := wallets.GetByID(context.Background(), uuid.New()); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetByID() error = %v, want ErrNotFound", err)
		}
		if _, err := wallets.GetByUserIDAndCurrency(context.Background(), uuid.New(), "USD"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetByUserIDAndCurrency() error = %v, want ErrNotFound", err)
		}

		wallet := &domain.Wallet{UserID: uuid.New(), Currency: "USD"}
		if err := wallets.Create(context.Background(), wallet); !errors.Is(err, ErrForeignKeyViolation) {
			t.Fatalf("Create() error = %v, want ErrForeignKeyViolation", err)
		}
	})
}

func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to a dedicated PostgreSQL test database with migrations applied")
	}
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	if !strings.HasSuffix(poolConfig.ConnConfig.Database, "_test") {
		t.Fatal("TEST_DATABASE_URL must point to a dedicated database whose name ends in _test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatalf("create test database pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("connect to TEST_DATABASE_URL: %v", err)
	}
	var schemaReady bool
	if err := pool.QueryRow(ctx, `
		SELECT to_regclass('public.users') IS NOT NULL
		   AND to_regclass('public.wallets') IS NOT NULL`).Scan(&schemaReady); err != nil {
		pool.Close()
		t.Fatalf("check test schema: %v", err)
	}
	if !schemaReady {
		pool.Close()
		t.Fatal("test database schema is missing; apply migrations before running integration tests")
	}
	t.Cleanup(pool.Close)
	return pool
}

func createTestUser(t *testing.T, pool *pgxpool.Pool, repo *UserRepository, email string) *domain.User {
	t.Helper()
	if email == "" {
		email = fmt.Sprintf("repo-%s@example.test", uuid.NewString())
	}
	user := &domain.User{Email: email, PasswordHash: "integration-test-hash"}
	if err := repo.Create(context.Background(), user); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, "DELETE FROM wallets WHERE user_id = $1", user.ID); err != nil {
			t.Errorf("delete test wallets: %v", err)
		}
		if _, err := pool.Exec(ctx, "DELETE FROM users WHERE id = $1", user.ID); err != nil {
			t.Errorf("delete test user: %v", err)
		}
	})
	return user
}

func currencies(wallets []domain.Wallet) []string {
	result := make([]string, len(wallets))
	for i := range wallets {
		result[i] = wallets[i].Currency
	}
	return result
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
