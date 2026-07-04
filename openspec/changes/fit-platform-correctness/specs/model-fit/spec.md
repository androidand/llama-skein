# Spec delta: Model-fit hardware inputs

## Changes

- MODIFIED: host VRAM discovery for fit, context recommendation, and offload
  recommendation aggregates the **latest sample per GPU ID** and **sums across
  GPUs** (llama.cpp splits layers across cards by default). Previously the
  oldest buffered sample of a single GPU was used.

- MODIFIED: on unified-memory hosts (Apple Silicon), **all** fit paths — GGUF
  and MLX — budget against the Metal wired limit (`iogpu.wired_limit_mb`, else
  70% of RAM). Free budget is `min(budget − GPU-used, MemAvailableMB)`.
  Previously only MLX was capped; GGUF fit scored against total RAM.

- MODIFIED: no-GPU hosts budget free memory from `MemAvailableMB`
  (reclaimable pool) everywhere; the near-zero-on-macOS `MemFreeMB` fallbacks
  in context recommendation and offload recommendation are removed.

- MODIFIED: the hardware endpoint's fallback GPU entry on Apple Silicon reports
  the real chip name (`machdep.cpu.brand_string`) instead of the placeholder
  `"Apple Silicon (unified)"`.

## Invariants preserved

- API shapes unchanged (`/api/fit`, `/api/models/context/*`,
  `/api/models/offload/*`, `/api/hardware`).
- The fit engine (`internal/fit`) itself is untouched; only its inputs change.
- A configured (deployed) model is never reported as `no` (engine guarantee).
