//go:build !windows

package server

import (
	"fmt"
	"net/http"
	"syscall"

	"github.com/androidand/llama-skein/internal/router"
)

func storageStats(w http.ResponseWriter, r *http.Request, dir string) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		router.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("statfs %s: %v", dir, err))
		return
	}
	bs := uint64(st.Bsize)
	writeJSON(w, map[string]any{
		"models_dir":      dir,
		"total_bytes":     st.Blocks * bs,
		"available_bytes": st.Bavail * bs,
		"used_bytes":      (st.Blocks - st.Bfree) * bs,
	})
}

func diskStorageStats(dir string) (map[string]any, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return nil, false
	}
	bs := uint64(st.Bsize)
	return map[string]any{
		"total_bytes":     st.Blocks * bs,
		"available_bytes": st.Bavail * bs,
		"used_bytes":      (st.Blocks - st.Bfree) * bs,
	}, true
}
