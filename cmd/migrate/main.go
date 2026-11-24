package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/joho/godotenv"
)

var errUsage = errors.New("usage: go run ./cmd/migrate <up|down|version|force VERSION>")

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errUsage
	}

	m, err := newMigrator()
	if err != nil {
		return err
	}
	defer func() {
		_, _ = m.Close()
	}()

	switch args[0] {
	case "up":
		return runUp(m, args)
	case "down":
		return runDown(m, args)
	case "version":
		return runVersion(m, args)
	case "force":
		return runForce(m, args)
	default:
		return errUsage
	}
}

func newMigrator() (*migrate.Migrate, error) {
	databaseURL, err := postgresURL()
	if err != nil {
		return nil, err
	}

	m, err := migrate.New("file://migrations", databaseURL.String())
	if err != nil {
		return nil, fmt.Errorf("initialize migration runner: %w", err)
	}
	return m, nil
}

func runUp(m *migrate.Migrate, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: go run ./cmd/migrate up")
	}

	err := m.Up()
	if errors.Is(err, migrate.ErrNoChange) {
		fmt.Println("database is up to date")
		return nil
	}
	return migrationError(err)
}

func runDown(m *migrate.Migrate, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: go run ./cmd/migrate down")
	}

	err := m.Steps(-1)
	if errors.Is(err, migrate.ErrNoChange) || errors.Is(err, migrate.ErrNilVersion) {
		fmt.Println("no migration to roll back")
		return nil
	}
	return migrationError(err)
}

func runVersion(m *migrate.Migrate, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: go run ./cmd/migrate version")
	}

	version, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		fmt.Println("no migrations applied")
		return nil
	}
	if err != nil {
		return fmt.Errorf("read migration version: %w", err)
	}
	fmt.Printf("version: %d, dirty: %t\n", version, dirty)
	return nil
}

func runForce(m *migrate.Migrate, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: go run ./cmd/migrate force VERSION")
	}

	version, err := strconv.Atoi(args[1])
	if err != nil || version < 0 {
		return errors.New("migration version must be a non-negative integer")
	}

	// Force only rewrites the bookkeeping table, so refuse it on a clean database
	// where a real migration would fix things.
	_, dirty, err := m.Version()
	if err != nil || !dirty {
		return errors.New("force is only allowed when the database is in a dirty migration state")
	}

	fmt.Fprintln(os.Stderr,
		"WARNING: force changes migration bookkeeping without applying SQL; "+
			"use only after manually recovering a dirty migration.")

	if err := m.Force(version); err != nil {
		return fmt.Errorf("force migration version: %w", err)
	}
	return nil
}

func migrationError(err error) error {
	if err != nil {
		return fmt.Errorf("migration command failed: %w", err)
	}
	return nil
}

func postgresURL() (*url.URL, error) {
	if err := godotenv.Load(".env"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("load .env: file could not be parsed")
	}

	password, ok := os.LookupEnv("POSTGRES_PASSWORD")
	if !ok || password == "" {
		return nil, errors.New("POSTGRES_PASSWORD is required")
	}

	port := envOrDefault("POSTGRES_PORT", "5432")
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return nil, errors.New("POSTGRES_PORT must be between 1 and 65535")
	}

	query := url.Values{}
	query.Set("sslmode", "disable")
	return &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(envOrDefault("POSTGRES_USER", "payment_user"), password),
		Host:     net.JoinHostPort(envOrDefault("POSTGRES_HOST", "localhost"), port),
		Path:     "/" + envOrDefault("POSTGRES_DB", "payment_db"),
		RawQuery: query.Encode(),
	}, nil
}

func envOrDefault(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}
