//go:build windows

package proxy

import (
	"fmt"
	"os/exec"
	"syscall"
)

// setProcAttributes sets platform-specific process attributes
func setProcAttributes(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}

// signalProcessGroup sends a signal to the process on Windows.
// Windows does not have Unix-style process groups, so we signal the process directly.
func signalProcessGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("process is nil")
	}
	return cmd.Process.Signal(sig)
}

// killProcessGroup forcefully kills the process on Windows.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	cmd.Process.Kill()
}
