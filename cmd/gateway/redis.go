package main

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const redisStartupPingTimeout = 5 * time.Second

func NewRedisClient(cfg Config) (*redis.Client, error) {
	options, err := redis.ParseURL(cfg.RedisDSN)
	if err != nil {
		return nil, fmt.Errorf("parse redis dsn: %w", err)
	}

	client := redis.NewClient(options)

	ctx, cancel := context.WithTimeout(context.Background(), redisStartupPingTimeout)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	return client, nil
}
