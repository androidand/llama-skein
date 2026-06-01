package server

import (
	"net/http"
	"runtime"
	"sort"

	"github.com/androidand/llama-skein/internal/perf"
)

// handleAPIResources implements GET /api/resources.
// Returns a point-in-time snapshot: disk storage, system memory, CPU, and GPU stats.
func (s *Server) handleAPIResources(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{}

	if dir := s.modelsDir(); dir != "" {
		if stats, ok := diskStorageStats(dir); ok {
			stats["models_dir"] = dir
			resp["storage"] = stats
		}
	}

	if s.perf != nil {
		sysStats, gpuStats := s.perf.Current()
		if len(sysStats) > 0 {
			sys := sysStats[len(sysStats)-1]
			memType := "system"
			if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
				memType = "unified"
			}
			resp["memory"] = map[string]any{
				"total_mb":   sys.MemTotalMB,
				"used_mb":    sys.MemUsedMB,
				"free_mb":    sys.MemFreeMB,
				"swap_total": sys.SwapTotalMB,
				"swap_used":  sys.SwapUsedMB,
				"type":       memType,
				"load_avg1":  sys.LoadAvg1,
				"load_avg5":  sys.LoadAvg5,
				"load_avg15": sys.LoadAvg15,
			}

			var cpuAvg float64
			for _, u := range sys.CpuUtilPerCore {
				cpuAvg += u
			}
			if n := len(sys.CpuUtilPerCore); n > 0 {
				cpuAvg /= float64(n)
			}
			resp["cpu"] = map[string]any{
				"cores":         len(sys.CpuUtilPerCore),
				"util_avg_pct":  cpuAvg,
				"util_per_core": sys.CpuUtilPerCore,
				"load_avg1":     sys.LoadAvg1,
				"load_avg5":     sys.LoadAvg5,
				"load_avg15":    sys.LoadAvg15,
			}
		}

		// Collect latest GPU reading per ID from the time-series.
		latest := make(map[int]perf.GpuStat)
		for _, g := range gpuStats {
			if prev, ok := latest[g.ID]; !ok || g.Timestamp.After(prev.Timestamp) {
				latest[g.ID] = g
			}
		}
		// On Apple Silicon with no GPU stats, synthesise from unified memory.
		if len(latest) == 0 && runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
			if len(sysStats) > 0 {
				sys := sysStats[len(sysStats)-1]
				latest[0] = perf.GpuStat{
					ID:         0,
					Name:       "Apple Silicon (unified)",
					MemTotalMB: sys.MemTotalMB,
					MemUsedMB:  sys.MemUsedMB,
				}
			}
		}

		ids := make([]int, 0, len(latest))
		for id := range latest {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		gpuList := make([]map[string]any, 0, len(ids))
		var vramTotalMB, vramUsedMB int
		for _, id := range ids {
			g := latest[id]
			gpuList = append(gpuList, map[string]any{
				"id":              g.ID,
				"name":            g.Name,
				"vram_total_mb":   g.MemTotalMB,
				"vram_used_mb":    g.MemUsedMB,
				"vram_free_mb":    g.MemTotalMB - g.MemUsedMB,
				"utilization_pct": g.GpuUtilPct,
				"temp_c":          g.TempC,
				"power_draw_w":    g.PowerDrawW,
			})
			vramTotalMB += g.MemTotalMB
			vramUsedMB += g.MemUsedMB
		}
		resp["gpus"] = gpuList
		resp["vram"] = map[string]any{
			"total_mb": vramTotalMB,
			"used_mb":  vramUsedMB,
			"free_mb":  vramTotalMB - vramUsedMB,
		}
	}

	writeJSON(w, resp)
}
