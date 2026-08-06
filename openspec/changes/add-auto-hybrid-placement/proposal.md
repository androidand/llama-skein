# Proposal: Automatic hybrid GPU + system-RAM model placement

## Context

Models larger than a host's VRAM but smaller than VRAM + safe host RAM can run
via llama.cpp's CPU-MoE expert offload — the concrete target is
`unsloth/DeepSeek-V4-Flash-0731-GGUF` (deepseek4 MoE, 284B total params,
82–162 GB per quant) on z4 (W7800 48 GB VRAM, 128 GB host RAM, ROCm/gfx1100).

The parts exist; nothing joins them:

- `pkg/gguf.RecommendCpuMoe` computes an exact per-layer expert-offload plan
  from the GGUF tensor table — but its only caller is the HTTP recommendation
  endpoint (`GET /api/models/offload/{model}`). Auto-application was
  deliberately deferred to clients (`add-model-offload-tuning` tasks 9–10,
  both `[D]`), and no client ever shipped it.
- `internal/fit` is offload-blind: `ModelShape` has no expert-bytes or
  GPU-resident split, `Params` no offload spec, and `fitForModel` parses no
  offload flag. A model bigger than VRAM scores `no` regardless of a viable
  hybrid plan; a *running* offloaded model is only rescued by the
  `fit.go:430` "not fully modeled" escape hatch.
- The fit load guard (`internal/server/fitguard.go`) has exactly two
  remedies: shrink `--ctx-size` or refuse with 507. Today it would refuse
  DeepSeek-V4-Flash outright — the exact case hybrid placement rescues.
- Meanwhile the deployed llama.cpp builds already contain **upstream
  automatic fitting**: `--fit` (default **on**), `--fit-target` (per-device
  free-memory margin, default 1024 MiB), `--fit-ctx`, `-ngl` defaulting to
  `auto`, `--cpu-moe`/`--n-cpu-moe`/`--override-tensor` (+ draft variants),
  and a `llama-fit-params` utility that prints the computed arguments without
  loading the model. Verified live on z4's gfx110X build. Upstream fitting is
  wholly disabled per-argument once the user pins `-ngl`/`--tensor-split`/
  `--override-tensor` — and our own `buildCmd` default (`--n-gpu-layers 99`)
  does exactly that.

## Why

The operator experience must be: pull or select a model; llama-skein decides
full-GPU / hybrid / CPU-only / refuse; generates safe llama.cpp arguments;
reports the placement; and a smaller model loaded later runs with its normal
full-GPU settings untouched. Today an oversized model is refused (507) or
requires hand-authored flags in `cmd`.

## What

### 1. Hybrid-aware fit engine (`internal/fit`)

- `ModelShape` gains expert weight bytes (total + per-layer) and MoE
  dimensions; `Params` gains the effective `offload.Spec` and an `NGpuLayers`
  input; `Result` gains `GPUResidentMB`, `HostResidentMB`, and an effective
  placement classification.
- `fitForModel` parses `-ncmoe/--n-cpu-moe`, `-cmoe/--cpu-moe`, `-ot`, `-ngl`
  from the model cmd (via the existing `offload.Translator.Parse`).
- `RecommendCpuMoe` stops using the FP16-only `gguf.KVCacheBytes` and budgets
  KV via the fit engine's cache-type-aware math (single source of truth).
- The hypothetical/descriptor path reports, per quant variant, whether it
  fits full-GPU, hybrid, or not at all — so the gallery/pull flow can rank
  variants honestly (a 90 GB quant on a 48 GB card is *hybrid*, not *no*).

### 2. Placement planner (new `internal/placement`)

Pure planning: inputs are the model shape, VRAM budget (reusing the
exclusive-group whole-GPU semantics from `apifit.go modelGetsWholeGPU`), and
the **effective host RAM limit** (from `add-container-memory-limits`), minus
configurable reserves. Output is a `Plan`:

- `mode`: `gpu` (fits fully), `hybrid` (needs host RAM), `cpu` (no viable GPU
  placement), `refuse` (exceeds VRAM + safe host RAM even at minimum context),
- flag ops to realize it, a memory estimate (GPU/host/KV split), and a
  qualitative performance class (`native-gpu | fast-hybrid | cpu-bound-hybrid
  | cpu-only`) — no invented tok/s numbers.

