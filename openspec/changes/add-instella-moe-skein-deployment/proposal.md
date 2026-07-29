# Add Instella-MoE model configuration and deployment to llama-skein

## Context

Running Instella-MoE on z4 requires a **custom-built `llama-server`**, because the model
needs architecture support that upstream does not have. z4 currently runs
`lemonade-sdk/llamacpp-rocm`'s gfx110X prebuilt at `/opt/llamacpp-rocm-gfx110X/`, kept
current by `POST /api/system/upgrade`. Introducing a second, locally-built binary alongside
it collides with three existing behaviours in this repo.

Full analysis: `docs/investigations/instella-moe-w7800.md` §10, §13.

## Why

The brief's constraint is not to modify the working llama-skein installation destructively.
The three collisions below are real and were found by reading the code, not hypothesised —
each has a mitigation that needs no code change, but they must be applied deliberately.

### Collision 1 — the upgrade endpoint clobbers custom builds

`internal/server/apiupgrade.go:110-131` resolves its install destination by
`pgrep -a llama-server`, **first match wins**. It does not parse `cmd` or macros:

```go
cmd := exec.Command("pgrep", "-a", "llama-server")
... fields := strings.Fields(lines[0]); return fields[1], nil
```

Worse than the binary swap: `libDir := filepath.Dir(serverPath)` then
`copySharedLibs(extractDir, libDir)` (`internal/runtime/llamacpp_upgrade.go:165,170,281`)
dumps every `.so` from the lemonade archive into that directory, overwriting our
`libggml*.so` / `libllama.so`. Restoring the `.bak` binary would then leave it running
against foreign libraries. And `restartLlamaServer()` (`:694-715`) kills **every**
`pgrep -a llama-server` match — by process name, not path.

**Mitigation, no code change: name the binary `llama-server-instella`.** Both the pgrep
destination-resolution and the restart sweep then miss it. `smokeTest` only runs
`<path> --version`, so nothing on the lemonade side cares.

Note `upgradeFromSource` (`:204`) clones `ggml-org/llama.cpp` only, so it cannot build our
patched fork — the source-build path is not a shipping vehicle.

### Collision 2 — the fit engine is blind to MoE, and silently rewrites ctx

`internal/fit/fit.go:210-230` (`ShapeFromGGUF`) drops every MoE field; `ModelShape` has no
`ExpertCount`. That is correct for all-experts-resident inference, but it means fit charges
CPU-offloaded experts against VRAM. `fitguard.go:100-113` then **silently rewrites
`--ctx-size`** when `VramRequiredMb > VramTotalMb`, and refuses the load with HTTP 507 if
`MaxFitCtx < 2048`.

**Mitigation: keep all experts on GPU** — which is the goal anyway. At Q8_0 with the full
32K context Instella needs ~17,805 MiB of 45,205 usable, so there is no reason to offload.

### Collision 3 — global tuning injection has no per-model opt-out

`internal/tuning/inject.go:131-165` rewrites `cmd` and `env` for **every** model whose
`backend` is `""` or `llamacpp`. z4 sets `tuning: flash_attn: true`. Instella gets no flash
attention regardless (its MLA `K->ne[0] = 544` is not an instantiated kernel dimension), so
the injected flag is at best inert.

**Mitigation: pin `--flash-attn off` explicitly in `cmd`** — explicit flags win
(`internal/tuning/apply.go:39-48`).

## What changes

A hand-written model entry in z4's `config.yaml` (mirrored into
`~/dev/docs-skein/config/z4/`), a pinned custom-build layout at `/opt/llamacpp-instella/`,
and a benchmark run against the incumbent baseline.

The entry **must be hand-written YAML**: `POST /api/config/models` and
`PATCH /api/config/models/{id}` cannot set `reasoning`, `env`, `unlisted`,
`sendLoadingState`, `useModelName`, `filters`, `metadata`, `maxRequestTimeSecs` or
`checkEndpoint`, and `POST` **replaces the whole entry node** for an existing id
(`apiconfig.go:480`), silently dropping hand-added keys.

## ⛔ License gate

The model is **ResearchRAIL — research-only**
(`license_name: researchrail`, "RESEARCH-ONLY RAIL Model License"). Using it as a Skein
worker agent on real work is production use and is **outside the license**.

Therefore this change deploys it as `unlisted: true` — reachable for evaluation, absent
from `/v1/models`, and **not** in the agent rotation. Promoting it beyond that requires an
explicit human licensing decision, which is out of scope here and must not be inferred from
this change being merged.

## Non-goals

- Making Instella a default or group member.
- Registering it with opencode-skein or advertising it to agent runners.
- Fixing the three collisions properly in code. Each deserves its own change:
  an explicit `serverPath`/`enginePath` config key feeding `opts.ServerPath`;
  MoE-aware fit (the math already exists in `pkg/gguf/offload.go:75-119` but is not wired
  into the fit path); and a per-model tuning opt-out.
