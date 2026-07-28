# Tasks: Add Instella-MoE model configuration and deployment

Target repo: **llama-skein** (this repo) + `~/dev/docs-skein/config/z4/`.
Blocked by `add-instella-moe-correctness-harness` Phase 4 — **do not deploy an unvalidated
GGUF.**

Reference: `docs/investigations/instella-moe-w7800.md` §10 (integration), §7 (ROCm), §8
(VRAM).

## Phase 1 — binary layout (non-destructive)
- [ ] 1. Install the custom build to `/opt/llamacpp-instella/` with the server binary named
      **`llama-server-instella`**. The rename is mandatory — see the proposal, Collision 1.
- [ ] 2. Verify `pgrep -a llama-server` does **not** match it while it is running.
- [ ] 3. Record the fork commit SHA and upstream base SHA in
      `/opt/llamacpp-instella/PROVENANCE` and in this file.
- [ ] 4. Confirm `/opt/llamacpp-rocm-gfx110X/` is untouched: checksum the existing
      `llama-server` and its `.so` set before and after.
- [ ] 5. Regression-test the incumbent: load `qwen3.6-35b-a3b-q8-0` and confirm normal
      generation, proving the existing setup still works.

## Phase 2 — quantize
- [ ] 6. Produce Q8_0 from the validated F16 GGUF. Expected ~16,083 MiB (15.71 GiB).
- [ ] 7. Record the Q8_0-vs-F16 perplexity delta (harness test 9). Report; do not gate.
- [ ] 8. Place at `/models/Instella-MoE-16B-A3B-Think-Q8_0.gguf`. **Do not overwrite any
      existing model file.** Confirm free space before writing.
- [ ] 9. Skip Q6_K. The investigation established the Q8-vs-Q6 tradeoff does not exist for
      this model: at Q8_0 the theoretical max context is ~1.01 M tokens against the model's
      own 32,768 ceiling, so Q6_K buys headroom that cannot be spent.

## Phase 3 — config entry (hand-written YAML)
- [ ] 10. Add to `/etc/llama-skein/config.yaml` on z4:
      ```yaml
      instella-moe-16b-a3b-think-q8-0:
        name: "AMD Instella-MoE 16B-A3B Think (Q8_0) — RESEARCH ONLY"
        description: "ResearchRAIL license. Evaluation only, not for production work."
        unlisted: true
        sendLoadingState: false
        env:
          - LD_LIBRARY_PATH=/opt/llamacpp-instella
        cmd: >
          /opt/llamacpp-instella/llama-server-instella --port ${PORT}
          --model /models/Instella-MoE-16B-A3B-Think-Q8_0.gguf
          --ctx-size 32768 --n-gpu-layers 99 --flash-attn off --jinja
        proxy: http://localhost:${PORT}
      ```
- [ ] 11. Set `reasoning:` only if harness task 10 confirmed the model actually emits a
      reasoning trace. It is advertise-only metadata (`internal/server/api.go:69-71`) and
      there is **no `<think>` token in the vocabulary** and no `<think>` handling anywhere
      in this repo — so claiming it falsely just misleads clients.
- [ ] 12. Keep `sendLoadingState: false`: llama-skein synthesizes its own
      `reasoning_content` for loading messages (`proxy/process.go:1213-1226`,
      `internal/router/loading.go:281-301`), which would interleave with a real trace.
- [ ] 13. Do **not** add it to any group, and do **not** make it `defaultModel`.
- [ ] 14. Mirror the config into `~/dev/docs-skein/config/z4/config.yaml` and commit —
      that directory is the declared source of truth.
- [ ] 15. `POST /api/config/reload`; confirm the incumbent models still load.
- [ ] 16. Check `GET /api/fit` reports a sane `fit_level` for the new entry and that
      `fitguard` did **not** rewrite `--ctx-size` away from 32768. If it did, capture it —
      that is a fit-engine bug worth its own change.

## Phase 4 — benchmark (no fabricated numbers)
- [ ] 17. **First, measure the incumbent baseline** `qwen3.6-35b-a3b-q8-0`. The
      investigation has VRAM figures from `/api/fit` but **no throughput measurement at
      all**, so no comparison is possible until this exists.
- [ ] 18. For both models: prompt lengths 1K / 4K / 8K / 16K (and 32K for Instella only if
      the baseline's configured ctx allows a fair point), generation lengths 128 and 512.
- [ ] 19. Record: prompt-processing tok/s, generation tok/s, peak VRAM, host RAM, GPU
      utilization and memory-controller activity (the healthy-vs-wedged signal from the
      `z4-wedge-rootcause` work), cold-load time, unload behaviour, reload behaviour.
- [ ] 20. Run Instella at Q8_0 and one lower quant (Q4_K_M, ~9,183 MiB) for a
      quality/speed point.
- [ ] 21. Stability: 30+ consecutive requests, then an idle-unload/reload cycle. Watch for
      the wedge signature (GPU pinned ~100% with memory-controller activity stuck at
      11–14%).
- [ ] 22. Qualitative comparison on coding, instruction-following and long-context
      behaviour — clearly labelled as subjective, with the prompts recorded.
- [ ] 23. Write results into `docs/investigations/instella-moe-w7800.md` and update §15's
      recommendation if the measurements contradict it.

## Phase 5 — follow-ups to file separately
- [ ] 24. llama-skein: explicit `serverPath`/`enginePath` config key feeding
      `opts.ServerPath`, so upgrades stop resolving destinations by `pgrep`.
- [ ] 25. llama-skein: wire the MoE-aware math in `pkg/gguf/offload.go:75-119` into the fit
      path so CPU-offloaded experts are not charged against VRAM.
- [ ] 26. llama-skein: per-model tuning opt-out.
- [ ] 27. llama.cpp: report [#24906 ROCm incorrect free VRAM](https://github.com/ggml-org/llama.cpp/issues/24906)
      impact on the fit engine, or work around it locally.
- [ ] 28. Refresh the stale `config-schema.json` (missing `reasoning`, `backend`,
      `maxRequestTimeSecs`, `healthCheckTimeout`, `tuning`, `memoryGuard`, `wedgeWatchdog`,
      `swapQueueTimeoutSecs`, `modelsDir`, `silentMode`, `profiles`).
