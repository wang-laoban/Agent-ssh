// Package mq provides pluggable message-queue senders for tamper-proof log
// replication. Implementations are selected by config type.
package mq

import (
	"context"
	"encoding/json"
	"fmt"

	"test-agent/internal/config"
	"test-agent/internal/logentry"
)

// Sender delivers a structured log entry to a message queue.
type Sender interface {
	Send(ctx context.Context, entry logentry.Entry) error
	Close() error
}

// New constructs a Sender from configuration.
func New(cfg config.MQConfig) (Sender, error) {
	switch cfg.Type {
	case "", "noop", "none":
		return &noopSender{}, nil
	case "file":
		return newFileSender(cfg.File, cfg.Topic)
	case "redis":
		return newRedisSender(cfg.Redis, cfg.Topic)
	case "kafka":
		return newKafkaSender(cfg.Kafka, cfg.Topic)
	default:
		return nil, fmt.Errorf("unsupported mq type %q", cfg.Type)
	}
}

// noopSender discards messages.
type noopSender struct{}

func (n *noopSender) Send(ctx context.Context, entry logentry.Entry) error { return nil }
func (n *noopSender) Close() error                                       { return nil }

// marshalEntry returns the JSON encoding of an entry.
func marshalEntry(entry logentry.Entry) ([]byte, error) {
	return json.Marshal(entry)
}
