//go:build darwin

package server

import "golang.org/x/sys/unix"

// gpuWiredLimitMB returns the macOS GPU wired-memory limit (iogpu.wired_limit_mb)
// in MB, or 0 when it is at the OS default (sysctl reports 0). This is the hard
// ceiling on how much unified memory Metal will let a process wire — the budget
// MLX actually crashes against, which is well below total RAM.
func gpuWiredLimitMB() int {
	v, err := unix.SysctlUint32("iogpu.wired_limit_mb")
	if err != nil {
		return 0
	}
	return int(v)
}
