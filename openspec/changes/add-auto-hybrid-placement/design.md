# Design notes: automatic hybrid GPU + system-RAM placement

Investigation performed 2026-08-06/07 against the live repo, the deployed z4
build, and upstream llama.cpp. Corrects several assumptions in the original
(externally drafted) feature brief.

## 1. Repository ground truth

Two stacks exist; only the **active** one matters: `internal/server` +
`internal/router` + `internal/process` (wired by `llama-skein.go`). The
`proxy/` tree is the legacy upstream stack, used only by `cmd/legacy/`.
Any placement work targeting `proxy/process.go` would be dead code.

Existing, complete building blocks:

| Piece | Where | State |
|---|---|---|
| Semantic offload knobs + translator | `internal/offload/offload.go` (`Spec`, `Translator`, `applyFlagOps`) | done (`add-model-offload-tuning`) |
| Exact expert-byte math + plan | `pkg/gguf/offload.go` (`ExpertWeightBytes`, `RecommendCpuMoe`) | done; only caller is the HTTP endpoint |
| Fit engine | `internal/fit/` (`AnalyzeShape`, cache-type-aware KV, MTP haircut, hybrid-attention interval) | done; **offload/MoE-blind** |
| Boot-time cmd mutation precedent | `internal/server/fitguard.go clampModelsToFit` (in-memory `--ctx-size` rewrite, fail-open) + `internal/tuning/apply.go` (append-only injection, `TuningOriginalCmd` preservation) | done |
| Load refusal | `fitguard.go` 507 gate + preload skip | done; refuses what hybrid could serve |
| Whole-GPU budget semantics | `apifit.go modelGetsWholeGPU` (exclusive swap groups ⇒ budget = total VRAM, not momentary free) | done — reuse verbatim for planning |
| Per-GPU flag injection | `internal/tuning/` gfx-keyed profiles | done — the injection pattern to copy |

Key seams (from code exploration):

- Config flags live **inside the `cmd` string**; there is no schema field for
  ngl/offload/ctx. Writers: `patchCommandFlags`, `applyFlagOps`
  (`internal/server/confighelpers.go`), `setCtxSizeInCmd` (`fitguard.go`).
- `Server.buildCmd` (`modelhelpers.go:303`) defaults new registrations to
  `--n-gpu-layers 99` — which **disables upstream auto-fitting** (see §2).
  The planner must own this default.
- There is no per-load cmd-mutation hook in the router; placement must be
  applied at boot/reload/config-write time, which is sufficient because
  placement is per-model (see §4).
- `fit.go:430`: a configured, proven model can never score `no` — the current
  implicit accommodation of hand-offloaded models. Hybrid modeling makes this
  honest instead of forgiving.

## 2. Upstream llama.cpp capabilities (verified, not remembered)

