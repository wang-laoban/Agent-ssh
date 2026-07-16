//go:build windows

package executor

import (
	"context"
	"os/exec"
)

// newShellCommand creates a command running through cmd.exe.
func newShellCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "cmd.exe", "/C", command)
}

// setProcessGroup is a no-op on Windows because POSIX process groups are not
// supported.
func setProcessGroup(cmd *exec.Cmd) {
}

// killProcessTree terminates the main process. Windows does not support POSIX
// process groups, so the entire subtree is not killed here.
func killProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
