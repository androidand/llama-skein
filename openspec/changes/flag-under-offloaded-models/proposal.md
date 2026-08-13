# Flag under-offloaded models, and stop grading wasted VRAM as "good"

> **Labels.** This repo is public, so fleet hosts and installed models are named by
> capability and shape rather than by hostname or model id; the mapping lives in the
> private companion repo (`docs-skein/fleet-labels.md`). Host A is a 24 GB RDNA3
> workstation, host B a 48 GB RDNA3 host, host C a 32 GB RDNA4 host. `M1` is a 27B
> Q5_K_M hybrid attention/SSM model (65 blocks, `full_attention_interval 4`); `M2` an
> 18B Q8_0; `M3` a 27B Q4_K_M; `M4` a 30B Q5_K_M; `M5` a 9B Q8_0; `M6` a 35B MoE.
> All measurements are verbatim.

## Why

On host A (2026-08-12), `M1` served an agent session at
**1.2 tok/s**. Its `cmd` pinned `--n-gpu-layers 40`; the GGUF reports
`block_count = 65`, so 26 of 66 offloadable layers decoded on the CPU.
Setting `n_gpu_layers` to 99 took it to **32.4 tok/s** — a 27× loss that had been
sitting in the config unnoticed.

The fit engine already knew. `GET /api/fit/{model}` reported, at the time of the
stall:

```json
{ "run_mode": "cpu_offload", "gpu_resident_mb": 11465, "host_resident_mb": 7165,
  "fit_level": "good", "reason": "fits comfortably at this context",
  "vram_required_mb": 16662, "vram_total_mb": 24560 }
```

Four separate defects are visible in that one payload, and the last one is the
worst.

**1. Nothing flags an under-offloaded model.** `host_resident_mb: 7165` is the
whole bug, in the response, as a number no caller is told to care about. This is
the exact gap `bound-max-safe-ctx` closed for context: that change added
`under_configured` because "an under-configured model is invisible" and the fit
report "faithfully echoes the tiny `configured_ctx` and nothing signals that"
more was available. Placement never got the same treatment. A model whose weights
are needlessly in host RAM is invisible in precisely the same way.

**2. `fit_level` is inverted with respect to performance.** It grades VRAM
headroom only. The broken config left 7.9 GB of VRAM unused, so it scored
`"good"` with the reason *"fits comfortably at this context"* — actively
reassuring, while the model ran 27× slow. After the fix, correct full-GPU
residency scored **`"marginal"`** with *"fits only above the VRAM safety margin;
reduce context"*. The grade rewards the configuration that wastes the GPU and
penalises the one that uses it.

This is not one anecdote. Every model on host A, read from `/api/fit` the same
evening (`M1` already corrected):

| model | run_mode | GPU / host MB | vram_req of 24560 | `fit_level` |
|---|---|---|---|---|
| `M2` | `cpu_offload` | 10063 / **6037** | 15938 (8.6 GB spare) | **`perfect`** |
| `M3` | `cpu_offload` | 9865 / **6166** | 19062 (5.5 GB spare) | `tight` |
| `M4` | `gpu` | 18305 / 0 | 20988 | `tight` |
| `M5` | `gpu` | 9332 / 0 | 17990 | `tight` |
| `M6` | `gpu` | 15831 / 0 | 19203 | `perfect` |

A model stranding 6 GB of weights in host RAM with 8.6 GB of VRAM free grades
**`perfect`**, while two correctly-resident models grade `tight`. Any client
ranking providers on `fit_level` — `ctx-aware-subagent-placement` scores
`fit_level×1000`, its dominant term — prefers the broken configuration.

**The remedy is usually to remove the pin, not to correct the number.** Two of
the five models above are pinned at `--n-gpu-layers 40` and are nonetheless fully
resident, because 40 happens to exceed their layer count. So a blanket bump is
wrong, and so is treating each bad value as its own mistake: the pin is what
opts the model out of the fork's own correct auto-placement in the first place.
An operator writing `-ngl` at all disables the planner for that model
(`placement.go:152`). Where a pin carries no intent, deleting it is the fix, and
the report should say so.

**3. Pinned placement is not reported as a decision.** `internal/placement`
returns `ModeCustom` and bails *before planning* (`placement.go:152`), so the
report reads `{"applied": false, "est_gpu_mb": 0, "est_host_mb": 0, "reason":
"command pins placement flags; automatic placement stays out"}`. Declining to
*apply* a plan is correct — an operator's pinned flags must be honoured. Not
*computing* it means nobody can see that the pinned flags are worse than what the
host would have chosen. The counterfactual is the diagnosis, and it is discarded.

**4. The `ModeCustom` result claims `native-gpu`, disabling a downstream guard.**
This is the most consequential of the four. `Compute` returns
`PerfClass: PerfNativeGPU` unconditionally for pinned placement
(`placement.go:153`), so host A reported `perf_class: "native-gpu"` for a model
decoding 26 of 66 layers on the CPU at 1.2 tok/s.

