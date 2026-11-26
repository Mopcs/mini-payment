package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	withCleanEnvironment(t)
	t.Setenv("POSTGRES_PASSWORD", "postgres-test")
	t.Setenv("REDIS_PASSWORD", "redis-test")
	t.Setenv("JWT_SECRET", "test-jwt-secret-with-at-least-32-bytes")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	assertDefaultValues(t, cfg)
	if cfg.RabbitMQURL == "" {
		t.Fatal("local RabbitMQ URL default was not applied")
	}
}

func assertDefaultValues(t *testing.T, cfg Config) {
	t.Helper()
	values := []struct {
		name string
		got  any
		want any
	}{
		{"AppAddr", cfg.AppAddr, ":8080"},
		{"PostgresHost", cfg.PostgresHost, "localhost"},
		{"PostgresPort", cfg.PostgresPort, uint16(5432)},
		{"PostgresDB", cfg.PostgresDB, "payment_db"},
		{"PostgresUser", cfg.PostgresUser, "payment_user"},
		{"PostgresMaxConns", cfg.PostgresMaxConns, 10},
		{"PostgresMinConns", cfg.PostgresMinConns, 1},
		{"PostgresMaxConnLifetime", cfg.PostgresMaxConnLifetime, time.Hour},
		{"PostgresMaxConnIdleTime", cfg.PostgresMaxConnIdleTime, 30 * time.Minute},
		{"RedisHost", cfg.RedisHost, "localhost"},
		{"RedisPort", cfg.RedisPort, uint16(6379)},
		{"LogLevel", cfg.LogLevel, "info"},
		{"ProfilingEnabled", cfg.ProfilingEnabled, false},
		{"ProfilingAddr", cfg.ProfilingAddr, "127.0.0.1:6060"},
		{"JWTIssuer", cfg.JWTIssuer, "mini-payment-system"},
		{"JWTAccessTokenTTL", cfg.JWTAccessTokenTTL, 15 * time.Minute},
		{"RabbitMQExchange", cfg.RabbitMQExchange, "payment.events"},
		{"RabbitMQQueue", cfg.RabbitMQQueue, "payment.events"},
		{"RabbitMQRoutingKey", cfg.RabbitMQRoutingKey, "transfer.completed"},
		{"OutboxBatchSize", cfg.OutboxBatchSize, 20},
		{"OutboxPollInterval", cfg.OutboxPollInterval, time.Second},
		{"OutboxLeaseDuration", cfg.OutboxLeaseDuration, 30 * time.Second},
		{"OutboxMaxBackoff", cfg.OutboxMaxBackoff, 5 * time.Minute},
	}
	for _, value := range values {
		if value.got != value.want {
			t.Errorf("%s = %v, want %v", value.name, value.got, value.want)
		}
	}
}

func TestLoadRejectsInvalidWorkerConfiguration(t *testing.T) {
	for _, test := range []struct {
		key   string
		value string
	}{
		{key: "RABBITMQ_URL", value: "http://localhost:5672"},
		{key: "OUTBOX_BATCH_SIZE", value: "0"},
		{key: "OUTBOX_POLL_INTERVAL", value: "not-a-duration"},
		{key: "OUTBOX_LEASE_DURATION", value: "0s"},
		{key: "OUTBOX_MAX_BACKOFF", value: "-1s"},
	} {
		t.Run(test.key, func(t *testing.T) {
			withCleanEnvironment(t)
			t.Setenv("POSTGRES_PASSWORD", "postgres-test")
			t.Setenv("REDIS_PASSWORD", "redis-test")
			t.Setenv("JWT_SECRET", "test-jwt-secret-with-at-least-32-bytes")
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted invalid %s", test.key)
			}
		})
	}
}

