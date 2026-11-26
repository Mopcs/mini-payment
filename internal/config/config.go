package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/Mopcs/mini-payment-system/internal/observability"
	"github.com/Mopcs/mini-payment-system/internal/shutdown"
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
	PostgresMaxConns             int
	PostgresMinConns             int
	PostgresMaxConnLifetime      time.Duration
	PostgresMaxConnIdleTime      time.Duration
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
	APIMetricsAddr               string
	WorkerMetricsAddr            string
	ConsumerMetricsAddr          string
	TracingEnabled               bool
	TracingServiceName           string
	TracingEndpoint              string
	TracingInsecure              bool
	TracingSampler               string
	TracingSamplerArg            float64
	ProfilingEnabled             bool
	ProfilingAddr                string
	AppEnv                       string
	HTTPReadHeaderTimeout        time.Duration
	HTTPReadTimeout              time.Duration
	HTTPWriteTimeout             time.Duration
	HTTPIdleTimeout              time.Duration
	ShutdownTimeout              time.Duration
	RateLimitEnabled             bool
	RateLimitAuthLimit           int
	RateLimitAuthWindow          time.Duration
	RateLimitFinancialLimit      int
	RateLimitFinancialWindow     time.Duration
	TrustedProxies               []netip.Prefix
	CORSAllowedOrigins           []string
	CORSAllowCredentials         bool
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
		AppEnv:       strings.ToLower(strings.TrimSpace(envOrDefault("APP_ENV", "development"))),
	}

	loaders := []func(*Config) error{
		loadConnections,
		loadPostgresPool,
		loadLogging,
		loadAuth,
		loadBroker,
		loadOutbox,
		loadConsumer,
		loadMetrics,
		loadTracing,
		loadProfiling,
		loadHTTP,
		loadRateLimit,
		loadCORS,
	}
	for _, load := range loaders {
		if err := load(&cfg); err != nil {
			return Config{}, err
		}
	}

	if err := validateProduction(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// validateProduction rejects settings that are convenient locally and unsafe in
// production. Everything here is a documented baseline for a payment API, and
// the message says which variable to change.
func validateProduction(cfg Config) error {
	if cfg.AppEnv != "production" {
		return nil
	}
	if !cfg.RateLimitEnabled {
		return errors.New("RATE_LIMIT_ENABLED must be true when APP_ENV is production")
	}
	if cfg.LogLevel == "debug" {
		return errors.New("LOG_LEVEL must not be debug when APP_ENV is production")
	}
	return nil
}

// TracingSettings builds the tracer configuration for one process. Each binary
// passes its own default service name, which OTEL_SERVICE_NAME can override.
func (c Config) TracingSettings(defaultServiceName string) observability.TracingSettings {
	service := c.TracingServiceName
	if service == "" {
		service = defaultServiceName
	}

	return observability.TracingSettings{
		Enabled:     c.TracingEnabled,
		ServiceName: service,
		Endpoint:    c.TracingEndpoint,
		Insecure:    c.TracingInsecure,
		Sampler:     c.TracingSampler,
		SamplerArg:  c.TracingSamplerArg,
	}
}

func loadMetrics(cfg *Config) error {
	addresses := []struct {
		key      string
		fallback string
		target   *string
	}{
		{key: "API_METRICS_ADDR", fallback: "127.0.0.1:9091", target: &cfg.APIMetricsAddr},
		{key: "WORKER_METRICS_ADDR", fallback: "127.0.0.1:9092", target: &cfg.WorkerMetricsAddr},
		{key: "CONSUMER_METRICS_ADDR", fallback: "127.0.0.1:9093", target: &cfg.ConsumerMetricsAddr},
	}
	seenPorts := make(map[int]string, len(addresses))
	appPort, err := validateListenAddress("APP_ADDR", cfg.AppAddr)
	if err != nil {
		return err
	}
	seenPorts[appPort] = "APP_ADDR"
	for _, address := range addresses {
		value := strings.TrimSpace(envOrDefault(address.key, address.fallback))
		port, err := validateListenAddress(address.key, value)
		if err != nil {
			return err
		}
		if previous, exists := seenPorts[port]; exists {
			return fmt.Errorf("%s and %s must use different ports", address.key, previous)
		}
		seenPorts[port] = address.key
		*address.target = value
	}
	return nil
}

func validateListenAddress(key, address string) (int, error) {
	_, portText, err := net.SplitHostPort(address)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid host:port address", key)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%s port must be between 1 and 65535", key)
	}
	return port, nil
}

