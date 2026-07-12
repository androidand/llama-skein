# Spec delta: backend-recovery (add-wedge-safeguards)

## ADDED

### Proxy concurrency matches backend slot count

- When a model does not set `concurrencyLimit` but its llama.cpp launch
  command sets `--parallel`/`-np` explicitly, the proxy MUST default its
  concurrency limit to that slot count, so a `--parallel 1` model admits one
  request at a time and requests never race into a single slot (which can
  deadlock the backend). When neither is set, the legacy default is kept
  (llama.cpp's implicit slot count is version-dependent, so no slot count is
  assumed). An explicitly configured `concurrencyLimit` always wins. Requests
  beyond the limit receive `429 Too Many Requests`, as today.

### Global default request timeout

- A top-level `maxRequestTimeSecs` config value MUST be inherited by any model
  that does not set its own `maxRequestTimeSecs`. `0` means no limit (current
  behavior). A model's own value overrides the global. This arms the existing
  hard-timeout recovery (cancel upstream → verify slot released → restart if
  wedged) for every model, so a stuck request self-heals after the cap without
  per-model configuration.

### GPU-stall wedge watchdog

- The server MAY run a background watchdog that detects a backend wedged in a
  GPU kernel — GPU utilization pinned high while GPU memory-controller activity
  is ~0 — and restart it even while a request appears in-flight and the client
  is still connected.
- The watchdog MUST be conservative to avoid killing legitimate long
  generations (which keep memory active): it acts only when, for a running
  llama.cpp process, a request has been in-flight longer than a grace floor
  AND `GpuUtilPct` stays at/above a high threshold AND memory-controller
  activity stays at/below a low threshold across several consecutive samples.
- The watchdog requires the performance monitor to be enabled and exactly one
  GPU to be detectable (so a stall is unambiguously attributable). It is a
  no-op otherwise. It is toggleable via config and enabled by default.
- On trigger it restarts the backend (equivalent to the existing wedge
  restart); the in-flight request fails and the client may retry — preferable
  to an indefinite hang.

## MODIFIED

### GPU telemetry

- GPU stats gain a memory-controller activity percentage
  (`mem_busy_percent` on Linux/amdgpu sysfs), distinct from VRAM-used
  percentage. Exposed in metrics; 0 on platforms that do not provide it. This
  is the signal that distinguishes a productive memory-bound decode from a
  spinning-but-idle wedged kernel.
