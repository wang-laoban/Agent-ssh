package executor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"test-agent/internal/config"
)

// ErrBlocked indicates the command matched a blacklist entry.
var ErrBlocked = errors.New("command blocked by security policy")

// ErrNotAllowed indicates the command is not in the configured whitelist.
var ErrNotAllowed = errors.New("command not in allowed list")

// ErrTimeout indicates the command exceeded its allowed runtime.
var ErrTimeout = errors.New("command execution timeout")

// ExecResult captures the outcome of a command execution.
type ExecResult struct {
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Duration  int64  `json:"duration_ms"`
	Truncated bool   `json:"truncated"`
}

// Executor runs shell commands with safety filters and resource limits.
type Executor struct {
	cfg       config.ExecutorConfig
	blockList []*regexp.Regexp
	logger    *slog.Logger
}

// New creates an Executor from configuration.
func New(cfg config.ExecutorConfig, logger *slog.Logger) (*Executor, error) {
	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return nil, fmt.Errorf("create work dir: %w", err)
	}

	uploadDir := cfg.UploadDir
	if uploadDir == "" {
		uploadDir = filepath.Join(cfg.WorkDir, "uploads")
	}
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		return nil, fmt.Errorf("create upload dir: %w", err)
	}

	blockList := make([]*regexp.Regexp, 0, len(cfg.BlockedKeywords))
	for _, pattern := range cfg.BlockedKeywords {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("compile blocked keyword %q: %w", pattern, err)
		}
		blockList = append(blockList, re)
	}

	return &Executor{
		cfg:       cfg,
		blockList: blockList,
		logger:    logger,
	}, nil
}

// Validate checks the command against whitelist and blacklist rules.
func (e *Executor) Validate(command string) error {
	if strings.TrimSpace(command) == "" {
		return errors.New("command is empty")
	}

	// Optional whitelist: if configured, the command must contain one of the
	// allowed command strings as its first token-ish segment.
	if len(e.cfg.AllowedCommands) > 0 {
		firstToken := strings.Fields(command)[0]
		allowed := false
		for _, allowedCmd := range e.cfg.AllowedCommands {
			if firstToken == allowedCmd {
				allowed = true
				break
			}
		}
		if !allowed {
			return ErrNotAllowed
		}
	}

	for _, re := range e.blockList {
		if re.MatchString(command) {
			return fmt.Errorf("%w: matched pattern %q", ErrBlocked, re.String())
		}
	}
	return nil
}

// Execute runs the command under /bin/sh -c with timeout and output limits.
func (e *Executor) Execute(ctx context.Context, command string, timeout time.Duration) *ExecResult {
	start := time.Now()

	if timeout <= 0 {
		timeout = time.Duration(e.cfg.DefaultTimeout) * time.Second
	}
	if max := time.Duration(e.cfg.MaxTimeout) * time.Second; timeout > max {
		timeout = max
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := newShellCommand(execCtx, command)
	setProcessGroup(cmd)
	cmd.Dir = e.cfg.WorkDir

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return &ExecResult{ExitCode: -1, Stderr: fmt.Sprintf("stdout pipe error: %v", err), Duration: time.Since(start).Milliseconds()}
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return &ExecResult{ExitCode: -1, Stderr: fmt.Sprintf("stderr pipe error: %v", err), Duration: time.Since(start).Milliseconds()}
	}

	if err := cmd.Start(); err != nil {
		return &ExecResult{ExitCode: -1, Stderr: fmt.Sprintf("start command error: %v", err), Duration: time.Since(start).Milliseconds()}
	}

	// Ensure the process is killed if the context is cancelled.
	go func() {
		<-execCtx.Done()
		if cmd.Process != nil {
			_ = killProcessTree(cmd)
		}
	}()

	limit := e.cfg.MaxOutputSize()
	stdoutCh := make(chan limitedRead, 1)
	stderrCh := make(chan limitedRead, 1)

	go func() {
		stdoutCh <- e.readLimited(stdoutPipe, limit)
	}()
	go func() {
		stderrCh <- e.readLimited(stderrPipe, limit)
	}()

	stdout := <-stdoutCh
	stderr := <-stderrCh

	waitErr := cmd.Wait()

	result := &ExecResult{
		Stdout:   stdout.data,
		Stderr:   stderr.data,
		Duration: time.Since(start).Milliseconds(),
	}

	if stdout.truncated {
		result.Stderr += "[TRUNCATED: output exceeded limit]"
		result.Truncated = true
	}
	if stderr.truncated {
		result.Stderr += "[TRUNCATED: output exceeded limit]"
		result.Truncated = true
	}

	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = -1
		}
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			result.Stderr += "[timeout]"
			result.ExitCode = -1
		}
	}

	return result
}

type limitedRead struct {
	data      string
	truncated bool
}

func (e *Executor) readLimited(r io.Reader, limit int64) limitedRead {
	lr := &io.LimitedReader{R: r, N: limit}
	buf, _ := io.ReadAll(lr)
	return limitedRead{
		data:      string(buf),
		truncated: lr.N == 0,
	}
}

// ExecuteWithID is a convenience wrapper that tags logs with a request/task ID.
func (e *Executor) ExecuteWithID(ctx context.Context, id string, command string, timeout time.Duration) *ExecResult {
	e.logger.Info("executing_command",
		slog.String("request_id", id),
		slog.String("command_preview", previewCommand(command)),
		slog.Duration("timeout", timeout),
	)
	return e.Execute(ctx, command, timeout)
}

// previewCommand returns a short, safe preview of the command for logging.
func previewCommand(command string) string {
	const maxLen = 80
	if len(command) <= maxLen {
		return command
	}
	return command[:maxLen] + "..."
}

// GenerateTaskID returns a short unique task identifier.
func GenerateTaskID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "task-" + time.Now().Format("20060102-") + hex.EncodeToString(b)
}

// EnsureWorkDir creates the configured work directory if it does not exist.
func (e *Executor) EnsureWorkDir() error {
	return os.MkdirAll(filepath.Clean(e.cfg.WorkDir), 0o755)
}

// UploadDir returns the configured upload directory, defaulting to
// {WorkDir}/uploads if not explicitly set.
func (e *Executor) UploadDir() string {
	if e.cfg.UploadDir != "" {
		return e.cfg.UploadDir
	}
	return filepath.Join(e.cfg.WorkDir, "uploads")
}

// MaxUploadSize returns the configured maximum upload size in bytes.
func (e *Executor) MaxUploadSize() int64 {
	return int64(e.cfg.MaxUploadSizeMB) * 1024 * 1024
}

// AllowedExtensions returns the configured allowed file extensions.
func (e *Executor) AllowedExtensions() []string {
	return e.cfg.AllowedExtensions
}