func TestLoadMetricsDefaults(t *testing.T) {
	withCleanEnvironment(t)
	setRequiredTestConfiguration(t)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIMetricsAddr != "127.0.0.1:9091" ||
		cfg.WorkerMetricsAddr != "127.0.0.1:9092" ||
		cfg.ConsumerMetricsAddr != "127.0.0.1:9093" {
		t.Fatalf("metrics defaults = %q, %q, %q", cfg.APIMetricsAddr,
			cfg.WorkerMetricsAddr, cfg.ConsumerMetricsAddr)
	}
}

func TestLoadRejectsInvalidOrDuplicateMetricsAddresses(t *testing.T) {
	tests := []struct {
		name     string
		api      string
		worker   string
		consumer string
	}{
		{name: "invalid port", api: "127.0.0.1:0"},
		{name: "API port conflict", api: "127.0.0.1:8080"},
		{name: "duplicate port", api: "127.0.0.1:9091", worker: "0.0.0.0:9091"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			withCleanEnvironment(t)
			setRequiredTestConfiguration(t)
			if test.api != "" {
				t.Setenv("API_METRICS_ADDR", test.api)
			}
			if test.worker != "" {
				t.Setenv("WORKER_METRICS_ADDR", test.worker)
			}
			if test.consumer != "" {
				t.Setenv("CONSUMER_METRICS_ADDR", test.consumer)
			}
			if _, err := Load(); err == nil {
				t.Fatal("Load() accepted invalid metrics addresses")
			}
		})
	}
}

func setRequiredTestConfiguration(t *testing.T) {
	t.Helper()
	t.Setenv("POSTGRES_PASSWORD", "postgres-test")
	t.Setenv("REDIS_PASSWORD", "redis-test")
	t.Setenv("JWT_SECRET", "test-jwt-secret-with-at-least-32-bytes")
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
	contents := "APP_ADDR=:9090\nPOSTGRES_PASSWORD=from-file\nREDIS_PASSWORD=redis-file\n"
	if err := os.WriteFile(".env", []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_ADDR", ":7070")
	t.Setenv("POSTGRES_PASSWORD", "from-environment")
	t.Setenv("REDIS_PASSWORD", "redis-environment")
	t.Setenv("JWT_SECRET", "test-jwt-secret-with-at-least-32-bytes")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.AppAddr != ":7070" || cfg.PostgresPass != "from-environment" {
		t.Fatalf("environment did not take precedence: %+v", cfg)
	}
}

func TestLoadRequiresStrongJWTSecret(t *testing.T) {
	withCleanEnvironment(t)
	t.Setenv("POSTGRES_PASSWORD", "postgres-test")
	t.Setenv("REDIS_PASSWORD", "redis-test")
	t.Setenv("JWT_SECRET", "too-short")

	_, err := Load()
	if err == nil || err.Error() != "JWT_SECRET must be at least 32 bytes" {
		t.Fatalf("Load() error = %v, want weak JWT secret error", err)
	}
}

func withCleanEnvironment(t *testing.T) {
	t.Helper()
	clearEnvironment(t)
	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	t.Cleanup(func() { restoreDirectory(t, previousDir) })
	if _, err := os.Stat(".env"); !os.IsNotExist(err) {
		t.Fatalf("temporary directory unexpectedly contains .env: %v", err)
	}
}

func clearEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range environmentKeys() {
		original, hadValue := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(environmentRestore(key, original, hadValue))
	}
}
func environmentRestore(key, value string, hadValue bool) func() {
	return func() {
		if hadValue {
			_ = os.Setenv(key, value)
			return
		}
		_ = os.Unsetenv(key)
	}
}

func restoreDirectory(t *testing.T, previousDir string) {
	if err := os.Chdir(previousDir); err != nil {
		t.Errorf("restore working directory: %v", err)
	}
}

