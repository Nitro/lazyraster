package repository

import (
	"context"
	"crypto/tls"
	"encoding/json"
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
	if err != nil {
		return nil, fmt.Errorf("failed to get the key '%s': %w", key, err)
	}

	annotations, err := rc.parseAnnotations(result)
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

func (RedisClient) parseAnnotations(input string) ([]any, error) {
	var rawEntries []json.RawMessage
	if err := json.Unmarshal([]byte(input), &rawEntries); err != nil {
		return nil, err
	}

	result := make([]any, 0, len(rawEntries))
	for _, rawEntry := range rawEntries {
		e := struct {
			Type string `json:"type"`
		}{}
		if err := json.Unmarshal(rawEntry, &e); err != nil {
			return nil, fmt.Errorf("failed to unmarshal message: %w", err)
		}
		var value any
		switch e.Type {
		case "checkbox":
			value = domain.AnnotationCheckbox{}
		case "image":
			value = domain.AnnotationImage{}
		case "text":
			value = domain.AnnotationText{}
		default:
			return nil, fmt.Errorf("unknow annotation type '%s'", e.Type)
		}
		if err := json.Unmarshal(rawEntry, &value); err != nil {
			return nil, fmt.Errorf("failed to unmarshal message: %w", err)
		}
		result = append(result, value)
	}

	return result, nil
}
