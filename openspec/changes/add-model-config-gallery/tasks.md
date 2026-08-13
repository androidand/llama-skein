# Tasks: Model Config Gallery

## 1. Research — resolve the open questions first

These gate the format. Answering them after implementation means a migration.

- [ ] 1.1 **Identity.** Decide the lookup key. Test the failure case: two unsloth requants
      published under the same filename with different content. Proposal to evaluate —
      file hash for exact match, base-model id for neighbour search. Verify: decision
      recorded with the rejected alternatives and why.
- [ ] 1.2 **Comparability.** Define when another host's entry applies here. Establish
      empirically whether offload counts transfer across same-arch/different-VRAM (compare
      host A gfx1100 24 GB against host B). Verify: a written rule, backed by measurements.
- [ ] 1.3 **Staleness.** Measure whether an engine upgrade materially changes throughput
      for a fixed config. Re-measure M4 on host A across two builds. Verify: data
      supports either invalidating or downranking; pick one.
- [ ] 1.4 **Prior art.** Survey what already exists — llama.cpp discussions, LocalLLaMA
      config posts, Ollama Modelfiles, LM Studio presets, `llmfit` in odysseus. Verify:
      a written comparison; adopt an existing format if one fits rather than inventing one.
- [ ] 1.5 **Sharing.** Decide whether a shared index is in scope at all, and if so its
      trust and privacy model. Verify: explicit go/no-go. Default is no-go for this change.

## 2. Entry format

- [ ] 2.1 Add `GalleryEntry` to `contracts/llama-skein.openapi.json` per the spec's field
      list. Verify: `make check-codegen` clean.
- [ ] 2.2 Add `ConfigRecommendation` with `match` (`exact`/`near`/`computed`) and a
      `differences` list. Verify: schema validates.

## 3. Local store

- [ ] 3.1 Implement `internal/gallery/` with a file-backed store beside the config.
      Verify: round-trip test.
- [ ] 3.2 Entries keyed per the 1.1 decision; support exact and neighbour lookup.
      Verify: table test over exact, near, and no-match.

## 4. Capture

- [ ] 4.1 Record per-completion throughput from the engine's reported timings.
      Verify: unit test over a recorded llama.cpp timings payload.
- [ ] 4.2 Aggregate into an entry once the sampling threshold is met; record sample count.
      Verify: test asserts no entry below threshold.
- [ ] 4.3 Reset accumulation when a model's arguments change. Verify: test asserts
      measurements are not carried across an argument change.
- [ ] 4.4 Record peak VRAM against the run. Verify: on host A, a captured entry reports
      ~19.7 GB for M4 at `--n-gpu-layers 99`.

## 5. Endpoints

- [ ] 5.1 `GET /api/gallery/{model}` returning a recommendation with match quality.
      Verify: `go test ./internal/server/ -run Gallery`.
- [ ] 5.2 `GET /api/gallery/viable` listing models runnable on this hardware.
      Verify: on host A, M4 appears with expected throughput.
- [ ] 5.3 `POST /api/gallery/entries` to import an entry. Verify: imported entries are
      marked with their provenance and never as locally measured.
- [ ] 5.4 Recommendations never mutate a running config. Verify: test asserts arguments
      are unchanged after a recommendation is fetched.

## 6. Seed data

- [ ] 6.1 Capture entries for every model currently configured on host A and host B, so the
      gallery starts with real data rather than empty. Verify: entries committed to this
      change's directory as the initial data set.
- [ ] 6.2 Record the M4 findings explicitly: `ngl 40` → 6.1 tok/s vs `ngl 99` →
      34.5 tok/s, and draft-model 34.5 vs 34.6 without. These are the worked example that
      motivated the change.

## 7. Verification

- [ ] 7.1 `go build ./... && go vet ./... && go test -short ./internal/...` clean.
- [ ] 7.2 End-to-end on host A: serve traffic, confirm an entry is captured, confirm the
      recommendation for M4 matches the measured `ngl 99` configuration.
