package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds the whole application configuration.
type Config struct {
	Server      ServerConfig      `yaml:"server"`
	Auth        AuthConfig        `yaml:"auth"`
	Executor    ExecutorConfig    `yaml:"executor"`
	TaskManager TaskManagerConfig `yaml:"task_manager"`
	Log         LogConfig         `yaml:"log"`
}

// ServerConfig describes the HTTP server binding.
type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

// AuthConfig describes token storage and rotation settings.
type AuthConfig struct {
	TokenFile    string `yaml:"token_file"`
	RotationHour int    `yaml:"rotation_hour"`
	TokenLength  int    `yaml:"token_length"`
}

// ExecutorConfig describes command execution limits and filters.
type ExecutorConfig struct {
	DefaultTimeout    int      `yaml:"default_timeout"`
	MaxTimeout        int      `yaml:"max_timeout"`
	MaxOutputSizeMB   int      `yaml:"max_output_size_mb"`
	AllowedCommands   []string `yaml:"allowed_commands"`
	BlockedKeywords   []string `yaml:"blocked_keywords"`
	WorkDir           string   `yaml:"work_dir"`
	UploadDir         string   `yaml:"upload_dir"`
	MaxUploadSizeMB   int      `yaml:"max_upload_size_mb"`
	AllowedExtensions []string `yaml:"allowed_extensions"`
}

// TaskManagerConfig describes async task limits and retention.
type TaskManagerConfig struct {
	MaxRunningTasks  int `yaml:"max_running_tasks"`
	RetentionMinutes int `yaml:"retention_minutes"`
}

// LogConfig describes tamper-proof structured logging and MQ dispatch.
type LogConfig struct {
	Enabled          bool     `yaml:"enabled"`
	File             string   `yaml:"file"`
	SignKeyFile      string   `yaml:"sign_key_file"`
	VerifyPubFile    string   `yaml:"verify_pub_file"`
	SignatureAlgo    string   `yaml:"signature_algo"`
	ConsoleOutput    bool     `yaml:"console_output"`
	AutoGenerateKeys bool     `yaml:"auto_generate_keys"`
	MQ               MQConfig `yaml:"mq"`
}

// MQConfig describes the message queue used for off-site log replication.
type MQConfig struct {
	Type  string        `yaml:"type"`
	Topic string        `yaml:"topic"`
	File  FileMQConfig  `yaml:"file"`
	Redis RedisMQConfig `yaml:"redis"`
	Kafka KafkaMQConfig `yaml:"kafka"`
}

// FileMQConfig is a disk-based queue for testing or local spooling.
type FileMQConfig struct {
	Dir string `yaml:"dir"`
}

// RedisMQConfig targets a Redis stream.
type RedisMQConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
	Stream   string `yaml:"stream"`
}

// KafkaMQConfig targets an Apache Kafka topic.
type KafkaMQConfig struct {
	Brokers []string `yaml:"brokers"`
	Topic   string   `yaml:"topic"`
}

// DefaultConfig returns a Config pre-filled with design-document defaults.
func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Host: "0.0.0.0",
			Port: 8080,
		},
		Auth: AuthConfig{
			TokenFile:    "/var/run/agent/.api_token",
			RotationHour: 0,
			TokenLength:  32,
		},
		Executor: ExecutorConfig{
			DefaultTimeout:  30,
			MaxTimeout:      300,
			MaxOutputSizeMB: 10,
			AllowedCommands: []string{},
			BlockedKeywords: []string{
				`rm -rf /`,
				`mkfs`,
				`dd if=/dev/zero`,
				`shutdown`,
				`:(){ :|:& };:`,
			},
			WorkDir:           "/tmp/agent_workspace",
			UploadDir:         "",
			MaxUploadSizeMB:   64,
			AllowedExtensions: []string{},
		},
		TaskManager: TaskManagerConfig{
			MaxRunningTasks:  50,
			RetentionMinutes: 60,
		},
		Log: LogConfig{
			Enabled:          true,
			File:             "/var/log/agent/agent.log",
			SignKeyFile:      "/etc/agent/agent-sign.key",
			VerifyPubFile:    "/etc/agent/agent-sign.pub",
			SignatureAlgo:    "ed25519",
			ConsoleOutput:    false,
			AutoGenerateKeys: true,
			MQ: MQConfig{
				Type:  "file",
				Topic: "agent-logs",
				File: FileMQConfig{
					Dir: "/var/spool/agent/mq",
				},
			},
		},
	}
}

