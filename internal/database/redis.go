package database

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// NewRedis connects to Redis and verifies it is reachable.
//
// Like NewMongo, the ping is the point: the client is lazy, so without it an
// unreachable cache surfaces on the first request rather than at startup.
func NewRedis(ctx context.Context, addr, password string, db int) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})

	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return client, nil
}
