//go:build unix && !darwin

package perf

import (
	"os"
	"path/filepath"
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

// resolveTool must find a binary via absolute fallback even when PATH is empty
// — the z4 case where the OCI-launched daemon had no PATH and GPU telemetry
// silently vanished.
func TestResolveTool_AbsoluteFallbackWithEmptyPath(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "faketool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "") // simulate the empty-PATH daemon environment

	if _, ok := resolveTool("faketool"); ok {
		t.Fatal("expected LookPath to fail with empty PATH and no fallback")
	}
	got, ok := resolveTool("faketool", filepath.Join(dir, "*tool"))
	if !ok || got != tool {
		t.Fatalf("resolveTool fallback = (%q,%v), want (%q,true)", got, ok, tool)
	}
	// A non-executable match must be rejected.
	noexec := filepath.Join(dir, "plain")
	os.WriteFile(noexec, []byte("x"), 0o644)
	if _, ok := resolveTool("plain", noexec); ok {
		t.Error("non-executable file must not resolve")
	}
}
