# Spec: Inference-engine runtime management

## Changes

- ADDED: `RuntimeManager` interface (install / upgrade / version / health) with per-backend
  implementations (`llamacpp`, `mlx`, `vllm`) behind a registry keyed by backend, mirroring
  `internal/offload`. Unsupported platform/backend combinations return a clear not-applicable
  result, never a silent failure.

- ADDED: control-API endpoints (spec-first in `contracts/llama-skein.openapi.json`) to install,
  upgrade, and query version/health of a backend's engine, with streamed NDJSON progress
  (consistent with the existing llama.cpp upgrade).

- CHANGED: `/api/system/version` reports per-engine runtime info beyond `llama_cpp_*` —
  add `mlx` and `vllm` version/health blocks. Surfaced via `skein providers probe`/status.

- ADDED: skein CLI `providers runtime <install|upgrade|status> <provider> --backend <mlx|llamacpp|vllm>`.

## Behaviour notes

- llama.cpp management reuses the existing prebuilt/source + CUDA/ROCm autodetect + SELinux
  `chcon` + backup/restore logic; it is refactored behind the interface, not rewritten.
- MLX management targets a Python venv (`pip install -U mlx-lm`) and is Apple-silicon-gated.
- vLLM management targets a venv (`pip install -U vllm`) and is Linux/CUDA-gated.
- All install/upgrade actions back up the prior state and restore on failure.
