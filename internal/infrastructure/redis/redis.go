package redis

import (
	"net"
	"strconv"

	redisclient "github.com/redis/go-redis/v9"

	"github.com/Mopcs/mini-payment-system/internal/config"
)

func NewClient(cfg config.Config) *redisclient.Client {
	return redisclient.NewClient(&redisclient.Options{
		Addr:         net.JoinHostPort(cfg.RedisHost, strconv.Itoa(int(cfg.RedisPort))),
		Password:     cfg.RedisPassword,
		PoolSize:     10,
		MinIdleConns: 1,
	})
}
