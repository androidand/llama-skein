package operation

import (
	"errors"
	"testing"
)

func TestEvaluateDiskPreflight(t *testing.T) {
	tests := []struct {
		name               string
		space              DiskSpace
		remainingBytes     int64
		safetyReserveBytes int64
		wantErr            bool
	}{
		{
			name:               "plenty of space",
			space:              DiskSpace{AvailableBytes: 100_000_000, TotalBytes: 500_000_000},
			remainingBytes:     10_000_000,
			safetyReserveBytes: 5_000_000,
			wantErr:            false,
		},
		{
			name:               "insufficient space",
			space:              DiskSpace{AvailableBytes: 10_000_000, TotalBytes: 500_000_000},
			remainingBytes:     10_000_000,
			safetyReserveBytes: 5_000_000,
			wantErr:            true,
		},
		{
			name:               "exact boundary is sufficient",
			space:              DiskSpace{AvailableBytes: 15_000_000, TotalBytes: 500_000_000},
			remainingBytes:     10_000_000,
			safetyReserveBytes: 5_000_000,
			wantErr:            false,
		},
		{
			name:               "one byte short of boundary fails",
			space:              DiskSpace{AvailableBytes: 14_999_999, TotalBytes: 500_000_000},
			remainingBytes:     10_000_000,
			safetyReserveBytes: 5_000_000,
			wantErr:            true,
		},
		{
			name:               "negative remaining bytes clamped to zero",
			space:              DiskSpace{AvailableBytes: 5_000_000, TotalBytes: 500_000_000},
			remainingBytes:     -10_000_000,
			safetyReserveBytes: 5_000_000,
			wantErr:            false,
		},
		{
			name:               "negative safety reserve clamped to zero",
			space:              DiskSpace{AvailableBytes: 5_000_000, TotalBytes: 500_000_000},
			remainingBytes:     5_000_000,
			safetyReserveBytes: -1_000_000,
			wantErr:            false,
		},
		{
			name:               "zero available and zero needed is sufficient",
			space:              DiskSpace{AvailableBytes: 0, TotalBytes: 0},
			remainingBytes:     0,
			safetyReserveBytes: 0,
			wantErr:            false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := evaluateDiskPreflight(tt.space, tt.remainingBytes, tt.safetyReserveBytes, "/models")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("evaluateDiskPreflight() = nil, want ErrInsufficientDisk")
				}
				if !errors.Is(err, ErrInsufficientDisk) {
					t.Fatalf("evaluateDiskPreflight() = %v, want wrapped ErrInsufficientDisk", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("evaluateDiskPreflight() = %v, want nil", err)
			}
		})
	}
}

// TestCheckDiskPreflight_RealFilesystem is a light smoke test against the
// actual filesystem hosting the test's temp dir — it can only assert shape
// (TotalBytes > 0, AvailableBytes <= TotalBytes), not exact values, since
// those depend on whatever machine runs the suite.
func TestCheckDiskPreflight_RealFilesystem(t *testing.T) {
	dir := t.TempDir()

	space, err := availableDiskSpace(dir)
	if err != nil {
		t.Fatalf("availableDiskSpace() error = %v", err)
	}
	if space.TotalBytes == 0 {
		t.Fatalf("availableDiskSpace() TotalBytes = 0, want > 0")
	}
	if space.AvailableBytes > space.TotalBytes {
		t.Fatalf("availableDiskSpace() AvailableBytes = %d > TotalBytes = %d", space.AvailableBytes, space.TotalBytes)
	}

	// Asking for far more than any real disk has must fail.
	const impossiblyLarge = 1 << 62
	if err := CheckDiskPreflight(dir, impossiblyLarge, 0); !errors.Is(err, ErrInsufficientDisk) {
		t.Fatalf("CheckDiskPreflight(impossiblyLarge) error = %v, want ErrInsufficientDisk", err)
	}

	// Asking for nothing must succeed.
	if err := CheckDiskPreflight(dir, 0, 0); err != nil {
		t.Fatalf("CheckDiskPreflight(0, 0) error = %v, want nil", err)
	}
}

func TestCheckDiskPreflight_NonexistentDir(t *testing.T) {
	err := CheckDiskPreflight("/nonexistent/path/that/should/not/exist/anywhere", 0, 0)
	if err == nil {
		t.Fatalf("CheckDiskPreflight() on nonexistent dir = nil, want error")
	}
}
