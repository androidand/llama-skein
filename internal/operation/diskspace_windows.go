//go:build windows

package operation

import "golang.org/x/sys/windows"

// availableDiskSpace mirrors internal/server/disk_windows.go's
// storageStats, duplicated rather than shared for the same reason as the
// unix variant (diskspace_unix.go) — returning a typed DiskSpace instead of
// that endpoint's reporting-shaped map.
func availableDiskSpace(dir string) (DiskSpace, error) {
	pathPtr, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return DiskSpace{}, err
	}
	var freeBytesAvailable, totalBytes, totalFreeBytes uint64
	if err := windows.GetDiskFreeSpaceEx(pathPtr, &freeBytesAvailable, &totalBytes, &totalFreeBytes); err != nil {
		return DiskSpace{}, err
	}
	_ = totalFreeBytes // total free space on the volume, distinct from freeBytesAvailable's per-user quota view; not needed here.
	return DiskSpace{
		AvailableBytes: freeBytesAvailable,
		TotalBytes:     totalBytes,
	}, nil
}