// knownSamplers lists the sampler names the tracer understands.
var knownSamplers = map[string]struct{}{
	"always_on":                {},
	"always_off":               {},
	"traceidratio":             {},
	"parentbased_always_on":    {},
	"parentbased_always_off":   {},
	"parentbased_traceidratio": {},
}

// loadTracing reads the OpenTelemetry settings. An empty service name is
// allowed, because each executable supplies its own default.
func loadTracing(cfg *Config) error {
	enabled, err := boolFromEnv("OTEL_TRACING_ENABLED", true)
	if err != nil {
		return err
	}

	endpoint, insecure, err := traceEndpoint(
		envOrDefault("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
	)
	if err != nil {
		return err
	}

	samplerName := envOrDefault("OTEL_TRACES_SAMPLER", "parentbased_traceidratio")
	sampler := strings.ToLower(strings.TrimSpace(samplerName))
	if _, ok := knownSamplers[sampler]; !ok {
		return errors.New("OTEL_TRACES_SAMPLER must be a supported sampler name")
	}

	ratio, err := floatFromEnv("OTEL_TRACES_SAMPLER_ARG", 1)
	if err != nil || ratio < 0 || ratio > 1 {
		return errors.New("OTEL_TRACES_SAMPLER_ARG must be between 0 and 1")
	}

	cfg.TracingEnabled = enabled
	cfg.TracingServiceName = strings.TrimSpace(envOrDefault("OTEL_SERVICE_NAME", ""))
	cfg.TracingEndpoint = endpoint
	cfg.TracingInsecure = insecure
	cfg.TracingSampler = sampler
	cfg.TracingSamplerArg = ratio
	return nil
}

// loadProfiling reads the opt-in for the pprof listener. It is off by default
// and, when on, the address must be loopback; the profiling package enforces
// that at bind time.
func loadProfiling(cfg *Config) error {
	enabled, err := boolFromEnv("PROFILING_ENABLED", false)
	if err != nil {
		return err
	}
	cfg.ProfilingEnabled = enabled
	cfg.ProfilingAddr = trimmedEnv("PROFILING_ADDR", "127.0.0.1:6060")
	return nil
}

// traceEndpoint accepts host:port or a URL and reports whether TLS is skipped.
func traceEndpoint(value string) (string, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false, errors.New("OTEL_EXPORTER_OTLP_ENDPOINT is required")
	}

	if !strings.Contains(value, "://") {
		if _, _, err := net.SplitHostPort(value); err != nil {
			return "", false, errors.New("OTEL_EXPORTER_OTLP_ENDPOINT must be host:port or a URL")
		}
		return value, true, nil
	}

	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return "", false, errors.New("OTEL_EXPORTER_OTLP_ENDPOINT must be host:port or a URL")
	}
	return parsed.Host, parsed.Scheme != "https", nil
}

const (
	minHTTPTimeout = time.Second
	maxHTTPTimeout = 5 * time.Minute
)

