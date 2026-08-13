# Model Config Gallery

> **Labels.** This repo is public, so fleet hosts and installed models are named by
> capability and shape rather than by hostname or model id; the mapping lives in the
> private companion repo (`docs-skein/fleet-labels.md`). Host A is a 24 GB RDNA3
> workstation. All measurements are verbatim.

## Why

Getting a model to run *well* on a given box is currently folklore. The flags that
matter — `--n-gpu-layers`, `--ctx-size`, KV cache quantisation, flash attention,
`--reasoning-budget`, whether a draft model helps — interact with the GPU, its VRAM,
the engine build, and the quantisation. People work these out by hand and post the
results on Reddit. Nothing in the ecosystem captures them.

The cost shows up as configs that are quietly wrong for years. On host A,
`M4` ran at `--n-gpu-layers 40` when the model fits entirely in
24 GB: 6.1 tok/s instead of 34.5, a 5.7× loss, with 4.8 GB of VRAM left unused. The
same entry carried `--model-draft dflash-kquant.gguf`, which measured 34.5 vs 34.6
tok/s without it — 1.6 GB of VRAM for no gain, and a `[spec] failed to measure draft
model memory` warning at every load. Neither was visible as a problem; the model
"worked".

That was not an isolated entry. On 2026-08-12 the same host was found serving
`M1` to a live agent session at **1.2 tok/s** —
`--n-gpu-layers 40` against a `block_count` of 65, so 26 of 66 layers decoded on
the CPU. Corrected, it ran at **32.4 tok/s**: a 27× loss, in the same file, from
the same pinned flag, found only because someone noticed the session felt slow.
Two further models on that host (`M2`,
`M3`) are still in the same state. Three of
six entries in one config were quietly wrong, and the worst of them reported
`fit_level: "perfect"`.

Two things are missing. First, a **record** of known-good configurations keyed by what
actually determines them: model, quantisation, GPU architecture, VRAM, and engine
build. Second, a way to **discover** which models are even viable on a given provider
before downloading 19 GB to find out.

The detection half of this — noticing that a configuration is wrong from first
principles, without needing a measurement — is llama-skein
`flag-under-offloaded-models`, which mirrors `under_configured` for placement. It is
complementary, not a substitute: it catches what arithmetic can prove, while the
gallery captures what only measurement can establish (the same entry's
`--model-draft`, which cost 1.6 GB of VRAM for 34.5 vs 34.6 tok/s, is invisible to
any fit calculation).

`add-model-fit-engine` already computes whether a model fits (`/api/fit` returns
`max_safe_ctx`, `vram_required_mb`, `run_mode`). That is the arithmetic. This change is
about the empirical layer on top: what someone actually ran, and how fast it went.

## What Changes

- **A configuration record format.** A gallery entry pins a configuration to the
  conditions it was measured under: model identity (repo, file, quant, size), hardware
  (GPU architecture, VRAM, host RAM), engine (name and build), the full argument set,
  and measured results (prefill tok/s, decode tok/s, VRAM used, context tested).
  A configuration without measurements is a suggestion, and is marked as such.
- **Local capture first.** llama-skein already observes everything an entry needs: it
  owns the command, knows the hardware, and the engine reports its own throughput.
  Entries are captured from real runs on the provider rather than hand-authored.
- **A recommendation endpoint.** Given a model and this host, return the best-known
  configuration: exact match on (model, gfx, VRAM, engine) if one exists, otherwise the
  nearest neighbour with its differences stated, otherwise the fit engine's computed
  starting point.
- **A viability gallery.** Given this provider's hardware, list models known to run on
  comparable hardware, with expected throughput and VRAM — answerable before download.
- **Sharing, deliberately staged.** The format and local store come first; a shared
  index is only worth designing once there is real data to share and a provenance
  model. Nothing is published anywhere by default.

## Capabilities

### New Capabilities

- `model-config-gallery`: the entry format, the local store, capture from real runs,
  the recommendation endpoint, and the viability listing.

## Non-Goals

- **Not** automatic application. The gallery recommends; changing a running config stays
  an explicit action. Silent retuning of a working model is the opposite of what this is for.
- **Not** a replacement for `add-model-fit-engine`. The fit engine computes from first
  principles and is the fallback when no measurement exists.
- **Not** a public service in this change. No upload, no central registry, no telemetry.
  The format is designed so sharing is *possible* later; whether and how is a separate
  decision with privacy implications (hostnames, file paths, model inventories).
- **Not** a benchmark harness. Measurements are captured from real serving traffic, not
  from a synthetic sweep. A dedicated sweep is a plausible follow-up, not this.

## Open Questions

These need resolving during the research phase, not at implementation time:

- **Identity.** What makes two models "the same" for lookup? Repo + filename is fragile
  (unsloth requants under the same name); a file hash is exact but defeats sharing across
  requants of the same base model. Probably both: hash for exactness, base-model id for
  neighbour search.
- **Comparability.** When is another host's entry applicable here? Same gfx and >= VRAM is
  clearly safe; same gfx with less VRAM is not. Cross-vendor (CUDA → ROCm) is almost
  certainly not transferable for offload counts.
- **Trust.** If entries are ever shared, an entry is a claim about someone else's hardware.
  What makes one trustworthy — reproduction count, provenance, signed submissions?
- **Staleness.** An entry measured on `b1-dd1ea52` may not hold on a later build. Does an
  engine upgrade invalidate entries, or just downrank them?

## Impact

- `contracts/llama-skein.openapi.json` — `GalleryEntry`, `ConfigRecommendation`;
  `GET /api/gallery/{model}`, `GET /api/gallery/viable`, `POST /api/gallery/entries`.
- `internal/gallery/` — entry store, matching, capture.
- `internal/router/` — hook completed generations for throughput capture.
- opencode-skein / skein — surface recommendations when adding or tuning a model.
