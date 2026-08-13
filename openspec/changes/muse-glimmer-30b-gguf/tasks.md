# Tasks: muse-glimmer-30b-gguf

Promoted to a real OpenSpec change 2026-08-14. It had only `research-notes.md` and
a non-delta `specs/` file, so OpenSpec did not see it and it never reached the
tracker. Four of its five original requirements have since been delivered by other
changes; those are recorded as done with their evidence.

## Delivered by other changes (verified in-tree 2026-08-14)

- [x] 1. Package discovery and download of main + drafter + projector.
       Delivered by `host-model-management-api` §3–4 (multi-artifact resolution and
       resumable install), covered by
       `TestExecutor_Run_MultiArtifactShardSetAndAuxiliary_HappyPath` and
       `TestExecutor_Run_AbortsTheWholeSetAndNeverRegistersWhenOneArtifactFails`.

- [x] 2. Fit accounts for companion weights.
       `internal/fit/fit.go:162-163` carries `DraftMB` and `ProjectorMB`.

- [x] 3. Runtime command carries companion flags and the right spec type.
       `injectCompanionFlags` emits `--model-draft` / `--mmproj`;
       `internal/server/apiconfig.go:240-251` distinguishes `draft-mtp` from
       `draft-dflash`.

- [x] 5. Model config records companion paths across restarts.
       `ModelConfig.DraftModelPath` and `ProjectorPath`.

## Remaining

- [ ] 4. Context-length override. Muse Glimmer declares `context_length 131072` but
       supports 262144, and no `--override-kv` support exists anywhere in the tree.
       Resolve the first Open Question before building: a raw `--override-kv` string
       in `cmd` is exactly the pinned-mechanism pattern `declare-placement-intent`
       (#24) argues against, so a typed field may fit the contract better.
       The drafter's own declared length needs overriding too
       (`dflash.context_length`), or speculative decoding caps the effective window.
       Validation: a model with the override reports the overridden `configured_ctx`
       on `/api/fit`, and serves a prompt beyond 131072.

- [ ] 6. Confirm 262k context fits a real host before building the override.
       `research-notes.md` budgets ~31–33 GB at 262k without mmproj: plausible on
       host B (48 GB), impossible on host A (24 GB). If no fleet host can serve it,
       the override is theoretical and should be deferred rather than built.
       Validation: a per-host fit report at 262k recorded for each reachable host.

- [ ] 7. Settle whether the DFlash drafter earns its VRAM. Measured on host A:
       **34.5 tok/s with the drafter versus 34.6 without**, costing 1.63 GB — no
       gain. Either find a configuration where it pays (larger batch, different
       quant, host B) or stop attaching it by default and reclaim the VRAM. The
       research recommended it on assumption; the measurement disagrees.
       Validation: before/after tok/s on at least two hosts, and a recorded decision.

- [ ] 8. Fix or explain the `[spec] failed to measure draft model memory` warning
       emitted at every load of this model. Either the drafter's memory is
       measurable and the fit report should include it, or it is not and the warning
       should say why rather than repeating on every load.
       Validation: warning is gone, or documented with its cause.

- [ ] 9. If task 7 concludes the drafter does not pay, drop `--model-draft` from
       host A's Muse Glimmer entry and re-measure to confirm the reclaimed 1.63 GB
       shows up as free VRAM.
       Validation: `/api/fit` shows the reduced requirement; throughput unchanged.
