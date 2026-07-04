package perf

import "sort"

// LatestGPUs reduces a GPU stat time-series to the most recent sample per GPU
// ID, sorted by ID. The monitor's Current() returns a buffered series that can
// hold many samples of many GPUs; fit and hardware reporting must aggregate
// per device, never index the raw buffer.
func LatestGPUs(stats []GpuStat) []GpuStat {
	latest := make(map[int]GpuStat, 4)
	for _, g := range stats {
		if prev, ok := latest[g.ID]; !ok || g.Timestamp.After(prev.Timestamp) {
			latest[g.ID] = g
		}
	}
	out := make([]GpuStat, 0, len(latest))
	for _, g := range latest {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
