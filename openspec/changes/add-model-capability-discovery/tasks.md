# Tasks: Model Capability Discovery

## 1. Research and validate the signal

- [ ] 1.1 Confirm `chat_template_caps.supports_preserve_reasoning` actually separates
      reasoning from non-reasoning models. Probe `/props` for at least six models across
      families (muse-glimmer, qwen3.6, qwopus, qwythos, instella, deepseek) and record
      the field against observed behaviour (does `reasoning_content` appear?).
      **This gates the design** — if the field does not correlate, the capability must be
      derived differently (e.g. a one-shot generation probe) before anything is built.
- [ ] 1.2 Record, for each probed model, how many tokens precede the first content token.
      Feeds the `add-model-config-gallery` defaults. Verify: results table committed in
      this change's directory.
- [ ] 1.3 Determine whether MLX and vLLM expose an equivalent. If not, note what a probe
      would cost. Verify: findings written up; no implementation in this change.

## 2. Contract

- [ ] 2.1 Add `ModelCapabilities` to `contracts/llama-skein.openapi.json`: `reasoning`,
      `tool_calls`, `parallel_tool_calls`, `system_role`, `modalities{vision,audio,video}`.
      Nullable. Verify: spec validates.
- [ ] 2.2 Add `capabilities` and `prefers_structured_output` to the model schemas used by
      `/v1/models` and `/api/models`. Both nullable. Verify: `make check-codegen` clean
      after `go generate ./pkg/apicontract`.

## 3. Probe and cache

- [ ] 3.1 Fetch `/props` once a llama.cpp model reaches Ready; map `chat_template_caps`
      and `modalities` onto `ModelCapabilities`. Verify: unit test with a recorded
      `/props` payload asserts the mapping.
- [ ] 3.2 Cache keyed by (path, size, mtime); invalidate when any component changes.
      Verify: test asserts a changed mtime forces a re-probe.
- [ ] 3.3 Persist the cache across restarts so a cold model reports capabilities after a
      service restart. Verify: test round-trips the cache.
- [ ] 3.4 Probe failure and unimplemented backends yield `null`, log once, and never fail
      the listing. Verify: test with an erroring probe asserts the model still lists.

## 4. Serve

- [ ] 4.1 Populate `capabilities` and `prefers_structured_output` on both model endpoints.
      Verify: `go test ./internal/server/ -run Capabilit`.
- [ ] 4.2 `prefers_structured_output` is `false` when `reasoning` is true, `null` when
      capabilities are unknown. Verify: table test covers all three states.

## 5. Client integration

- [ ] 5.1 Regenerate the opencode TypeScript client
      (`bun run build:llama-skein-client`). Verify: types include the new fields.
- [ ] 5.2 In opencode, default the `compaction` agent's model to a provider model with
      `prefers_structured_output: true`, preferring the current provider before crossing
      providers. Falls back to today's behaviour when nothing qualifies.
      Verify: compaction on a rocky session no longer selects muse-glimmer.
- [ ] 5.3 Same treatment for the `title` agent — also a strict-format, short-output job.

## 6. Verification

- [ ] 6.1 `go build ./... && go vet ./... && go test -short ./internal/...` clean.
- [ ] 6.2 End-to-end on rocky: list models cold, confirm cached capabilities; load
      muse-glimmer, confirm `reasoning: true` and `prefers_structured_output: false`.
      Record commands and output here.
