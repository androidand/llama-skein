# Spec delta: tuning-api (add-gpu-tuning-profiles)

## ADDED

### GPU tuning profiles

- The server MUST detect the host GPU's gfx target at startup from PCI
  device IDs (sysfs), independent of ROCm CLI tooling. A `tuning.gfxTarget`
  config value overrides detection.
- The server ships built-in tuning profiles keyed by gfx target. A profile
  declares: flash-attention on/off, parallel slot count, optional MTP
  speculative-decoding flags, and a `verified` boolean.
- Profiles are **recommended defaults, not forced**. The operator can, at any
  layer, override or disable them:
  - a per-host override may set any profile field to any value (including
    disabling a recommended flag) and may add arbitrary `extra_args`;
  - `tuning.enabled = false` disables all auto-injection — the model's `cmd`
    launches verbatim;
  - any flag explicitly present in a model's `cmd` always wins.
- When launching a `backend: llamacpp` model with tuning enabled, the server
  MUST append effective-profile flags that are NOT already present in the
  command (matching by flag name and known aliases).
- MTP profile flags MUST be applied only to MTP-capable models
  (`metadata.mtp.enabled`, or GGUF-name fallback); never to plain models.
- Profiles apply only to the llamacpp backend; MLX/vLLM launches are
  untouched.

### GET /api/tuning

- Returns the detected gfx target, PCI device ID, whether tuning is enabled,
  the effective profile, and per-value provenance labeling each as
  `recommended` (from the built-in profile) or `override` (set by the user),
  so clients can show the difference and offer "reset to recommended".

### GET /api/tuning/profiles

- Returns all built-in profiles (for client pickers), each with its
  `verified` flag.

### PATCH /api/tuning

- Accepts a partial override (any of: `enabled`, `flash_attn`, `parallel`,
  `mtp`, `extra_args`, `gfx_target`) persisted to the config `tuning:` block.
  A field may force a value that disables a recommendation (e.g. flash_attn
  false); an omitted field defers to the built-in profile. `extra_args`
  appends arbitrary flags the curated profile does not model. The change
  takes effect on the next model (re)load; already-running models are
  unaffected until reloaded.
- Clearing a field (sending it null) resets it to the recommended value.

## MODIFIED

### GET /api/config/models/{id}

- Response gains a read-only `effective_flags` string: the launch command
  after profile injection. The stored `cmd` is returned unchanged; clients
  compare the two to see what the profile added.
