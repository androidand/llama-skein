package server

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/logmon"
	"github.com/androidand/llama-skein/internal/offload"
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
