package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

const (
	minJWTSecretBytes = 32
	maxJWTTTL         = 24 * time.Hour
	maxQueueTTL       = int64(^uint32(0) >> 1)
)

type Config struct {
	AppAddr                      string
	PostgresHost                 string
	PostgresPort                 uint16
	PostgresDB                   string
	PostgresUser                 string
	PostgresPass                 string
	RedisHost                    string
	RedisPort                    uint16
	RedisPassword                string
	LogLevel                     string
	JWTSecret                    string
	JWTIssuer                    string
	JWTAccessTokenTTL            time.Duration
	RabbitMQURL                  string
	RabbitMQExchange             string
	RabbitMQQueue                string
	RabbitMQRoutingKey           string
	RabbitMQRetryExchange        string
	RabbitMQRetryQueue           string
	RabbitMQDeadLetterExchange   string
	RabbitMQDeadLetterQueue      string
	RabbitMQDeadLetterRoutingKey string
	RabbitMQRetryDelay           time.Duration
	OutboxBatchSize              int
	OutboxPollInterval           time.Duration
	OutboxLeaseDuration          time.Duration
	OutboxMaxBackoff             time.Duration
	ConsumerMaxRetries           int
	ConsumerPrefetchCount        int
	ConsumerReconnectInterval    time.Duration
}

// Load reads .env when it exists, then the environment, and validates every
// value here so the rest of the program can use Config without rechecking it.
func Load() (Config, error) {
	if err := godotenv.Load(".env"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, errors.New("load .env: file could not be parsed")
	}

	// Defaults first, so each loader below only overwrites what it validates.
	cfg := Config{
		AppAddr:      envOrDefault("APP_ADDR", ":8080"),
		PostgresHost: envOrDefault("POSTGRES_HOST", "localhost"),
		PostgresDB:   envOrDefault("POSTGRES_DB", "payment_db"),
		PostgresUser: envOrDefault("POSTGRES_USER", "payment_user"),
		RedisHost:    envOrDefault("REDIS_HOST", "localhost"),
		LogLevel:     envOrDefault("LOG_LEVEL", "info"),
		JWTIssuer:    strings.TrimSpace(envOrDefault("JWT_ISSUER", "mini-payment-system")),
	}

	loaders := []func(*Config) error{
		loadConnections,
		loadLogging,
		loadAuth,
		loadBroker,
		loadOutbox,
		loadConsumer,
	}
	for _, load := range loaders {
		if err := load(&cfg); err != nil {
			return Config{}, err
		}
	}

	return cfg, nil
}

// loadConnections validates the PostgreSQL and Redis endpoints.
func loadConnections(cfg *Config) error {
	postgresPort, err := portFromEnv("POSTGRES_PORT", "5432")
	if err != nil {
		return err
	}
	redisPort, err := portFromEnv("REDIS_PORT", "6379")
	if err != nil {
		return err
	}

	postgresPassword, err := requiredEnv("POSTGRES_PASSWORD")
	if err != nil {
		return err
	}
	redisPassword, err := requiredEnv("REDIS_PASSWORD")
	if err != nil {
		return err
	}

	cfg.PostgresPort = postgresPort
	cfg.PostgresPass = postgresPassword
	cfg.RedisPort = redisPort
	cfg.RedisPassword = redisPassword
	return nil
}

func loadLogging(cfg *Config) error {
	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
		return nil
	default:
		return errors.New("LOG_LEVEL must be debug, info, warn, or error")
	}
}

func loadAuth(cfg *Config) error {
	secret, err := requiredEnv("JWT_SECRET")
	if err != nil {
		return err
	}
	if len([]byte(secret)) < minJWTSecretBytes {
		return errors.New("JWT_SECRET must be at least 32 bytes")
	}
	if cfg.JWTIssuer == "" {
		return errors.New("JWT_ISSUER must not be empty")
	}

	ttl, err := time.ParseDuration(envOrDefault("JWT_ACCESS_TOKEN_TTL", "15m"))
	if err != nil || ttl < time.Second || ttl > maxJWTTTL {
		return errors.New("JWT_ACCESS_TOKEN_TTL must be between 1s and 24h")
	}

	cfg.JWTSecret = secret
	cfg.JWTAccessTokenTTL = ttl
	return nil
}

