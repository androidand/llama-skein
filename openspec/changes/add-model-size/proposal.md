# Proposal: Expose model file size in /v1/models

## Why

When a provider hosts several similar models (same family, different
quantizations), you can't tell them apart by size — the quant tag in the name
(`Q8_0`, `Q4_K_M`, …) doesn't translate to "how many GB." Picking a model in
opencode is guesswork. The size is already known to llama-skein (the fit engine
computes it, and the config API stats the GGUF), it just isn't surfaced on the
listing clients actually use to pick a model.

## What

- Add `size_bytes` (int64) to the `Model` object in `GET /v1/models`: the GGUF
  file size for llama.cpp, the summed safetensors size for MLX. Omitted when
  undeterminable (peer models, unresolvable path, unmodeled backend).
- Computed from a cheap `os.Stat` (GGUF) or the existing MLX shape resolver,
  memoized per model id (cleared on reload) so the listing stays fast.
- Companion wiring (separate repos): opencode-skein shows the size in the model
  picker (the one place it's needed — the loaded size is already in the
  sidebar VRAM/RAM meter); skein's model/provider listing shows it too.

## Constraints

- Spec-first (`contracts/llama-skein.openapi.json`), regen Go + the opencode TS
  client.
- Additive + omitempty — no change for clients that ignore it.

## Non-goals

- Parameter count or a "download size vs resident size" split — file size is
  what the user asked for and what maps to the GB on disk.
