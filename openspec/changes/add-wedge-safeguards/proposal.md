# Proposal: Backend wedge safeguards — serialize slots, default timeout, GPU-stall watchdog

## Why

A `--parallel 1` model deadlocked inside its GPU kernel when two opencode
sessions hit it at once: GPU pinned at 100% utilization while memory-controller
activity read ~0 (a real decode is memory-bandwidth-bound, so 100% util + ~0
memory activity = the GPU spinning on a stuck kernel, producing nothing). The
model sat wedged for minutes and never recovered on its own.

The existing slot-cancel recovery (`cancelBusySlots`) only runs when the
downstream client disconnects **or** the per-model `maxRequestTimeSecs` hard
timeout fires (`proxy/process.go`). Neither happened here: opencode kept the
connection open, and the model's config (written by fleet provisioning) has no
`maxRequestTimeSecs`. So the recovery path was never entered.

Three gaps, three safeguards:

1. **Nothing stops two requests racing into one slot.** The proxy's
   concurrency limit defaults to 10 regardless of the backend's actual
   `--parallel` slot count, so 10 requests can pile into a 1-slot llama-server.
2. **Recovery depends on a per-model timeout that is easy to omit.** There is
   no global default, so a model without `maxRequestTimeSecs` can wedge
   forever.
3. **Recovery can't see a live wedge.** While a request looks in-flight and the
   client stays connected, nothing detects that the GPU is spinning uselessly.

## What

- **Serialize to slot count (B):** when a model sets `--parallel`/`-np`
  explicitly (as the fleet's tuned models do) and no `concurrencyLimit`,
  default the proxy concurrency limit to that slot count. A `--parallel 1`
  model then admits one request at a time; excess requests get the existing
  `429`, so they never deadlock the single slot. Models that set neither keep
  the legacy default (llama.cpp's implicit slot count is version-dependent, so
  we don't assume it). Explicit `concurrencyLimit` still wins.
- **Global default request timeout (A):** add a top-level
  `maxRequestTimeSecs` that each model inherits when it does not set its own
  (mirrors how `healthCheckTimeout` propagates). Arms the existing
  hard-timeout → `cancelBusySlots` → restart path for every model, so a wedge
  self-heals after the cap even without per-model config. Shipped default
  config sets a sane value; `0` preserves today's no-limit behavior.
- **GPU-stall watchdog (C):** a background loop in the proxy manager that, for
  a running llama.cpp process with a request in-flight longer than a grace
  period, samples the GPU; if utilization stays high while memory-controller
  activity stays ~0 across several samples (the wedge signature), it restarts
  the backend even though the request still looks in-flight. Requires the perf
  monitor, a single detectable AMD GPU, and the new `mem_busy_percent`
  telemetry. Conservative thresholds + an in-flight grace floor keep genuine
  long generations (which are memory-active) safe. Toggleable; on by default.

## Constraints

- Reuse the existing recovery primitives (`cancelBusySlots`,
  `StopImmediately`, the `concurrencyLimitSemaphore`, `inFlightRequestsCount`)
  rather than adding parallel machinery.
- `contracts/llama-skein.openapi.json` is source of truth for any exposed
  field (e.g. a new GpuStat memory-activity metric); spec-first + regen.
- Defaults must not surprise upstream mergers: behavior change is limited to
  (a) concurrency now matching slot count and (b) an opt-in-via-config global
  timeout; the watchdog is gated on telemetry + single GPU.

## Non-goals

- Fixing the underlying llama.cpp single-slot deadlock (upstream bug); we
  prevent triggering it and recover if it happens.
- Multi-GPU stall attribution (watchdog gates itself to single-GPU hosts).
- Blocking/queuing excess requests instead of `429` (a possible follow-up;
  keeps the current non-blocking semantics for now).
