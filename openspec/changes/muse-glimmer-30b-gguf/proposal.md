# Run Muse Glimmer 30B as a multi-artifact model package

> **Labels.** This repo is public, so fleet hosts are named by capability rather
> than hostname; the mapping lives in the private companion repo
> (`docs-skein/fleet-labels.md`). Host A is a 24 GB RDNA3 workstation, host B a
> 48 GB RDNA3 host.

## Why

Muse Glimmer 30B (~29.6B params, `muse-glimmer` arch, 131k declared context, hybrid
local/global attention) ships on HuggingFace as a **package**, not a file: main
weights, a DFlash drafter for speculative decoding, and an `mmproj` projector for
vision. llama-skein originally treated a model as one GGUF, so it could neither
discover nor run the companions.

This change was researched in depth (`research-notes.md`, 400 lines of architecture,
quant comparison, and VRAM budgeting) but never carried a `proposal.md`, so OpenSpec
did not recognise it as a change and it was invisible to `openspec list` and to the
tracker. It is being promoted now so the remaining work is not lost — the research is
sound and most of it has since been delivered by other changes.

**Most of the original scope already shipped.** Verified in the tree 2026-08-14:

| Original requirement | Status | Evidence |
|---|---|---|
| 1. Package discovery + download | **delivered** | `host-model-management-api` §3–4; `TestExecutor_Run_MultiArtifactShardSetAndAuxiliary_HappyPath` |
| 2. Fit accounts for companions | **delivered** | `internal/fit/fit.go:162-163` — `DraftMB`, `ProjectorMB` |
| 3. Companion flags + spec type | **delivered** | `injectCompanionFlags`; `draft-mtp`/`draft-dflash` at `internal/server/apiconfig.go:240-251` |
| 4. Context-length override | **not delivered** | no `--override-kv` support anywhere in the tree |
| 5. Config stores companions | **delivered** | `ModelConfig.DraftModelPath` / `ProjectorPath` |

So the change is 4/5 done by other work, and what remains is one unimplemented
requirement plus one question the research could not have answered.

**The drafter does not currently earn its VRAM.** Muse Glimmer runs on host A today
with both companions attached. Measured (see `add-model-config-gallery`):
**34.5 tok/s with the DFlash drafter versus 34.6 without** — no gain, for 1.63 GB of
VRAM, and a `[spec] failed to measure draft model memory` warning at every load. The
research recommended attaching the drafter on the assumption it would help; on this
hardware, at this quant, it does not. That assumption is worth retiring explicitly
rather than leaving a 1.6 GB cost in place because a document once recommended it.

## What Changes

- **Context-length override.** Muse Glimmer's GGUF declares `context_length 131072`
  but the model supports 262144. Support `--override-kv` so the full context is
  reachable, including the drafter's own declared length
  (`muse-glimmer.context_length=int:262144,dflash.context_length=int:262144`).
- **Settle the drafter question with measurement, not assumption.** Either establish
  a configuration where DFlash pays for its 1.63 GB, or record that it does not on
  RDNA3 at this quant and stop attaching it by default. Fix or explain the
  `[spec] failed to measure draft model memory` warning either way.
- **Close out the delivered requirements** by pointing them at the changes that
  actually shipped them, so this change stops implying unbuilt work.

## Capabilities

### Modified Capabilities

- `muse-glimmer`: the package requirements, with delivered ones marked and the
  context override specified.

## Non-Goals

- **Not** automatic quantisation selection — the caller picks the variant.
- **Not** reimplementing llama.cpp's HF resolver — delegate where possible.
- **Not** general recipe extraction from HF config files.
- **Not** re-litigating the delivered requirements. They work; this change records
  that and moves on.

## Open Questions

- **Is `--override-kv` the right mechanism, or a model-config field?** A raw
  `--override-kv` string in `cmd` is another pinned mechanism of the kind
  `declare-placement-intent` (#24) argues against. A typed `context_override` may fit
  the contract better, and would survive an engine flag rename.
- **Does the 262k context fit anywhere in the fleet?** `research-notes.md` budgets
  ~31–33 GB at 262k without mmproj, which suits host B (48 GB) but not host A (24 GB).
  Worth confirming before building the override.
- **Is DFlash worth keeping at all?** If it shows no gain on any fleet host, the
  honest outcome is to stop attaching it and reclaim 1.63 GB.

## Impact

- `contracts/llama-skein.openapi.json` — context-override field, if typed.
- `internal/server/apiconfig.go` — override flag injection.
- `internal/fit/` — fit at an overridden context, not the declared one.
- Host A's Muse Glimmer entry — drop `--model-draft` if the measurement holds.
