//go:build !windows

package process

import (
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// reclaimStalePort SIGKILLs any process listening on the given host:port when
// the host is loopback. Used before starting a model's upstream to clear a
// stale orphan that survived a SIGKILL of llama-skein (which prevents reaping
// children) and still holds the model's assigned port. Returns how many
// processes were killed. No-op for non-loopback hosts so peer/remote proxy
// URLs are never touched.
func reclaimStalePort(hostPort string) int {
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil || port == "" {
		return 0
	}
	switch host {
	case "", "localhost", "127.0.0.1", "::1":
	default:
		return 0
	}
	// lsof exits non-zero when nothing matches — that's the common case.
	out, err := exec.Command("lsof", "-ti", "tcp:"+port, "-sTCP:LISTEN").Output()
	if err != nil {
		return 0
	}
	self := os.Getpid()
	killed := 0
	for _, f := range strings.Fields(string(out)) {
		pid, convErr := strconv.Atoi(f)
		if convErr != nil || pid <= 1 || pid == self {
			continue // never SIGKILL ourselves (llama-skein, or the test binary
			// hosting an in-process mock server on this port)
		}
		if syscall.Kill(pid, syscall.SIGKILL) == nil {
			killed++
		}
	}
	return killed
}

// setProcAttributes starts the upstream in its own process group (Setpgid) so
// the entire process tree can be signalled at once via its negative PID. This
// is what lets us reap a forked grandchild — e.g. a shell wrapper that
// backgrounds the real binary and exits — instead of leaking it as an orphan
// that holds the inherited stdout/stderr pipes open.
func setProcAttributes(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateProcessTree sends SIGTERM to the whole process group led by the
// command, giving every process in the tree a chance to shut down gracefully.
func terminateProcessTree(cmd *exec.Cmd) error {
	return signalProcessTree(cmd, syscall.SIGTERM)
}

// killProcessTree sends SIGKILL to the whole process group, force-terminating
// every process in the tree.
func killProcessTree(cmd *exec.Cmd) error {
	return signalProcessTree(cmd, syscall.SIGKILL)
}

// signalProcessTree signals the process group led by cmd.Process. Because the
// child was started with Setpgid it is its own group leader (pgid == pid), so
// targeting -pid reaches the child and every descendant still in the group.
// Falls back to signalling just the child if the group send fails (e.g. the
// group has already drained), so we never silently skip the signal.
func signalProcessTree(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil {
		return cmd.Process.Signal(sig)
	}
	return nil
}
