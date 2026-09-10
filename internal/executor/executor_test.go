package executor

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"log/slog"

	"test-agent/internal/config"
)

func newTestExecutor(t *testing.T, workDir string) *Executor {
	t.Helper()
	cfg := config.ExecutorConfig{
		DefaultTimeout:  5,
		MaxTimeout:      30,
		MaxOutputSizeMB: 1,
		WorkDir:         workDir,
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	exec, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	return exec
}

func TestResolveWorkDirEmpty(t *testing.T) {
	base := filepath.Join(t.TempDir(), "workspace")
	exec := newTestExecutor(t, base)

	dir, err := exec.ResolveWorkDir("")
	if err != nil {
		t.Fatalf("ResolveWorkDir failed: %v", err)
	}
	if dir != filepath.Clean(base) {
		t.Fatalf("expected %q, got %q", filepath.Clean(base), dir)
	}
}

func TestResolveWorkDirRelativeSubdir(t *testing.T) {
	base := filepath.Join(t.TempDir(), "workspace")
	exec := newTestExecutor(t, base)

	dir, err := exec.ResolveWorkDir("proj-a/sub")
	if err != nil {
		t.Fatalf("ResolveWorkDir failed: %v", err)
	}
	expected := filepath.Join(base, "proj-a", "sub")
	if dir != expected {
		t.Fatalf("expected %q, got %q", expected, dir)
	}
	// Directory must have been created.
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("expected directory to be created, stat err=%v", err)
	}
}

func TestResolveWorkDirAbsoluteInside(t *testing.T) {
	base := filepath.Join(t.TempDir(), "workspace")
	exec := newTestExecutor(t, base)

	inside := filepath.Join(base, "proj-b")
	dir, err := exec.ResolveWorkDir(inside)
	if err != nil {
		t.Fatalf("ResolveWorkDir failed: %v", err)
	}
	if dir != filepath.Clean(inside) {
		t.Fatalf("expected %q, got %q", filepath.Clean(inside), dir)
	}
}

func TestResolveWorkDirEscapeRejected(t *testing.T) {
	base := filepath.Join(t.TempDir(), "workspace")
	exec := newTestExecutor(t, base)

	cases := []string{
		"../outside",
		"../../..",
		"proj/../../escape",
		filepath.Join(base, "..", "sibling"),
		filepath.Dir(base), // parent of base
	}
	// Note: on Windows a rooted-but-volumeless path like `\etc` is not
	// absolute for filepath.IsAbs; filepath.Join treats it as base-relative,
	// so it stays confined inside the workspace rather than escaping to the
	// drive root.
	for _, c := range cases {
		if _, err := exec.ResolveWorkDir(c); err == nil {
			t.Fatalf("expected escape rejection for %q", c)
		}
	}
}

func TestResolveWorkDirBaseItselfAllowed(t *testing.T) {
	base := filepath.Join(t.TempDir(), "workspace")
	exec := newTestExecutor(t, base)

	dir, err := exec.ResolveWorkDir(base)
	if err != nil {
		t.Fatalf("ResolveWorkDir(base) failed: %v", err)
	}
	if dir != filepath.Clean(base) {
		t.Fatalf("expected %q, got %q", filepath.Clean(base), dir)
	}
}

func TestExecuteUsesResolvedWorkDir(t *testing.T) {
	base := filepath.Join(t.TempDir(), "workspace")
	exec := newTestExecutor(t, base)

	dir, err := exec.ResolveWorkDir("proj-a")
	if err != nil {
		t.Fatalf("ResolveWorkDir failed: %v", err)
	}

	// Print the current directory; it must equal the resolved work dir.
	// Note: on Windows the shell is Git Bash, which reports MSYS-style paths
	// like /tmp/... instead of C:\..., so compare base names as the
	// platform-independent signal and rely on the ResolveWorkDir tests for
	// exact path resolution.
	result := exec.Execute(context.Background(), dir, "pwd", 5*time.Second)
	if result.ExitCode != 0 {
		t.Fatalf("exec failed: exit=%d stderr=%q", result.ExitCode, result.Stderr)
	}
	got := strings.TrimSpace(result.Stdout)
	if !strings.HasSuffix(filepath.ToSlash(got), "workspace/proj-a") {
		t.Fatalf("expected pwd to end with workspace/proj-a, got %q", got)
	}

	// A command without an explicit work dir must run in the base directory.
	baseResult := exec.Execute(context.Background(), "", "pwd", 5*time.Second)
	if baseResult.ExitCode != 0 {
		t.Fatalf("base exec failed: exit=%d stderr=%q", baseResult.ExitCode, baseResult.Stderr)
	}
	baseGot := strings.TrimSpace(baseResult.Stdout)
	if strings.HasSuffix(filepath.ToSlash(baseGot), "proj-a") {
		t.Fatalf("expected default work dir, got %q", baseGot)
	}
}
