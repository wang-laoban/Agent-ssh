package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"test-agent/internal/config"
)

// TokenManager handles token generation, caching, rotation and verification.
type TokenManager struct {
	mu           sync.RWMutex
	token        string
	tokenFile    string
	tokenLength  int
	rotationHour int
	logger       *slog.Logger

	manualRotate chan struct{}
}

// New creates a TokenManager from configuration.
func New(cfg config.AuthConfig, logger *slog.Logger) *TokenManager {
	return &TokenManager{
		tokenFile:    cfg.TokenFile,
		tokenLength:  cfg.TokenLength,
		rotationHour: cfg.RotationHour,
		logger:       logger,
		manualRotate: make(chan struct{}, 1),
	}
}

// InitToken loads an existing token or generates a new one and ensures file
// permissions are exactly 0o600.
func (tm *TokenManager) InitToken() error {
	if err := os.MkdirAll(filepath.Dir(tm.tokenFile), 0o700); err != nil {
		return fmt.Errorf("create token directory: %w", err)
	}

	info, err := os.Stat(tm.tokenFile)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("stat token file: %w", err)
		}
		// File does not exist: generate and atomically write a new token.
		token, err := tm.RotateToken()
		if err != nil {
			return fmt.Errorf("initial token rotation: %w", err)
		}
		tm.logger.Info("token_initialized",
			slog.String("token_hash_prefix", tm.hashPrefix(token)),
			slog.String("token_file", tm.tokenFile),
		)
		return nil
	}

	// File exists: fix permissions if needed, then load.
	mode := info.Mode().Perm()
	if mode != 0o600 {
		if err := os.Chmod(tm.tokenFile, 0o600); err != nil {
			return fmt.Errorf("chmod token file: %w", err)
		}
		tm.logger.Warn("token_file_permissions_fixed",
			slog.String("token_file", tm.tokenFile),
			slog.String("old_mode", fmt.Sprintf("%04o", mode)),
		)
	}

	data, err := os.ReadFile(tm.tokenFile)
	if err != nil {
		return fmt.Errorf("read token file: %w", err)
	}
	if len(data) == 0 {
		// Empty file: rotate to create a valid token.
		token, err := tm.RotateToken()
		if err != nil {
			return fmt.Errorf("initial token rotation on empty file: %w", err)
		}
		tm.logger.Info("token_initialized_from_empty_file",
			slog.String("token_hash_prefix", tm.hashPrefix(token)),
		)
		return nil
	}

	token := string(data)
	tm.setToken(token)
	tm.logger.Info("token_loaded_from_file",
		slog.String("token_hash_prefix", tm.hashPrefix(token)),
	)
	return nil
}

// RotateToken generates a new token, atomically writes it to disk, updates the
// in-memory cache and returns the new token.
func (tm *TokenManager) RotateToken() (string, error) {
	token, err := generateToken(tm.tokenLength)
	if err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}

	if err := tm.atomicWriteToken(token); err != nil {
		return "", fmt.Errorf("write token: %w", err)
	}

	tm.setToken(token)
	tm.logger.Info("token_rotated_successfully",
		slog.String("token_hash_prefix", tm.hashPrefix(token)),
	)
	return token, nil
}

// atomicWriteToken writes the token to a temp file and renames it over the target.
func (tm *TokenManager) atomicWriteToken(token string) error {
	tmpFile := tm.tokenFile + ".tmp"
	if err := os.WriteFile(tmpFile, []byte(token), 0o600); err != nil {
		return fmt.Errorf("write temp token file: %w", err)
	}
	if err := os.Rename(tmpFile, tm.tokenFile); err != nil {
		// Best effort cleanup.
		_ = os.Remove(tmpFile)
		return fmt.Errorf("rename token file: %w", err)
	}
	if err := os.Chmod(tm.tokenFile, 0o600); err != nil {
		return fmt.Errorf("chmod token file after rotation: %w", err)
	}
	return nil
}

// setToken updates the in-memory cached token.
func (tm *TokenManager) setToken(token string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.token = token
}

// getToken returns the current cached token.
func (tm *TokenManager) getToken() string {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.token
}

// VerifyToken checks the provided token first against the in-memory cache, then
// falls back to reading the token file once. If the disk token matches, the
// in-memory cache is refreshed.
func (tm *TokenManager) VerifyToken(token string) bool {
	if subtle.ConstantTimeCompare([]byte(token), []byte(tm.getToken())) == 1 {
		return true
	}

	// Fallback: read disk token (may have been rotated externally).
	data, err := os.ReadFile(tm.tokenFile)
	if err != nil {
		tm.logger.Warn("token_file_read_failed",
			slog.String("error", err.Error()),
		)
		return false
	}
	diskToken := string(data)
	if subtle.ConstantTimeCompare([]byte(token), []byte(diskToken)) == 1 {
		tm.setToken(diskToken)
		tm.logger.Info("token_verified_from_disk",
			slog.String("token_hash_prefix", tm.hashPrefix(diskToken)),
		)
		return true
	}
	return false
}

// TriggerManualRotation requests an immediate token rotation.
func (tm *TokenManager) TriggerManualRotation() {
	select {
	case tm.manualRotate <- struct{}{}:
	default:
	}
}

// StartAutoRotation starts a goroutine that rotates the token daily at the
// configured local-time hour.
func (tm *TokenManager) StartAutoRotation(ctx context.Context) {
	go tm.rotationLoop(ctx)
}

func (tm *TokenManager) rotationLoop(ctx context.Context) {
	for {
		delay := tm.nextRotationDelay()
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
			if _, err := tm.RotateToken(); err != nil {
				tm.logger.Error("auto_token_rotation_failed", slog.String("error", err.Error()))
			}
		case <-tm.manualRotate:
			timer.Stop()
			if _, err := tm.RotateToken(); err != nil {
				tm.logger.Error("manual_token_rotation_failed", slog.String("error", err.Error()))
			}
		case <-ctx.Done():
			timer.Stop()
			return
		}
	}
}

// nextRotationDelay returns the duration until the next rotation point in the
// local timezone.
func (tm *TokenManager) nextRotationDelay() time.Duration {
	now := time.Now()
	loc := now.Location()
	next := time.Date(now.Year(), now.Month(), now.Day(), tm.rotationHour, 0, 0, 0, loc)
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return next.Sub(now)
}

// hashPrefix returns the first 8 hex characters of the SHA256 of the token.
func (tm *TokenManager) hashPrefix(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])[:8]
}

// generateToken generates a cryptographically secure random hex token.
func generateToken(length int) (string, error) {
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
