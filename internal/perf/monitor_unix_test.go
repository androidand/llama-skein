//go:build unix && !darwin

package perf

import (
	"testing"
	"time"
)

func TestParseROCmSmiJSON_SetsTimestampAndValues(t *testing.T) {
	in := []byte(`{"card0":{"VRAM Total Memory (B)":"34208743424","VRAM Total Used Memory (B)":"31675207680","GPU use (%)":"100","Temperature (Sensor edge) (C)":"62.0","Average Graphics Package Power (W)":"314.0"}}`)

	stats := parseROCmSmiJSON(in)
	if len(stats) != 1 {
		t.Fatalf("len(stats)=%d want 1", len(stats))
	}
	g := stats[0]
	if g.Timestamp.IsZero() {
		t.Fatal("Timestamp should be set for ROCm samples")
	}
	if time.Since(g.Timestamp) > 10*time.Second {
		t.Fatalf("Timestamp too old: %v", g.Timestamp)
	}
	if g.MemTotalMB <= 0 || g.MemUsedMB <= 0 {
		t.Fatalf("unexpected memory stats total=%d used=%d", g.MemTotalMB, g.MemUsedMB)
	}
}
