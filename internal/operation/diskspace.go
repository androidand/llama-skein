package operation

import (
	"errors"
	"fmt"
)

// DiskSpace is available/total bytes on the filesystem containing a
// directory.
type DiskSpace struct {
	AvailableBytes uint64
	TotalBytes     uint64
}

// ErrInsufficientDisk is returned by CheckDiskPreflight when the target
// filesystem does not have enough free space for the remaining artifact
// bytes plus the safety reserve.
var ErrInsufficientDisk = errors.New("operation: insufficient disk space")

// CheckDiskPreflight verifies dir's filesystem has enough free space for
// remainingBytes plus safetyReserveBytes (design.md decision 4: "checks
// available disk for remaining bytes plus safety reserve"). remainingBytes
// is the bytes still needed, not necessarily an artifact set's full size —
// for a brand-new install that's the same thing, but a future resume (task
// 4.x) can pass what's left after a partial download, which is the whole
// reason "remaining" is the documented term rather than "total".
// safetyReserveBytes is the caller's choice, not a hardcoded default: how
// much headroom to leave for the OS, other models, and anything else
// sharing the disk depends on the deployment, not on this package.
func CheckDiskPreflight(dir string, remainingBytes, safetyReserveBytes int64) error {
	space, err := availableDiskSpace(dir)
	if err != nil {
		return fmt.Errorf("operation: disk preflight: %w", err)
	}
	return evaluateDiskPreflight(space, remainingBytes, safetyReserveBytes, dir)
}

// evaluateDiskPreflight is CheckDiskPreflight's pure decision logic, split
// out so it's testable against fabricated DiskSpace values instead of
// whatever happens to be free on the machine running the test suite.
func evaluateDiskPreflight(space DiskSpace, remainingBytes, safetyReserveBytes int64, dir string) error {
	if remainingBytes < 0 {
		remainingBytes = 0
	}
	if safetyReserveBytes < 0 {
		safetyReserveBytes = 0
	}
	needed := uint64(remainingBytes) + uint64(safetyReserveBytes)
	if space.AvailableBytes < needed {
		return fmt.Errorf("%w: %d bytes available at %s, %d needed (%d remaining + %d safety reserve)",
			ErrInsufficientDisk, space.AvailableBytes, dir, needed, remainingBytes, safetyReserveBytes)
	}
	return nil
}
