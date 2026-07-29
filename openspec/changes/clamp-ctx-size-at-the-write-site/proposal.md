# Clamp --ctx-size where it is written

## Context

`PATCH /api/config/models/{id}` wrote `--ctx-size` verbatim from the request into the
model's `cmd`, with **no validation of any kind** (`internal/server/apiconfig.go`,
`patchModelInConfig`). That is a single unguarded door, and several components write
through it autonomously:

- opencode-skein's 413-overflow adjuster (`adjustLocalContextOnOverflow`)
- opencode-skein's TUI context dialog (`local.model.setCtxSize`)
- skein's context sweep, and its `SetModelContext(Auto: true)` path
- `skein models ctx-tune`
- plain `curl`

Log forensics on z4 (`/var/log/tengil-lxc-102-console.log`) caught it concretely: a
`98304` context was written onto a model whose trained ceiling is `32768`, while
`GET /api/fit` was simultaneously reporting `configured_ctx: 98304` and
`max_fit_ctx: 32768`. **The server already knew the value was wrong and wrote it
anyway.**

Drift goes both ways. The measured skew in the TUI dialog was
`max_safe_ctx / configured_ctx = 26050 / 32768 = 0.795` — a systematic ~20%
*under*-recommendation, which matches the operator-reported symptom of contexts
"always ending up very small". The same function could also produce `312,555` from
real host numbers, an 8x over-recommendation.

## Why

Above the model's trained context, RoPE extrapolates and output quality degrades in a
way that reads as "this model is bad" rather than "this config is wrong" — an
expensive misdiagnosis. Below a usable context, agents fail with overflow errors that
look like harness bugs.

`fitguard` structurally cannot catch either case: `ctxClampDecision` only fires when
`VramRequiredMb > VramTotalMb`, and an over-trained context can still fit VRAM
comfortably (98304 needed ~35.6 GB of 48 GB). It is an OOM guard, not a correctness
guard.

Fixing each writer independently means fixing it repeatedly and missing the next one.
The write site is the one place that covers all of them.

## What changes

Before writing `--ctx-size`, consult `s.fitForModel(id)` and clamp to `MaxFitCtx` —
which the fit engine already computes as the smaller of VRAM-achievable and the
model's trained context. Report the clamp in the response's existing `warnings` array.

Warnings rather than a hard `422`: the endpoint stays non-breaking for existing
callers, while the drift becomes visible instead of silent.

## Non-goals

- Enforcing a *lower* bound. The clamp only ever lowers a value, deliberately — a
  floor here would be a second policy in the wrong place, and the downward-drift
  causes are being fixed in the callers that compute the bad numbers.
- Replacing `fitguard`. It still owns the VRAM-exceeded case at load time.
- Fixing the callers. Done separately in opencode-skein and skein; this change is the
  backstop that holds when a new caller appears.

## Risks

- A caller that deliberately over-sets context — for example to probe behaviour past
  the trained window — will now be clamped. It gets a warning saying so, and
  `enginePath`-style explicit override was judged not worth adding for a case nobody
  has asked for.
- `fitForModel` is called on the patch path, adding a GGUF metadata read. It is
  already cached (`parseGGUFCached`) and the patch path is not hot.
- When fit cannot be computed the clamp does not apply, so an unmodeled backend keeps
  today's behaviour. That is intentional fail-open; see
  `fix-unmodeled-fit-reports-unknown` for why such a model reports `unknown`.
