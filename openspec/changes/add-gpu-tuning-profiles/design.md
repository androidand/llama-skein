# Design: add-gpu-tuning-profiles

## D1 — GPU identification: PCI device-ID → gfx map (sysfs), config override

**Chosen:** read `/sys/class/drm/card*/device/{vendor,device}`; for AMD
(`vendor 0x1002`) map the device ID to a gfx target via a built-in table.
A `tuning.gfxTarget` config key overrides detection.

```go
// internal/tuning/detect.go
var deviceGfx = map[uint32]string{
    0x7550: "gfx1201", 0x7551: "gfx1201", // Navi 48 R9700 / RX 9070
    0x744c: "gfx1100",                    // Navi 31 RX 7900 XTX/XT
    0x7448: "gfx1100", 0x7449: "gfx1100", // Navi 31 W7800/W7900
    0x73bf: "gfx1030", 0x73a5: "gfx1030", // Navi 21 RX 6800/6900
}
func DetectGfx(sysfsRoot string) (gfx string, deviceID uint32, ok bool)
```

**Alternatives considered:**
- *Parse `rocminfo`* — authoritative gfx string, but not installed on the
  proxmox host and absent in minimal containers; a hard dependency we can't
  assume. Kept as an optional fallback only.
- *Match on GPU name string from perf snapshot* — names are inconsistent
  ("AMD GPU [0]", "Radeon AI PRO R9700"); device ID is stable and exact.

**Why:** device ID is available everywhere the amdgpu driver is loaded, needs
no extra tooling, and maps cleanly to gfx. Unknown IDs → no profile (safe).

## D2 — Profiles as a curated data file (embedded, overridable), keyed by (gfx, use-case)

Profiles live in a **human-editable data file in the repo**, not Go constants
— an open, PR-able "tuning database" that grows as we verify hardware and
harvest community findings. Seeded with our GPUs and the single-stream
agentic use case.

`internal/tuning/profiles.yaml` (embedded via `//go:embed`):

```yaml
version: 1
usecases:
  agentic-single:
    description: One request at a time, latency-sensitive (opencode/agents)
    default: true
profiles:
  - gfx: gfx1201            # RDNA4 — R9700, RX 9070/XT
    usecase: agentic-single
    verified: true
    verified_on: "R9700 32GB @ proxmox, 2026-07-06"
    flags: { flash_attn: on, parallel: 1 }
    mtp:   { apply_to_mtp_models: true, draft_n_max: 3, draft_p_min: 0.5 }
    notes: "MTP 2.4x (19→45 t/s). q8_0/q8_0 KV fine. ngram-mod HURT single-GPU."
    device_ids: ["0x7550","0x7551","0x7448","0x7449"]   # 744x = W-series, see D2a
    sources:
      - measured in-repo 2026-07-06
      - https://github.com/ggml-org/llama.cpp/discussions/21043
  - gfx: gfx1100            # RDNA3 — W7800/W7900, RX 7900 XTX/XT, RX 7800 XT
    usecase: agentic-single
    verified: false
    flags: { flash_attn: on, parallel: 1 }
    notes: "Conservative. RDNA3 has WMMA; a rocWMMA-FATTN build helps prefill. MTP untested."
    sources: [https://github.com/ggml-org/llama.cpp/discussions/21043]
  - gfx: gfx1030            # RDNA2 — RX 6800/6900 XT
    usecase: agentic-single
    verified: false
    flags: { flash_attn: on, parallel: 1 }
    notes: "RDNA2, no WMMA. Community reports Vulkan often > ROCm here; use latest llama.cpp master."
    sources: ["r/ROCm build-script thread, 2026-07"]
```

`Profile`/`MTPProfile` structs unmarshal from this. A **user data file**
(`<configdir>/tuning-profiles.yaml`, same schema) is merged over the embedded
one at load — users add GPUs/use-cases or override ours without forking.

**Why a data file, not Go constants:** the whole point is a living database
curated from sources; contributors (and the user) edit YAML + cite a source
in a PR, no Go rebuild-of-logic needed. Keeps data separate from mechanism.

**Why no KV-cache-type flag injected:** KV quant is model- and VRAM-dependent
(already in the model `cmd`) and changing it can break a fit the operator
proved. `notes` records the recommendation (e.g. q8_0/q8_0 on gfx1201); users
apply KV choices via the model `cmd` or `extra_args`. Profiles only auto-inject
safe, additive flags (fa/parallel/spec).

