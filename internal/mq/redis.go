package mq

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
	"test-agent/internal/config"
	"test-agent/internal/logentry"
)

// redisSender pushes entries into a Redis stream.
type redisSender struct {
	client *redis.Client
	stream string
}

func newRedisSender(cfg config.RedisMQConfig, topic string) (*redisSender, error) {
	if cfg.Addr == "" {
		return nil, fmt.Errorf("redis addr is required")
	}
	if cfg.Stream == "" {
		cfg.Stream = topic
	}
	if cfg.Stream == "" {
		cfg.Stream = "agent-logs"
	}
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})
	if err := client.Ping(context.Background()).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis ping failed: %w", err)
	}
	return &redisSender{client: client, stream: cfg.Stream}, nil
}

func (s *redisSender) Send(ctx context.Context, entry logentry.Entry) error {
	data, err := marshalEntry(entry)
	if err != nil {
		return err
	}
	return s.client.XAdd(ctx, &redis.XAddArgs{
		Stream: s.stream,
		Values: map[string]interface{}{
			"payload": string(data),
		},
	}).Err()
}

func (s *redisSender) Close() error {
	return s.client.Close()
}
