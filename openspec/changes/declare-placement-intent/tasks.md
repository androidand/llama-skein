# Tasks: declare-placement-intent

Design-first: the contract changes before any handler. See `AGENTS.md` and the
`skein-dev` skill.

**Phase 1 (tasks 2–8) is independently shippable and changes no planner
behaviour.** Ship it first; it delivers the declared/undeclared distinction that
#23's warning needs. Later phases can slip without blocking it.

- [ ] 1. Resolve the four Open Questions in `proposal.md` before writing schema.
       Two change Phase 1's shape: whether `intent` ships in Phase 1 at all (an
       enum with no behaviour attached risks values that later mean something
       else), and whether `minContext` is restated or read from the model's
       existing `--ctx-size`. Record in `design.md`.
       Validation: `design.md` states each decision and why.

## Phase 1 — schema and reporting (no behaviour change)

- [ ] 2. Add `Placement` to `ModelConfig` (`internal/config/model_config.go`)
       following the `Timeouts`/`Filters` nested-struct precedent. Fields per the
       task-1 decision; `reason` and `declared` are the minimum.
       Validation: `go test ./internal/config/ -run TestModelConfig_Placement -v`

- [ ] 3. Contract: add `declared` (boolean) and `reason` (string, optional) to the
       fit report's placement object in `contracts/llama-skein.openapi.json`.
       Never hand-write the Go struct.
       Validation: `python3 -c "import json;d=json.load(open('contracts/llama-skein.openapi.json'));print(d['components']['schemas']['PlacementReport']['properties'].keys())"`

- [ ] 4. Regenerate the Go contract per the `skein-dev` skill; commit the regen as
       its own step so the generated diff is reviewable.
       Validation: `grep -n "Declared" pkg/apicontract/llama_skein.gen.go`

- [ ] 5. Thread `declared` through `placement.Compute`: a model with a `placement:`
       block reports `declared: true`; a raw flag with no block reports
       `declared: false`. **`Mode` and `FlagOps` are unchanged in this phase** — a
       declared model with pinned flags still returns `ModeCustom` and still applies
       nothing.
       Validation: `go test ./internal/placement/ -run TestCompute_Declared -v`

