package mq

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"test-agent/internal/config"
	"test-agent/internal/logentry"
)

// fileSender writes each entry as a JSON line file under a spool directory.
// It is useful for local testing or as a disk-based queue.
type fileSender struct {
	dir   string
	topic string
	mu    sync.Mutex
}

func newFileSender(cfg config.FileMQConfig, topic string) (*fileSender, error) {
	if cfg.Dir == "" {
		return nil, fmt.Errorf("file mq dir is required")
	}
	if topic == "" {
		topic = "agent-logs"
	}
	dir := filepath.Join(cfg.Dir, topic)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create file mq dir: %w", err)
	}
	return &fileSender{dir: dir, topic: topic}, nil
}

func (s *fileSender) Send(ctx context.Context, entry logentry.Entry) error {
	data, err := marshalEntry(entry)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	name := fmt.Sprintf("%s_%020d.json", entry.Timestamp.UTC().Format("20060102T150405.000000"), entry.Seq)
	path := filepath.Join(s.dir, name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write mq temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename mq file: %w", err)
	}
	return nil
}

func (s *fileSender) Close() error { return nil }
