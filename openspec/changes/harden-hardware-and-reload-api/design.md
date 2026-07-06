# Design: harden-hardware-and-reload-api

## D1 — loaded_model selection: largest weights win

**Chosen:** `loadedModelInfo()` collects all running models that resolve to a
model file, then picks the one with the largest `model_mb`; ties break on
lexicographically smallest ID.

**Alternatives considered:**
- *Sort IDs, pick first* — deterministic but meaningless: a 0.5 GB draft model
  alphabetically before a 34 GB primary would own the meter.
- *Report an array of running models* — the honest fix, but a wire-contract
  change rippling through opencode's sidebar and skein; deferred (non-goal).

**Why:** the meter's job is "what dominates memory right now"; the largest
model is that answer, and determinism falls out for free.

**Contract:** `LoadedModelInfo` schema unchanged in shape;
`kv_estimate_mb`/`model_mb` semantics unchanged. Description documents the
selection rule.

## D2 — reload coalescing via dirty flag, extracted helper

**Chosen:** extract the run/skip logic into `internal/server/reloadrunner.go`:

```go
// NewCoalescingRunner returns a func() that runs fn at most once
// concurrently. A trigger during a run marks it dirty; the finishing run
// re-runs fn exactly once more (not once per dropped trigger).
func NewCoalescingRunner(fn func()) func()
```

`llama-skein.go` wraps its existing `reload` closure with the runner and
passes the wrapped func to `SetReloadFn`.

**Alternatives considered:**
- *Queue every trigger* — N rapid patches would run N reloads; wasteful, and
  each reload re-reads the whole file so the last pass sees everything anyway.
- *Fix inline in llama-skein.go* — untestable from `main`; the bug proved this
  logic needs a unit test.

**Why "one more pass" is sufficient:** reload reads the config file fresh at
start. Any number of writes during a run are all visible to the single
follow-up pass. Invariant: after the last trigger returns, a reload that
started at-or-after that trigger's file write eventually completes.

**Concurrency contract:** the runner serializes fn; fn itself keeps its
existing internal guards (belt and braces — upstream file-watcher also calls
reload directly).

## D3 — GGUF parse cache keyed by (path, mtime)

**Chosen:** a small `sync.Mutex`-guarded map on `server.Server`:
`map[string]cachedGGUF{mtime time.Time; g *gguf.GGUF}`. `fitForModel` consults
it before `gguf.ParseFile`. Whole-file invalidation via mtime comparison; the
cache dies with the Server on hot reload (see constraints).

**Alternatives considered:**
- *Cache the fit Result* — wrong layer: Result depends on live VRAM numbers
  which change every sample; the expensive, stable part is the GGUF header.
- *Global package-level cache* — survives reload; would need explicit
  invalidation; server-generation lifetime is simpler and provably correct.

**Interface:** unexported `(s *Server) parseGGUFCached(path string) (*gguf.GGUF, error)`;
`fitForModel` is its only caller today. `gguf.ParseFile` has other per-request
callers (`apimodels.go:202,278`, `modelhelpers.go:142`) — deliberately out of
scope here (single concern: the hardware-poll path); migrate them in a
follow-up if their traffic warrants it.

## D4 — slot-aware effective context (added 2026-07-06 after z4 incident)

**Observed defect:** llama-server splits `n_ctx` across `--parallel` slots.
On z4, build b-a410713 with no `--ctx-size`/`--parallel` ran 262144 ctx ÷ 4
slots = 65,536 effective — while `/api/fit` advertised max_safe_ctx 237,076
(computed from the trained context). opencode trimmed prompts to ~232k and
the server rejected everything past ~65k with "context size exceeded" —
at 23% on the context meter and 81% on the RAM meter. Worse, with
`--parallel 1` the same build defaults n_ctx to 4096, disproving the config
comment that "llama-server defaults to the model's trained context".

**Chosen:**
1. Fit divides by parsed `--parallel`/`-np` (`fit.Params.ParallelSlots`).
2. Deployment policy: our shipped configs always set `--ctx-size` and
   `--parallel 1` explicitly — llama-server's defaults are version-dependent
   and have now burned us twice.

**Alternatives considered:**
- *Reconcile with live `/props` (n_ctx, total_slots) when the model is
  running* — authoritative and version-proof; deferred as follow-up because
  it needs plumbing into the running process's upstream port and the explicit
  -config policy already removes the ambiguity for our deployments.
- *Assume a hardcoded llama-server default when flags are absent* — the
  default provably varies by build; encoding it would be a new lie.
