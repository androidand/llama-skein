# Proposal: Per-GPU tuning profiles (auto-injected llama-server flags)

## Why

Getting good llama.cpp performance on AMD GPUs is gfx-arch-specific and
non-obvious. On the R9700 (gfx1201) we measured that the *shipped* default
config ran at 19 t/s; adding `--flash-attn on --parallel 1` and, for MTP
models, `--spec-type draft-mtp` took it to **44 t/s (2.3×)** — none of which
was applied automatically. Every operator has to rediscover these flags per
card, and the good values differ by architecture (RDNA4 vs RDNA3 vs RDNA2).

The user runs one request at a time and wants flash attention on by default,
with verified per-GPU tuning shipped in the box and tweakable at runtime.

## What

- **Detect the GPU's gfx target** at startup (PCI device-ID map, with a
  config override), independent of ROCm tooling being installed.
- **Ship a curated tuning database (a data file in the repo)** keyed by
  (gfx target, use-case). A profile is a set of llama-server flag defaults
  (flash attention, parallel slots, and — for MTP-capable models —
  speculative-decoding flags) plus notes and cited sources. It's an open,
  PR-able collection that grows as we verify hardware and harvest community
  findings (Reddit/GitHub); a user data file can extend or override it.
  Seeded with our GPUs and the single-stream agentic/opencode use case.
- **Auto-inject profile flags at model launch**, adding only flags the model's
  `cmd` does not already set. An explicit flag in `cmd` always wins. Applies
  to the llamacpp backend only.
- **Expose and allow editing** the effective profile via new control-plane
  API (`/api/tuning/*`), so opencode/skein can show and tweak it.
- Mark each profile `verified: true|false` so unbenchmarked archs (gfx1100,
  gfx1030) ship conservative-but-safe defaults without claiming they're tuned.

## Guiding principle: recommended, never forced

Profiles are **defaults, not policy**. At every layer the operator can see,
change, or fully opt out of them:

- **Per-model**: any flag set explicitly in a model's `cmd` wins over the
  profile (already the injection rule).
- **Per-host**: `PATCH /api/tuning` overrides any profile field to any value
  (e.g. `--parallel 4`, flash-attn off) AND accepts arbitrary extra flags via
  `extra_args` for values the curated profile doesn't model.
- **Off entirely**: `tuning.enabled = false` (config or PATCH) disables all
  auto-injection — the server launches exactly the `cmd` as written.
- **Visible**: `effective_flags` always shows what was actually launched, and
  `GET /api/tuning` reports whether values are `recommended` (from the
  profile) or `override` (set by the user), so nothing is silently imposed.

The verified profile values are a starting point the user is expected to
tune, not a fixed contract.

## Verified inventory (2026-07-06)

| gfx | arch | cards | status |
|-----|------|-------|--------|
| gfx1201 | RDNA4 | R9700 (proxmox) | **verified**: fa on, parallel 1, draft-mtp for MTP models |
| gfx1100 | RDNA3 | W7800 (z4), RX 7900 XTX (rocky) | conservative: fa on, parallel 1 |
| gfx1030 | RDNA2 | RX 6800 XT | conservative: fa on, parallel 1 (no WMMA) |

## Constraints

- `contracts/llama-skein.openapi.json` is the source of truth — spec first,
  then `go generate ./pkg/apicontract`, then handlers.
- Auto-injection must NEVER override a flag the user set explicitly in `cmd`;
  the profile only fills gaps.
- Detection must work without `rocminfo`/ROCm CLI installed (proxmox host
  lacks it) — PCI device ID via sysfs is the primary path.
- Profiles apply to `backend: llamacpp` only; MLX/vLLM untouched.
- MTP flags apply only to models flagged MTP-capable (existing
  `metadata.mtp` plumbing), never to plain models.

## Non-goals

- Auto-benchmarking / auto-tuning at runtime (profiles are curated constants).
- Building ROCm binaries or GPU passthrough into containers (z4's CPU-only
  LXC is separate work).
- Multi-GPU tensor-split tuning (single-GPU hosts today).
- Changing KV cache quant type automatically (model-cmd concern; profiles
  only add fa/parallel/spec flags, which are safe to inject).
