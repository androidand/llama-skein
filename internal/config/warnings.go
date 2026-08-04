package config

import (
	"fmt"
	"regexp"
)

// Warning annotates a risky-but-loadable piece of configuration. Warnings are
// informational only — nothing in this codebase may use one to reject a
// config or refuse to load a model. The principle (established for the ctx
// ceiling, then flash-attn, then this): the system informs, the operator
// decides.
type Warning struct {
	// Model is the model ID the warning concerns, or "" for a config-wide
	// warning not tied to one model.
	Model   string `json:"model,omitempty"`
	Flag    string `json:"flag,omitempty"`
	Message string `json:"message"`
	// Source identifies what produced the warning, e.g. "flash-attn-gfx" —
	// stable across releases so a client can key on it rather than parsing
	// Message text.
	Source string `json:"source"`
}

// flashAttnOnRe matches "--flash-attn on" (or "-fa on") allowing for the
// arbitrary internal whitespace macro substitution can introduce.
var flashAttnOnRe = regexp.MustCompile(`(?:--flash-attn|-fa)\s+on\b`)

// knownFlashAttnRisk maps a detected GPU family (as tuning.DetectGfx / a
// model's gfx_target override reports it) to a short, non-alarming
// description of why --flash-attn on is risky there. Absence from this map
// is not a claim that a family is safe — it means nobody has characterized
// it yet; only families with an actual observed failure mode are listed.
var knownFlashAttnRisk = map[string]string{
	// RDNA3 (gfx110x): observed wedging the GPU (host-wide hang requiring a
	// restart) in most llama.cpp builds as of 2026. See
	// openspec/changes/fix-rdna3-flash-attn-wedge and
	// openspec/changes/harden-live-wedge-recovery.
	"gfx1100": "flash-attn is known to wedge most llama.cpp builds on this GPU (RDNA3) — usable, but expect hangs; a fixed build may not exhibit this",
	"gfx1101": "flash-attn is known to wedge most llama.cpp builds on this GPU (RDNA3) — usable, but expect hangs; a fixed build may not exhibit this",
	"gfx1102": "flash-attn is known to wedge most llama.cpp builds on this GPU (RDNA3) — usable, but expect hangs; a fixed build may not exhibit this",
}

// FlashAttnWarnings scans every model's cmd for an explicit "--flash-attn on"
// and warns when the effective GPU family is known-risky. gfx is the host's
// auto-detected target (tuning.DetectGfx); cfg.Tuning.GfxTarget, when set,
// overrides it — same precedence internal/tuning already uses for injection.
// Pass gfx = "" when detection is unavailable; no warnings are produced
// without a family to check against.
func FlashAttnWarnings(cfg Config, gfx string) []Warning {
	target := gfx
	if cfg.Tuning != nil && cfg.Tuning.GfxTarget != "" {
		target = cfg.Tuning.GfxTarget
	}
	risk, known := knownFlashAttnRisk[target]
	if !known {
		return nil
	}
	var warnings []Warning
	for id, mc := range cfg.Models {
		if !flashAttnOnRe.MatchString(mc.Cmd) {
			continue
		}
		warnings = append(warnings, Warning{
			Model:   id,
			Flag:    "--flash-attn on",
			Message: fmt.Sprintf("%s (detected GPU: %s)", risk, target),
			Source:  "flash-attn-gfx",
		})
	}
	return warnings
}
