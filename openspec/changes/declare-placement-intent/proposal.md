# Declare placement intent, not placement flags

> **Labels.** This repo is public, so fleet hosts and installed models are named by
> capability and shape rather than by hostname or model id; the mapping lives in the
> private companion repo (`docs-skein/fleet-labels.md`). Host A is a 24 GB RDNA3
> workstation, host B a 48 GB RDNA3 host, host C a 32 GB RDNA4 host. `M1`–`M6` are
> that fleet's installed models. All measurements are verbatim.

## Why

`--n-gpu-layers 40` and a deliberate hybrid split are **byte-identical** in
`config.yaml`. Nothing in the system can tell an intentional trade from an
inherited mistake, and that is why host A served an agent session at 1.2 tok/s for
months without anyone noticing: the misconfiguration was indistinguishable from a
choice.

The policy layer was never wrong. `placement.mode` defaults to `auto` —
*"full GPU when it fits, hybrid when it doesn't, refuse when even hybrid can't fit
safely"* (`internal/config/placement.go:12-13`) — and host A carries **no
`placement:` block at all**, so `auto` was active the entire time. Every model
opted out individually by having `-ngl` in its `cmd`, because
`hasPinnedPlacement` keys on the *presence* of any placement flag
(`internal/server/placementguard.go:66-76`) and `Compute` returns `ModeCustom`
before planning (`placement.go:152`).

So the default is right and its effective adoption on that host was zero. This is
not a defaults problem, and changing the default cannot fix it.

The deeper problem is what a pinned flag *is*. `-ngl 40` is a **mechanism frozen
at one moment**, for one model's layer count, on one card:

- It goes stale silently. Requantise the model, swap the card, copy the line to a
  sibling entry, and the number is wrong with no signal.
- Its meaning is relative to `block_count`, so the same value is correct for one
  model and catastrophic for another. Verified on host A: `-ngl 40` cost 7–30× on
  three models and was harmless on two others whose layer count it exceeded.
- Disabling the planner is a **side effect** nobody asked for. An operator writing
  `-ngl` is expressing something about layers, not requesting that automatic
  placement stand down for the lifetime of that entry.

A goal does not go stale. *"This model needs 200k context and I accept the speed
cost"* stays true across requants, model swaps, and GPU upgrades — and it leaves
the planner free to pick the cheapest mechanism that satisfies it, re-deciding on
every load against the hardware actually present.

## The generator was already fixed — for auto-install only

Investigated before Phase 1 (2026-08-12), because a generative bug would outrank
migrating existing entries. **It is not generative in the shipping binary**, and the
reason is the strongest available argument for this change.

`internal/server.buildCmd` (`modelhelpers.go:322-337`) emits exactly
`llama-server --port ${PORT} --model <path>` plus caller-supplied flags, and its doc
comment records this precise bug being fixed once already:

> "does NOT inherit flags from any existing model — an auto-installed model must let
> llama.cpp's fit engine size ctx/offload/placement to the host's actual VRAM, not
> silently copy whatever the first configured model happened to use (previously this
> produced deepseek's small-VRAM hybrid flags like `--n-cpu-moe 25 --ctx-size 32768`
> on every model, under-using large cards)."

The live install paths are clean: `POST /api/models/operations` passes
`flags := ""` (`apimodeloperations.go:221`) and resolves companions from the
operation's *own* artifacts. `POST /api/models/pull` passes operator-supplied
flags, which is a legitimate explicit choice.

So the fork already reached this change's conclusion — *a generated model must not
carry a placement pin, because the pin defeats the fit engine* — and applied it to
one code path. This change generalises the same principle from auto-installed
models to every model, and gives operators a way to express placement that does not
defeat the planner.

**The pre-fix copy still exists.** `proxy/proxymanager_config.go:526-572` retains
both defects: it clones the template model's *entire* flag set (the comment claims
"everything up to (and including) `--model`", but the loop copies every flag,
including `--model-draft` and `--mmproj` pointing at an unrelated model's companion
files), and falls back to `--n-gpu-layers 99`. It is unreachable in the shipping
binary — `proxy.New` is constructed only in `cmd/legacy/llama-skein.go`, which the
Makefile does not build — but it is a landmine for anyone reviving or copying it.

## Automatic placement has zero adoption across the fleet

The real finding is scope. Placement pins in the fleet configs
(`the private companion repo's config/`):

| host | pins |
|---|---|
| host A | 6 × `--n-gpu-layers 40` |
| host B | 4 × `--n-gpu-layers 99` |
| host C | 1 × `--n-gpu-layers 99`, 5 × `--n-gpu-layers 999999` |

**Every model on every host carries a placement pin, so `auto` is disabled
fleet-wide.** host A's pins were the ones that cost throughput, but `99` and
`999999` are not harmless — they are "all layers" idioms that still switch off the
whole planner: no hybrid split for an oversized model, no `--fit-target` reserve, no
adaptive retry ladder. `add-auto-hybrid-placement` exists specifically to run models
larger than VRAM, and on these hosts it can never engage.

Caveat on the evidence: host B and host C were unreachable during this investigation,
so those two rows are the repo copies, not live reads. host A's repo copy had already
drifted from its live config, so both rows need confirming on the hosts before
being acted on.

## What Changes

A per-model `placement:` block that expresses **constraints the operator cares
about**, with llama-skein owning the flags that satisfy them:

```yaml
models:
  long-doc-summariser:
    cmd: llama-server --port ${PORT} --model /models/foo.gguf --ctx-size 200000
    placement:
      intent: context
      minContext: 200000
      allowKvQuantization: true
      reason: "whole-document passes need 200k; speed cost accepted"
```

