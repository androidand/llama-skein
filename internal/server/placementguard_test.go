package server

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/logmon"
	"github.com/androidand/llama-skein/internal/offload"
	"github.com/androidand/llama-skein/internal/perf"
	"github.com/androidand/llama-skein/internal/placement"
)

func TestHasPinnedPlacement(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"llama-server", "-m", "x.gguf", "--ctx-size", "8192"}, false},
		{[]string{"llama-server", "-ngl", "99"}, true},
		{[]string{"llama-server", "--n-gpu-layers", "40"}, true},
		{[]string{"llama-server", "--n-gpu-layers=40"}, true},
		{[]string{"llama-server", "--cpu-moe"}, true},
		{[]string{"llama-server", "-ncmoe", "12"}, true},
		{[]string{"llama-server", "--override-tensor", "exps=CPU"}, true},
		{[]string{"llama-server", "--tensor-split", "1,1"}, true},
		// Only exact flag names pin; a value that looks like one does not.
		{[]string{"llama-server", "--alias", "-ngl-model"}, false},
	}
	for _, c := range cases {
		if got := hasPinnedPlacement(c.args); got != c.want {
			t.Errorf("hasPinnedPlacement(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}

func placementTestServer(models map[string]config.ModelConfig) *Server {
	return &Server{
		cfg:        config.Config{Models: models},
		proxylog:   logmon.NewWriter(io.Discard),
		placements: map[string]placementRecord{},
		unfittable: map[string]string{},
	}
}

// A hybrid plan rewrites the in-memory command with its flag ops and keeps
// the original in the record.
func TestApplyPlanned_HybridRewritesCmd(t *testing.T) {
	orig := "llama-server --port 9000 -m /models/big.gguf --ctx-size 32768"
	s := placementTestServer(map[string]config.ModelConfig{
		"big": {Cmd: orig},
	})
	s.applyPlanned("big", placementRecord{
		OriginalCmd: orig,
		Plan: placement.Plan{
			Mode:      placement.ModeHybrid,
			Confident: true,
			NCpuMoe:   24,
			FlagOps: []offload.FlagOp{
				{Name: "--n-cpu-moe", Value: "24"},
				{Name: "--fit-target", Value: "2458"},
			},
		},
	})
	got := s.cfg.Models["big"].Cmd
	if !strings.Contains(got, "--n-cpu-moe 24") || !strings.Contains(got, "--fit-target 2458") {
		t.Fatalf("cmd not rewritten: %q", got)
	}
	if rec := s.placements["big"]; rec.OriginalCmd != orig {
		t.Fatalf("original cmd not preserved: %q", rec.OriginalCmd)
	}
	if _, refused := s.unfittable["big"]; refused {
		t.Fatal("hybrid plan must not mark the model unfittable")
	}
}

// A confident refuse plan feeds the same unfittable map the load gate and
// preload read — the model is refused, not launched.
func TestApplyPlanned_ConfidentRefuseMarksUnfittable(t *testing.T) {
	s := placementTestServer(map[string]config.ModelConfig{
		"huge": {Cmd: "llama-server -m /models/huge.gguf"},
	})
	s.applyPlanned("huge", placementRecord{
		Plan: placement.Plan{Mode: placement.ModeRefuse, Confident: true, Reason: "exceeds every budget"},
	})
	if _, refused := s.unfittable["huge"]; !refused {
		t.Fatal("confident refuse must mark the model unfittable")
	}
	if reason, refuse := s.modelLoadRefusal("huge"); !refuse || reason == "" {
		t.Fatal("load gate must refuse a placement-refused model")
	}
}

// An unconfident refuse fails open: no unfittable entry, no rewrite.
func TestApplyPlanned_UnconfidentRefuseFailsOpen(t *testing.T) {
	orig := "llama-server -m /models/x.gguf"
	s := placementTestServer(map[string]config.ModelConfig{"x": {Cmd: orig}})
	s.applyPlanned("x", placementRecord{
		Plan: placement.Plan{Mode: placement.ModeRefuse, Confident: false, Reason: "guessing"},
	})
	if _, refused := s.unfittable["x"]; refused {
		t.Fatal("unconfident refuse must fail open")
	}
	if s.cfg.Models["x"].Cmd != orig {
		t.Fatal("command must stay untouched")
	}
}

// gpu/custom/unknown plans leave the command byte-for-byte untouched — this
// is what makes "revert to normal" automatic for small models.
func TestApplyPlanned_NoOpModesLeaveCmdUntouched(t *testing.T) {
	orig := "llama-server -m /models/small.gguf --ctx-size 16384"
	for _, mode := range []placement.Mode{placement.ModeGPU, placement.ModeCustom, placement.ModeUnknown} {
		s := placementTestServer(map[string]config.ModelConfig{"m": {Cmd: orig}})
		s.applyPlanned("m", placementRecord{OriginalCmd: orig, Plan: placement.Plan{Mode: mode}})
		if s.cfg.Models["m"].Cmd != orig {
			t.Fatalf("mode %s rewrote the command: %q", mode, s.cfg.Models["m"].Cmd)
		}
		if _, refused := s.unfittable["m"]; refused {
			t.Fatalf("mode %s must not refuse", mode)
		}
	}
}

// applyAutoPlacement in gpu policy mode is a global no-op even for models a
// planner would rewrite.
func TestApplyAutoPlacement_PolicyGPUIsNoOp(t *testing.T) {
	orig := "llama-server -m /nonexistent/big.gguf"
	s := placementTestServer(map[string]config.ModelConfig{"big": {Cmd: orig}})
	s.cfg.Placement = config.PlacementConfig{Mode: config.PlacementModeGPU}
	s.applyAutoPlacement()
	if s.cfg.Models["big"].Cmd != orig || len(s.placements) != 0 {
		t.Fatal("gpu mode must not plan or rewrite anything")
	}
}

// writeSparseGGUF writes a minimal, header-only GGUF describing a dense model
// of the given architecture dimensions, then extends the file to sizeBytes as a
// sparse file — fit sizes mmap'd llama.cpp weights from the file length, and a
// hole costs no disk. This is how a test can hold a "91 GB model" without one.
func writeSparseGGUF(t *testing.T, path string, sizeBytes int64, arch string, kv map[string]uint32) {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("GGUF")
	writeLE(t, &buf, uint32(3))         // version
	writeLE(t, &buf, uint64(0))         // tensor count
	writeLE(t, &buf, uint64(len(kv)+1)) // kv count (+ general.architecture)
	writeGGUFString(t, &buf, "general.architecture")
	writeLE(t, &buf, uint32(8)) // GGUF string type
	writeGGUFString(t, &buf, arch)
	for key, val := range kv {
		writeGGUFString(t, &buf, arch+"."+key)
		writeLE(t, &buf, uint32(4)) // GGUF uint32 type
		writeLE(t, &buf, val)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if int64(buf.Len()) < sizeBytes {
		if err := os.Truncate(path, sizeBytes); err != nil {
			t.Fatal(err)
		}
	}
}

func writeLE(t *testing.T, buf *bytes.Buffer, v any) {
	t.Helper()
	if err := binary.Write(buf, binary.LittleEndian, v); err != nil {
		t.Fatal(err)
	}
}

func writeGGUFString(t *testing.T, buf *bytes.Buffer, s string) {
	t.Helper()
	writeLE(t, buf, uint64(len(s)))
	buf.WriteString(s)
}

// TestApplyAutoPlacement_NoHardwareSampleDoesNotRefuse is the regression for
// the z4 boot race (2026-08-08) at its source: planning must never turn an
// unsampled perf monitor's zeros into a verdict. A monitor that has been
// created but never started reports exactly what boot used to see — no sys
// samples, no GPU samples — and against that, a 91 GB model must come back
// undecided (so the fit guard defers and ensurePlacement re-plans it on first
// load), never refused as unfittable.
func TestApplyAutoPlacement_NoHardwareSampleDoesNotRefuse(t *testing.T) {
	const gib = int64(1) << 30
	gguf := filepath.Join(t.TempDir(), "deepseek-v4-flash-ud-iq2-m.gguf")
	writeSparseGGUF(t, gguf, 91*gib, "llama", map[string]uint32{
		"block_count":             80,
		"embedding_length":        8192,
		"attention.head_count":    64,
		"attention.head_count_kv": 8,
		"context_length":          32768,
	})

	cmd := "llama-server --port 9000 -m " + gguf + " --ctx-size 32768"
	s := placementTestServer(map[string]config.ModelConfig{"big": {Cmd: cmd}})
	// A monitor with no samples yet — the boot-time state, zeros everywhere.
	mon, err := perf.New(config.PerformanceConfig{}, logmon.NewWriter(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	s.perf = mon
	if sys, gpu := mon.Current(); len(sys) != 0 || len(gpu) != 0 {
		t.Fatalf("precondition: monitor must hold no samples, got %d sys / %d gpu", len(sys), len(gpu))
	}

	s.applyAutoPlacement()

	if reason, refused := s.unfittable["big"]; refused {
		t.Fatalf("a 91 GB model was refused from an unsampled monitor's zero budgets: %s", reason)
	}
	rec, planned := s.placements["big"]
	if !planned {
		t.Fatal("expected a placement record for a llamacpp GGUF model")
	}
	// The fixture must really read as a 91 GB model, or "unknown" below would
	// be about unreadable weights rather than about unknown budgets.
	if rec.WeightBytes != 91*gib {
		t.Fatalf("weight size = %d bytes, want %d — the GGUF fixture was not sized as intended", rec.WeightBytes, 91*gib)
	}
	if rec.Plan.Mode != placement.ModeUnknown {
		t.Fatalf("plan mode = %s, want %s: with no hardware sample the planner must say it does not know, not decide from zeros",
			rec.Plan.Mode, placement.ModeUnknown)
	}
	if rec.Plan.Confident {
		t.Fatal("a plan made without any hardware figures must never be confident")
	}
	if s.cfg.Models["big"].Cmd != cmd {
		t.Fatalf("an undecided plan rewrote the command: %q", s.cfg.Models["big"].Cmd)
	}
	// And the fit guard must be able to see that nothing was decided.
	if s.placementDecided("big") {
		t.Fatal("placementDecided must report an undecided model, or the fit guard refuses it")
	}
}

// A model placement never considered — pinned flags, a foreign backend, no
// readable weights — has nothing pending and counts as decided; only a plan
// that came back ModeUnknown is undecided.
func TestPlacementDecided(t *testing.T) {
	s := placementTestServer(map[string]config.ModelConfig{"m": {Cmd: "llama-server -m x.gguf"}})
	if !s.placementDecided("m") {
		t.Fatal("a model with no placement record must count as decided")
	}
	for _, mode := range []placement.Mode{placement.ModeGPU, placement.ModeHybrid, placement.ModeCPU, placement.ModeCustom, placement.ModeRefuse} {
		s.placements["m"] = placementRecord{Plan: placement.Plan{Mode: mode}}
		if !s.placementDecided("m") {
			t.Fatalf("mode %s is a decision", mode)
		}
	}
	s.placements["m"] = placementRecord{Plan: placement.Plan{Mode: placement.ModeUnknown}}
	if s.placementDecided("m") {
		t.Fatal("ModeUnknown means the budgets could not be read — not decided")
	}
}

func TestFitParamsPath(t *testing.T) {
	dir := t.TempDir()
	engine := filepath.Join(dir, "llama-server")
	if fitParamsPath(engine) != "" {
		t.Fatal("missing tool must resolve to empty")
	}
	tool := filepath.Join(dir, "llama-fit-params")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if fitParamsPath(engine) != "" {
		t.Fatal("non-executable tool must resolve to empty (z4: shipped without +x)")
	}
	if err := os.Chmod(tool, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := fitParamsPath(engine); got != tool {
		t.Fatalf("resolved %q, want %q", got, tool)
	}
}
