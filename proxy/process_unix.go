//go:build !windows

package proxy

import (
	"fmt"
	"os/exec"
	"syscall"
)

// setProcAttributes sets platform-specific process attributes.
// On Unix, Setsid creates a new session and process group for the child,
// ensuring that all descendant processes (e.g. MLX worker processes) are
// killed together when the parent is stopped. Without this, SIGTERM only
// kills the parent Python process while MLX workers survive and hold GPU
// memory, blocking other models from loading.
func setProcAttributes(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}
}

// signalProcessGroup sends a signal to the entire process group rooted at
// the child process. This ensures all descendant processes (e.g. MLX worker
// processes spawned via Python multiprocessing) are terminated together.
func signalProcessGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("process is nil")
	}
	// Negative PID sends the signal to every process in the process group.
	return syscall.Kill(-cmd.Process.Pid, sig)
}

// killProcessGroup forcefully kills the entire process group with SIGKILL.
// Used as a last resort when SIGTERM does not terminate the tree.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
