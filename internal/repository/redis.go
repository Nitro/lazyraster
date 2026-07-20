package repository

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/nitro/lazyraster/v2/internal/domain"
)

type RedisClient struct {
	baseClient *redis.Client
}

func (rc RedisClient) FetchAnnotation(ctx context.Context, key string) ([]any, error) {
	result, err := rc.baseClient.Get(ctx, key).Result()
	if err == redis.Nil {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("failed to get the key '%s': %w", key, err)
	}

	annotations, err := domain.ParseAnnotations([]byte(result))
	if err != nil {
		return nil, fmt.Errorf("failed to parse the annotations: %w", err)
	}

	return annotations, nil
}

func NewRedisClient(addr, username, password string) (RedisClient, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Username: username,
		Password: password,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	})
	ctx, ctxcancel := context.WithTimeout(context.Background(), time.Second)
	defer ctxcancel()
	if _, err := rdb.Ping(ctx).Result(); err != nil {
		return RedisClient{}, fmt.Errorf("failed to connect to redis: %w", err)
	}
	return RedisClient{
		baseClient: rdb,
	}, nil
}
