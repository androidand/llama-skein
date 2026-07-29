# Report an uncomputed fit as "unknown", not "no"

## Context

`fitForModel` (`internal/server/apifit.go:146-150`) initialises its response with
`FitLevel: apicontract.No` and then returns that value unchanged on **every**
cannot-compute path:

- an MLX model with no `useModelName` to locate the HF cache
- MLX metadata that could not be read
- any backend that is not llamacpp (vLLM, or a generic OpenAI-compatible server)
- a GGUF whose metadata could not be parsed

The code's own comment at `:172` says *"report unknown rather than a wrong number"* —
the intent was already right; the value is wrong.

`"no"` is not a neutral placeholder. It is a **verdict**: the contract defines it as
"the model will not fit this host". `ModelFitFitLevel` already includes `unknown`
(`pkg/apicontract/llama_skein.gen.go:189`) and `internal/fit/fit.go:327` uses it
correctly for the missing-VRAM case — the server layer just doesn't.

## Why

Consumers act on `fit_level`. opencode-skein and the TUI surface it, and a `"no"`
reads to an operator as "this host cannot run this model" when the truth is "nobody
measured". For a `backend: vllm` model — which is a fully supported way to front an
arbitrary OpenAI-compatible server — every single fit report is currently a false
negative, with `max_safe_ctx: 0` and no `model_mb`/`vram_total_mb` at all.

Emitting a verdict that was never computed is worse than admitting ignorance, and it
trains operators to ignore the field.

## What changes

The initialiser becomes `apicontract.Unknown`. `fillModelFit` (`apifit.go:251-252`)
unconditionally overwrites `FitLevel`, so every genuinely-computed fit is unaffected —
only the early returns change.

## Non-goals

- Adding a fit model for vLLM/safetensors-on-Linux weights. That is a real gap (there
  are only two shape builders, GGUF and MLX-via-HF-cache) but it is separate work;
  this change is about not lying when the gap applies.
- Changing what the fit *guard* does. `fitguard` already fails open on nil fields, so
  nothing was being blocked by the wrong value — the damage was purely informational.

## Risks

- A consumer that branches on `fit_level == "no"` to mean "unmodeled" would stop
  matching. That behaviour was already wrong and is the reason for the change; the
  contract has always documented `unknown` for this case.
