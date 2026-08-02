package config

import "testing"

func TestFlashAttnWarnings_KnownRiskyFamilyWarns(t *testing.T) {
	cfg := Config{Models: map[string]ModelConfig{
		"m": {Cmd: "/opt/llama-server --model x.gguf --flash-attn on --ctx-size 8192"},
	}}
	got := FlashAttnWarnings(cfg, "gfx1100")
	if len(got) != 1 {
		t.Fatalf("want 1 warning, got %d", len(got))
	}
	if got[0].Model != "m" || got[0].Source != "flash-attn-gfx" {
		t.Errorf("unexpected warning: %+v", got[0])
	}
}

func TestFlashAttnWarnings_UnknownFamilyIsSilent(t *testing.T) {
	cfg := Config{Models: map[string]ModelConfig{
		"m": {Cmd: "/opt/llama-server --model x.gguf --flash-attn on"},
	}}
	if got := FlashAttnWarnings(cfg, "gfx90a"); len(got) != 0 {
		t.Errorf("unrecognized GPU family should produce no warning, got %+v", got)
	}
	if got := FlashAttnWarnings(cfg, ""); len(got) != 0 {
		t.Errorf("empty gfx should produce no warning, got %+v", got)
	}
}

func TestFlashAttnWarnings_FlashAttnOffOrAutoIsSilent(t *testing.T) {
	cfg := Config{Models: map[string]ModelConfig{
		"off":  {Cmd: "/opt/llama-server --flash-attn off"},
		"auto": {Cmd: "/opt/llama-server --flash-attn auto"},
		"none": {Cmd: "/opt/llama-server --ctx-size 8192"},
	}}
	if got := FlashAttnWarnings(cfg, "gfx1100"); len(got) != 0 {
		t.Errorf("want no warnings when flash-attn is not explicitly on, got %+v", got)
	}
}

func TestFlashAttnWarnings_HostTuningGfxTargetOverridesDetection(t *testing.T) {
	cfg := Config{
		Models: map[string]ModelConfig{"m": {Cmd: "--flash-attn on"}},
		Tuning: &TuningConfig{GfxTarget: "gfx1100"},
	}
	// detected gfx says something benign, but the operator's explicit
	// override says gfx1100 — the override must win, matching
	// internal/tuning's own precedence.
	got := FlashAttnWarnings(cfg, "gfx90a")
	if len(got) != 1 {
		t.Fatalf("want the override to be consulted, got %d warnings", len(got))
	}
}
