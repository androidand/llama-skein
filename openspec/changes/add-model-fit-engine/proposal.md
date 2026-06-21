# Proposal: Model-fit engine and /api/fit (absorb llmfit-core)

## Context

`llmfit` (`~/dev/llmfit`, Rust, MIT) right-sizes LLMs to a machine: it detects
hardware, scores each model across **quality, speed, fit, and context**, estimates
tok/s from a benchmark catalog, and reports what will actually run well. Its engine
(`llmfit-core`) is excellent, but its delivery model is **single-host** — a desktop/TUI
that scans one machine — and its provider layer (`providers.rs`) is single-host
*discovery* (probe `localhost:11434/api/tags`, list installed models). That is the
inverse of the skein world: llama-skein **is** the runtime (it already serves
`/api/models`, `/api/tags`, `/api/hardware`), deployed on every provider host.

So the engine belongs in llama-skein, the fleet logic belongs in skein, and the
presentation belongs in opencode-skein / skein's TUI. This change is the **foundation**:
the per-host fit engine + its API. Cross-repo initiative map and task tracking live in
the linked skein beads epic.

## Why

llama-skein already owns the hardware truth (`/api/hardware`, GPU/VRAM monitoring, the
`available_mb` + macOS kernel-pressure work) and the local model list, but it cannot
answer **"what fits here, how well, and at what context size?"** — only a stub
`GET /api/models/context/{model}` exists, and it is not trusted. Recent incidents (a 35B
configured at 98k ctx that loaded then crashed mid-prefill on a 24 GB card; the
`--ctx-size` MLX breakage) are exactly what a real fit engine prevents: it would have
reported the model as Tight/over-context before it was ever started.

## What

Absorb `llmfit-core`'s engine into llama-skein as native Go (port, not fork — a Rust
fork would be a fourth rebase treadmill in a Go/TS shop; the algorithms and data are
MIT, so we reimplement and attribute):

- **Fit scorer** (`fit.rs` → `internal/fit`): `Analyze(model, system) → ModelFit` with
  memory-headroom fit (VRAM/RAM/unified), run mode (gpu / tensor-parallel / moe-offload /
  cpu-offload / cpu-only), the four-dimension weighted score, tok/s estimate from
  benchmark baselines, fit level (perfect/good/tight/marginal/no), and a **max safe
  context size** for the host (the ctx-calculator need).
- **Hardware spec** (`hardware.rs` → extend `internal/perf` + a fit-facing `SystemSpecs`):
  reuse the existing detection (we already ported the macOS vm_stat/pressure logic);
  add the fit-relevant fields (backend, unified-memory flag, total/usable VRAM).
- **Model catalog** (`models.rs` + data → `internal/fit/catalog`): the `LlmModel` /
  `Capability` / `ModelFormat` / `UseCase` types and a loader over the **vendored full
  catalog** (`hf_models.json`, `benchmark_cache.json`, `baselines.json`,
  `docker_models.json`), embedded via `go:embed`, MIT-attributed.
- **API (design-first)**: extend `contracts/llama-skein.openapi.json` with a fit surface,
  regenerate Go/TS:
  - `GET /api/fit` — fit report for every configured model on this host (the per-host
    payload skein aggregates).
  - `GET /api/fit/{model}` — fit for one model id (configured or catalog-known), with an
    optional `?ctx=` to score a specific context size.
  - `GET /api/catalog` — query the vendored catalog (filter by family/size/quant/use-case)
    so callers can browse "what could run here."
  - replace the `GET /api/models/context/{model}` stub with the real engine.
- **MLX/safetensors fit** (extends the scorer): the engine currently estimates only
  llama.cpp GGUF models; for `backend: mlx` it returns `max_safe_ctx: 0` ("GGUF only").
  Resolve the model's Hugging Face cache snapshot from `useModelName`, parse its
  `config.json` architecture dims (handling nested/MoE configs such as `qwen3_5_moe`),
  sum the `safetensors` blob sizes for resident weight bytes, and run the same KV math so
  MLX models get a real `max_safe_ctx`. This is the backend the recent Metal-OOM crashes
  hit (mlx_lm SIGABRTs with no graceful path), so it must be covered.
- **Automatic context enforcement** (new behaviour — the "fix it automatically" ask): the
  engine stops only *reporting* and starts *acting*:
  - **Pre-flight 413** — before forwarding a chat/completions request, estimate the prompt
    size and, when it exceeds the model's `max_safe_ctx`, reject with `413` plus the safe
    size (header `X-Skein-Max-Safe-Ctx`) instead of forwarding it to a backend that would
    OOM-crash (MLX) or 413 late (llama.cpp). Applies to all backends. The client
    (opencode-skein) catches the 413, trims to the advertised size, and retries.
  - **Load-time cap (llama.cpp)** — when a `llamacpp` model's command does not pin
    `--ctx-size`, inject `--ctx-size = max_safe_ctx` at start so the model is right-sized
    on load. MLX has no `--ctx-size` (stripped), so MLX relies on the pre-flight 413.

## Scope

In: the fit scorer (incl. MLX/safetensors), hardware spec extension, vendored catalog,
the `/api/fit` + catalog API, and **automatic context enforcement** (pre-flight 413 +
llama.cpp load-time `--ctx-size` cap) in llama-skein.

Out (separate changes, downstream of this one):
- **skein** `add-fleet-fit-placement` — aggregate `/api/fit` across providers, placement.
- **opencode-skein** `add-fit-sidebar` — per-host fit scores in the sidebar.
- **Not absorbed at all**: `providers.rs` single-host discovery (llama-skein already is
  the provider), the Rust desktop/TUI, the Rust crate itself (no fork).
