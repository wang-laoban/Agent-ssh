package mq

import (
	"context"
	"fmt"

	"github.com/segmentio/kafka-go"
	"test-agent/internal/config"
	"test-agent/internal/logentry"
)

// kafkaSender publishes entries to a Kafka topic.
type kafkaSender struct {
	writer *kafka.Writer
}

func newKafkaSender(cfg config.KafkaMQConfig, topic string) (*kafkaSender, error) {
	if len(cfg.Brokers) == 0 {
		return nil, fmt.Errorf("kafka brokers are required")
	}
	if cfg.Topic != "" {
		topic = cfg.Topic
	}
	if topic == "" {
		topic = "agent-logs"
	}
	writer := &kafka.Writer{
		Addr:     kafka.TCP(cfg.Brokers...),
		Topic:    topic,
		Balancer: &kafka.LeastBytes{},
	}
	return &kafkaSender{writer: writer}, nil
}

func (s *kafkaSender) Send(ctx context.Context, entry logentry.Entry) error {
	data, err := marshalEntry(entry)
	if err != nil {
		return err
	}
	return s.writer.WriteMessages(ctx, kafka.Message{
		Key:   []byte(fmt.Sprintf("%020d", entry.Seq)),
		Value: data,
	})
}

func (s *kafkaSender) Close() error {
	return s.writer.Close()
}