// loadBroker reads the RabbitMQ connection, topology names and retry policy.
func loadBroker(cfg *Config) error {
	brokerURL, err := rabbitURL()
	if err != nil {
		return err
	}

	retryDelay, err := durationFromEnv("RABBITMQ_RETRY_DELAY", 5*time.Second)
	if err != nil || retryDelay < time.Millisecond || retryDelay.Milliseconds() > maxQueueTTL {
		return errors.New("RABBITMQ_RETRY_DELAY must be between 1ms and the supported queue TTL limit")
	}

	cfg.RabbitMQURL = brokerURL
	cfg.RabbitMQExchange = strings.TrimSpace(envOrDefault("RABBITMQ_EXCHANGE", "payment.events"))
	cfg.RabbitMQQueue = strings.TrimSpace(envOrDefault("RABBITMQ_QUEUE", "payment.events"))
	cfg.RabbitMQRoutingKey = strings.TrimSpace(envOrDefault("RABBITMQ_ROUTING_KEY", "transfer.completed"))
	cfg.RabbitMQRetryExchange = strings.TrimSpace(envOrDefault("RABBITMQ_RETRY_EXCHANGE", "payment.events.retry"))
	cfg.RabbitMQRetryQueue = strings.TrimSpace(envOrDefault("RABBITMQ_RETRY_QUEUE", "payment.events.retry"))
	cfg.RabbitMQDeadLetterExchange = strings.TrimSpace(envOrDefault("RABBITMQ_DEAD_LETTER_EXCHANGE", "payment.events.dlx"))
	cfg.RabbitMQDeadLetterQueue = strings.TrimSpace(envOrDefault("RABBITMQ_DEAD_LETTER_QUEUE", "payment.events.dlq"))
	cfg.RabbitMQDeadLetterRoutingKey = strings.TrimSpace(envOrDefault("RABBITMQ_DEAD_LETTER_ROUTING_KEY", "transfer.completed.dlq"))
	cfg.RabbitMQRetryDelay = retryDelay

	if cfg.RabbitMQExchange == "" || cfg.RabbitMQQueue == "" || cfg.RabbitMQRoutingKey == "" ||
		cfg.RabbitMQRetryExchange == "" || cfg.RabbitMQRetryQueue == "" ||
		cfg.RabbitMQDeadLetterExchange == "" || cfg.RabbitMQDeadLetterQueue == "" ||
		cfg.RabbitMQDeadLetterRoutingKey == "" {
		return errors.New("RabbitMQ exchange, queue, and routing key settings must not be empty")
	}
	return nil
}

// rabbitURL builds the local development URL from the user and password when
// RABBITMQ_URL is not set explicitly.
func rabbitURL() (string, error) {
	user := envOrDefault("RABBITMQ_USER", "payment_user")
	password := envOrDefault("RABBITMQ_PASSWORD", "local_dev_rabbitmq_password")
	defaultURL := "amqp://" + url.UserPassword(user, password).String() + "@localhost:5672/"

	brokerURL := strings.TrimSpace(envOrDefault("RABBITMQ_URL", defaultURL))
	if brokerURL == "" {
		return "", nil
	}

	parsed, err := url.Parse(brokerURL)
	if err != nil || (parsed.Scheme != "amqp" && parsed.Scheme != "amqps") || parsed.Host == "" {
		return "", errors.New("RABBITMQ_URL must be a valid amqp or amqps URL")
	}
	return brokerURL, nil
}

func loadOutbox(cfg *Config) error {
	batchSize, err := intFromEnv("OUTBOX_BATCH_SIZE", 20, 1, 1000)
	if err != nil {
		return err
	}
	pollInterval, err := durationFromEnv("OUTBOX_POLL_INTERVAL", time.Second)
	if err != nil {
		return err
	}
	leaseDuration, err := durationFromEnv("OUTBOX_LEASE_DURATION", 30*time.Second)
	if err != nil {
		return err
	}
	maxBackoff, err := durationFromEnv("OUTBOX_MAX_BACKOFF", 5*time.Minute)
	if err != nil {
		return err
	}

	cfg.OutboxBatchSize = batchSize
	cfg.OutboxPollInterval = pollInterval
	cfg.OutboxLeaseDuration = leaseDuration
	cfg.OutboxMaxBackoff = maxBackoff
	return nil
}

func loadConsumer(cfg *Config) error {
	maxRetries, err := intFromEnv("CONSUMER_MAX_RETRIES", 5, 0, 100)
	if err != nil {
		return err
	}
	prefetch, err := intFromEnv("CONSUMER_PREFETCH_COUNT", 10, 1, 1000)
	if err != nil {
		return err
	}
	reconnectInterval, err := durationFromEnv("CONSUMER_RECONNECT_INTERVAL", time.Second)
	if err != nil {
		return err
	}

	cfg.ConsumerMaxRetries = maxRetries
	cfg.ConsumerPrefetchCount = prefetch
	cfg.ConsumerReconnectInterval = reconnectInterval
	return nil
}

func envOrDefault(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func requiredEnv(key string) (string, error) {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}

func portFromEnv(key, fallback string) (uint16, error) {
	value := envOrDefault(key, fallback)
	port, err := strconv.ParseUint(value, 10, 16)
	if err != nil || port == 0 {
		return 0, fmt.Errorf("%s must be a port between 1 and 65535", key)
	}
	return uint16(port), nil
}

func intFromEnv(key string, fallback, minimum, maximum int) (int, error) {
	value := envOrDefault(key, fmt.Sprint(fallback))
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s must be between %d and %d", key, minimum, maximum)
	}
	return parsed, nil
}

func durationFromEnv(key string, fallback time.Duration) (time.Duration, error) {
	value := envOrDefault(key, fallback.String())
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < time.Microsecond {
		return 0, fmt.Errorf("%s must be a positive duration", key)
	}
	return parsed, nil
}
