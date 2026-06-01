//go:build windows

package server

import (
	"fmt"
	"net/http"

	"github.com/androidand/llama-skein/internal/router"
	"golang.org/x/sys/windows"
)

func storageStats(w http.ResponseWriter, r *http.Request, dir string) {
	pathPtr, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		router.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("encode path: %v", err))
		return
	}
	var freeBytesAvailable, totalBytes, totalFreeBytes uint64
	if err := windows.GetDiskFreeSpaceEx(pathPtr, &freeBytesAvailable, &totalBytes, &totalFreeBytes); err != nil {
		router.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("GetDiskFreeSpaceEx: %v", err))
		return
	}
	writeJSON(w, map[string]any{
		"models_dir":      dir,
		"total_bytes":     totalBytes,
		"available_bytes": freeBytesAvailable,
		"used_bytes":      totalBytes - freeBytesAvailable,
	})
}

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
