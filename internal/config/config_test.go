package config

import (
	"os"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	withCleanEnvironment(t)
	t.Setenv("POSTGRES_PASSWORD", "postgres-test")
	t.Setenv("REDIS_PASSWORD", "redis-test")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.AppAddr != ":8080" || cfg.PostgresHost != "localhost" || cfg.PostgresPort != 5432 || cfg.PostgresDB != "payment_db" || cfg.PostgresUser != "payment_user" || cfg.RedisHost != "localhost" || cfg.RedisPort != 6379 || cfg.LogLevel != "info" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadRequiresPasswords(t *testing.T) {
	withCleanEnvironment(t)
	t.Setenv("POSTGRES_PASSWORD", "postgres-test")

	_, err := Load()
	if err == nil || err.Error() != "REDIS_PASSWORD is required" {
		t.Fatalf("Load() error = %v, want missing Redis password", err)
	}
}

func TestEnvironmentOverridesDotEnv(t *testing.T) {
	withCleanEnvironment(t)
	if err := os.WriteFile(".env", []byte("APP_ADDR=:9090\nPOSTGRES_PASSWORD=from-file\nREDIS_PASSWORD=redis-file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_ADDR", ":7070")
	t.Setenv("POSTGRES_PASSWORD", "from-environment")
	t.Setenv("REDIS_PASSWORD", "redis-environment")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.AppAddr != ":7070" || cfg.PostgresPass != "from-environment" {
		t.Fatalf("environment did not take precedence: %+v", cfg)
	}
}

func withCleanEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"APP_ADDR", "POSTGRES_HOST", "POSTGRES_PORT", "POSTGRES_DB", "POSTGRES_USER", "POSTGRES_PASSWORD",
		"REDIS_HOST", "REDIS_PORT", "REDIS_PASSWORD", "LOG_LEVEL",
	} {
		original, hadValue := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if hadValue {
				_ = os.Setenv(key, original)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}

	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	t.Cleanup(func() {
		if err := os.Chdir(previousDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	if _, err := os.Stat(".env"); !os.IsNotExist(err) {
		t.Fatalf("temporary directory unexpectedly contains .env: %v", err)
	}
}
