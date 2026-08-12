# Links

## Related

- androidand/llama-skein#23 — `flag-under-offloaded-models`

  Sibling, not a dependency. #23 makes a bad pin *visible*; this change gives
  operators the way to say "yes, I meant it." They are independently shippable and
  each is better with the other: #23's `under_offloaded` warning is imprecise
  without `declared` (it cannot distinguish a deliberate hybrid from a stale pin),
  and `declared` has nothing to suppress without #23's warning.

  This change's Phase 4 **supersedes #23 tasks 17 and 18**: with placement in a
  structured block, removing a pin is deleting a field rather than patching a `cmd`
  string, which sidesteps both the missing `n_gpu_layers` removal path and the
  `${PORT}` round-trip corruption. If Phase 4 lands first, close those two as
  superseded rather than implementing them.

- `add-auto-hybrid-placement` (this repo, complete) — owns `internal/placement` and
  the `auto` default this change builds on. Its point stands: the planner was always
  correct; nothing adopted it.
- `add-placement-retry-learning` (this repo, complete) — owns `ladder.go` and the
  adaptive retry this change bounds (Phase 2) and extends (Phase 3).
- `bound-max-safe-ctx` (this repo, complete) — closed the fleet-wide context
  confusion. Phase 2's `minContext` floor must not reopen it; see task 11.
- `add-model-config-gallery` (this repo) — the empirical layer. A declared intent is
  a *stated* constraint; a gallery entry is a *measured* result. Complementary: the
  gallery could eventually suggest a declaration, but never write one.
- `add-fit-load-guard` (this repo) — `intent: latency` refuses loads, so it must
  respect the same fail-open rule the guard established.

## Blocks

- androidand/opencode-skein#12 — `per-model-placement-controls`

  Partial. That change's control currently has no safe write path (see #23 tasks
  17–18). This change's Phase 4 gives it one. Its Phase 1 `declared`/`reason`
  fields also let the client show placement as a *choice* rather than a warning,
  which is the distinction its sidebar indicator needs to avoid nagging about
  correct hybrids.

## Sequencing notes

**Ship Phase 1 first and alone.** It is schema plus reporting, changes no planner
behaviour, and delivers the core declared/undeclared distinction. Phases 2–4 can
slip without blocking it or #23.

Phase 3 (the KV rung) is separable from Phase 2 and stands on its own — it is a
policy-ordering fix, not an intent feature. If Phase 2's open questions drag, ship
Phase 3 ahead of it.

Resolve task 1 before writing schema. Two of its questions change Phase 1's shape:
whether `intent` ships at all in Phase 1 (an enum with no behaviour attached risks
values that later mean something subtly different), and whether `minContext` is
restated or read from the model's existing `--ctx-size` — three places expressing
context is how the original ctx confusion started.
