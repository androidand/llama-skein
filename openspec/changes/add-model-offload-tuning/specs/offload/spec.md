# Spec: Per-model CPU / MoE offload tuning

## Changes

- ADDED: Semantic offload fields on the model config API.
  - `ConfigModelPatchRequest` and `ConfigModelRequest` gain (dual snake/dash naming, matching
    the existing `ctx_size`/`ctx-size` convention):
    - `n_cpu_moe` / `n-cpu-moe` (integer) — experts of the first N layers offloaded to CPU.
    - `cpu_moe` (boolean) — all MoE experts offloaded to CPU.
    - `cpu_offload_gb` / `cpu-offload-gb` (integer) — GB of weights offloaded to CPU (vLLM).
    - `override_tensor` / `override-tensor` (string) — advanced tensor placement regex (llamacpp).
  - `Model` response gains read-back fields: `n_cpu_moe`, `cpu_moe`, `cpu_offload_gb`,
    `override_tensor`, reflecting the current persisted command.

- ADDED: Backend translation layer (`internal/offload`).
  - `OffloadSpec` carries the semantic knobs; `OffloadTranslator.Flags` maps them to native CLI
    flags per backend and returns warnings for unsupported knobs; `OffloadTranslator.Parse` reads
    settings back out of an arg slice.
  - llamacpp: `n_cpu_moe→--n-cpu-moe`, `cpu_moe→--cpu-moe`, `override_tensor→--override-tensor`;
    `cpu_offload_gb`→warn. vllm: `cpu_offload_gb→--cpu-offload-gb`; others→warn. mlx: all→warn (no-op).

- ADDED: `GET /api/models/offload/{model}` → `OffloadRecommendation`.
  - `{ applicable, backend, n_cpu_moe, reason, expert_bytes_total, vram_free_mb, fits_fully_on_gpu }`.
  - Computed in llama-skein from the GGUF tensor table (exact `*.ffn_*_exps.*` byte sizes, with a
    dimensional fallback) and live VRAM from `/api/hardware`. MoE-scoped: non-MoE models and the
    mlx backend return `applicable: false` with a `reason`.

## Behaviour notes

- Offload flags are written into the persisted model `cmd` via the existing `patchCommandFlags`
  mechanism and survive config reload — identical to `ctx_size` / `n_gpu_layers` today.
- Clients consume the recommendation endpoint; recommendation math is not duplicated client-side.