Verified against the deployed lemonade-sdk gfx110X build on z4
(`/opt/llamacpp-rocm-gfx110X`, version `956973c`) and upstream discussion
[#18049](https://github.com/ggml-org/llama.cpp/discussions/18049):

- `--fit [on|off]` — **default on**: virtual test allocations, iteratively
  adjusts *unset* args to fit device memory.
- `--fit-target MiB[,MiB…]` — per-device free-memory margin, default 1024.
- `--fit-ctx N` — floor for auto-shrunk context, default 4096.
- `-ngl` default is now **`auto`** (also accepts `all`/exact).
- Auto-fit adjusts: context, `n_gpu_layers`, `tensor_split`,
  `override_tensor` — including **MoE expert placement to CPU** (dense
  tensors prioritized on GPU over sparse expert tensors).
- **All-or-nothing per argument**: a user-pinned `-ngl` / `--tensor-split` /
  `--override-tensor` removes that argument from program control entirely.
- `llama-fit-params` — prints the fitted CLI args **without loading the
  model**: our preflight dry-run. Present in the deployed build (needs +x;
  the upgrade path should chmod tool binaries).
- MoE/draft flags all present: `--cpu-moe`, `--n-cpu-moe`, `-ot`, plus
  `--cpu-moe-draft`, `--n-cpu-moe-draft`, `-ngld`, `-otd`.
- KV offload toggle `--kv-offload` (default on), `--mmap` (default on),
  `--mlock`, `--numa`, `--direct-io`.

What upstream fitting does **not** do — llama-skein's value-add:

- No **host-RAM feasibility** check: it happily overflows layers to system
  RAM without knowing the cgroup limit or leaving an OS reserve. The OOM
  killer arbitrates. llama-skein owns the host-side gate.
- No cross-model awareness (swap groups, eviction, persistent co-residents).
- No policy (KV-quantization consent, minimum context, reserves).
- Fitting happens at load; refusing *before* spawning a doomed process (and
  before downloading a doomed quant) remains our job.

Corrections to the external brief: `--gpu-layers`/`--n-cpu-moe`/`--fit` all
exist and are current (no need to design around their absence); `-ngl auto` +
`--fit on` is already the upstream default path; the "DSpark" files in the
target repo are a speculative-decoding module (draft), confirmed on the model
card — never a standalone quant.

## 3. z4 deployment ground truth (verified live 2026-08-06)

- Host: 125 GiB RAM total, no swap; **LXC 102 capped at 48 GiB RAM +
  512 MiB swap** (`pct config 102`). Running the 90–104 GB quants requires
  raising the container allocation (recommendation: `pct set 102 --memory
  114688` ≈ 112 GiB, leaving the host ≥ 13 GiB plus the other containers'
  actual usage; revisit against live host headroom at rollout).
- Container disk: 116 GiB free on `/` — fits UD-IQ2_M (90.9 GB) with
  cleanup headroom; UD-IQ3_XXS (104 GB) is tight (142 GiB of existing GGUFs
  under `/models`, several redundant).
- GPU: W7800 48 GB via passthrough (`/dev/kfd`, `/dev/dri`), gfx1100.
- Build: lemonade-sdk gfx110X, `deepseek4` architecture strings present in
  `libllama.so` (14 hits) — the arch is supported; still verify at pull time
  via preflight rather than assuming.
- 8 cores allocated to the container — CPU-side expert math will be
  bandwidth- and core-limited; performance class for the target scenario is
  `cpu-bound-hybrid`, suitable for async agent work, not autocomplete.

## 4. Architecture decisions

**Planner is pure; application is thin.** `internal/placement` takes
(ModelShape + expert bytes, VRAM budget, effective host budget, policy) and
returns a `Plan{Mode, FlagOps, Estimate, PerfClass, Reason}`. No HTTP, no
config I/O — unit-testable with synthetic shapes (no 100 GB fixtures).

**Budgets.** GPU budget reuses `modelGetsWholeGPU` semantics (exclusive-group
⇒ total VRAM minus reserve; shared ⇒ live free minus reserve; capped at
physical total per the `bound-max-safe-ctx` lesson). Host budget = effective
limit (cgroup-aware, from `add-container-memory-limits`) minus host reserve
minus current non-model usage. Swap never counts.

**Pin vs delegate.** For MoE hybrid we *pin* `--n-cpu-moe N` (deterministic,
inspectable, computed from exact tensor bytes) and set `--fit-target` to the
GPU reserve so upstream handles residual fitting. For dense hybrid we
*delegate* (leave `-ngl` at `auto`, set `--fit-target`/`--fit-ctx`) because
upstream's per-layer split search is better than anything we'd re-derive.
Either way `llama-fit-params` preflight (when the binary exists) turns "we
think it fits" into "upstream's own allocator agrees", and its output is the
effective-args report exposed via the API. Candidate evaluation between
`--cpu-moe`, partial `--n-cpu-moe`, and pure delegation happens once, on z4,
during acceptance — measured, not assumed.

**Revert-by-construction.** Placement flags are injected in-memory per model
at boot/reload (the `clampModelsToFit`/tuning precedent), never written to
`config.yaml`. A small model's cmd is never touched, so loading it after a
hybrid giant *is* the revert; swap-group eviction already guarantees the GPU
is handed over whole. No global mode, no undo path, nothing to clean up.

**Fail-open, refuse-confidently.** Like the fit guard: act only on plans
backed by known VRAM, known effective host limit, and a parsed tensor table.
Anything else leaves the model exactly as configured. Refusal (507 +
`model_does_not_fit_error`) requires confident infeasibility of the most
conservative plan (full CPU-MoE, minimum context).

**KV quality.** `allowKvQuantization` defaults false; the planner never
quantizes KV to force a fit. deepseek4 uses MLA (small KV) — context is not
the bottleneck for the target scenario; weights are.

**Draft models.** Speculative/dspark modules are never auto-enabled by
placement; if a model's cmd already carries draft flags, their VRAM cost is
counted (draft flags parsed, weights added to the GPU estimate) and the
planner may report `refuse` sooner — disabling the draft is an operator
decision surfaced in the reason string, not an automatic mutation (that
ladder belongs to `add-placement-retry-learning`).

## 5. Open questions (resolve during implementation)

1. Does `llama-fit-params` model host-RAM at all in its output (vs only
   device memory)? Determines whether preflight can also validate the host
   side or only the GPU side.
2. `--fit-target` semantics on ROCm inside LXC: whether the free-memory query
   sees other containers' VRAM usage correctly (expected yes via
   amdgpu sysfs; verify).
3. Whether pinned `--n-cpu-moe` + `--fit on` interact cleanly (pinning `-ot`
   patterns is what `--n-cpu-moe` expands to internally; upstream treats
   user-set override-tensor as removing it from program control — confirm the
   expansion counts as "user-set" in the deployed build).
4. Interaction with `tuning.ApplyProfile` ordering (tuning injects fa/parallel
   flags; placement must run after tuning so both see the final cmd).
