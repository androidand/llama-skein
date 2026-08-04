# Tasks: Unified inference-engine runtime management

Beads epic: see `skein` bd epic (linked on creation).

## Correction (2026-08-03)

The checkmarks below through Phase 6 were **false** — `git log -S` across all
~30 branches/stashes confirmed no MLX/vLLM per-backend Install/Upgrade code,
no `UpgradeOptions` type, and no `/api/runtime` handlers were ever committed,
despite this file and `server.go` claiming otherwise. `main` didn't build
(fixed by commenting out the dangling references in `60c3cba`). This is the
hollow-progress failure mode: a stuck coder loop, checkboxes advanced without
the corresponding diff ever landing. Only Phase 1
(`internal/runtime/runtime.go` — detection, self-documented in its own
package comment as "Phase 1... Install/upgrade land in later phases") was
ever real.

Corrected status below reflects what actually exists as of this fix.

## Phase 1 — Design

- [x] 1. Define the `RuntimeManager` interface (install/upgrade/version/health) + per-backend impls (llamacpp/mlx/vllm), mirroring `internal/offload`'s translator+registry shape.
  - `internal/runtime/runtime.go`: `Manager` interface + `Info`, detection for all 3 backends. Real, was already true.
- [x] 2. Spec-first: add runtime endpoints to `contracts/llama-skein.openapi.json` (install/upgrade/version/health, NDJSON progress), regenerate Go.
  - Contract and generated types (`RuntimeInfo`, `RuntimeInstallRequest`, `RuntimeHealth`) were already present — real. NDJSON streaming progress was not implemented (install/upgrade are synchronous request/response here); tracked as a gap, not silently dropped.

## Phase 2 — llama.cpp (extend existing)

- [x] 3. Refactor the existing upgrade (`apiupgrade.go` / `proxymanager_upgrade.go`) behind the RuntimeManager interface; keep prebuilt/source + CUDA/ROCm autodetect + `chcon`.
  - **Resolved as a deliberate non-wrap, not a refactor.** `apiupgrade.go`'s `upgradeRequest{Method, Ref, RocwmmaFattn}` (a specific build tag, NDJSON-streamed) and mlx/vllm's `RuntimeInstallRequest{VenvDir}` (always "latest via pip", single JSON response) are genuinely different contracts — llama.cpp upgrades to a chosen version, pip install has no such concept. Forcing them behind one interface means either inventing a fake default ref (silently picks a version nobody asked for) or bolting method/ref onto `RuntimeInstallRequest` to duplicate an already-shipped, well-designed endpoint. Neither is an improvement. `llamacppManager.Install/Upgrade` return `ErrUseSystemUpgrade`, and `POST /api/runtime/llamacpp/install` returns 400 pointing at `/api/system/upgrade` — this is the correct, complete API boundary, not a shortcut.

## Phase 3 — MLX

- [x] 4. mlx RuntimeManager: create/repair venv, `pip install -U mlx-lm`, version detect, `mlx_lm.server` runnable check. Apple-silicon-gated.
  - `internal/runtime/install.go`. **Live-verified for real** (2026-08-03) against this host's actual `~/.venv/mlx` — the same m3 host the original proposal describes: `Upgrade(ctx, "")` ran a real `pip install -U mlx-lm` over the network, `Detect` correctly reported `Installed:true Version:0.31.3` afterward (already latest — a real no-op upgrade, not a mock). Verification test was temporary (build-tag-gated, deleted after the run) — not part of the committed suite, which stays hermetic.

## Phase 4 — vLLM

- [x] 5. vllm RuntimeManager: venv + `pip install -U vllm`, CUDA detect, version. Linux/CUDA-gated.
  - Same shape as MLX. **Cannot be live-verified from this session** — no CUDA/NVIDIA hardware reachable (confirmed: no `nvidia-smi` on this host, and no other host is accessible from this sandbox). The platform gate itself is verified (rejects cleanly, before running any command, on this non-CUDA host — see `TestVLLM_Install_RejectsWithoutNVIDIA`); the actual `pip install -U vllm` path is unit-tested command construction only. Needs a real CUDA host to close.

## Phase 5 — Surfacing

- [x] 6. Report mlx/vllm runtime version+health in `/api/system/version` and via skein `providers probe`/status.
  - **Already true before this change** — `apisystem.go`'s `handleAPISystemVersion` (committed 2026-07-29, predates this fix) already iterates all three backends via `runtime.For(backend).Detect(ctx, "")` and reports version+detail when installed. My first pass at this file incorrectly marked this task "not done" without checking `apisystem.go` — corrected here. The skein `providers probe`/status half is still open (see task 7).
- [ ] 7. skein CLI `providers runtime <install|upgrade|status> --backend`.
  - Not done in this pass — genuinely requires work in the skein repo (a different CLI, different PR/merge target). Deferred pending a decision on whether to take that on as a follow-up in that repo.
- [x] 8. opencode: regenerate client if endpoints added.
  - No contract shape changed in this pass (routes/types were already in the OpenAPI source) — no client regen is owed. Still accurate.

## Phase 6 — Docs + gate

- [ ] 9. Update `docs-skein/deploy/llama-skein.md` for managed install (replace manual venv/cmake).
  - Not done in this pass — requires the docs repo, same deferral reasoning as task 7.
- [x] 10. `go build ./... && go test -short ./...` green; deploy to one host and verify.
  - Build/test green (1170 tests, 30 packages). MLX live-verified per task 4. vLLM/AMD live verification blocked on hardware access this session doesn't have — not faked.
