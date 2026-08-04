# Fit scoring for models not on disk

## Why

The fit engine (`internal/fit`, absorbed from llmfit-core in
`add-model-fit-engine`) answers "will this model run well here?" — but only
for models already installed, because it reads metadata from the local GGUF
file. The skein fleet gallery (companion change `fleet-model-gallery` in
the skein repo) needs the same verdict *before* a download: a user browsing
a catalog must see "Q4_K_M fits with 32k ctx on this host, Q8 won't"
without pulling 20GB first.

The scoring math doesn't care where the metadata came from. What's missing
is an entry point that accepts a model descriptor instead of a file path.

## What

### 1. Descriptor-based fit entry point

Extend the fit engine to score a hypothetical model from a descriptor:
parameter count, architecture family, quant variants with file sizes,
and requested context. Reuse the existing scoring paths (llama.cpp KV/VRAM
estimation, and the MLX path in `fit/mlx.go`) unchanged — only the metadata
source differs. Where GGUF-derived detail (layer count, head dims) is
unavailable, estimate from params + architecture family the way llmfit's
benchmark catalog did, and mark the verdict as estimated.

### 2. API endpoint

`POST /api/fit/hypothetical` taking a descriptor with one or more quant
variants; returns per-variant verdicts (fits / tight / won't-fit), max safe
context per variant (respecting `bound-max-safe-ctx` semantics), and the
recommended variant. Same response vocabulary as the existing `/api/fit`
so clients render both identically.

### 3. Contract

Update `contracts/llama-skein.openapi.json` (source of truth for cross-repo
codegen) with the new endpoint and schemas; regenerate downstream clients.

## Non-goals

- No catalog fetching, HF search, or curation — that is skein's
  `fleet-model-gallery`; this endpoint scores what it is handed.
- No changes to installed-model fit behavior or existing response shapes.
- No download/pull changes — `/api/pull` already exists.