- **`intent`** — `auto` (default, and the behaviour when the block is absent),
  `latency` (never offload; refuse rather than serve slowly), `context` (satisfy
  `minContext` as a hard floor by the cheapest available means), `custom` (the
  operator pins mechanisms in `cmd` and is declaring that deliberate).
- **`declared` on the fit report.** A model with a `placement:` block is a
  *declared* placement; a raw flag with no block is *undeclared*. This is the
  distinction `under_offloaded` (llama-skein #23) needs to warn precisely: fire on
  unexplained host residency, stay silent on a stated trade.
- **`reason`** is free text, surfaced in the fit report and any UI. A declaration
  without a reason is still declared, but the reason is what survives staff
  turnover and is what makes the entry reviewable.
- **Constraint-ordered satisfaction.** When meeting a context floor, levers are
  spent cheapest-first. The retry ladder
  (`internal/placement/ladder.go:29`) already encodes most of this ordering —
  `RungShrinkContext` before `RungFullCpuMoe` — which this change makes explicit
  and bounds: a hard `minContext` floor stops `RungShrinkContext` at the floor
  rather than below it.
- **Fix the KV-quantization inversion.** `AllowKvQuantization` defaults `false`,
  deliberately — *"KV quality is never traded silently"* — so the **cheap** rung is
  off the ladder by policy while the **expensive** one stays reachable. KV
  quantization pays once, in quality; layer offload pays forever, on every token
  (measured: 7–30×). Add a KV rung ahead of `RungFullCpuMoe`, reachable only when
  the operator has opted in — which a declared `context` intent is exactly the
  place to do.
- **Legacy flags keep working.** A raw `-ngl` is still honoured verbatim. It is
  reported as undeclared and migratable; nothing is rewritten under an operator.

## Phasing

Ordered so the first slice ships on its own and delivers the core distinction.

- **Phase 1 — schema + reporting.** The `placement:` block, `intent`, `reason`,
  and `declared` on `/api/fit`. **No planner behaviour change at all.** This alone
  separates intent from accident and lets #23's warning be precise. Small, additive,
  independently shippable.
- **Phase 2 — intent semantics.** `latency` refuses rather than offloading;
  `context` bounds `RungShrinkContext` at `minContext`.
- **Phase 3 — the KV rung.** Per-model `allowKvQuantization` and a KV rung ahead
  of `RungFullCpuMoe`. Separable: it stands alone if Phase 2 slips.
- **Phase 4 — migration.** An API to write the block, and detection that offers the
  declared equivalent of a legacy raw flag. This **supersedes #23 task 17** (the
  missing `n_gpu_layers` removal path): with placement in a structured block,
  removing a pin is deleting a field, not patching a `cmd` string — which also
  sidesteps the `${PORT}` round-trip corruption (#23 task 18).

## Capabilities

### Modified Capabilities

- `placement`: declared vs undeclared placement; intent-driven constraint
  satisfaction; a KV rung on the ladder.
- `config-management`: the per-model `placement:` block and its precedence
  against raw flags in `cmd`.

## Non-Goals

- **Not** removing support for raw placement flags. They stay honoured verbatim.
  The existing "explicit flags always win" contract — which upstream llama.cpp also
  follows per-argument — is unchanged.
- **Not** auto-migrating existing configs. Phase 4 *offers* the equivalent
  declaration; rewriting an operator's config unasked is the opposite of this
  change's point.
- **Not** a new placement algorithm. The planner and ladder already work; this
  gives them the operator's actual constraints instead of a frozen mechanism.
- **Not** the client surface. Displaying and editing declarations is
  opencode-skein `per-model-placement-controls` (#12).
- **Not** raising `placement.mode`'s default. It is already `auto` and correct.

## Open Questions

- **Conflict semantics.** When a `placement:` block and a raw `-ngl` both exist,
  the proposal keeps "explicit flags win" and emits a config warning. The
  alternative — a hard config error — is arguably better, since silently honouring
  one of two contradictory instructions is the exact failure mode this change
  exists to end. A warning is non-breaking; an error is clearer. Leaning warning
  for Phase 1, revisited in Phase 4 when migration exists.
- **Is `intent` needed at all in Phase 1?** `declared` + `reason` may carry the
  whole value, with constraints arriving in Phase 2. Shipping `intent` with no
  behaviour attached risks an enum whose values later mean something subtly
  different. Consider Phase 1 as `reason` + `declared` only.
- **Where the ctx floor belongs.** `minContext` in a placement block overlaps
  `--ctx-size` in `cmd` and the global `placement.minimumContext`. Three places
  expressing context is how the fleet ctx confusion started
  (`bound-max-safe-ctx`). Possibly it should read the model's existing
  `--ctx-size` as the floor rather than restating it.
- **Does model-add generate pins?** If the add-model path writes `-ngl` into new
  entries, this bug is *generative* and every future model inherits it. Unverified,
  and it changes Phase 4's urgency.

## Impact

- `internal/config/model_config.go` — `Placement` on `ModelConfig` (no such field
  today; `Backend`/`Filters`/`Timeouts` are the nested-struct precedent).
- `internal/config/placement.go` — per-model overrides of policy defaults.
- `internal/placement/placement.go` — declared-vs-pinned handling in `Compute`.
- `internal/placement/ladder.go` — bounded `RungShrinkContext`, new KV rung.
- `internal/server/placementguard.go` — `hasPinnedPlacement` gains a declared path.
- `contracts/llama-skein.openapi.json` — `declared`, `intent`, `reason` on the fit
  report's placement object; the patch request for Phase 4.
- Depends on nothing; **unblocks** the precision of #23's `under_offloaded` warning.
