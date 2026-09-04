package main

import (
	"context"
	"flag"
	"fmt"
	"io"
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
	"test-agent/internal/logger"
	"test-agent/internal/taskmanager"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	generateKeys := flag.Bool("generate-keys", false, "generate an Ed25519 signing key pair and exit")
	keyPrefix := flag.String("key-prefix", "agent-sign", "output prefix for generated key files")
	verifyLog := flag.String("verify-log", "", "verify a tamper-proof log file using the configured public key and exit")
	flag.Parse()

	if *generateKeys {
		priv, pub, err := logger.GenerateKeyPair(*keyPrefix)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to generate key pair: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Generated signing key pair:\n  private: %s\n  public:  %s\n", priv, pub)
		os.Exit(0)
	}

	if *verifyLog != "" {
		cfg, err := config.Load(*configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
			os.Exit(1)
		}
		pubKey, err := logger.LoadPublicKey(cfg.Log.VerifyPubFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to load public key: %v\n", err)
			os.Exit(1)
		}
		if err := logger.VerifyLogFile(*verifyLog, pubKey); err != nil {
			fmt.Fprintf(os.Stderr, "log verification failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("log verification succeeded")
		os.Exit(0)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Auto-generate the signing key pair when configured and missing, so the
	// agent starts directly without a prior --generate-keys step. An existing
	// key is never overwritten.
	if cfg.Log.Enabled && cfg.Log.AutoGenerateKeys {
		generated, err := logger.EnsureKeyPair(cfg.Log.SignatureAlgo, cfg.Log.SignKeyFile, cfg.Log.VerifyPubFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to auto-generate signing keys: %v\n", err)
			os.Exit(1)
		}
		if generated {
			fmt.Fprintf(os.Stderr, "auto-generated signing key pair: private=%s public=%s\n",
				cfg.Log.SignKeyFile, cfg.Log.VerifyPubFile)
		}
	}

	logger := initLogger(cfg)
	defer func() {
		if closer, ok := logger.Handler().(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}()

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
		slog.String("log_file", cfg.Log.File),
		slog.String("mq_type", cfg.Log.MQ.Type),
	)

	// Print the token on startup so operators can grab it without opening the
	// token file. This goes to stdout, never into the tamper-proof log.
	fmt.Printf(`
============================================================
  Agent-ssh is running
  Listen addr : %s
  API Token   : %s
  Token file  : %s
  Log file    : %s
------------------------------------------------------------
  The token rotates daily at %02d:00 local time; after a
  rotation, re-read it from the token file above.
============================================================

`, cfg.Server.ListenAddr(), authMgr.CurrentToken(), cfg.Auth.TokenFile, cfg.Log.File, cfg.Auth.RotationHour)

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

// initLogger attempts to create the tamper-proof logger. If it fails, it falls
// back to stderr logging so that startup diagnostics are still visible.
func initLogger(cfg *config.Config) *slog.Logger {
	if !cfg.Log.Enabled {
		return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		}))
	}

	var console io.Writer
	if cfg.Log.ConsoleOutput {
		console = os.Stderr
	}
	tpLogger, err := logger.New(cfg.Log, console)
	if err != nil {
		return logger.NewFallback(err)
	}

	handlerLogger := slog.New(tpLogger)
	return handlerLogger
}