// Load reads a YAML config file, applies defaults for missing fields, and
// optionally overrides values from environment variables.
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config file %q: %w", path, err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse config file %q: %w", path, err)
		}
	}

	applyEnvOverrides(cfg)

	if cfg.Server.Port <= 0 {
		return nil, fmt.Errorf("server.port must be > 0")
	}
	if cfg.Auth.TokenLength <= 0 {
		cfg.Auth.TokenLength = 32
	}
	if cfg.Executor.DefaultTimeout <= 0 {
		cfg.Executor.DefaultTimeout = 30
	}
	if cfg.Executor.MaxTimeout <= 0 {
		cfg.Executor.MaxTimeout = 300
	}
	if cfg.Executor.MaxOutputSizeMB <= 0 {
		cfg.Executor.MaxOutputSizeMB = 10
	}
	if cfg.Executor.MaxUploadSizeMB <= 0 {
		cfg.Executor.MaxUploadSizeMB = 64
	}
	if cfg.TaskManager.MaxRunningTasks <= 0 {
		cfg.TaskManager.MaxRunningTasks = 50
	}
	if cfg.TaskManager.RetentionMinutes <= 0 {
		cfg.TaskManager.RetentionMinutes = 60
	}
	if cfg.Log.File == "" {
		cfg.Log.File = "/var/log/agent/agent.log"
	}
	if cfg.Log.SignatureAlgo == "" {
		cfg.Log.SignatureAlgo = "ed25519"
	}
	if cfg.Log.MQ.Topic == "" {
		cfg.Log.MQ.Topic = "agent-logs"
	}

	return cfg, nil
}

// applyEnvOverrides reads specific environment variables and overrides config.
func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("SERVER_HOST"); v != "" {
		cfg.Server.Host = v
	}
	if v := os.Getenv("SERVER_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Server.Port = n
		}
	}
	if v := os.Getenv("AUTH_TOKEN_FILE"); v != "" {
		cfg.Auth.TokenFile = v
	}
	if v := os.Getenv("AUTH_ROTATION_HOUR"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Auth.RotationHour = n
		}
	}
	if v := os.Getenv("EXECUTOR_WORK_DIR"); v != "" {
		cfg.Executor.WorkDir = v
	}
	if v := os.Getenv("EXECUTOR_UPLOAD_DIR"); v != "" {
		cfg.Executor.UploadDir = v
	}
	if v := os.Getenv("EXECUTOR_MAX_UPLOAD_SIZE_MB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Executor.MaxUploadSizeMB = n
		}
	}
	if v := os.Getenv("EXECUTOR_ALLOWED_EXTENSIONS"); v != "" {
		cfg.Executor.AllowedExtensions = strings.Split(v, ",")
	}
	if v := os.Getenv("LOG_ENABLED"); v != "" {
		cfg.Log.Enabled = parseBool(v, cfg.Log.Enabled)
	}
	if v := os.Getenv("LOG_FILE"); v != "" {
		cfg.Log.File = v
	}
	if v := os.Getenv("LOG_SIGN_KEY_FILE"); v != "" {
		cfg.Log.SignKeyFile = v
	}
	if v := os.Getenv("LOG_CONSOLE_OUTPUT"); v != "" {
		cfg.Log.ConsoleOutput = parseBool(v, cfg.Log.ConsoleOutput)
	}
	if v := os.Getenv("LOG_AUTO_GENERATE_KEYS"); v != "" {
		cfg.Log.AutoGenerateKeys = parseBool(v, cfg.Log.AutoGenerateKeys)
	}
	if v := os.Getenv("LOG_MQ_TYPE"); v != "" {
		cfg.Log.MQ.Type = v
	}
	if v := os.Getenv("LOG_MQ_TOPIC"); v != "" {
		cfg.Log.MQ.Topic = v
	}
	if v := os.Getenv("LOG_MQ_FILE_DIR"); v != "" {
		cfg.Log.MQ.File.Dir = v
	}
	if v := os.Getenv("LOG_MQ_REDIS_ADDR"); v != "" {
		cfg.Log.MQ.Redis.Addr = v
	}
	if v := os.Getenv("LOG_MQ_KAFKA_BROKERS"); v != "" {
		cfg.Log.MQ.Kafka.Brokers = strings.Split(v, ",")
	}
}

func parseBool(v string, def bool) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

// MaxOutputSize returns the configured maximum output size in bytes.
func (c *ExecutorConfig) MaxOutputSize() int64 {
	return int64(c.MaxOutputSizeMB) * 1024 * 1024
}

// Retention returns the configured retention duration.
func (c *TaskManagerConfig) Retention() time.Duration {
	return time.Duration(c.RetentionMinutes) * time.Minute
}

// ListenAddr returns the server listen address.
func (c *ServerConfig) ListenAddr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}
