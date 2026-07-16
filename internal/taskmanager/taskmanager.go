package taskmanager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"test-agent/internal/config"
	"test-agent/internal/executor"
)

// TaskStatus represents the lifecycle state of a task.
type TaskStatus string

const (
	TaskPending   TaskStatus = "pending"
	TaskRunning   TaskStatus = "running"
	TaskCompleted TaskStatus = "completed"
	TaskFailed    TaskStatus = "failed"
	TaskKilled    TaskStatus = "killed"
	TaskTimeout   TaskStatus = "timeout"
)

// Task describes an async command execution job.
type Task struct {
	ID         string               `json:"task_id"`
	Command    string               `json:"command"`
	Status     TaskStatus           `json:"status"`
	Result     *executor.ExecResult `json:"result,omitempty"`
	CreatedAt  time.Time            `json:"created_at"`
	UpdatedAt  time.Time            `json:"updated_at"`
	StartedAt  *time.Time           `json:"started_at,omitempty"`
	FinishedAt *time.Time           `json:"finished_at,omitempty"`

	ctx    context.Context
	cancel context.CancelFunc
}

// TaskManager manages async task execution lifecycle.
type TaskManager struct {
	mu           sync.RWMutex
	tasks        map[string]*Task
	executor     *executor.Executor
	maxRunning   int
	retention    time.Duration
	runningCount int
	logger       *slog.Logger
}

// New creates a TaskManager from configuration.
func New(cfg config.TaskManagerConfig, exec *executor.Executor, logger *slog.Logger) *TaskManager {
	return &TaskManager{
		tasks:      make(map[string]*Task),
		executor:   exec,
		maxRunning: cfg.MaxRunningTasks,
		retention:  cfg.Retention(),
		logger:     logger,
	}
}

// ErrMaxRunning is returned when too many tasks are running.
var ErrMaxRunning = errors.New("too many running tasks")

// Submit creates and starts a new async task.
func (tm *TaskManager) Submit(command string, timeout time.Duration) (string, error) {
	tm.mu.Lock()
	if tm.runningCount >= tm.maxRunning {
		tm.mu.Unlock()
		return "", fmt.Errorf("%w: max %d", ErrMaxRunning, tm.maxRunning)
	}

	taskID := executor.GenerateTaskID()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	task := &Task{
		ID:        taskID,
		Command:   command,
		Status:    TaskPending,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		ctx:       ctx,
		cancel:    cancel,
	}
	tm.tasks[taskID] = task
	tm.runningCount++
	tm.mu.Unlock()

	tm.logger.Info("task_submitted",
		slog.String("task_id", taskID),
		slog.String("command_preview", previewCommand(command)),
	)

	go tm.runTask(task, timeout)
	return taskID, nil
}

// Get returns a copy of the task for read-only access.
func (tm *TaskManager) Get(taskID string) (*Task, error) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	task, ok := tm.tasks[taskID]
	if !ok {
		return nil, fmt.Errorf("task %s not found", taskID)
	}
	return task, nil
}

// Cancel kills a running task by cancelling its context.
func (tm *TaskManager) Cancel(taskID string) (*Task, error) {
	tm.mu.Lock()
	task, ok := tm.tasks[taskID]
	if !ok {
		tm.mu.Unlock()
		return nil, fmt.Errorf("task %s not found", taskID)
	}
	if task.Status == TaskCompleted || task.Status == TaskFailed || task.Status == TaskKilled || task.Status == TaskTimeout {
		tm.mu.Unlock()
		return task, nil
	}
	task.cancel()
	task.Status = TaskKilled
	task.UpdatedAt = time.Now()
	tm.runningCount--
	tm.mu.Unlock()

	tm.logger.Info("task_cancelled", slog.String("task_id", taskID))
	return task, nil
}

// CancelAll cancels every running task. Used during graceful shutdown.
func (tm *TaskManager) CancelAll() {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	for _, task := range tm.tasks {
		switch task.Status {
		case TaskPending, TaskRunning:
			task.cancel()
			task.Status = TaskKilled
			task.UpdatedAt = time.Now()
			tm.runningCount--
		}
	}
}

// runTask executes the command and updates task state.
func (tm *TaskManager) runTask(task *Task, timeout time.Duration) {
	now := time.Now()

	tm.mu.Lock()
	task.Status = TaskRunning
	task.StartedAt = &now
	task.UpdatedAt = now
	tm.mu.Unlock()

	result := tm.executor.ExecuteWithID(task.ctx, task.ID, task.Command, timeout)

	finish := time.Now()
	tm.mu.Lock()
	defer tm.mu.Unlock()

	task.Result = result
	task.FinishedAt = &finish
	task.UpdatedAt = finish
	tm.runningCount--

	if task.ctx.Err() == context.DeadlineExceeded {
		task.Status = TaskTimeout
	} else if task.Status == TaskKilled {
		// keep killed
	} else if result.ExitCode == 0 {
		task.Status = TaskCompleted
	} else {
		task.Status = TaskFailed
	}

	tm.logger.Info("task_finished",
		slog.String("task_id", task.ID),
		slog.String("status", string(task.Status)),
		slog.Int("exit_code", result.ExitCode),
	)
}

// StartCleanup starts a background goroutine that removes expired completed tasks.
func (tm *TaskManager) StartCleanup(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				tm.cleanup()
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (tm *TaskManager) cleanup() {
	cutoff := time.Now().Add(-tm.retention)
	tm.mu.Lock()
	defer tm.mu.Unlock()
	for id, task := range tm.tasks {
		switch task.Status {
		case TaskCompleted, TaskFailed, TaskKilled, TaskTimeout:
			if task.FinishedAt != nil && task.FinishedAt.Before(cutoff) {
				delete(tm.tasks, id)
				tm.logger.Info("task_expired", slog.String("task_id", id))
			}
		}
	}
}

func previewCommand(command string) string {
	const maxLen = 80
	if len(command) <= maxLen {
		return command
	}
	return command[:maxLen] + "..."
}