// loadHTTP reads the server timeouts. They are deliberately short: a client
// that cannot send its headers within seconds is not a client this API serves.
func loadHTTP(cfg *Config) error {
	switch cfg.AppEnv {
	case "development", "production", "test":
	default:
		return errors.New("APP_ENV must be development, production, or test")
	}

	timeouts := []struct {
		key      string
		fallback time.Duration
		target   *time.Duration
	}{
		{"HTTP_READ_HEADER_TIMEOUT", 5 * time.Second, &cfg.HTTPReadHeaderTimeout},
		{"HTTP_READ_TIMEOUT", 15 * time.Second, &cfg.HTTPReadTimeout},
		{"HTTP_WRITE_TIMEOUT", 30 * time.Second, &cfg.HTTPWriteTimeout},
		{"HTTP_IDLE_TIMEOUT", 60 * time.Second, &cfg.HTTPIdleTimeout},
		{"SHUTDOWN_TIMEOUT", shutdown.DefaultBudget, &cfg.ShutdownTimeout},
	}

	for _, timeout := range timeouts {
		value, err := durationFromEnv(timeout.key, timeout.fallback)
		if err != nil || value < minHTTPTimeout || value > maxHTTPTimeout {
			return fmt.Errorf("%s must be between 1s and 5m", timeout.key)
		}
		*timeout.target = value
	}

	// A read timeout below the header timeout would cut a client off while it
	// is still sending headers.
	if cfg.HTTPReadTimeout < cfg.HTTPReadHeaderTimeout {
		return errors.New("HTTP_READ_TIMEOUT must not be shorter than HTTP_READ_HEADER_TIMEOUT")
	}
	return nil
}

// loadRateLimit reads the policies and the proxy list used to identify a
// client. An empty TRUSTED_PROXIES means forwarded headers are ignored.
func loadRateLimit(cfg *Config) error {
	enabled, err := boolFromEnv("RATE_LIMIT_ENABLED", true)
	if err != nil {
		return err
	}

	authLimit, err := intFromEnv("RATE_LIMIT_AUTH_LIMIT", 10, 1, 100000)
	if err != nil {
		return err
	}
	authWindow, err := windowFromEnv("RATE_LIMIT_AUTH_WINDOW", time.Minute)
	if err != nil {
		return err
	}
	financialLimit, err := intFromEnv("RATE_LIMIT_FINANCIAL_LIMIT", 30, 1, 100000)
	if err != nil {
		return err
	}
	financialWindow, err := windowFromEnv("RATE_LIMIT_FINANCIAL_WINDOW", time.Minute)
	if err != nil {
		return err
	}
	proxies, err := parsePrefixes(envOrDefault("TRUSTED_PROXIES", ""))
	if err != nil {
		return err
	}

	cfg.RateLimitEnabled = enabled
	cfg.RateLimitAuthLimit = authLimit
	cfg.RateLimitAuthWindow = authWindow
	cfg.RateLimitFinancialLimit = financialLimit
	cfg.RateLimitFinancialWindow = financialWindow
	cfg.TrustedProxies = proxies
	return nil
}

// loadCORS keeps CORS off unless origins are listed explicitly.
func loadCORS(cfg *Config) error {
	origins := splitList(envOrDefault("CORS_ALLOWED_ORIGINS", ""))
	credentials, err := boolFromEnv("CORS_ALLOW_CREDENTIALS", false)
	if err != nil {
		return err
	}

	for _, origin := range origins {
		if origin == "*" {
			if credentials {
				return errors.New("CORS_ALLOWED_ORIGINS must not contain * when credentials are enabled")
			}
			continue
		}
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return errors.New("CORS_ALLOWED_ORIGINS must contain absolute origins")
		}
	}

	cfg.CORSAllowedOrigins = origins
	cfg.CORSAllowCredentials = credentials
	return nil
}

func windowFromEnv(key string, fallback time.Duration) (time.Duration, error) {
	value, err := durationFromEnv(key, fallback)
	if err != nil || value < time.Second || value > time.Hour {
		return 0, fmt.Errorf("%s must be between 1s and 1h", key)
	}
	return value, nil
}

