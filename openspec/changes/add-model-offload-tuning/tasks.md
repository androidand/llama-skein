# Tasks: Per-model CPU / MoE offload tuning

Beads epic: `skein-mdi0` (children `skein-mdi0.1`–`.6`).

## Phase 1 — llama-skein contract (spec-first)
- [ ] 1. Add offload fields to `ConfigModelPatchRequest` + `ConfigModelRequest` and read-back fields to `Model` in `contracts/llama-skein.openapi.json`; add `GET /api/models/offload/{model}` + `OffloadRecommendation` schema.
  - Validation: `python3 -c "import json;json.load(open('contracts/llama-skein.openapi.json'))"`
- [ ] 2. Regenerate Go types.
  - Validation: `go generate ./pkg/apicontract && gofmt -w pkg/apicontract/llama_skein.gen.go && make check-codegen`

## Phase 2 — Offload translator
- [ ] 3. Create `internal/offload` package: `OffloadSpec`, `OffloadTranslator`, llamacpp/vllm/mlx translators, registry.
  - Validation: `go test ./internal/offload/...`

## Phase 3 — GGUF tensor table + recommendation
- [ ] 4. Extend `pkg/gguf/gguf.go` to read the tensor-info section; add `ExpertWeightBytes`/`ExpertWeightBytesPerLayer` (dimensional fallback) and `RecommendCpuMoe(freeBytes, ctxLen)`.
  - Validation: `go test ./pkg/gguf/...`

## Phase 4 — Handlers + read-back
- [ ] 5. Translate offload fields in PATCH + add-model handlers (`internal/server/apiconfig.go`) via the translator into the model `cmd` (reuse `patchCommandFlags`).
  - Validation: `go test ./internal/server/... -run Offload`
- [ ] 6. Add `handleAPIOffloadRecommendation` (`internal/server/apimodels.go`) + register route.
  - Validation: `go test ./internal/server/... -run Offload`
- [ ] 7. Surface current offload settings on `/v1/models` (sibling to `addModelRuntimeHints`).
  - Validation: `go test ./internal/server/... -run Offload`

## Phase 5 — Gate + push
- [ ] 8. `go build ./... && go test -short ./... && make test-dev`; push origin; record commit.
  - Validation: `go build ./... && go test -short ./...`

## Phase 6 — Clients
- [ ] 9. opencode: regen client + `setModelOffload` endpoint/handler (mirror `setModelCtxSize`).
  - Validation: `cd ~/dev/opencode/packages/opencode && bun run build:llama-skein-client`
- [ ] 10. skein: re-pin + consume read-back/recommendation via `internal/provider/llamaswap`.
  - Validation: `GOWORK=off go build ./... && go test -short ./...`
