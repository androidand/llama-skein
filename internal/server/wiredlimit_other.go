//go:build !darwin

package server

// gpuWiredLimitMB is macOS-only; elsewhere there is no unified-memory wired
// limit to read.
func gpuWiredLimitMB() int { return 0 }
