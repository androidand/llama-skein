//go:build !windows

package operation

import "syscall"

// availableDiskSpace mirrors internal/server/disk_unix.go's storageStats,
// duplicated rather than shared (same reasoning as atomicWriteFile in
// store.go — a cross-package dependency isn't worth it for a few lines) but
// returning a typed DiskSpace instead of that endpoint's reporting-shaped
// map, since this is a preflight decision, not an HTTP response body.
func availableDiskSpace(dir string) (DiskSpace, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return DiskSpace{}, err
	}
	bs := uint64(st.Bsize)
	return DiskSpace{
		AvailableBytes: st.Bavail * bs,
		TotalBytes:     st.Blocks * bs,
	}, nil
}