func environmentKeys() []string {
	return []string{
		"APP_ADDR", "POSTGRES_HOST", "POSTGRES_PORT", "POSTGRES_DB", "POSTGRES_USER",
		"POSTGRES_PASSWORD", "REDIS_HOST", "REDIS_PORT", "REDIS_PASSWORD", "LOG_LEVEL",
		"JWT_SECRET", "JWT_ISSUER", "JWT_ACCESS_TOKEN_TTL", "RABBITMQ_URL", "RABBITMQ_USER",
		"RABBITMQ_PASSWORD", "RABBITMQ_EXCHANGE", "RABBITMQ_QUEUE", "RABBITMQ_ROUTING_KEY",
		"RABBITMQ_RETRY_EXCHANGE", "RABBITMQ_RETRY_QUEUE", "RABBITMQ_DEAD_LETTER_EXCHANGE",
		"RABBITMQ_DEAD_LETTER_QUEUE", "RABBITMQ_DEAD_LETTER_ROUTING_KEY", "RABBITMQ_RETRY_DELAY",
		"CONSUMER_MAX_RETRIES", "CONSUMER_PREFETCH_COUNT", "CONSUMER_RECONNECT_INTERVAL",
		"API_METRICS_ADDR", "WORKER_METRICS_ADDR", "CONSUMER_METRICS_ADDR",
		"OUTBOX_BATCH_SIZE", "OUTBOX_POLL_INTERVAL", "OUTBOX_LEASE_DURATION", "OUTBOX_MAX_BACKOFF",
	}
}

func withRequiredSecrets(t *testing.T, jwtSecret string) {
	t.Helper()

	withCleanEnvironment(t)
	t.Setenv("POSTGRES_PASSWORD", "postgres-test")
	t.Setenv("REDIS_PASSWORD", "redis-test")
	t.Setenv("JWT_SECRET", jwtSecret)
}

func TestLoadRejectsMinConnsAboveMaxConns(t *testing.T) {
	withRequiredSecrets(t, "test-jwt-secret-with-at-least-32-bytes")
	t.Setenv("POSTGRES_MAX_CONNS", "5")
	t.Setenv("POSTGRES_MIN_CONNS", "6")

	if _, err := Load(); err == nil {
		t.Fatal("min conns above max conns was accepted")
	}
}

func TestLoadRejectsPlaceholderSecretInProduction(t *testing.T) {
	withRequiredSecrets(t, "replace-this-placeholder-with-a-random-secret-of-at-least-32-bytes")
	t.Setenv("APP_ENV", "production")

	if _, err := Load(); err == nil {
		t.Fatal("placeholder JWT secret was accepted in production")
	}
}

func TestLoadAcceptsRandomSecretInProduction(t *testing.T) {
	withRequiredSecrets(t, "9f2a7c1e5b8d3f6a0c4e7b1d9a2f5c8e4b7d1a3f")
	t.Setenv("APP_ENV", "production")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.AppEnv != "production" {
		t.Fatalf("AppEnv = %q, want production", cfg.AppEnv)
	}
}

func TestLoadRejectsDisabledRateLimitInProduction(t *testing.T) {
	withRequiredSecrets(t, "9f2a7c1e5b8d3f6a0c4e7b1d9a2f5c8e4b7d1a3f")
	t.Setenv("APP_ENV", "production")
	t.Setenv("RATE_LIMIT_ENABLED", "false")

	if _, err := Load(); err == nil {
		t.Fatal("disabled rate limiting was accepted in production")
	}
}

func TestLoadRejectsDebugLoggingInProduction(t *testing.T) {
	withRequiredSecrets(t, "9f2a7c1e5b8d3f6a0c4e7b1d9a2f5c8e4b7d1a3f")
	t.Setenv("APP_ENV", "production")
	t.Setenv("LOG_LEVEL", "debug")

	if _, err := Load(); err == nil {
		t.Fatal("debug logging was accepted in production")
	}
}

func TestLoadRejectsUnknownEnvironment(t *testing.T) {
	withRequiredSecrets(t, "9f2a7c1e5b8d3f6a0c4e7b1d9a2f5c8e4b7d1a3f")
	t.Setenv("APP_ENV", "staging")

	if _, err := Load(); err == nil {
		t.Fatal("unknown APP_ENV was accepted")
	}
}

