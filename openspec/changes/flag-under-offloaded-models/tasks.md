# Tasks: flag-under-offloaded-models

Design-first: the contract changes before any handler. See `AGENTS.md` and the
`skein-dev` skill.

- [ ] 1. Resolve the three Open Questions in `proposal.md` before implementing —
       especially whether `fit_level` is redefined or a new field is added. Record
       the decision in a `design.md`. The `fit_level` answer determines tasks 6
       and 7.
       Validation: `design.md` exists and states the chosen option and why.

- [ ] 2. Contract: add `under_offloaded` (boolean, optional) to the `ModelFit`
       schema in `contracts/llama-skein.openapi.json`, with a description that
       states the condition (host-resident weights while full GPU residency fits)
       and that it is advisory. Do not hand-write the Go struct.
       Validation: `python3 -c "import json;print(json.load(open('contracts/llama-skein.openapi.json'))['components']['schemas']['ModelFit']['properties']['under_offloaded'])"`

- [ ] 3. Regenerate the Go contract per the `skein-dev` skill; commit the regen
       as its own step so the generated diff is reviewable.
       Validation: `grep -n "UnderOffloaded" pkg/apicontract/llama_skein.gen.go`

- [ ] 4. Derive `under_offloaded` in `internal/fit`: true when
       `host_resident_mb > 0` and the fully-resident weight bytes fit the VRAM
       budget at the configured ctx. Set `reason` to name the achievable
       placement. Follow the `under_configured` derivation as the model.
       Validation: `go test ./internal/fit/ -run TestFit_UnderOffloaded -v`

- [ ] 5. Detect the pinned-`-ngl` case specifically: when `-ngl` is below the
       GGUF offloadable layer count (`block_count + 1`) and the full model fits,
       the reason states the layer count the host can hold. Cover the host A case
       as a fixture: `block_count 65`, `-ngl 40`, 18630 MB weights, 24560 MB VRAM.
       Validation: `go test ./internal/fit/ -run TestFit_PinnedNglBelowLayerCount -v`

- [ ] 6. Remove the `fit_level` inversion per the task-1 decision: a model with
       avoidable host-resident weights must not grade above what its fully
       resident equivalent would earn. Add a regression test asserting the host A
       payload cannot score `"good"` while 7165 MB of weights sit in host RAM.
       Validation: `go test ./internal/fit/ -run TestFit_LevelNotInvertedByOffload -v`

- [ ] 7. Audit `fit_level` consumers for the behavioural change: this repo, plus
       opencode-skein `ctx-aware-subagent-placement` (`fit_level×1000`) and
       skein's sweeps. Record which need updating; open follow-ups rather than
       changing sibling repos here.
       Validation: findings recorded in `design.md`; `specsync note` per finding.

- [ ] 8. `internal/placement`: for `PinnedPlacement`, compute the plan that would
       have been chosen and populate `Estimate` + `PerfClass` on the `ModeCustom`
       result. Keep `FlagOps` empty so `Applies()` stays false — nothing is
       applied. Reason states better / equivalent / worse than the pinned flags.
       Validation: `go test ./internal/placement/ -run TestCompute_CustomReportsCounterfactual -v`

- [ ] 9. Emit one WARN on the model-load path when a model loads under-offloaded,
       and add the warning source to `configWarnings` (`internal/config/warnings.go`,
       wired at `internal/server/apiconfig.go:499`). One warning per load, not
       per request.
       Validation: `go test ./internal/server/ -run TestServer_UnderOffloadedWarning -v`

- [ ] 10. Surface the field in `internal/server/apifit.go` for both `/api/fit`
       and `/api/fit/{model}`.
       Validation: `go test ./internal/server/ -run TestServer_APIFit -v`

- [ ] 11. Repo validation per `AGENTS.md`: `gofmt -w` touched files,
       `make test-dev`, then `make test-all`. Fix every staticcheck error.
       Validation: `make test-all`

- [ ] 12. Verify against the live host A host, whose fit reports were captured
       2026-08-12 and are the acceptance fixture. Must flag:
       `M2` (host 6037 MB, 8.6 GB spare, today grades
       **`perfect`**) and `M3`
       (host 6166 MB, 5.5 GB spare, `tight`). Must **not** flag `M4`,
       `M5`, or `M6` — all `run_mode: gpu`, and the
       latter two are pinned at `-ngl 40` yet fully resident because 40 exceeds
       their layer count.
       Validation: recorded per-model verdict for every host A model pinning `-ngl`;
       the two false-negative models flag and the three correct ones do not.