## D2a — device-ID → gfx still lives in code (detection), names/notes in data

The PCI-ID→gfx map (D1) stays in Go (it's mechanism, and must be exhaustive
for detection). The data file's optional `device_ids`/name lists are
documentation + let a profile assert coverage; detection is authoritative.

## D3 — Injection point: merge into the launch command, explicit wins

**Chosen:** a pure function `ApplyProfile(cmd string, p Profile, isMTP bool)
string` that tokenizes the command, checks which profile flags are already
present (by flag name and its aliases: `--flash-attn/-fa`,
`--parallel/-np`), and appends only the missing ones. Called where the
llamacpp launch command is assembled (the same place `SanitizedCommand`
is used for fit).

```
ApplyProfile("... -ngl 99", gfx1201, isMTP=true)
  → "... -ngl 99 --flash-attn on --parallel 1 --spec-type draft-mtp
     --spec-draft-n-max 3 --draft-p-min 0.5"
ApplyProfile("... --parallel 4 -ngl 99", gfx1201, isMTP=false)
  → "... --parallel 4 -ngl 99 --flash-attn on"   # user's --parallel 4 kept
```

**Alternatives considered:**
- *Rewrite the config file on disk* — invasive, fights the operator's
  hand-edits, and muddies the source of truth. Injection at launch keeps the
  config as-written and the profile as an overlay.
- *Structured flags model* — llama-skein already treats `cmd` as the
  authority; a parallel structured representation would double the surface.

**Boundary:** injection is launch-time only. `GET /api/config/models/:id`
still returns the user's `cmd` as written; a new field reports the *effective*
launched flags (see D4) so the difference is visible, not hidden.

## D4 — API surface (contract-first)

New paths under `/api/tuning`:
- `GET /api/tuning` → `{ detected_gfx, device_id, profile, source }` where
  `source` ∈ `detected|override|none`.
- `GET /api/tuning/profiles` → all built-in profiles (for UI pickers).
- `PATCH /api/tuning` → override profile fields for this host (persisted to
  config `tuning:` block); body is a partial Profile. Reload re-applies.

Also extend the existing model detail so the UI can show the delta:
- `ConfigModelDetail.effective_flags?: string` — the launch command after
  profile injection (read-only, computed).

**Why a host-level profile, not per-model:** the GPU is per-host; per-model
override is achieved by setting the flag explicitly in that model's `cmd`
(which already wins). Keeps the model schema unchanged except the read-only
`effective_flags`.

## D6 — Override model: recommended, never forced

The effective tuning for a host is `resolve(builtinProfile, userOverride)`:

```go
type Override struct {
    Enabled   *bool     // nil = use default (on); false = inject nothing
    FlashAttn *bool     // nil = profile value; set = force this value (incl. off)
    Parallel  *int      // nil = profile; set = this value
    MTP       *bool     // nil = profile; false = never inject spec flags
    ExtraArgs []string  // always appended (missing-flag rule still applies)
    GfxTarget string    // override detection
}
```

Persisted in the config `tuning:` block; `PATCH /api/tuning` writes it. Rules:
- `Enabled=false` → `ApplyProfile` is a no-op; the `cmd` launches verbatim.
- A set pointer field forces that value even if it *disables* a profile
  recommendation (e.g. `FlashAttn=false` on gfx1201 turns fa off).
- `ExtraArgs` lets the user inject flags the curated `Profile` struct doesn't
  model (e.g. `--cache-type-v q4_0`, `-ub 2048`) without hand-editing every
  model `cmd` — still subject to the explicit-cmd-wins merge.
- `GET /api/tuning` labels each effective value `recommended` or `override`
  so clients render the difference and offer "reset to recommended".

**Why pointers, not plain fields:** we must distinguish "user chose the
profile default" from "user explicitly set the same value" and from "user
turned it off" — a zero `bool`/`int` can't. `nil` = defer to profile.

## D5 — MTP-capability gate

MTP flags inject only when the model is MTP-capable. Reuse the existing
`metadata.mtp.enabled` signal (added for opencode discovery); if absent, fall
back to a filename heuristic (`-mtp-` / `MTP` in the GGUF name) so the R9700
qwopus model is covered without hand-set metadata. Non-MTP models never get
spec flags regardless of profile.
