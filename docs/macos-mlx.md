# Apple Silicon / MLX

llama-skein can run [MLX](https://github.com/ml-explore/mlx-lm) models
(`mlx_lm.server`) alongside llama.cpp, so an Apple Silicon Mac can serve models
in either format. This page covers the parts that are specific to macOS and
easy to get wrong: how to configure an MLX model, how much memory it can
actually use, and how to recover a host that's run out of it.

## Configuring an MLX model

```yaml
models:
  mlx-phi-4:
    backend: mlx
    useModelName: "mlx-community/phi-4-4bit" # required — see below
    cmd: |
      /path/to/venv/bin/mlx_lm.server --host 127.0.0.1 --port ${PORT}
    proxy: http://127.0.0.1:${PORT}
```

- **`useModelName` is required for every `backend: mlx` model.** `mlx_lm.server`
  treats the request's `model` field as a Hugging Face repo to load. Without
  `useModelName` set to that repo, llama-skein forwards its own model ID
  instead and `mlx_lm` 404s trying to fetch it from Hugging Face.
- **Don't pass `--ctx-size`, `--cache-type-k/v`, or `--n-gpu-layers`.**
  `mlx_lm.server` doesn't understand llama.cpp flags and exits immediately if
  given one. llama-skein strips these automatically from `backend: mlx`
  commands, so it's safe to leave them out rather than a hazard to include.
- Pin the proxy to `127.0.0.1` (not `localhost`) — `mlx_lm.server` binds IPv4
  only, and `localhost` can resolve to `::1` and cause spurious connection
  failures.

## The memory ceiling: Metal's wired limit

On Apple Silicon, GPU/Metal memory is unified with system RAM but Metal will
not use all of it. By default macOS caps GPU-resident allocations
(`iogpu.wired_limit_mb`) at **~70% of physical RAM** — the rest is reserved for
the OS and other apps. This is not a llama-skein setting; it's a kernel/Metal
limit that exists whether or not llama-skein is involved.

llama-skein's [fit engine](#checking-what-will-fit) budgets against this
ceiling, not total RAM, when it decides whether a model fits, what context size
it can safely use, and (see below) whether to allow a load at all. A model
whose weights exceed the wired limit will crash `mlx_lm.server` with a Metal
allocation abort if loaded — llama-skein now refuses these before they load
(see [Load refusal](#load-refusal-the-507-guard)), but a *tight* fit can still
load successfully while leaving little headroom for anything else.

Check the current limit:

```bash
sysctl iogpu.wired_limit_mb
# 0 means "use the OS default" (~70% of RAM)
```

Raise it to fit bigger models:

```bash
sudo sysctl -w iogpu.wired_limit_mb=<megabytes>
```

This is **runtime-only** — it resets on reboot. To persist it across restarts,
install a LaunchDaemon that runs the same `sysctl -w` at boot (ask an LLM or
see Apple's launchd documentation for the plist format; this repo does not
ship one, since the right value is host-specific).

**After changing the limit, restart llama-skein.** It reads the wired limit
once and caches the resulting budget; a running instance won't see a change
until it restarts.

### Choosing a value — the real tradeoff

Raising the limit lets bigger/less-quantized models load. But every GB you
give to Metal is a GB macOS and your other apps don't have. This isn't just a
theoretical ceiling:

- **Too little headroom (e.g., 1-2 GB left on a 24 GB Mac) risks swap
  thrashing.** A model can load successfully (the fit engine now allows a
  "tight" fit) and then the machine has almost nothing left for normal use.
  Observed in practice: memory pressure to near-zero, swap usage climbing by
  gigabytes within seconds, and the host becoming unresponsive to new requests
  for a period — not a crash, but close enough to interrupt whatever else you
  were doing on that machine.
- **A dedicated inference box** (nothing else running on it) can reasonably go
  tighter than a machine you also use interactively (browser, editor, other
  agent sessions).
- There's no universally "correct" number — it's a judgment call based on how
  the machine is used. Leaving several GB of headroom is the safer default;
  tightening it is a deliberate trade you're making for bigger models.

## Checking what will fit

`GET /api/fit` reports, per model, whether it fits this host and how much
context is safe:

```bash
curl http://localhost:8080/api/fit
```

Key fields per model:

| Field | Meaning |
|---|---|
| `fit_level` | `perfect` / `good` / `tight` / `marginal` / `no` / `unknown` |
| `model_mb` | on-disk weight size |
| `vram_total_mb` | the budget it was checked against (the wired limit on macOS) |
| `max_safe_ctx` | the largest context that fits alongside the weights |

`unknown` means the fit engine couldn't determine an answer yet (e.g.
performance telemetry still warming up after a restart) — it is not the same
as "doesn't fit," and llama-skein never refuses a load on an `unknown`
verdict.

## Load refusal (the 507 guard)

Since 2026-07, llama-skein refuses to load a model whose weights confidently
don't fit the current budget, rather than letting it load and crash the host:

```
HTTP 507 Insufficient Storage
{"error":{"code":"model_over_host_memory", "message": "model \"...\" will not fit this host and was not loaded: ..."}}
```

This only fires when the fit engine has a *confident* "no" (known budget,
known weight size) — an `unknown` verdict always allows the load through, so
this never blocks a model llama-skein simply hasn't sized yet. If you hit a
507 for a model you believe should fit, check `/api/fit` for that model and
consider raising the wired limit (above).

## Recovering a stuck or thrashing host

If a model load pushed the host into heavy swap (see above) and it's slow to
respond:

```bash
curl http://<host>:<port>/unload
```

This stops all locally-loaded models. Under heavy swap the request itself may
be slow or time out — retry it; it will not race with itself in a way that
makes things worse. SSH/local access to the machine typically stays usable
even while it's slow, so you're not locked out, just waiting on macOS to page
things back in as pressure eases.
