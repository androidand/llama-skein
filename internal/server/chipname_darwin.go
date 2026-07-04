//go:build darwin

package server

import "golang.org/x/sys/unix"

// appleChipName returns the SoC marketing name (e.g. "Apple M3 Pro") from
// machdep.cpu.brand_string, or a generic label when the sysctl is unavailable.
func appleChipName() string {
	v, err := unix.Sysctl("machdep.cpu.brand_string")
	if err != nil || v == "" {
		return "Apple Silicon"
	}
	return v
}
