# Automatic hybrid GPU + system-RAM placement

llama-skein can run models **larger than the GPU's VRAM** by planning a
hybrid placement: latency-critical weights stay on the GPU, the overflow
(for MoE models, routed expert tensors) lives in system RAM. Planning is
automatic, per model, inspectable, and never persisted to your config file.

## How it works

At startup and on every config reload, each llama.cpp model is planned:

1. **Budgets.** The GPU budget is what the model gets once resident — the
   whole card for a model in an exclusive swap group, live free VRAM
   otherwise — minus a configurable reserve. The host budget is the
   **effective** available memory (the cgroup limit inside Docker/LXC, not
   the host's physical RAM) minus a configurable reserve. Swap never counts.
2. **Plan.** From the GGUF's exact tensor table:
   - fits fully in the GPU budget → `gpu`: the command is left untouched;
   - MoE model over budget → `hybrid`: the minimal `--n-cpu-moe N` is
     pinned (experts of the first N layers go to CPU) plus a
     `--fit-target`/`--fit-ctx` margin for the engine's own fitting;
   - dense model over budget → `hybrid`: the layer split is delegated to
     llama.cpp's built-in fitting (`--fit`, on by default, with `-ngl` left
     at `auto`) — llama-skein only gates host-RAM feasibility, which the
     engine's fitting does not check;
   - nothing fits within safe budgets → `refuse`: the model is never
     launched (HTTP 507), instead of OOM-killing the host.
3. **Apply.** Plans rewrite the launch command **in memory only**. The
   config file is never touched, and a model that fits fully is
   byte-for-byte untouched — loading a small model after a hybrid giant
   automatically runs it with its normal full-GPU settings. There is no
   global state to revert.
4. **Preflight.** When the engine build ships `llama-fit-params` next to
   `llama-server`, the planned command is cross-checked against the
   engine's own allocator and the fitted arguments are reported (advisory).

Anything the planner cannot size confidently — unknown VRAM, unreadable
GGUF, unknown container limit, a non-llamacpp backend — **fails open**: the
model runs exactly as configured.

## Your flags always win

A command that pins any placement flag (`-ngl`/`--n-gpu-layers`,
`--n-cpu-moe`, `--cpu-moe`, `--override-tensor`, `--tensor-split`) is
`custom`: automatic placement never touches it. This matches upstream
llama.cpp, where setting those flags disables its automatic fitting for
that argument.

## Why file size ≠ required memory, and VRAM + RAM ≠ capacity

Weights are only part of the footprint: the KV cache scales with context
(and parallel slots), compute buffers and the runtime need headroom, and
the OS needs to keep functioning. A 90 GB model on a 48 GB card + 128 GB
host is loadable — but not because 90 < 176. The planner budgets weights,
KV at the configured context, and overhead against *reserved* budgets, and
refuses plans that would leave the host starving. In a container it uses
the cgroup limit (`/api/hardware` → `memory.effective_total_mb`), because
that is where the OOM killer actually lives.

## Configuration

```yaml
placement:
  mode: auto            # auto | gpu | hybrid | cpu   (default auto)
  hostReserveGiB: 0     # 0 = max(12 GiB, 10% of effective RAM)
  gpuReserveGiB: 0      # 0 = max(2 GiB, 5% of VRAM); also the --fit-target
  minimumContext: 0     # 0 = 8192; also the --fit-ctx floor
  allowKvQuantization: false   # never trade KV quality silently
```

`mode: gpu` disables all rewriting (today's behavior). The planner never
quantizes the KV cache and never enables/disables speculative-draft models;
configured draft flags are counted as GPU cost.

## Inspecting a decision

- `GET /api/fit/{model}` — `placement` block: mode, perf class, reason,
  planned GPU/host MB, `n_cpu_moe`, and the preflight's effective args;
  plus `gpu_resident_mb` / `host_resident_mb` / `run_mode` on the fit
  itself.
- `GET /v1/models` — a `placement` hint (mode, perf class, applied) and the
  offload flag read-back (`n_cpu_moe`, …), which reflects the rewritten
  command.
- `POST /api/fit/hypothetical` — per-quant `placement` verdicts before
  downloading anything: a 90 GB quant on a 48 GB card ranks `hybrid`
  (loadable with caveats), not unloadable.
- Server log lines are prefixed `placement:`.

## Performance expectations

Placement reports a qualitative class, never invented tokens/sec:

| class | meaning |
|---|---|
| `native-gpu` | everything on the card |
| `fast-hybrid` | ≤ ~⅓ of weights in RAM; decode usually still GPU-paced |
| `cpu-bound-hybrid` | most weights in RAM; host memory bandwidth paces every token — fine for async agents, poor for interactive use |
| `cpu-only` | no GPU share |

A `cpu-bound-hybrid` model is *slow, not stuck*: generation legitimately
takes far longer than full-GPU serving. Bear that in mind before tightening
`maxRequestTimeSecs`, and when reading the wedge watchdog — a hybrid model
busy in host RAM shows low GPU memory-controller activity without being
wedged.

## Limitations

- The dense `-ngl` split and descriptor-based (pre-download) MoE verdicts
  are proportional approximations; the exact per-layer walk needs the GGUF
  tensor table on disk.
- MLX and vLLM backends are never auto-placed (the offload translator warns
  on unsupported knobs).
- Container limits are read from cgroup v2/v1; Proxmox LXC also virtualizes
  `/proc/meminfo` via lxcfs, so both paths agree there.
- Adaptive retry after a real OOM and learned placement profiles are
  tracked separately (`add-placement-retry-learning`, issue #21).

## Example: DeepSeek-V4-Flash on a 48 GB W7800 + 128 GB host

`unsloth/DeepSeek-V4-Flash-0731-GGUF:UD-IQ2_M` (~91 GB, deepseek4 MoE)
on z4 (LXC container, limit raised to ~112 GiB): the planner pins
`--n-cpu-moe` so the experts live in host RAM (~50+ GB), keeps attention/
shared weights + KV on the GPU (~40+ GB), classifies the result
`cpu-bound-hybrid`, and a later swap to a 9B model runs full-GPU with its
own untouched flags. Exact layer counts vary with llama.cpp version,
context, and quant — read them from `GET /api/fit/{model}`, don't copy
them from this page.
