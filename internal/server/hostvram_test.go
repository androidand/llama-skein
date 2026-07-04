package server

import (
	"testing"
	"time"

	"github.com/androidand/llama-skein/internal/perf"
)

func sysWith(totalMB, availMB, freeMB int) []perf.SysStat {
	return []perf.SysStat{{MemTotalMB: totalMB, MemAvailableMB: availMB, MemFreeMB: freeMB}}
}

// Discrete multi-GPU hosts sum the newest sample of every card.
func TestHostVRAM_MultiGPUSumsLatestPerID(t *testing.T) {
	t0 := time.Now()
	gpus := []perf.GpuStat{
		{ID: 0, Timestamp: t0, MemTotalMB: 24000, MemUsedMB: 20000},                 // stale
		{ID: 0, Timestamp: t0.Add(time.Second), MemTotalMB: 24000, MemUsedMB: 4000}, // newest GPU 0
		{ID: 1, Timestamp: t0, MemTotalMB: 16000, MemUsedMB: 1000},
	}
	total, free := hostVRAM(sysWith(64000, 32000, 500), gpus, false, 0)
	if total != 40000 {
		t.Errorf("total: expected 24000+16000=40000, got %d", total)
	}
	if free != 35000 {
		t.Errorf("free: expected (24000-4000)+(16000-1000)=35000 from newest samples, got %d", free)
	}
}

// Unified hosts cap total at the wired limit and free at available memory.
func TestHostVRAM_UnifiedWiredLimitCap(t *testing.T) {
	gpus := []perf.GpuStat{{ID: 0, Timestamp: time.Now(), MemTotalMB: 36000, MemUsedMB: 6000}}
	total, free := hostVRAM(sysWith(36000, 12000, 100), gpus, true, 27000)
	if total != 27000 {
		t.Errorf("total: expected wired limit 27000, got %d", total)
	}
	// budget-used = 21000, but only 12000 is reclaimable system-wide.
	if free != 12000 {
		t.Errorf("free: expected min(27000-6000, avail 12000)=12000, got %d", free)
	}
}

// Without an explicit wired limit the 70%-of-RAM default applies.
func TestHostVRAM_UnifiedDefaultBudget(t *testing.T) {
	gpus := []perf.GpuStat{{ID: 0, Timestamp: time.Now(), MemTotalMB: 36000, MemUsedMB: 1000}}
	total, free := hostVRAM(sysWith(36000, 30000, 100), gpus, true, 0)
	if want := 36000 * 70 / 100; total != want {
		t.Errorf("total: expected 70%% default %d, got %d", want, total)
	}
	if want := 36000*70/100 - 1000; free != want {
		t.Errorf("free: expected budget-used=%d, got %d", want, free)
	}
}

// No-GPU hosts budget from MemAvailableMB, never the near-zero macOS MemFreeMB.
func TestHostVRAM_NoGPUUsesAvailable(t *testing.T) {
	total, free := hostVRAM(sysWith(64000, 48000, 200), nil, false, 0)
	if total != 64000 || free != 48000 {
		t.Errorf("expected total=64000 free=48000 (available), got total=%d free=%d", total, free)
	}
}

// Unified host with no GPU monitor backend still gets the wired-limit budget.
func TestHostVRAM_UnifiedNoGPUStats(t *testing.T) {
	total, free := hostVRAM(sysWith(36000, 20000, 100), nil, true, 0)
	if want := 36000 * 70 / 100; total != want {
		t.Errorf("total: expected budget %d, got %d", want, total)
	}
	if free != 20000 {
		t.Errorf("free: expected min(budget, avail)=20000, got %d", free)
	}
}

// Over-committed cards never report negative free VRAM.
func TestHostVRAM_ClampsNegativeFree(t *testing.T) {
	gpus := []perf.GpuStat{{ID: 0, Timestamp: time.Now(), MemTotalMB: 16000, MemUsedMB: 16500}}
	if _, free := hostVRAM(sysWith(64000, 32000, 500), gpus, false, 0); free != 0 {
		t.Errorf("expected free clamped to 0, got %d", free)
	}
}

func TestHostVRAM_NoSnapshots(t *testing.T) {
	if total, free := hostVRAM(nil, nil, false, 0); total != 0 || free != 0 {
		t.Errorf("expected zeros without snapshots, got total=%d free=%d", total, free)
	}
}
