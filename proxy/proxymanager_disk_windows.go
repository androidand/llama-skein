//go:build windows

package proxy

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func diskStorageStats(dir string) (map[string]any, bool) {
	pathPtr, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return nil, false
	}
	var freeBytesAvailable, totalBytes, totalFreeBytes uint64
	if err := windows.GetDiskFreeSpaceEx(pathPtr, &freeBytesAvailable, &totalBytes, &totalFreeBytes); err != nil {
		return nil, false
	}
	return map[string]any{
		"total_bytes":     totalBytes,
		"available_bytes": freeBytesAvailable,
		"used_bytes":      totalBytes - freeBytesAvailable,
	}, true
}

// checkDiskSpaceForModel verifies that the filesystem containing the model file
// has enough free space to memory-map it. Returns nil if the check passes or
// cannot be performed (e.g. model path not found in cmd).
func checkDiskSpaceForModel(cmd string) error {
	modelPath := parseModelPath(cmd)
	if modelPath == "" {
		return nil
	}
	info, err := os.Stat(modelPath)
	if err != nil {
		return nil // file doesn't exist yet or inaccessible — let the loader handle it
	}
	dir := filepath.Dir(modelPath)
	stats, ok := diskStorageStats(dir)
	if !ok {
		return nil // can't stat filesystem — let the loader handle it
	}
	available := stats["available_bytes"].(uint64)
	needed := uint64(info.Size())
	if available < needed {
		return fmt.Errorf("insufficient disk space to load model: need %s, have %s available on %s",
			formatBytes(needed), formatBytes(available), dir)
	}
	return nil
}

func formatBytes(b uint64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