func TestLoadAppliesTimeoutDefaults(t *testing.T) {
	withRequiredSecrets(t, "9f2a7c1e5b8d3f6a0c4e7b1d9a2f5c8e4b7d1a3f")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	timeouts := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"HTTPReadHeaderTimeout", cfg.HTTPReadHeaderTimeout, 5 * time.Second},
		{"HTTPReadTimeout", cfg.HTTPReadTimeout, 15 * time.Second},
		{"HTTPWriteTimeout", cfg.HTTPWriteTimeout, 30 * time.Second},
		{"HTTPIdleTimeout", cfg.HTTPIdleTimeout, 60 * time.Second},
	}
	for _, timeout := range timeouts {
		if timeout.got != timeout.want {
			t.Fatalf("%s = %v, want %v", timeout.name, timeout.got, timeout.want)
		}
	}
}

func TestLoadRejectsExcessiveTimeout(t *testing.T) {
	withRequiredSecrets(t, "9f2a7c1e5b8d3f6a0c4e7b1d9a2f5c8e4b7d1a3f")
	t.Setenv("HTTP_WRITE_TIMEOUT", "10m")

	if _, err := Load(); err == nil {
		t.Fatal("a ten minute write timeout was accepted")
	}
}

func TestLoadRejectsReadTimeoutBelowHeaderTimeout(t *testing.T) {
	withRequiredSecrets(t, "9f2a7c1e5b8d3f6a0c4e7b1d9a2f5c8e4b7d1a3f")
	t.Setenv("HTTP_READ_HEADER_TIMEOUT", "10s")
	t.Setenv("HTTP_READ_TIMEOUT", "5s")

	if _, err := Load(); err == nil {
		t.Fatal("read timeout shorter than the header timeout was accepted")
	}
}

func TestLoadParsesTrustedProxies(t *testing.T) {
	withRequiredSecrets(t, "9f2a7c1e5b8d3f6a0c4e7b1d9a2f5c8e4b7d1a3f")
	t.Setenv("TRUSTED_PROXIES", "10.0.0.0/8, 192.168.1.5")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.TrustedProxies) != 2 {
		t.Fatalf("TrustedProxies = %v, want two entries", cfg.TrustedProxies)
	}
}

func TestLoadRejectsInvalidTrustedProxy(t *testing.T) {
	withRequiredSecrets(t, "9f2a7c1e5b8d3f6a0c4e7b1d9a2f5c8e4b7d1a3f")
	t.Setenv("TRUSTED_PROXIES", "not-an-address")

	if _, err := Load(); err == nil {
		t.Fatal("invalid TRUSTED_PROXIES entry was accepted")
	}
}

func TestLoadRejectsWildcardCORSWithCredentials(t *testing.T) {
	withRequiredSecrets(t, "9f2a7c1e5b8d3f6a0c4e7b1d9a2f5c8e4b7d1a3f")
	t.Setenv("CORS_ALLOWED_ORIGINS", "*")
	t.Setenv("CORS_ALLOW_CREDENTIALS", "true")

	if _, err := Load(); err == nil {
		t.Fatal("wildcard origin with credentials was accepted")
	}
}

func TestLoadKeepsCORSDisabledByDefault(t *testing.T) {
	withRequiredSecrets(t, "9f2a7c1e5b8d3f6a0c4e7b1d9a2f5c8e4b7d1a3f")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.CORSAllowedOrigins) != 0 {
		t.Fatalf("CORSAllowedOrigins = %v, want none", cfg.CORSAllowedOrigins)
	}
}

func TestLoadRejectsZeroRateLimit(t *testing.T) {
	withRequiredSecrets(t, "9f2a7c1e5b8d3f6a0c4e7b1d9a2f5c8e4b7d1a3f")
	t.Setenv("RATE_LIMIT_AUTH_LIMIT", "0")

	if _, err := Load(); err == nil {
		t.Fatal("a zero auth rate limit was accepted")
	}
}
