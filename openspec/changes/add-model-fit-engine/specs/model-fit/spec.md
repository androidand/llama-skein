# Spec: Model-fit engine

## Changes

- ADDED: `internal/fit` package — a native-Go port of `llmfit-core`'s scoring engine.
  `Analyze(model LlmModel, system SystemSpecs, cfg CalcConfig) ModelFit` returns memory-fit
  (against the VRAM/RAM/unified pool), a run mode (`gpu` / `tensor-parallel` / `moe-offload`
  / `cpu-offload` / `cpu-only`), a four-dimension weighted score (quality, speed, fit,
  context) selected by use-case, an estimated tok/s from the benchmark catalog, a fit level
  (`perfect` / `good` / `tight` / `marginal` / `no`), and the **max safe context size** for
  the host. Ported from `fit.rs`; margins (e.g. 1.2× memory headroom) preserved.

- ADDED: `internal/fit/catalog` — the model catalog (`LlmModel`, `Capability`, `ModelFormat`,
  `UseCase`) loaded from the **full vendored llmfit dataset** (`hf_models.json`,
  `benchmark_cache.json`, `baselines.json`, `docker_models.json`) embedded via `go:embed`,
  MIT-attributed. Ported from `models.rs`.

- ADDED: a fit-facing `SystemSpecs` derived from the existing `internal/perf` snapshot
  (backend, unified-memory flag, total/usable VRAM, RAM, cores). Reuses the macOS
  vm_stat/kernel-pressure detection already in `internal/perf`; does not re-detect hardware.

- ADDED: control-API fit surface (spec-first in `contracts/llama-skein.openapi.json`):
  - `GET /api/fit` — fit report for every configured model on this host. This is the
    per-host payload the skein supervisor aggregates for fleet placement.
  - `GET /api/fit/{model}` — fit for one model id (configured or catalog-known); optional
    `?ctx=<n>` scores a specific context size instead of computing the max safe one.
  - `GET /api/catalog` — query the vendored catalog (filter by family / size / quant /
    use-case) to browse what could run on this host.

- CHANGED: `GET /api/models/context/{model}` now returns the real engine's max-safe-context
  (and the reasoning: model bytes, KV bytes at the host's cache-type, compute-buffer
  headroom) instead of the untrusted stub.

- ADDED: MLX/safetensors fit. For `backend: mlx` the engine resolves the model's Hugging
  Face cache snapshot from `useModelName`, parses `config.json` architecture dims
  (handling nested/MoE layouts such as `qwen3_5_moe` where top-level dim keys are absent),
  sums the `safetensors` blob sizes for resident weight bytes, and reuses the KV-per-token
  math (attention dims only — MoE expert count does not affect KV) to produce a real
  `max_safe_ctx`. Replaces the prior "fit estimate is currently computed for llamacpp GGUF
  models only" stub for MLX.

- ADDED: automatic context enforcement (pre-flight). Before a chat/completions request is
  forwarded, the proxy estimates the request's prompt token count (conservative byte/word
  heuristic — over-estimate to fail safe) and compares it against the model's
  `max_safe_ctx`. When the prompt exceeds it, the proxy returns `413 Request Entity Too
  Large` with `X-Skein-Max-Safe-Ctx: <n>` and a structured body
  (`{error:{type:"exceed_context_size_error", code:"prompt_over_max_safe_ctx"}}`) WITHOUT
  forwarding to the backend — preventing the MLX Metal-OOM SIGABRT and the late llama.cpp
  413. The guard is skipped when `max_safe_ctx` is unknown (0) so an un-analyzable model
  is never falsely rejected.

- ADDED: automatic load-time context cap (llama.cpp). When a `llamacpp` model is started
  and its command does not already pin `--ctx-size`, the engine injects
  `--ctx-size = max_safe_ctx` so the model loads right-sized for the host. MLX commands are
  never given `--ctx-size` (it is stripped as unsupported); MLX is protected by the
  pre-flight 413 instead.

## Behaviour notes

- Port, not fork: the Rust crate is not vendored or shelled out to; the algorithms and data
  are reimplemented in Go and the data files are carried as embedded assets with MIT
  attribution. No new language toolchain enters the build.
- The engine accounts for backend (llama.cpp / MLX / vLLM), KV cache quantization
  (`--cache-type-k/v`), MoE active-vs-total params, and unified memory (Apple Silicon),
  where the host's reclaimable pool — not raw free memory — is the budget.
- The fit report is the shared cross-repo contract: skein consumes `/api/fit` for
  placement, opencode-skein consumes it for the sidebar. specsync keeps the generated
  clients aligned across repos.
- Calibration is pinned by golden tests against observed real outcomes (the 18B-Q8 / 24 GB
  and 35B-MoE / 36 GB unified cases) so the scorer's predictions stay trustworthy.