- [ ] 12b. Extend the acceptance fixture beyond host A. Fleet audit 2026-08-12 found
       a placement pin on **every model on every host** — host B 4 × `-ngl 99`, host C
       1 × `-ngl 99` + 5 × `-ngl 999999` (repo copies; both hosts were unreachable, and
       host A's repo copy had drifted from live). `under_offloaded` must stay silent
       for all-layers pins on models that fit, or it fires fleet-wide on day one and
       gets ignored. Confirm live before treating these as fixtures.
       Validation: live pin inventory for host B and host C; no false positive on any
       all-layers pin whose model is fully resident.

- [ ] 12c. Handle engine-side `--fit` splits, which `host_resident_mb` cannot see.
       Measured on host A 2026-08-13: `M1` read `run_mode: "gpu"`,
       `host_resident_mb: 0`, `fit_level: marginal` at **both** 32.6 tok/s (pinned
       `-ngl 99`) and 14.7 tok/s (unpinned, `--fit` shed layers to hit the planner's
       22512 MB target). So the signal this change keys on is blind to the offload
       the planner itself causes. Decide whether `under_offloaded` can detect it at
       all — probably needs the engine's own layer report, not flag parsing — or
       document the blind spot explicitly rather than implying full coverage.
       Validation: a test asserting the blind spot is either closed or documented;
       no claim of detecting engine-side splits unless it actually does.

- [ ] 12d. Stop the delegated dense plan reporting `est_host_mb: 0`. For a plan that
       hands the split to `--fit`, the planner cannot know the outcome, so zero is a
       guess presented as fact — and it was wrong on `M1`. Report it as delegated
       and unknown.
       Validation: `go test ./internal/placement/ -run TestCompute_DelegatedEstimateNotZero -v`

- [x] 13. Fix the under-offloaded host A models. **Done 2026-08-12/13**, and the
       result corrected this change's own advice. `M2` 5.42 → 39.42 tok/s and `M3`
       4.04 → 35.05 tok/s by *removing* the pin (planner then reported
       `applied: true`, full residency). But `M1` **regressed 32.6 → 14.7 tok/s**
       when unpinned, because its 139264 ctx does not fit the planner's 22512 MB
       target and `--fit` shed layers; reducing ctx to 110592 did not restore it
       either. `M1` was restored to `-ngl 99` at 139264 ctx, re-measured at
       32.64 tok/s, and is the motivating case for #24: it needs a way to say
       "spend the reserve on layers" that does not also disable everything else.
       So "prefer removing the pin" is conditional on the model fitting the
       *reserved* budget — see the proposal's correction section.

- [ ] 14. Decide what `preloadFitRefusal` (`internal/server/fitguard.go:182`)
       reads. It refuses startup preload for exactly `fit_level == marginal`, so
       making `fit_level` placement-aware silently changes which models preload —
       an under-offloaded model capped at its fully-resident grade lands on
       `marginal` and stops preloading. Either key the guard on the headroom grade
       (preserving today's behaviour) or accept the new behaviour deliberately.
       Not a side effect to discover later.
       Validation: a test asserting preload behaviour for an under-offloaded model
       whose fully-resident equivalent grades `marginal`, with the chosen
       semantics stated in the test name.

- [x] 15. Confirm no shipped caller ranks on `fit_level` ordinally before
       redefining it. Survey as of 2026-08-12: skein's `llm.HypotheticalFit.Fits()`
       (`internal/llm/client.go:297`) is binary over `perfect|good|tight|marginal`;
       opencode-skein's `provider.ts` deliberately does not gate on it.
       **Resolved:** the ordinal ranking is real and live, in opencode-skein rather
       than skein's Go — `packages/opencode/src/local/placement.ts:81-86` defines
       `FIT_RANK` (`perfect:4 good:3 tight:2 marginal:1 no:0`) and line 189 scores
       `rank * 1_000`. The claim stands; the citation is now exact.

- [ ] 16. Fix `perf_class` for `ModeCustom` (`placement.go:153`), which returns
       `PerfNativeGPU` unconditionally. This is the highest-severity item: it
       disables opencode-skein's `HOST_PACED_PENALTY`, whose `isHostPaced()`
       (`packages/opencode/src/local/placement.ts:104-107`) keys on `perf_class`
       being `cpu-bound-hybrid`/`cpu-only`. Every pinned model claims `native-gpu`,
       so the penalty never fires for the configuration that most reliably produces
       CPU-bound models. Verified live on host A: `perf_class: "native-gpu"` while
       `run_mode: "cpu_offload"` with 7165 MB host-resident at 1.2 tok/s.
       Validation: `go test ./internal/placement/ -run TestCompute_CustomPerfClassReflectsSplit -v`;
       and the host A fixture reports a host-paced `perf_class`.

- [ ] 17. Add a removal path for `n_gpu_layers` to the contract, per the
       `config-management` delta. `patchModelInConfig` (`apiconfig.go:793-798`) maps
       every value into the flag map and can only overwrite; `0` would write
       `--n-gpu-layers 0` (all layers on CPU). This change's own recommended remedy
       is not expressible through the API today — verified 2026-08-12, when removing
       the two host A pins required patching the whole `cmd` string instead.
       Validation: `go test ./internal/server/ -run TestServer_PatchModelRemovesNgl -v`

- [ ] 18. Stop `GET /api/models/config/{id}` returning `${PORT}` pre-resolved.
       Observed on host A: stored `--port ${PORT}` returned as `--port 5803`, so a
       read-modify-write through the API hardcodes a dynamically allocated port and
       silently breaks the model later. Expose the resolved form as a separate field
       if needed (`effective_flags` is the precedent).
       Validation: `go test ./internal/server/ -run TestServer_GetModelConfigPreservesPortPlaceholder -v`

- [ ] 19. Coordinate with opencode-skein before shipping tasks 6 and 16 together:
       `HOST_PACED_PENALTY` is a client-side workaround for the `fit_level`
       inversion. Once `fit_level` is placement-aware *and* `perf_class` is honest,
       an under-offloaded model is penalised twice. Decide which layer owns the
       penalty and record it; do not change the client from this repo.
       Validation: decision recorded in `design.md`; follow-up noted on
       opencode-skein `per-model-placement-controls`.
