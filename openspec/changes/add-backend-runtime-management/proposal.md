# Proposal: Unified inference-engine runtime management (llama.cpp, MLX, vLLM)

## Context

llama-skein spawns three inference engines as model backends: **llama.cpp**
(`llama-server`), **MLX** (`mlx_lm.server`), and **vLLM** (`vllm serve`). Today only
llama.cpp has lifecycle management — the existing upgrade path (`POST /api/skein/upgrade`
/ `skein providers upgrade`, prebuilt/source, CUDA/ROCm autodetect). MLX and vLLM are
installed and updated **by hand**: e.g. the m3 MLX engine lives in a hand-built
`~/.venv/mlx`, its binary path hardcoded in model commands, with no version detection,
install, upgrade, or health surface.

This opacity recently bit us: an MLX model "wouldn't load" — the actual chain was a
stray `--ctx-size` flag (llama.cpp flag on an MLX command) and then the host memory
guard unloading it, with no structured error. Manual, unmanaged engine setup made it
slow to diagnose.

## Why

Operators and skein need to **install, version-check, upgrade, and health-check** the
inference engine appropriate to each backend on each provider, uniformly — so adding or
repairing a provider doesn't require manual venv/pip/cmake steps, and so engine version
drift is visible across the fleet.

## What

- A **backend-neutral runtime-management interface**, mirroring the `OffloadTranslator`
  pattern (per-backend implementations behind one interface; nothing hardcoded to one engine):
  - **llamacpp** — wrap/extend the existing upgrade (prebuilt/source, CUDA/ROCm autodetect).
  - **mlx** — manage a Python venv + `pip install -U mlx-lm`; detect `mlx_lm` version;
    verify `mlx_lm.server` is runnable (Apple Silicon only).
  - **vllm** — manage a venv + `pip install -U vllm`; CUDA detection; version.
- **API (design-first)**: install / upgrade / version / health per backend, streamed
  NDJSON progress like the existing upgrade. Spec first, then generated Go/TS.
- **Version + health surfaced** in `/api/system/version` (already carries `llama_cpp_*`)
  and via `skein providers probe`/status — add mlx/vllm runtime info.
- **skein CLI**: `skein providers runtime <install|upgrade|status> <provider> --backend <mlx|llamacpp|vllm>`.

## Scope

- llama-skein: runtime-manager interface + 3 implementations; OpenAPI endpoints;
  version/health reporting; tests.
- skein: CLI + provider runtime status surfacing.
- opencode: regenerate client if endpoints are added.
- Docs: deploy doc updates so managed install replaces the manual venv/cmake steps.

## Risks

- Engine installs are heavy and host-specific (CUDA vs ROCm vs Apple unified) — mitigate
  with per-backend implementations, dry-run, and binary backups (the llama.cpp upgrade
  already backs up + restores on failure).
- Running `pip`/`cmake` as the service user — gate behind the authenticated control API;
  reuse the SELinux `chcon` handling already in `proxymanager_upgrade.go`.
- Apple-silicon-only MLX and CUDA-only vLLM must degrade cleanly on the wrong platform.

## Out of scope (already shipped, related)

- CPU/MoE offload tuning (`internal/offload`, `/api/models/offload/{model}`).
- The `MemoryGuardTrippedEvent` error surfacing for the memory guard.