opencode-skein's only protection against routing sub-agents onto host-paced models
reads that exact field. `isHostPaced()`
(`packages/opencode/src/local/placement.ts:104-107`) tests `perf_class` for
`cpu-bound-hybrid` or `cpu-only` and applies a 200,000-point penalty, with the
comment: *"a hybrid model earns a perfectly respectable fit_level … so without this
it wins on residency alone (+100_000) and a subagent silently lands on a model
~90x slower."* That workaround exists because of defect 2 — and it never fires for
a pinned model, because every pinned model claims `native-gpu`. **The guard is
disabled by precisely the configuration that produces the models it guards
against.** A correct client, written against the contract, was defeated by the
server asserting something untrue.

This is a recurring shape in this repo, not a one-off. `add-auto-hybrid-placement`
records it plainly: auto-application "was deliberately deferred to clients
(`add-model-offload-tuning` tasks 9–10, both `[D]`), and no client ever shipped
it." `add-model-config-gallery` documents the same misconfiguration on the same
host — `M4` at `--n-gpu-layers 40`, 6.1 vs 34.5 tok/s. That
one was found and fixed by hand; `M1` was the same bug on the same box
and survived because nothing looks for it.

**It is still live on that host**, and the table above is the acceptance test.
The two `cpu_offload` rows must flag; the three `gpu` rows must not — including
`M6` and `M5`, which are *also* pinned at `-ngl 40` and are
nonetheless fully resident. So the flag cannot key on the pinned number. It has to
key on the outcome, which is what `host_resident_mb` already reports.

## The counterfactual was measured, and it was right

On 2026-08-12 both remaining models had their `--n-gpu-layers` pins **removed** (not
raised), letting the planner compute placement. Measured decode throughput, same
prompt, same host:

| model | before | after | change | placement after |
|---|---|---|---|---|
| `M1` | 1.2 tok/s | 32.4 tok/s | **27×** | pinned to 99 |
| `M2` | 5.42 tok/s | 39.42 tok/s | **7.3×** | pin removed, `applied: true` |
| `M3` | 4.04 tok/s | 35.05 tok/s | **8.7×** | pin removed, `applied: true` |

All six models on host A now report `run_mode: "gpu"` with `host_resident_mb: 0`.
Two still carry `-ngl 40` harmlessly, because 40 exceeds their layer count — the
outcome-keyed detection this change specifies correctly leaves them alone.

This matters for the design, not just the host: when the pins came off, the planner
chose full GPU residency unaided and reported `placement.applied: true`. The
counterfactual this change proposes to *report* is the same one the planner already
computes correctly. Nothing new has to be invented to know the pinned value was
wrong — only surfaced.

### But "remove the pin" is not universally right, and the counterfactual can lie

Measured on host A, 2026-08-13. `M1` was left pinned at `99` (32.6 tok/s). Removing
that pin — the remedy this proposal recommends — **halved throughput to 14.7 tok/s.**

The mechanism: the planner reserves 2048 MB of VRAM and delegates the dense layer
split to the engine's `--fit`, so it targets 22512 MB of a 24560 MB card. `M1` at its
configured 139264 ctx needs ~23260 MB for full residency (18630 weights + ~4630 KV).
That does not fit the target, so `--fit` shed layers to the CPU and the model became
host-paced. Pinning `99` had been *overriding the reserve* — which is exactly what
made it fast.

Two corrections fall out of this:

- **The counterfactual reported `est_host_mb: 0` while the real outcome offloaded
  layers.** For a delegated dense plan the planner cannot know what `--fit` will do,
  so reporting a precise host-resident estimate of zero is not conservative — it is a
  guess presented as a fact. A delegated plan must report its estimate as delegated
  and unknown, not as zero.
- **`host_resident_mb` stayed `0` across the regression.** The fit report showed
  `run_mode: "gpu"`, `host_resident_mb: 0`, and `fit_level: marginal` both at
  32.6 tok/s and at 14.7 tok/s. So the field this change keys `under_offloaded` on
  **cannot see an engine-side `--fit` split** — it only sees splits from flags
  llama-skein can parse. `under_offloaded` will therefore miss exactly this class of
  offload, which is the class the planner itself creates.

Reducing ctx to 110592 to fit inside the reserve did not rescue it either: still
`fit_level: marginal` at `vram_required_mb 23233`, because the safety margin is
computed against the full card, not the planner's target. `M1` was restored to
`-ngl 99` at 139264 ctx and re-measured at **32.64 tok/s**.

