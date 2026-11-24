package postgres

import (
	"context"
	"time"

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
	poolConfig.MaxConns = 10
	poolConfig.MinConns = 1
	poolConfig.MaxConnLifetime = time.Hour
	poolConfig.MaxConnIdleTime = 30 * time.Minute

	return pgxpool.NewWithConfig(ctx, poolConfig)
}
