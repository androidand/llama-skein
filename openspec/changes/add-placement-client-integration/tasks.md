# Tasks: Placement-aware clients (opencode + skein)

Supersedes `add-auto-hybrid-placement` tasks 18–19, which were deferred.
Consumes the contract already pushed on `skein/auto-hybrid-placement`; no
llama-skein changes here.

## Phase 1 — opencode (TypeScript)
- [x] 1. Regenerate the typed client from the updated spec
  (`bun run build:llama-skein-client`), confirming the placement fields and
  the new `ModelFit` fields appear in `src/local/llama-skein/gen`.
  - Validation: `bun run typecheck` in `packages/opencode`
- [x] 2. Local routing reads placement: host-paced placements
  (`cpu-bound-hybrid`, `cpu-only`) rank below GPU-resident models rather than
  being excluded, so a large model stays reachable when it is the only
  option.
  - Validation: unit test over the scoring function with synthetic candidates
- [D] 3. Surface the placement wherever local model status is already shown.
  Deferred: the TUI carries a SECOND, hand-synced copy of the generated
  client (`packages/tui/src/local/llama-skein/gen`) that is already stale and
  has no regeneration script, so a placement badge needs that copy fixed
  first — a separate concern from routing.

## Phase 2 — skein (Go)
- [x] 4. Pin `github.com/androidand/llama-skein` to the placement commit and
  tidy, so `pkg/apicontract` carries the placement types.
  - Validation: `GOWORK=off go build ./...`
- [x] 5. Provider/model selection consumes placement with the same
  degrade-preference rule as opencode.
  - Validation: `GOWORK=off go test ./internal/providers/...`
- [x] 6. Verify the context-fit sweep still behaves against a hybrid model.
  It reads `max_fit_ctx`/`configured_ctx`/`under_configured` only, all of
  which now describe the GPU share — the correct budget for a KV cache that
  stays on the card. Suite passes unchanged.
  - Validation: `GOWORK=off go test ./internal/supervisor/...`

## Phase 3 — Gate
- [x] 7. Both repos build and test clean. skein's `replace` still points at
  `../llama-skein`, so its branch builds once the llama-skein work lands on
  `main` there (validated against the worktree meanwhile).
  - Validation: opencode `bun run typecheck`; skein `GOWORK=off go build ./... && go test -short ./...`