- [ ] 6. Emit a config warning when a `placement:` block and a raw placement flag
       both exist. Per task 1, the raw flag wins (preserving "explicit flags always
       win") and the conflict is reported. Add to `configWarnings`
       (`internal/server/apiconfig.go:499`).
       Validation: `go test ./internal/server/ -run TestServer_PlacementConflictWarning -v`

- [ ] 7. Surface `declared` and `reason` in `internal/server/apifit.go` for both
       `/api/fit` and `/api/fit/{model}`.
       Validation: `go test ./internal/server/ -run TestServer_APIFitDeclared -v`

- [ ] 8. Phase 1 validation per `AGENTS.md`: `gofmt -w` touched files,
       `make test-dev`, then `make test-all`. Fix every staticcheck error.
       Confirm against host A that all six models report `declared: false` (none has
       a block yet) and that the two still carrying `-ngl 40` are unaffected.
       Validation: `make test-all`; host A reports `declared: false` for all six.

## Phase 2 — intent semantics

- [ ] 9. Implement `intent: latency`: refuse rather than plan a placement with any
       host-resident weights. Must respect the existing fail-open rule — an
       unplannable model still runs as configured, and only a confident refusal
       refuses.
       Validation: `go test ./internal/placement/ -run TestCompute_IntentLatency -v`

- [ ] 10. Implement `intent: context` with `minContext` as a hard floor: bound
       `RungShrinkContext` (`internal/placement/ladder.go:29`) so retry never
       shrinks below the floor, and the ladder moves to the next rung instead of
       violating it.
       Validation: `go test ./internal/placement/ -run TestLadder_ContextFloorBounded -v`

- [ ] 11. Verify the floor interacts correctly with `bound-max-safe-ctx`: a
       declared `minContext` above what the host can serve must be refused or
       reported, never silently advertised as achievable via `max_safe_ctx`. This is
       the exact confusion `bound-max-safe-ctx` closed — do not reopen it.
       Validation: `go test ./internal/fit/ -run TestFit_DeclaredFloorAboveCapacity -v`

## Phase 3 — the KV rung (separable)

- [ ] 12. Add per-model `allowKvQuantization`, overriding the policy default
       (`internal/config/placement.go:35`, default `false`).
       Validation: `go test ./internal/config/ -run TestPlacement_PerModelKvOverride -v`

- [ ] 13. Insert a KV-quantization rung into `ladderOrder` **ahead of**
       `RungFullCpuMoe`, reachable only when the operator opted in. Rationale to
       carry in the code comment: KV quantization pays once in quality, layer
       offload pays on every token forever — measured at 7–30× on host A.
       Validation: `go test ./internal/placement/ -run TestLadder_KvRungBeforeCpuMoe -v`

- [ ] 14. Confirm the rung never fires without opt-in, preserving the existing
       "KV quality is never traded silently" guarantee.
       Validation: `go test ./internal/placement/ -run TestLadder_KvRungRequiresOptIn -v`

## Phase 4 — migration

- [ ] 15. Add a patch path that writes the `placement:` block, and confirm it
       supersedes #23 task 17: removing a pin becomes deleting a field, not
       patching a `cmd` string. Verify it also sidesteps #23 task 18 — no `cmd`
       round-trip means no `${PORT}` corruption.
       Validation: `go test ./internal/server/ -run TestServer_PatchPlacementBlock -v`;
       a round-trip test proving `${PORT}` is untouched.

- [ ] 16. Detect a legacy raw placement flag and report the equivalent declaration
       the operator could adopt. **Report only — never rewrite the config.**
       Validation: `go test ./internal/server/ -run TestServer_SuggestsDeclaration -v`

- [x] 17. Check whether the add-model path writes `-ngl` into new entries.
       **Resolved 2026-08-12 — not generative in the shipping binary.**
       `internal/server.buildCmd` (`modelhelpers.go:322-337`) emits only
       `llama-server --port ${PORT} --model <path>` plus caller flags, and its doc
       comment records this exact bug already being fixed (the deepseek
       `--n-cpu-moe 25 --ctx-size 32768` incident). Live paths are clean:
       `apimodeloperations.go:221` passes `flags := ""` and resolves companions from
       the operation's own artifacts; `apipull.go:326` passes operator-supplied
       flags, which is a legitimate explicit choice. No generator fix needed.

- [ ] 18. Delete or quarantine the pre-fix `buildCmd` at
       `proxy/proxymanager_config.go:526-572`, which retains both original defects:
       it clones the template model's **entire** flag set (the comment claims
       "everything up to (and including) `--model`" but the loop copies every flag,
       including `--model-draft` and `--mmproj` pointing at an unrelated model's
       companion files), and falls back to `--n-gpu-layers 99`. Unreachable today —
       `proxy.New` is constructed only in `cmd/legacy/llama-skein.go`, which the
       Makefile does not build — but a landmine for anyone reviving or copying it.
       Confirm no shipping route reaches it before deleting.
       Validation: `grep -rn "pm.buildCmd" --include="*.go" .` returns nothing, or
       the function is gone; `make test-all` passes.

- [ ] 19. Confirm the fleet-wide pin audit against the **live** hosts before acting.
       Repo copies (`the private companion repo's config/`) show host A 6 × `-ngl 40`, host B
       4 × `-ngl 99`, host C 1 × `-ngl 99` + 5 × `-ngl 999999` — i.e. `auto` is
       disabled for every model on every host. host B and host C were unreachable during
       the investigation and host A's repo copy had drifted from live, so these
       figures need confirming on the hosts. Note `99`/`999999` are not harmless:
       they still disable hybrid placement, the `--fit-target` reserve, and the retry
       ladder, so `add-auto-hybrid-placement` can never engage on these hosts.
       Validation: per-host live pin inventory recorded, with drift from the repo
       copies noted.

- [ ] 20. Full validation and a live host A acceptance run: declare `intent: custom`
       on one of the two models still pinned at `-ngl 40`, confirm it reports
       `declared: true` and that #23's `under_offloaded` stays silent for it while
       still firing for an undeclared equivalent.
       Validation: `make test-all`; the declared model warns not, the undeclared does.