func splitList(value string) []string {
	parts := strings.Split(value, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return items
}

func parsePrefixes(value string) ([]netip.Prefix, error) {
	items := splitList(value)
	prefixes := make([]netip.Prefix, 0, len(items))

	for _, item := range items {
		if prefix, err := netip.ParsePrefix(item); err == nil {
			prefixes = append(prefixes, prefix.Masked())
			continue
		}
		address, err := netip.ParseAddr(item)
		if err != nil {
			return nil, errors.New("TRUSTED_PROXIES must contain IP addresses or CIDR ranges")
		}
		prefixes = append(prefixes, netip.PrefixFrom(address, address.BitLen()))
	}
	return prefixes, nil
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

// loadPostgresPool reads the pgxpool limits. The defaults are deliberately
// small: api, worker, and consumer each open their own pool against the same
// PostgreSQL, so the sum has to stay well under max_connections. Ten per
// process leaves room for a second instance of each and still fits in the
// default of one hundred.
func loadPostgresPool(cfg *Config) error {
	maxConns, err := intFromEnv("POSTGRES_MAX_CONNS", 10, 1, 200)
	if err != nil {
		return err
	}
	minConns, err := intFromEnv("POSTGRES_MIN_CONNS", 1, 0, 200)
	if err != nil {
		return err
	}
	if minConns > maxConns {
		return errors.New("POSTGRES_MIN_CONNS must not exceed POSTGRES_MAX_CONNS")
	}
	maxLifetime, err := durationFromEnv("POSTGRES_MAX_CONN_LIFETIME", time.Hour)
	if err != nil {
		return err
	}
	maxIdle, err := durationFromEnv("POSTGRES_MAX_CONN_IDLE_TIME", 30*time.Minute)
	if err != nil {
		return err
	}

	cfg.PostgresMaxConns = maxConns
	cfg.PostgresMinConns = minConns
	cfg.PostgresMaxConnLifetime = maxLifetime
	cfg.PostgresMaxConnIdleTime = maxIdle
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
	if cfg.AppEnv == "production" && isPlaceholderSecret(secret) {
		return errors.New("JWT_SECRET must not be a placeholder value in production")
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
	cfg.RabbitMQExchange = trimmedEnv("RABBITMQ_EXCHANGE", "payment.events")
	cfg.RabbitMQQueue = trimmedEnv("RABBITMQ_QUEUE", "payment.events")
	cfg.RabbitMQRoutingKey = trimmedEnv("RABBITMQ_ROUTING_KEY", "transfer.completed")
	cfg.RabbitMQRetryExchange = trimmedEnv("RABBITMQ_RETRY_EXCHANGE", "payment.events.retry")
	cfg.RabbitMQRetryQueue = trimmedEnv("RABBITMQ_RETRY_QUEUE", "payment.events.retry")
	cfg.RabbitMQDeadLetterExchange = trimmedEnv("RABBITMQ_DEAD_LETTER_EXCHANGE", "payment.events.dlx")
	cfg.RabbitMQDeadLetterQueue = trimmedEnv("RABBITMQ_DEAD_LETTER_QUEUE", "payment.events.dlq")
	cfg.RabbitMQDeadLetterRoutingKey = trimmedEnv(
		"RABBITMQ_DEAD_LETTER_ROUTING_KEY", "transfer.completed.dlq",
	)
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

// isPlaceholderSecret catches the values shipped in .env.example and the ones
// people type by hand. A real secret is random and matches none of these.
func isPlaceholderSecret(secret string) bool {
	lowered := strings.ToLower(secret)
	for _, marker := range []string{
		"replace", "placeholder", "change", "example", "local_dev", "secret", "password", "your-",
	} {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

func envOrDefault(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func trimmedEnv(key, fallback string) string {
	return strings.TrimSpace(envOrDefault(key, fallback))
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

func boolFromEnv(key string, fallback bool) (bool, error) {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}

	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return parsed, nil
}

func floatFromEnv(key string, fallback float64) (float64, error) {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}

	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number", key)
	}
	return parsed, nil
}
