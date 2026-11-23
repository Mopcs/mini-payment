package redis

import (
	"context"

	redisclient "github.com/redis/go-redis/v9"
)

type Pinger struct {
	client *redisclient.Client
}

func NewPinger(client *redisclient.Client) Pinger {
	return Pinger{client: client}
}

func (p Pinger) Ping(ctx context.Context) error {
	return p.client.Ping(ctx).Err()
}
