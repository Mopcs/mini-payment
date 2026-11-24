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

type Config struct {
	AppAddr             string
	PostgresHost        string
	PostgresPort        uint16
	PostgresDB          string
	PostgresUser        string
	PostgresPass        string
	RedisHost           string
	RedisPort           uint16
	RedisPassword       string
	LogLevel            string
	JWTSecret           string
	JWTIssuer           string
	JWTAccessTokenTTL   time.Duration
	RabbitMQURL         string
	RabbitMQExchange    string
	RabbitMQQueue       string
	RabbitMQRoutingKey  string
	OutboxBatchSize     int
	OutboxPollInterval  time.Duration
	OutboxLeaseDuration time.Duration
	OutboxMaxBackoff    time.Duration
}

// Load reads .env when it exists, then the environment, and validates every
// value here so the rest of the program can use Config without rechecking it.
func Load() (Config, error) {
	if err := godotenv.Load(".env"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, errors.New("load .env: file could not be parsed")
	}

	postgresPort, err := portFromEnv("POSTGRES_PORT", "5432")
	if err != nil {
		return Config{}, err
	}
	redisPort, err := portFromEnv("REDIS_PORT", "6379")
	if err != nil {
		return Config{}, err
	}

	postgresPassword, err := requiredEnv("POSTGRES_PASSWORD")
	if err != nil {
		return Config{}, err
	}
	redisPassword, err := requiredEnv("REDIS_PASSWORD")
	if err != nil {
		return Config{}, err
	}

	logLevel := envOrDefault("LOG_LEVEL", "info")
	if logLevel != "debug" && logLevel != "info" && logLevel != "warn" && logLevel != "error" {
		return Config{}, fmt.Errorf("LOG_LEVEL must be debug, info, warn, or error")
	}
	jwtSecret, err := requiredEnv("JWT_SECRET")
	if err != nil {
		return Config{}, err
	}
	if len([]byte(jwtSecret)) < 32 {
		return Config{}, errors.New("JWT_SECRET must be at least 32 bytes")
	}
	jwtIssuer := strings.TrimSpace(envOrDefault("JWT_ISSUER", "mini-payment-system"))
	if jwtIssuer == "" {
		return Config{}, errors.New("JWT_ISSUER must not be empty")
	}
	jwtTTL, err := time.ParseDuration(envOrDefault("JWT_ACCESS_TOKEN_TTL", "15m"))
	if err != nil || jwtTTL < time.Second || jwtTTL > 24*time.Hour {
		return Config{}, errors.New("JWT_ACCESS_TOKEN_TTL must be between 1s and 24h")
	}
	rabbitUser := envOrDefault("RABBITMQ_USER", "payment_user")
	rabbitPassword := envOrDefault("RABBITMQ_PASSWORD", "local_dev_rabbitmq_password")
	defaultRabbitURL := "amqp://" + url.UserPassword(rabbitUser, rabbitPassword).String() + "@localhost:5672/"
	rabbitURL := strings.TrimSpace(envOrDefault("RABBITMQ_URL", defaultRabbitURL))
	if rabbitURL != "" {
		parsedURL, err := url.Parse(rabbitURL)
		if err != nil || (parsedURL.Scheme != "amqp" && parsedURL.Scheme != "amqps") || parsedURL.Host == "" {
			return Config{}, errors.New("RABBITMQ_URL must be a valid amqp or amqps URL")
		}
	}
	batchSize, err := intFromEnv("OUTBOX_BATCH_SIZE", 20, 1, 1000)
	if err != nil {
		return Config{}, err
	}
	pollInterval, err := durationFromEnv("OUTBOX_POLL_INTERVAL", time.Second)
	if err != nil {
		return Config{}, err
	}
	leaseDuration, err := durationFromEnv("OUTBOX_LEASE_DURATION", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	maxBackoff, err := durationFromEnv("OUTBOX_MAX_BACKOFF", 5*time.Minute)
	if err != nil {
		return Config{}, err
	}

	return Config{
		AppAddr:             envOrDefault("APP_ADDR", ":8080"),
		PostgresHost:        envOrDefault("POSTGRES_HOST", "localhost"),
		PostgresPort:        postgresPort,
		PostgresDB:          envOrDefault("POSTGRES_DB", "payment_db"),
		PostgresUser:        envOrDefault("POSTGRES_USER", "payment_user"),
		PostgresPass:        postgresPassword,
		RedisHost:           envOrDefault("REDIS_HOST", "localhost"),
		RedisPort:           redisPort,
		RedisPassword:       redisPassword,
		LogLevel:            logLevel,
		JWTSecret:           jwtSecret,
		JWTIssuer:           jwtIssuer,
		JWTAccessTokenTTL:   jwtTTL,
		RabbitMQURL:         rabbitURL,
		RabbitMQExchange:    strings.TrimSpace(envOrDefault("RABBITMQ_EXCHANGE", "payment.events")),
		RabbitMQQueue:       strings.TrimSpace(envOrDefault("RABBITMQ_QUEUE", "payment.events")),
		RabbitMQRoutingKey:  strings.TrimSpace(envOrDefault("RABBITMQ_ROUTING_KEY", "transfer.completed")),
		OutboxBatchSize:     batchSize,
		OutboxPollInterval:  pollInterval,
		OutboxLeaseDuration: leaseDuration,
		OutboxMaxBackoff:    maxBackoff,
	}, nil
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
