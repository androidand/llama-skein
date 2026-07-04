package server

import (
	"encoding/json"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/androidand/llama-skein/internal/perf"
	"github.com/androidand/llama-skein/internal/router"
	"github.com/androidand/llama-skein/internal/thermal"
)

// modelFilePath extracts the --model <path> argument from a llama-server command string.
func modelFilePath(cmd string) string {
	fields := strings.Fields(cmd)
	for i, f := range fields {
		if (f == "--model" || f == "-m") && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

// loadedModelInfo returns the first running model's file size (MB) and ID,
// or zeros if nothing is loaded or the file cannot be stat'd.
func (s *Server) loadedModelInfo() (id string, modelMB int64) {
	running := s.local.RunningModels()
	for mid := range running {
		mc, ok := s.cfg.Models[mid]
		if !ok {
			continue
		}
		path := modelFilePath(mc.Cmd)
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		return mid, info.Size() / (1024 * 1024)
	}
	return "", 0
}

// handleAPIHardware implements GET /api/hardware.
// Returns a point-in-time snapshot: storage, memory, CPU, and GPU stats.
func (s *Server) handleAPIHardware(w http.ResponseWriter, r *http.Request) {
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
				"total_mb": sys.MemTotalMB,
				"used_mb":  sys.MemUsedMB,
				"free_mb":  sys.MemFreeMB,
				// available_mb is the reclaimable pool (the memory guard's
				// signal). On macOS this is free+inactive+purgeable, far larger
				// and more meaningful than free_mb — use it for fit decisions.
				"available_mb": sys.MemAvailableMB,
				"swap_total":   sys.SwapTotalMB,
				"swap_used":    sys.SwapUsedMB,
				"type":         memType,
				"load_avg1":    sys.LoadAvg1,
				"load_avg5":    sys.LoadAvg5,
				"load_avg15":   sys.LoadAvg15,
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

		gpus := perf.LatestGPUs(gpuStats)
		if len(gpus) == 0 && runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
			if len(sysStats) > 0 {
				sys := sysStats[len(sysStats)-1]
				gpus = []perf.GpuStat{{
					ID:         0,
					Name:       appleChipName() + " (unified)",
					MemTotalMB: sys.MemTotalMB,
					MemUsedMB:  sys.MemUsedMB,
				}}
			}
		}

		gpuList := make([]map[string]any, 0, len(gpus))
		var vramTotalMB, vramUsedMB int
		for _, g := range gpus {
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

	// Add loaded model info: model file size as a proxy for VRAM used by weights.
	// kv_estimate_mb = max(0, vram_used_mb - model_mb) approximates KV cache + overhead.
	if modelID, modelMB := s.loadedModelInfo(); modelID != "" {
		vramUsed := int64(0)
		if v, ok := resp["vram"].(map[string]any); ok {
			if u, ok := v["used_mb"].(int); ok {
				vramUsed = int64(u)
			}
		}
		kvEst := vramUsed - modelMB
		if kvEst < 0 {
			kvEst = 0
		}
		resp["loaded_model"] = map[string]any{
			"id":             modelID,
			"model_mb":       modelMB,
			"kv_estimate_mb": kvEst,
		}
	}

	writeJSON(w, resp)
}

// handleAPIHardwareStorage implements GET /api/hardware/storage.
// Returns disk usage for the models directory.
func (s *Server) handleAPIHardwareStorage(w http.ResponseWriter, r *http.Request) {
	dir := s.modelsDir()
	if dir == "" {
		router.SendResponse(w, r, http.StatusUnprocessableEntity,
			"models directory unknown; set modelsDir in config or use --models-dir flag")
		return
	}
	storageStats(w, r, dir)
}

// handleAPIHardwarePerformance implements GET /api/hardware/performance.
// Returns buffered GPU/system time-series, optionally filtered by ?after=<RFC3339>.
func (s *Server) handleAPIHardwarePerformance(w http.ResponseWriter, r *http.Request) {
	if s.perf == nil {
		router.SendResponse(w, r, http.StatusServiceUnavailable, "performance monitor not available")
		return
	}

	sysStats, gpuStats := s.perf.Current()

	if afterStr := r.URL.Query().Get("after"); afterStr != "" {
		after, err := time.Parse(time.RFC3339, afterStr)
		if err != nil {
			router.SendResponse(w, r, http.StatusBadRequest, "invalid 'after' timestamp, use RFC3339 format")
			return
		}
		filtered := sysStats[:0]
		for _, st := range sysStats {
			if st.Timestamp.After(after) {
				filtered = append(filtered, st)
			}
		}
		sysStats = filtered

		filteredGpu := gpuStats[:0]
		for _, g := range gpuStats {
			if g.Timestamp.After(after) {
				filteredGpu = append(filteredGpu, g)
			}
		}
		gpuStats = filteredGpu
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"sys_stats": sysStats,
		"gpu_stats": gpuStats,
	})
}

// handleAPIHardwarePower implements GET /api/hardware/power.
// Always returns 200; callers check the "available" field to discover HW support.
func (s *Server) handleAPIHardwarePower(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.silentMode.GetState())
}

// handleAPIHardwarePowerSet implements PUT /api/hardware/power.
// Body is optional: { "power_limit_pct": 65, "temp_target_celsius": 82 }
// Returns 503 when GPU power control is unavailable, 500 on unexpected HW error.
func (s *Server) handleAPIHardwarePowerSet(w http.ResponseWriter, r *http.Request) {
	state := s.silentMode.GetState()
	if !state.Available {
		hardwareError(w, http.StatusServiceUnavailable, state.UnavailableReason)
		return
	}
	profile := thermal.DefaultSilentProfile
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&profile)
	}
	if err := s.silentMode.Apply(profile); err != nil {
		hardwareError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, s.silentMode.GetState())
}

// handleAPIHardwarePowerRestore implements DELETE /api/hardware/power.
// Returns 503 when GPU power control is unavailable, 500 on unexpected HW error.
func (s *Server) handleAPIHardwarePowerRestore(w http.ResponseWriter, r *http.Request) {
	state := s.silentMode.GetState()
	if !state.Available {
		hardwareError(w, http.StatusServiceUnavailable, state.UnavailableReason)
		return
	}
	if err := s.silentMode.Restore(); err != nil {
		hardwareError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, s.silentMode.GetState())
}

func hardwareError(w http.ResponseWriter, status int, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": reason})
}
