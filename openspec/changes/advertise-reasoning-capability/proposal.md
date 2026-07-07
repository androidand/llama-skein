# Proposal: Advertise reasoning capability in /v1/models

## Why

Reasoning models (that stream `reasoning_content` before the answer) look
"not responding" in opencode: opencode auto-discovers local models with
`capabilities.reasoning = false`, so it ignores the reasoning stream and shows
nothing during the (often long) think phase. Observed 2026-07-06 with
qwythos-9b on z4 — it emitted 775 chars of reasoning for "2+2" (19 reasoning
deltas, 0 content deltas up front) and the TUI appeared frozen.

Today the only fix is a per-client opencode config override. That doesn't
scale: the capability is a property of the model on the host, so the host
should advertise it once and every client should pick it up.

## What

- Add a `reasoning` field to each model object in `GET /v1/models`, resolved
  from a new `reasoning` field on the model config.
- opencode's discovery maps `item.reasoning` → `capabilities.reasoning`
  (separate change `map-reasoning-capability`), so a reasoning model set once
  on the host lights up thinking-stream rendering for all clients.

## OpenAI-API-style note

OpenAI's `/v1/models` has no capability flags, so there is nothing to mirror
directly. We extend the model object minimally with a flat `reasoning`
boolean — consistent with the fork's existing non-standard extensions on the
same object (`context_length`, `max_output_tokens`, `state`, `loaded`,
`metadata`). No nested `capabilities` object for now; keep it flat and simple.

## Constraints

- `contracts/llama-skein.openapi.json` is source of truth — spec first, then
  `go generate ./pkg/apicontract`, then handler.
- `reasoning` is omitted when unset (pointer/omitempty), so nothing changes
  for models that don't declare it.

## Non-goals

- Auto-detecting reasoning from the GGUF chat template or by observing
  `reasoning_content` at runtime (the GGUF parser doesn't expose the template
  today). Config-declared is the reliable first step; auto-detection is a
  possible follow-up.
- Changing how reasoning is streamed (llama-server already emits
  `reasoning_content`).
