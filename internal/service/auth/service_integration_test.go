package auth

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

	"github.com/Mopcs/mini-payment-system/internal/repository/postgres"
)

const integrationPassword = "StrongPassword123!"

func TestAuthServiceIntegration(t *testing.T) {
	pool := authTestPool(t)
	tokens, err := NewTokenManager(testJWTSecret, "auth-integration-test", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(pool, tokens)
	users := postgres.NewUserRepository(pool)
	wallets := postgres.NewWalletRepository(pool)

	t.Run("registration creates user and initial wallet", func(t *testing.T) {
		email := fmt.Sprintf("register-%s@example.test", uuid.NewString())
		result, err := service.Register(context.Background(), " "+strings.ToUpper(email)+" ", integrationPassword)
		if err != nil {
			t.Fatalf("Register() error = %v", err)
		}
		cleanupAuthUser(t, pool, result.User.ID)
		if result.User.Email != email || result.AccessToken == "" || result.ExpiresIn != 900 {
			t.Fatalf("unexpected registration result: %+v", result)
		}
		user, err := users.GetByID(context.Background(), result.User.ID)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}
		valid, err := VerifyPassword(integrationPassword, user.PasswordHash)
		if err != nil || !valid {
			t.Fatalf("stored password hash verification = %t, %v", valid, err)
		}
		wallet, err := wallets.GetByUserIDAndCurrency(context.Background(), result.User.ID, "USD")
		if err != nil || wallet.Balance != 0 {
			t.Fatalf("initial wallet = %+v, %v", wallet, err)
		}
	})

	t.Run("duplicate registration", func(t *testing.T) {
		email := fmt.Sprintf("duplicate-%s@example.test", uuid.NewString())
		result, err := service.Register(context.Background(), email, integrationPassword)
		if err != nil {
			t.Fatalf("first Register() error = %v", err)
		}
		cleanupAuthUser(t, pool, result.User.ID)
		if _, err := service.Register(context.Background(), strings.ToUpper(email), integrationPassword); !errors.Is(err, ErrEmailAlreadyRegistered) {
			t.Fatalf("duplicate Register() error = %v, want ErrEmailAlreadyRegistered", err)
		}
	})

	t.Run("login success and generic failures", func(t *testing.T) {
		email := fmt.Sprintf("login-%s@example.test", uuid.NewString())
		registered, err := service.Register(context.Background(), email, integrationPassword)
		if err != nil {
			t.Fatalf("Register() error = %v", err)
		}
		cleanupAuthUser(t, pool, registered.User.ID)
		loggedIn, err := service.Login(context.Background(), strings.ToUpper(email), integrationPassword)
		if err != nil || loggedIn.User.ID != registered.User.ID || loggedIn.AccessToken == "" {
			t.Fatalf("Login() = %+v, %v", loggedIn, err)
		}
		if _, err := service.Login(context.Background(), email, "IncorrectPassword123!"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("wrong password error = %v, want ErrInvalidCredentials", err)
		}
		missingEmail := fmt.Sprintf("missing-%s@example.test", uuid.NewString())
		if _, err := service.Login(context.Background(), missingEmail, integrationPassword); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("missing user error = %v, want ErrInvalidCredentials", err)
		}
	})

	t.Run("wallet failure rolls registration back", func(t *testing.T) {
		functionName := "reject_test_wallet_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		triggerName := "reject_test_wallet_trigger_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		functionSQL := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM users WHERE id = NEW.user_id AND email LIKE 'rollback-%%@example.test') THEN
        RAISE EXCEPTION 'test wallet failure' USING ERRCODE = '23514', CONSTRAINT = 'test_wallet_failure';
    END IF;
    RETURN NEW;
END;
$$`, functionName)
		if _, err := pool.Exec(context.Background(), functionSQL); err != nil {
			t.Fatalf("create rollback test function: %v", err)
		}
		if _, err := pool.Exec(context.Background(), fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT ON wallets FOR EACH ROW EXECUTE FUNCTION %s()", triggerName, functionName)); err != nil {
			_, _ = pool.Exec(context.Background(), fmt.Sprintf("DROP FUNCTION %s()", functionName))
			t.Fatalf("create rollback test trigger: %v", err)
		}
		t.Cleanup(func() {
			if _, err := pool.Exec(context.Background(), fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON wallets", triggerName)); err != nil {
				t.Errorf("drop rollback test trigger: %v", err)
			}
			if _, err := pool.Exec(context.Background(), fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", functionName)); err != nil {
				t.Errorf("drop rollback test function: %v", err)
			}
		})

		email := fmt.Sprintf("rollback-%s@example.test", uuid.NewString())
		if _, err := service.Register(context.Background(), email, integrationPassword); err == nil {
			t.Fatal("Register() succeeded despite a failing initial wallet insert")
		}
		if _, err := users.GetByEmail(context.Background(), email); !errors.Is(err, postgres.ErrNotFound) {
			t.Fatalf("user remained after failed registration: %v", err)
		}
	})
}

func authTestPool(t *testing.T) *pgxpool.Pool {
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
		t.Fatalf("create auth test pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("connect to TEST_DATABASE_URL: %v", err)
	}
	var schemaReady bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.users') IS NOT NULL AND to_regclass('public.wallets') IS NOT NULL`).Scan(&schemaReady); err != nil {
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

func cleanupAuthUser(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, "DELETE FROM wallets WHERE user_id = $1", userID); err != nil {
			t.Errorf("delete test wallets: %v", err)
		}
		if _, err := pool.Exec(ctx, "DELETE FROM users WHERE id = $1", userID); err != nil {
			t.Errorf("delete test user: %v", err)
		}
	})
}
