# Spec delta: model-listing (advertise-reasoning-capability)

## MODIFIED

### GET /v1/models — reasoning capability

- Each model object MAY include a `reasoning` boolean indicating the model
  emits reasoning/thinking output (`reasoning_content`) before its answer.
- The value is resolved from the model config's `reasoning` field. When the
  config does not declare it, `reasoning` is omitted from the response (no
  behavioral change for non-reasoning models).
- Clients use this to enable reasoning-stream rendering, so a reasoning model
  declared once on the host is recognized by all clients rather than requiring
  per-client configuration.

## ADDED

### Model config `reasoning` field

- A model config entry MAY set `reasoning: true|false`. It is advertised
  verbatim in `/v1/models` and does not affect the launch command.
