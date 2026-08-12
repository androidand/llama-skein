# Links

## Related

- androidand/llama-skein#24 — `declare-placement-intent`

  Sibling, not a dependency. This change makes a bad pin visible; #24 gives
  operators the way to declare that a placement is deliberate. Independently
  shippable, and each is better with the other: `under_offloaded` cannot
  distinguish a deliberate hybrid from a stale pin without #24's `declared` flag.

  **#24 Phase 4 supersedes tasks 17 and 18 of this change.** With placement in a
  structured block, removing a pin is deleting a field rather than patching a `cmd`
  string, which sidesteps both the missing `n_gpu_layers` removal path (task 17)
  and the `${PORT}` round-trip corruption (task 18). If that phase lands first,
  close 17 and 18 as superseded (`[~]`) rather than implementing them.

- `add-model-config-gallery` (this repo) — documents the same misconfiguration
  class on the same host (`muse-glimmer-30b-q5-k-m` at `--n-gpu-layers 40`,
  6.1 vs 34.5 tok/s). That change owns the *empirical* layer: measured known-good
  configurations. This change owns the *first-principles* detection that would
  have caught it without a measurement. Neither supersedes the other.
- `bound-max-safe-ctx` (this repo, complete) — the precedent. Added
  `under_configured` for the identical "the report knew and said nothing" gap in
  context sizing. `under_offloaded` deliberately mirrors its semantics: optional,
  advisory, one WARN on load, never enforced.
- `add-auto-hybrid-placement` (this repo, complete) — owns
  `internal/placement`. Records the deferral pattern this change breaks:
  auto-application "was deliberately deferred to clients … and no client ever
  shipped it."
- `add-model-offload-tuning` (this repo, complete) — introduced the typed offload
  fields on the config-patch contract; its tasks 9–10 (client auto-application)
  were dropped and never picked up.
- `add-fit-load-guard` (this repo) — `preloadFitRefusal` keys on
  `fit_level == marginal`, so making `fit_level` placement-aware changes which
  models preload. See task 14.

## Blocks

- androidand/opencode-skein#12

  opencode-skein `per-model-placement-controls` — consumes `under_offloaded` and
  the corrected `perf_class`. Its sidebar indicator ships without this change
  (falling back to `run_mode` + `vram_required_mb`); its `under_offloaded`
  preference and the "remove the pin" wording depend on it.

## Sequencing notes

Task 16 (`perf_class` for `ModeCustom`) is the highest-severity item and is
independent of the `fit_level` work. It can ship first, and probably should:
until it lands, opencode-skein's `HOST_PACED_PENALTY` does not fire for any
pinned model, so sub-agents keep landing on CPU-bound models.

Tasks 6 and 16 together change two signals that opencode-skein's scorer reads.
Ship them with task 17's coordination decision recorded, or an under-offloaded
model is penalised twice — once by the placement-aware `fit_level` and again by
`HOST_PACED_PENALTY`.
