//go:build !darwin

package server

// appleChipName is only meaningful on macOS; the call site is guarded by a
// runtime GOOS check.
func appleChipName() string { return "Apple Silicon" }
