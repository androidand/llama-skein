# Model Capability Discovery

## Why

opencode-skein picks a model for a job, but has no way to ask what a model *is*.
Every model looks the same over `/v1/models`: an id, a context length, an output
limit. Whether it reasons before answering, whether it can call tools, whether it
accepts images — none of it is visible, so callers guess from the model name or
hard-code a choice.

This produced a real failure. opencode's compaction agent has no configured model,
so it falls back to whatever the user is chatting with
(`session/compaction.ts:339`). On rocky that was `muse-glimmer-30b-q5-k-m`, a
reasoning model. Compaction demands an exact Markdown structure ("Output exactly
the Markdown structure shown inside `<template>`"), and a model that spends its
first ~300 tokens thinking is a poor fit for a strict-format, non-creative
transformation. Measured on rocky: the model emits 900–1200 characters of
`reasoning_content` before its first content token, and under a tight output cap
returns `content: ""` with `finish_reason: length` — intermittently, which is the
worst kind of failure to diagnose.

The information needed to avoid this already exists. llama.cpp computes it at load
time and serves it on its own `/props`:

```json
"chat_template_caps": {
  "supports_preserve_reasoning": true,
  "supports_tool_calls": true,
  "supports_parallel_tool_calls": true,
  "supports_system_role": true,
  ...
},
"modalities": { "vision": true, "video": true, "audio": false }
```

llama-skein proxies the engine but does not surface any of it. This change closes
that gap.

## What Changes

- **A capability block per model.** `/v1/models` and `/api/models` gain a
  `capabilities` object: reasoning, tool calling, parallel tool calls, system-role
  support, and modalities. Sourced from the engine, not inferred from the model id.
- **Capabilities are available without loading the model.** A cold model must still
  report capabilities, or callers can only plan around models that happen to be
  resident. Values are cached per model file (keyed by path + mtime + size) and
  populated on first load; a never-yet-loaded model reports `capabilities: null`
  rather than a wrong guess.
- **A job-suitability hint.** Callers should not have to re-derive "is this a good
  compaction model" from primitives. Each model exposes `prefers_structured_output`
  (true when the model does not force reasoning), which is the single field a
  caller needs to pick a summarizer.

## Capabilities

### New Capabilities

- `model-capability-discovery`: engine-sourced per-model capability reporting, its
  cache, and the cold-model contract.

## Non-Goals

- **Not** a model *selector*. llama-skein reports what a model can do; choosing one
  for a job is the caller's decision. See `add-model-config-gallery` for shared
  defaults.
- **Not** benchmark-derived quality scoring. Capabilities are structural facts from
  the chat template, not judgements about how well a model performs.
- **Not** engine-agnostic on day one. llama.cpp exposes `chat_template_caps`
  directly; MLX and vLLM need their own probes and are staged behind
  `add-provider-runtime-inventory`. Backends without a probe report `null`, never a
  fabricated value.

## Impact

- `contracts/llama-skein.openapi.json` — `ModelCapabilities` schema; `capabilities`
  on the model objects. Spec first, then `go generate ./pkg/apicontract`.
- `internal/router/` — fetch and cache `/props` after a model reaches Ready.
- `internal/server/` — serve the cached block on the model endpoints.
- opencode-skein — regenerate the TypeScript client; use `prefers_structured_output`
  to pick the compaction model instead of inheriting the chat model.