So the honest remedy is conditional, not general: remove the pin when the model fits
inside the planner's *reserved* budget, and keep an explicit pin when the operator
has deliberately traded the reserve for residency. That second case is precisely what
`declare-placement-intent` (#24) exists to express — and `M1` is now its clearest
example, since today the only way to say "I want the reserve spent on layers" is a
raw pin that also disables everything else.

## What Changes

- **`ModelFit.under_offloaded` (boolean).** True when a model's weights are
  host-resident (`host_resident_mb > 0`) while full GPU residency would fit the
  VRAM budget at the configured context. `reason` names the achievable placement.
  Additive and optional, mirroring `under_configured`.
- **A single WARN on load** when a model loads under-offloaded, matching the
  `under_configured` load-path warning. Reported through the existing
  `config.Warning` source set so it also appears in config warnings.
- **`fit_level` accounts for placement, not just headroom.** A model with
  avoidable host-resident weights may not grade above the level its fully
  resident equivalent would earn. This removes the inversion; it does not change
  grades for models that genuinely need hybrid placement.
- **Report the counterfactual for pinned placement.** `Compute` still returns
  `ModeCustom` and still applies nothing, but computes the plan it *would* have
  chosen and reports it (`est_gpu_mb` / `est_host_mb` / `perf_class`), with a
  reason that says whether the pinned flags are better, equivalent, or worse.
- **Detect the specific `-ngl` case.** When `-ngl` is pinned below the GGUF's
  offloadable layer count (`block_count + 1`) and the full model fits, the reason
  states the layer count the host can hold.
- **Prefer "remove the pin" as the stated remedy.** When the counterfactual plan
  would place the model fully on the GPU, the reason says the pin can be dropped
  rather than quoting a replacement number — a raised number is another value
  that goes stale on the next model or card, and it keeps the planner disabled.

## Capabilities

### Modified Capabilities

- `model-fit`: `under_offloaded`, and `fit_level` no longer rewarding unused VRAM.
- `placement`: pinned-placement plans are computed for reporting, never applied.

## Non-Goals

- **Not** auto-correcting a pinned flag. Operator-pinned placement stays
  authoritative — this change makes a bad pin *visible*, it does not overwrite it.
  Silent retuning of a running model is out of scope here and in
  `add-model-config-gallery`.
- **Not** a throughput model. `under_offloaded` is a placement fact derived from
  bytes and budgets, not a tok/s prediction. `perf_class` remains qualitative.
- **Not** the client surface. Editing and displaying placement is
  opencode-skein's `per-model-placement-controls`.
- **Not** the empirical layer. Measured known-good configurations remain
  `add-model-config-gallery`'s scope; this change is first-principles only.

## Open Questions

- **Threshold.** `under_configured` uses a factor (`underConfigFactor`) rather
  than a strict inequality, to avoid flagging trivial gaps. Does
  `under_offloaded` need one, or is "any avoidable host-resident weight byte"
  the right bar? Leaning strict: unlike context, there is no reason to leave
  weights in host RAM when they fit in VRAM.
- **Marginal-fit interaction.** Full residency for the host A case leaves 993 MB
  free and grades `marginal`. If flagging `under_offloaded` steers an operator
  toward a placement the safety margin then flags, the two signals disagree.
  Does `under_offloaded` need to be suppressed when full residency would not be
  `safe`, or reported alongside a recommended ctx reduction?
- **Scope of the `fit_level` change.** Callers rank on `fit_level` today.
  Changing its meaning is a behavioural change for
  `ctx-aware-subagent-placement` and skein's sweeps. Is a new field safer than
  redefining this one?

  *Surveyed against the shipped consumers (2026-08-12) — the answer is narrower
  than feared, with one exception that must be handled:*

  - **skein treats it as a binary.** `llm.HypotheticalFit.Fits()`
    (`internal/llm/client.go:297`) returns true for `perfect|good|tight|marginal`
    and false only for `no|unknown`. Any demotion **within** the fitting band is
    invisible to it. No `fit_level×1000` ranking exists in skein's Go — that term
    should be cited to a spec or dropped from the Why.
  - **opencode-skein deliberately does not gate on it.** `provider/provider.ts`
    carries three comments to that effect ("fit_level can't gate this",
    "independent of `fit_level`"), added after `fit_level:"no"` wrongly discarded
    a real ceiling on M1.
  - **The exception: `preloadFitRefusal`** (`internal/server/fitguard.go:182`)
    refuses startup preload for exactly `fit_level == marginal`. Capping an
    under-offloaded model at the grade its fully-resident equivalent would earn
    demotes the host A case to `marginal` — so this change silently stops those
    models preloading. That is arguably right (do not preload a misconfigured
    model) but it is a behavioural change nobody asked for, and it must be an
    explicit decision with a test, not a side effect.

  Given the above, redefining `fit_level` looks safe for ranking and unsafe only
  for preload. Prefer redefining over adding a field, and handle preload
  explicitly — either by keying `preloadFitRefusal` on the headroom grade rather
  than the placement-adjusted one, or by accepting the new behaviour deliberately.

## Impact

- `contracts/llama-skein.openapi.json` — `ModelFit.under_offloaded`.
- `pkg/apicontract/llama_skein.gen.go` — regenerated.
- `internal/fit/` — placement-aware `fit_level`, `under_offloaded` derivation.
- `internal/placement/placement.go` — compute-but-don't-apply for `ModeCustom`.
- `internal/config/warnings.go` — new warning source.
- `internal/server/apifit.go` — surface the field.
- `internal/server/fitguard.go` — `preloadFitRefusal` keys on `marginal`; decide
  whether it reads the headroom grade or the placement-adjusted one.
- Downstream: opencode-skein `per-model-placement-controls` consumes the flag.
