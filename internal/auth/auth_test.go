package auth

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"test-agent/internal/config"
)

func newTestTokenManager(t *testing.T) *TokenManager {
	t.Helper()
	cfg := config.AuthConfig{
		TokenFile:    filepath.Join(t.TempDir(), ".api_token"),
		RotationHour: 0,
		TokenLength:  16,
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return New(cfg, logger)
}

func TestOnRotateCallbackInvoked(t *testing.T) {
	tm := newTestTokenManager(t)

	var got string
	tm.OnRotate = func(token string) { got = token }

	token, err := tm.RotateToken()
	if err != nil {
		t.Fatalf("RotateToken failed: %v", err)
	}
	if got == "" {
		t.Fatal("expected OnRotate to be invoked")
	}
	if got != token {
		t.Fatalf("expected callback token %q, got %q", token, got)
	}
	if got != tm.CurrentToken() {
		t.Fatal("callback token does not match current token")
	}
}

func TestRotateWithoutCallback(t *testing.T) {
	tm := newTestTokenManager(t)
	// OnRotate is nil; rotation must not panic.
	if _, err := tm.RotateToken(); err != nil {
		t.Fatalf("RotateToken failed: %v", err)
	}
}
