# Proposal: Per-model CPU / MoE offload tuning

## Context

Large Mixture-of-Experts models (e.g. Qwen3-235B-A22B) have a huge total parameter count
but a small active set. llama.cpp can keep attention/shared weights on the GPU while pushing
the **expert** tensors to system RAM, letting a small-VRAM card run a much larger quant at
usable speed. The skein inference hosts (`proxmox`, `rocky`) have 64 GB RAM each, so this is
a real, untapped lever. Today the only way to set this is to hand-edit raw flags inside a
model's `cmd` string.

## Why

Operators (and skein/opencode) need a first-class, **backend-neutral** way to enable, disable,
and tune CPU/MoE offload per model — without hardcoding llama.cpp flag names into every caller,
and without re-implementing fragile capacity math on the client side.

## What

- A semantic offload configuration (`n_cpu_moe`, `cpu_moe`, `cpu_offload_gb`, `override_tensor`)
  exposed on the model config add/patch API and read back on `/v1/models`.
- A per-backend `OffloadTranslator` (llamacpp / vllm / mlx) that maps the semantic knobs to the
  correct native CLI flags. Unsupported knobs produce warnings, never silent misconfiguration.
- A **reliable, MoE-scoped** recommendation endpoint `GET /api/models/offload/{model}` that
  computes a suggested `n_cpu_moe` from the GGUF tensor table (exact expert byte sizes) plus
  live VRAM from `/api/hardware`. Non-MoE models and MLX return `applicable: false` with a reason.
- Clients (opencode, skein) **consume** the recommendation; they do not recompute it.

## Scope

- llama-skein: OpenAPI spec, generated types, `internal/offload` package, `pkg/gguf` tensor-table
  parsing + `RecommendCpuMoe`, config add/patch handlers, recommendation handler, `/v1/models`
  read-back, tests.
- opencode: regenerated TS client + `setModelOffload` endpoint/handler.
- skein: re-pin + consume read-back and recommendation via `internal/provider/llamaswap`.

## Risks

- **Capacity math accuracy** — mitigated by reading exact per-tensor byte sizes from the GGUF
  tensor info section (not guessing from param counts), reusing the proven VRAM/KV plumbing, and
  scoping recommendations to MoE models only.
- **Backend divergence** — mitigated by the translator interface: each engine owns its own flag
  mapping and warns on unsupported knobs.
- **Cross-repo drift** — mitigated by the design-first OpenAPI workflow (spec → generated Go/TS).
