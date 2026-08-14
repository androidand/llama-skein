# Proposal: Cap advertised fit contexts at the model's trained context

## Why

The fit engine's `MaxFitCtx` ("the largest --ctx-size that fits VRAM") is computed purely from
VRAM — it is **never bounded by the model's trained context**. For Muse Glimmer on z4:

- `trained_ctx` (GGUF) = 131072
- `vramMaxCtx` (48 GB, q8_0, weights 29.6 GB) = **576,482**
- opencode-skein auto-fit read `MaxFitCtx` and wrote `--ctx-size 393216` = **3× native**

Consequences: KV cache sized for 3× the trained window; every token attends over 3× window
(pure bandwidth waste); RoPE extrapolates past trained positions → silent quality loss. And the
same failure mode already hit Muse-Glimmer's `max_safe_ctx` budget twice (two separate, both
root-caused).

The fix is local and one-directional: auto-fit/recommend paths (fit engine + opencode-skein's
ctx adjustment) must not advertise past the model's trained context. Explicit user config (an
operator running `--ctx-size 262144` where the model supports it) must remain honored — this
is a *recommendation/grow target*, not a write-site clamp.

## What changes

In `internal/fit/fit.go`:

- `res.MaxFitCtx = min(vramMaxCtx, TrainedCtx)` (when both are known). The advertised "largest
  ctx that fits" is therefore bounded by `TrainedCtx` — the model's real capability.
- The VRAM-achievable ceiling is still computable and used: it just no longer *is* `MaxFitCtx`
  — that name/role (grow target) is now the capped value. This keeps over-config detection
  (`under_configured`) and the reason string correct, since both used
  `MaxFitCtx` as the comparison.
- No new fields, no back-compat break: all existing consumers (`/api/fit`, hypothetical-fit
  report, fitguard) read the corrected, now-sane number automatically.

Non-goals

- No write-site clamp / lower-bound enforcement. Explicit user ctx (curl/PATCH)
  above `TrainedCtx` is still honored end-to-end — this is a grow-target fix only.
- No attempt to detect "user intends 262144 as a real capability": it is not
  this change's place to infer intent.

## Risks

- TrainedCtx comes from GGUF / model config. Filename vs config mismatch (e.g. a GGUF that lies) would make
  every recommendation smaller. Fail-open handled: when TrainedCtx <= 0 or unknown, unchanged.
- Combining with the (separate, upstream) write-site clamp spec changes the effective ceiling — noted
  in the spec's scenarios.