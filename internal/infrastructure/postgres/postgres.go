package postgres

import (
	"context"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Mopcs/mini-payment-system/internal/config"
)

func NewPool(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	// ParseConfig with an empty DSN gives us the library defaults; the real
	// values are filled in from Config below.
	poolConfig, err := pgxpool.ParseConfig("")
	if err != nil {
		return nil, err
	}
	poolConfig.ConnConfig.Host = cfg.PostgresHost
	poolConfig.ConnConfig.Port = cfg.PostgresPort
	poolConfig.ConnConfig.Database = cfg.PostgresDB
	poolConfig.ConnConfig.User = cfg.PostgresUser
	poolConfig.ConnConfig.Password = cfg.PostgresPass
	poolConfig.MaxConns = int32(cfg.PostgresMaxConns)
	poolConfig.MinConns = int32(cfg.PostgresMinConns)
	poolConfig.MaxConnLifetime = cfg.PostgresMaxConnLifetime
	poolConfig.MaxConnIdleTime = cfg.PostgresMaxConnIdleTime

	// The tracer reads the statement text but never the bind parameters, so
	// wallet identifiers and amounts stay out of the spans.
	if cfg.TracingEnabled {
		poolConfig.ConnConfig.Tracer = otelpgx.NewTracer()
	}

	return pgxpool.NewWithConfig(ctx, poolConfig)
}
