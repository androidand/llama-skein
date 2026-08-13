# Design: declare-placement-intent

Resolves the four Open Questions in `proposal.md`. Decided 2026-08-14.

## D1. `intent` does NOT ship in Phase 1

**Decision:** Phase 1 ships `reason` and `declared` only. The `intent` enum is
deferred to Phase 2, where its values acquire behaviour in the same change that
defines them.

**Why.** An enum with no behaviour attached is a promise about semantics that nobody
has had to honour yet. Ship `intent: latency` with no refusal logic and it means
"documentation"; add the logic in Phase 2 and it silently starts refusing loads on
configs written when it meant nothing. Every operator who adopted it early gets a
behaviour change they did not ask for, and there is no way to distinguish a
declaration written under the old meaning from a new one.

This repo has the scar. `add-model-offload-tuning` shipped typed offload fields whose
auto-application was deferred to clients (tasks 9–10, both `[D]`); no client shipped
it, and the fields sat meaning less than they appeared to for months. The lesson is
not "never phase" — it is "do not ship a vocabulary before the thing that gives it
meaning."

`declared` and `reason` do not have this problem. Both are *descriptive*: `declared`
reports a fact about the config (a block exists), `reason` carries operator prose.
Neither promises the planner will do anything, so neither can change meaning when the
planner later does.

**What Phase 1 therefore ships:**

```yaml
placement:
  reason: "200k context for whole-document passes; speed cost accepted"
```

That is the whole schema. A block with a `reason` marks placement as deliberate, which
is precisely what `flag-under-offloaded-models` (#23) needs to suppress a false
warning — and #23 is the change with a live bug behind it.

**Consequence for Phase 2:** adding `intent` later is additive, and a Phase 1 block
without `intent` keeps meaning exactly what it meant. Rejecting an unknown `intent`
value in Phase 1 (rather than ignoring it) prevents operators from writing
forward-looking values that Phase 2 would reinterpret.

## D2. `minContext` is NOT restated — the floor is read from `--ctx-size`

**Decision:** no `minContext` field. The model's configured `--ctx-size` **is** the
floor. `RungShrinkContext` may not reduce below it when placement is declared.

**Why.** Three places already express context — `--ctx-size` in `cmd`,
`placement.minimumContext` in policy, and `max_safe_ctx` in the fit report — and the
fleet-wide context confusion `bound-max-safe-ctx` closed came precisely from callers
trusting the wrong one. A fourth would be indefensible, and the failure mode is
obvious: `--ctx-size 200000` with `minContext: 150000` is a config that contradicts
itself, and nothing can tell which the operator meant.

Reading the floor from `--ctx-size` also matches what an operator already believes.
Someone who wrote `--ctx-size 200000` has *already said* they want 200k; making them
say it twice adds a way to be wrong without adding a way to be right.

**What this costs.** It removes the ability to say "shrink to 150k but no further"
— a floor *below* the configured context. That is a real capability, and it is
deliberately not offered: it is a third number whose only purpose is to be different
from the other two. If a genuine need appears, it can be added later against evidence
rather than speculation.

**What it means for the ladder.** For a declared model, `RungShrinkContext` is
skipped entirely rather than bounded — there is no room between the configured
context and the floor, because they are the same value. The ladder advances to the
next rung. This is simpler than the bounded variant the proposal assumed, and removes
the "floor above host capacity" case in D2's original framing: a configured context
above capacity is already `bound-max-safe-ctx`'s problem and is handled there.

## D3. Conflict semantics: warn, do not error

**Decision:** a `placement:` block alongside a raw placement flag emits a config
warning; the raw flag wins. Unchanged from the proposal, and revisited in Phase 4.

**Why.** An error is cleaner in principle and I nearly chose it. Against it: config
warnings in this repo are documented as "informational only, never enforced"
(`internal/config/warnings.go`), and the fit-guard work established that llama-skein
fails open — a model runs as configured unless refusing is *confidently* correct.
Turning a config into a hard failure over a redundancy the operator can see in their
own file is a bigger behavioural commitment than this change needs, and it can only
be walked back by breaking someone's config a second time.

Warn-first is also reversible in the right direction: Phase 4 adds migration, and once
an operator can convert a raw flag to a declaration in one action, upgrading the
warning to an error costs them nothing.

## D4. Scope of the `fit_level` change is #23's, not this change's

**Decision:** this change does not touch `fit_level`. Recorded here because the
proposal listed it as an open question and it needs an owner.

`flag-under-offloaded-models` (#23) owns the grading fix and its own task 14 already
tracks the `preloadFitRefusal` interaction. Splitting a single field's semantics
across two changes is how the original confusion arose. This change consumes
`fit_level`; it does not redefine it.

## What this means for Phase 1's tasks

Tasks 2–8 shrink. Task 2 adds a `Placement` struct with one field (`Reason`), task 3
adds two fit-report fields (`declared`, `reason`), and the `intent` and `minContext`
work moves wholly into Phase 2 — which is now a smaller, better-defined change than
the proposal assumed, because D2 removed a field from it rather than adding one.