Strategy per mode (prefer upstream over reimplementation):

- **gpu**: current behavior, explicit flags preserved.
- **hybrid, MoE**: pin `--n-cpu-moe N` from the unified recommendation and
  set `--fit-target` to our GPU reserve so upstream fitting handles the
  remainder; validate with `llama-fit-params` preflight where available.
- **hybrid, dense**: leave `-ngl` unset (`auto`) and delegate layer split to
  upstream `--fit` with our `--fit-target`/`--fit-ctx`; llama-skein still
  owns the *host-RAM* feasibility gate, which upstream fitting does not check.
- **custom**: any user-pinned `-ngl`/`-ot`/`--cpu-moe`/`--n-cpu-moe`/
  `--tensor-split` in `cmd` disables automatic placement for that model
  (matching upstream's own semantics and the tuning-injection precedent).

### 3. Automatic application with revert-by-construction

- A boot/reload-time `applyAutoPlacement()` sibling of `clampModelsToFit()`
  (same call site, `server.go:277` region): computes the plan per model and
  applies flag ops **in-memory** via `applyFlagOps`, preserving the original
  in `TuningOriginalCmd` fashion. Never persisted to `config.yaml`; explicit
  flags always win; fails open when the plan cannot be computed confidently.
- Because placement is per-model and in-memory, "going back to normal" for a
  smaller model is automatic: its cmd was never touched, and swap-group
  eviction already serializes GPU tenancy. There is no global state to revert.
- The fit guard gains hybrid placement as a third remedy between "shrink ctx"
  and "refuse": 507 only when even full CPU-MoE at minimum context exceeds
  the safe host budget.
- The config write path (`PATCH/POST /api/config/models`) reports the planned
  placement in its response so callers see what will actually run.

### 4. Policy + visibility (spec-first)

- New top-level `placement:` config block: `mode` (default `auto`), GPU/host
  reserves (defaults: host `max(12 GiB, 10%)`, GPU `max(2 GiB, 5%)`),
  `minimumContext`, `allowKvQuantization` (default **false** — never silently
  quantize KV to force a fit).
- `/api/fit*` responses and `/v1/models` runtime hints expose the effective
  placement: mode, estimated GPU/host bytes, applied flags, performance
  class, and why. OpenAPI spec first, Go/TS regenerated.
- Guards learn about hybrid: the prompt guard's `maxSafeCtx` uses hybrid-aware
  results; wedge-watchdog and `maxRequestTimeSecs` documentation gain a
  cpu-bound-hybrid caveat (slow ≠ wedged).

### 5. Acceptance scenario (z4 / DeepSeek-V4-Flash)

Documented, manually executed: UD-IQ2_M (90.9 GB) or UD-IQ3_XXS (104 GB) on
z4 loads via auto placement (expected: hybrid, ~44 GB GPU / remainder host,
32K initial ctx, KV unquantized, dspark draft **off**), a chat completion
succeeds, and a small model loaded afterward runs full-GPU untouched.
Prerequisites recorded in design.md: LXC 102 memory raise (48 → ~112 GiB),
disk headroom, `llama-fit-params` chmod, deepseek4 arch present in the
deployed build (verified via strings).

## Non-goals

- OOM retry ladders and learned placement profiles —
  `add-placement-retry-learning`.
- Auxiliary-file classification at pull time (dspark/draft/projector) — the
  install-plan surface of `host-model-management-api`; this change only
  guarantees the *fit verdict* side ranks variants honestly.
- Multi-GPU tensor-split planning (fleet has single-GPU hosts; upstream
  `--fit` handles multi-GPU when it arrives).
- vLLM/MLX automatic placement: the translator warns as today; planner treats
  them as `custom`.

## Risks

- **Estimate accuracy**: mitigated by delegating actual allocation to
  upstream `--fit` where possible, exact tensor-table byte math where we pin
  flags, `llama-fit-params` preflight, and conservative default reserves.
- **Slow-hybrid misclassification as wedge/timeout**: addressed explicitly in
  guard integration tasks.
- **Config churn**: in-memory application + the existing `bytes.Equal` no-op
  write guard means no restart storms and no persisted drift.
