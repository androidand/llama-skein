package perf

import "time"

type GpuStat struct {
	Timestamp time.Time `json:"timestamp"`

	ID         int     `json:"id"`
	Name       string  `json:"name"`
	UUID       string  `json:"uuid"`
	TempC      int     `json:"temp_c"`
	VramTempC  int     `json:"vram_temp_c"`
	GpuUtilPct float64 `json:"gpu_util_pct"`
	MemUtilPct float64 `json:"mem_util_pct"`
	// MemActivityPct is the memory-controller busy percentage (amdgpu sysfs
	// mem_busy_percent), distinct from MemUtilPct (VRAM used %). A productive
	// memory-bound decode keeps this high; a wedged GPU kernel spins with
	// GpuUtilPct high but MemActivityPct ~0. 0 when the platform lacks it.
	MemActivityPct float64 `json:"mem_activity_pct"`
	// MemActivityKnown is true only when MemActivityPct was actually measured
	// (amdgpu sysfs). Consumers that would act on a low value (the wedge
	// watchdog) MUST check this, so a platform that never reports the metric
	// is not mistaken for a stalled GPU.
	MemActivityKnown bool    `json:"mem_activity_known,omitempty"`
	MemUsedMB        int     `json:"mem_used_mb"`
	MemTotalMB       int     `json:"mem_total_mb"`
	FanSpeedPct      float64 `json:"fan_speed_pct"`
	PowerDrawW       float64 `json:"power_draw_w"`
}

type NetIOStat struct {
	Name      string `json:"name"`
	BytesRecv uint64 `json:"bytes_recv"`
	BytesSent uint64 `json:"bytes_sent"`
}

type SysStat struct {
	Timestamp time.Time `json:"timestamp"`

	CpuUtilPerCore []float64 `json:"cpu_util_per_core"`
	MemTotalMB     int       `json:"mem_total_mb"`
	MemUsedMB      int       `json:"mem_used_mb"`
	MemFreeMB      int       `json:"mem_free_mb"`
	// MemAvailableMB is memory available for new allocations without paging
	// (includes reclaimable cache/inactive pages). Use this — not MemFreeMB,
	// which on macOS excludes inactive pages and is always near zero — to
	// judge memory pressure.
	MemAvailableMB int `json:"mem_available_mb"`
	// MemPressureLevel is the macOS kernel's holistic memory-pressure verdict
	// from kern.memorystatus_vm_pressure_level: 1=normal, 2=warning,
	// 4=critical. It accounts for compression, swap, and wired memory — unlike
	// a raw available-% figure, which a legitimately-resident large model
	// drives low without the system being in any danger. 0 on platforms that
	// don't expose it (Linux, Windows).
	MemPressureLevel int         `json:"mem_pressure_level"`
	SwapTotalMB      int         `json:"swap_total_mb"`
	SwapUsedMB       int         `json:"swap_used_mb"`
	LoadAvg1         float64     `json:"load_avg_1"`
	LoadAvg5         float64     `json:"load_avg_5"`
	LoadAvg15        float64     `json:"load_avg_15"`
	NetIO            []NetIOStat `json:"net_io"`
}
