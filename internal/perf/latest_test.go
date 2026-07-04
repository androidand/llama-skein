package perf

import (
	"testing"
	"time"
)

func TestLatestGPUs_PicksNewestPerIDSortedByID(t *testing.T) {
	t0 := time.Now()
	stats := []GpuStat{
		{ID: 1, Timestamp: t0, MemUsedMB: 100},
		{ID: 0, Timestamp: t0, MemUsedMB: 900},
		{ID: 0, Timestamp: t0.Add(2 * time.Second), MemUsedMB: 500},
		{ID: 1, Timestamp: t0.Add(time.Second), MemUsedMB: 200},
		{ID: 0, Timestamp: t0.Add(time.Second), MemUsedMB: 700},
	}
	got := LatestGPUs(stats)
	if len(got) != 2 {
		t.Fatalf("expected 2 GPUs, got %d", len(got))
	}
	if got[0].ID != 0 || got[1].ID != 1 {
		t.Errorf("expected IDs [0 1], got [%d %d]", got[0].ID, got[1].ID)
	}
	if got[0].MemUsedMB != 500 {
		t.Errorf("GPU 0: expected newest sample (500MB used), got %d", got[0].MemUsedMB)
	}
	if got[1].MemUsedMB != 200 {
		t.Errorf("GPU 1: expected newest sample (200MB used), got %d", got[1].MemUsedMB)
	}
}

func TestLatestGPUs_Empty(t *testing.T) {
	if got := LatestGPUs(nil); len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}
