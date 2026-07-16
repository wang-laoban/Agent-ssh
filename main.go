package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"test-agent/internal/auth"
	"test-agent/internal/config"
	"test-agent/internal/executor"
	"test-agent/internal/gateway"
	"test-agent/internal/taskmanager"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("failed_to_load_config", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// Auth manager: initialize or load token, enforce 0o600 permissions.
	authMgr := auth.New(cfg.Auth, logger)
	if err := authMgr.InitToken(); err != nil {
		logger.Error("failed_to_initialize_token", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// Executor: command filters and execution engine.
	execEngine, err := executor.New(cfg.Executor, logger)
	if err != nil {
		logger.Error("failed_to_initialize_executor", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// Task manager: async job lifecycle.
	tm := taskmanager.New(cfg.TaskManager, execEngine, logger)

	// Gateway: HTTP server with routes and middleware.
	server := gateway.New(cfg.Server, authMgr, execEngine, tm, logger)

	// Context for background services.
	svcCtx, svcCancel := context.WithCancel(context.Background())
	defer svcCancel()

	authMgr.StartAutoRotation(svcCtx)
	tm.StartCleanup(svcCtx)

	// Graceful shutdown handling.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := server.Start(); err != nil && err != http.ErrServerClosed {
			logger.Error("http_server_error", slog.String("error", err.Error()))
		}
	}()

	logger.Info("agent_started",
		slog.String("listen_addr", cfg.Server.ListenAddr()),
		slog.String("token_file", cfg.Auth.TokenFile),
	)

	<-quit
	logger.Info("shutdown_signal_received")

	// Cancel all running tasks first.
	tm.CancelAll()

	// Stop background services.
	svcCancel()

	// Gracefully shut down HTTP server.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("http_server_shutdown_error", slog.String("error", err.Error()))
	}

	wg.Wait()
	logger.Info("agent_stopped_gracefully")
}
