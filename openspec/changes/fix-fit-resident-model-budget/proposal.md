# Proposal: Fix fit budget for resident models

## Why

`/api/fit` and the load guard severely misreport models that are **currently
loaded** on a host. The budget formula in `internal/fit/fit.go:456-474` is:

```
budgetMB = VRAMFreeMB + gpuWeightMB
```

That formula is correct only when the model being scored is *stopped* (about to
cold-load): `VRAMFreeMB` then excludes some *other* resident model, and the
weights we're about to place genuinely land in newly-freed space. But when the
model being scored is **itself resident**, `VRAMFreeMB` already excludes the
model's own weights **and its KV cache**, so adding back only `gpuWeightMB`
drops the resident KV/overhead from the budget. `kvBudgetMB` then collapses to
~0 — or negative — and:

- `max_fit_ctx` reads 0 (qwopus3.6-27b-v2-mtp-q8-0 on proxmox: 80k ctx, 96%
  VRAM, still refused a bump to ~100k as "will not fit"), or
- `max_fit_ctx` reads a tiny fraction of what demonstrably runs (qwen3.6-35b-a3b
  q8-0 on z4: 256k ctx at 83% VRAM, fit suggests 26k).

Because opencode-skein's ctx dialog reads `max_fit_ctx`/`max_safe_ctx` to size
its suggestions, and the load guard refuses loads whose fit is "no", this one
formula produces both "you can't have more" (guard refusal) and "why is it so
small" (context suggestion) — the exact pair of symptoms seen on both hosts.

## What changes

When the fit engine's caller knows the scored model is currently resident, the
budget must be the model's own ceiling — `VRAMTotalMB` minus only what *unrelated*
resident work holds — not `VRAMFreeMB + gpuWeightMB` (which double-accounts the
model's own residency). This mirrors the existing exclusive-group workaround
(`modelGetsWholeGPU` → `p.VRAMFreeMB = 0` in `apifit.go`), extended to the
general "model is loaded right now" case.

In `internal/server/apifit.go`'s fit path: when `modelState(id)` reports the
model loaded, pass `VRAMFreeMB = 0` so `fit.Analyze` falls back to
`VRAMTotalMB` as the budget (same mechanism the exclusive-group branch already
uses and documents). The fit engine itself is unchanged — the fix is at the
caller, where the residency fact lives.

## Non-goals

- Not touching `fit-platform-correctness`'s hardware-feeding fixes (stale
  samples, unified-memory caps) — that change is complete and orthogonal.
- Not removing the Qwythos regression guard: `VRAMFreeMB = 0` falls back to
  `VRAMTotalMB`, and the "never exceed the hard physical total" cap stays.

## Risks

- A resident model's `max_fit_ctx` can be optimistic if *other* non-group
  models share the card. The exclusive-group path already handles eviction
  semantics; for the general case, budgeting against total is the same
  assumption every cold-load makes, and the load guard still refuses on a
  genuinely-overflowing request at load time.
- Behavior for stopped models is unchanged (their `VRAMFreeMB` is already the
  correct near-total figure).