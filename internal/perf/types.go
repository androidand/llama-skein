package perf

import "time"

type GpuStat struct {
	Timestamp time.Time `json:"timestamp"`

	ID          int     `json:"id"`
	Name        string  `json:"name"`
	UUID        string  `json:"uuid"`
	TempC       int     `json:"temp_c"`
	VramTempC   int     `json:"vram_temp_c"`
	GpuUtilPct  float64 `json:"gpu_util_pct"`
	MemUtilPct  float64 `json:"mem_util_pct"`
	MemUsedMB   int     `json:"mem_used_mb"`
	MemTotalMB  int     `json:"mem_total_mb"`
	FanSpeedPct float64 `json:"fan_speed_pct"`
	PowerDrawW  float64 `json:"power_draw_w"`
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
	MemAvailableMB int         `json:"mem_available_mb"`
	SwapTotalMB    int         `json:"swap_total_mb"`
	SwapUsedMB     int         `json:"swap_used_mb"`
	LoadAvg1       float64     `json:"load_avg_1"`
	LoadAvg5       float64     `json:"load_avg_5"`
	LoadAvg15      float64     `json:"load_avg_15"`
	NetIO          []NetIOStat `json:"net_io"`
}
