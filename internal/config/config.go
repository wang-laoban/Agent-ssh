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
			DefaultTimeout:    30,
			MaxTimeout:        300,
			MaxOutputSizeMB:   10,
			AllowedCommands:   []string{},
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
