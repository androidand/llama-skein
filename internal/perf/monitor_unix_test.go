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

func writeSysfsFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The sysfs reader is the unbreakable fallback when ROCm userland is missing
// or points at a moved install (the rocky case: wrapper hardcoding a stale
// version dir, exit 127). It must produce VRAM/busy/temp/power from the bare
// amdgpu counters, skip cards without VRAM info, and unit-convert hwmon's
// millidegrees and microwatts.
func TestReadSysfsGpuStats(t *testing.T) {
	root := t.TempDir()

	// card0: full amdgpu counters (values from a real gfx1100 host).
	dev0 := filepath.Join(root, "card0", "device")
	writeSysfsFile(t, filepath.Join(dev0, "mem_info_vram_total"), "25753026560\n")
	writeSysfsFile(t, filepath.Join(dev0, "mem_info_vram_used"), "23439769600\n")
	writeSysfsFile(t, filepath.Join(dev0, "gpu_busy_percent"), "98\n")
	writeSysfsFile(t, filepath.Join(dev0, "hwmon", "hwmon3", "temp1_input"), "61000\n")
	writeSysfsFile(t, filepath.Join(dev0, "hwmon", "hwmon3", "power1_average"), "250000000\n")

	// card1: a display-only device without VRAM counters — must be skipped.
	writeSysfsFile(t, filepath.Join(root, "card1", "device", "gpu_busy_percent"), "3\n")

	stats := readSysfsGpuStats(root)
	if len(stats) != 1 {
		t.Fatalf("len(stats) = %d, want 1", len(stats))
	}
	s := stats[0]
	if s.ID != 0 {
		t.Errorf("ID = %d, want 0", s.ID)
	}
	if s.MemTotalMB != 24560 {
		t.Errorf("MemTotalMB = %d, want 24560", s.MemTotalMB)
	}
	if s.MemUsedMB != 22353 {
		t.Errorf("MemUsedMB = %d, want 22353", s.MemUsedMB)
	}
	if s.GpuUtilPct != 98 {
		t.Errorf("GpuUtilPct = %v, want 98", s.GpuUtilPct)
	}
	if s.TempC != 61 {
		t.Errorf("TempC = %d, want 61 (millidegrees converted)", s.TempC)
	}
	if s.PowerDrawW != 250 {
		t.Errorf("PowerDrawW = %v, want 250 (microwatts converted)", s.PowerDrawW)
	}
	if s.MemUtilPct < 90 || s.MemUtilPct > 92 {
		t.Errorf("MemUtilPct = %v, want ~91", s.MemUtilPct)
	}
	if s.Timestamp.IsZero() {
		t.Error("Timestamp should be set")
	}
}

func TestReadSysfsGpuStats_NoCards(t *testing.T) {
	if stats := readSysfsGpuStats(t.TempDir()); len(stats) != 0 {
		t.Fatalf("expected no stats from empty root, got %d", len(stats))
	}
}
