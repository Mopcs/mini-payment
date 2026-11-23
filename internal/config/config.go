package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	AppAddr        string
	PostgresHost   string
	PostgresPort   uint16
	PostgresDB     string
	PostgresUser   string
	PostgresPass   string
	RedisHost      string
	RedisPort      uint16
	RedisPassword  string
	LogLevel       string
}

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

	return Config{
		AppAddr:       envOrDefault("APP_ADDR", ":8080"),
		PostgresHost:  envOrDefault("POSTGRES_HOST", "localhost"),
		PostgresPort:  postgresPort,
		PostgresDB:    envOrDefault("POSTGRES_DB", "payment_db"),
		PostgresUser:  envOrDefault("POSTGRES_USER", "payment_user"),
		PostgresPass:  postgresPassword,
		RedisHost:     envOrDefault("REDIS_HOST", "localhost"),
		RedisPort:     redisPort,
		RedisPassword: redisPassword,
		LogLevel:      logLevel,
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
