# Tasks: Unified inference-engine runtime management

Beads epic: see `skein` bd epic (linked on creation).

## Phase 1 — Design
- [ ] 1. Define the `RuntimeManager` interface (install/upgrade/version/health) + per-backend impls (llamacpp/mlx/vllm), mirroring `internal/offload`'s translator+registry shape.
  - Validation: `go doc ./internal/runtime` (after stub)
- [ ] 2. Spec-first: add runtime endpoints to `contracts/llama-skein.openapi.json` (install/upgrade/version/health, NDJSON progress), regenerate Go.
  - Validation: `make check-codegen`

## Phase 2 — llama.cpp (extend existing)
- [ ] 3. Refactor the existing upgrade (`apiupgrade.go` / `proxymanager_upgrade.go`) behind the RuntimeManager interface; keep prebuilt/source + CUDA/ROCm autodetect + `chcon`.
  - Validation: `go test ./internal/server/... -run Upgrade`

## Phase 3 — MLX
- [ ] 4. mlx RuntimeManager: create/repair venv, `pip install -U mlx-lm`, version detect, `mlx_lm.server` runnable check. Apple-silicon-gated.
  - Validation: `go test ./internal/runtime/... -run MLX`

## Phase 4 — vLLM
- [ ] 5. vllm RuntimeManager: venv + `pip install -U vllm`, CUDA detect, version. Linux/CUDA-gated.
  - Validation: `go test ./internal/runtime/... -run VLLM`

## Phase 5 — Surfacing
- [ ] 6. Report mlx/vllm runtime version+health in `/api/system/version` and via skein `providers probe`/status.
  - Validation: `go test ./internal/server/... -run Version`
- [ ] 7. skein CLI `providers runtime <install|upgrade|status> --backend`.
  - Validation: `GOWORK=off go build ./...` in skein
- [ ] 8. opencode: regenerate client if endpoints added.
  - Validation: `bun run build:llama-skein-client`

## Phase 6 — Docs + gate
- [ ] 9. Update `docs-skein/deploy/llama-skein.md` for managed install (replace manual venv/cmake).
- [ ] 10. `go build ./... && go test -short ./...` green; deploy to one host and verify.
