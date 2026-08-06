# Proposal: Adaptive placement retry and learned placement profiles

## Context

`add-auto-hybrid-placement` plans a placement before launch and preflights it
with `llama-fit-params` where available — but estimates are estimates, ROCm
fragmentation is real, and the first hybrid launch of a 100 GB MoE model on a
given host is still an experiment. Today (`internal/process/
process_command.go`) a startup failure is classified only as
`start`/`crash`, there is **no OOM detection** (no exit-code 137 handling, no
`hipErrorOutOfMemory` / allocation-failure log parsing), no retry with safer
settings, and nothing records what actually worked: `s.unfittable` is
in-memory, and no per-model "this placement succeeded with these peaks"
record exists anywhere.

## Why

- One bad estimate must not strand an otherwise-servable model (today: crash
  → crash-loop breaker → refused), nor hammer the host with identical doomed
  relaunches.
- Every distinguishable failure class needs a different response: GPU OOM →
  move more to CPU; host OOM → refuse sooner; unsupported architecture /
  missing shard / bad flag → never retry.
- A successful hybrid launch is expensive knowledge (minutes of load time on
  100 GB weights). It should seed the next launch instead of being recomputed
  and re-risked.

## What

### 1. Failure classification

Extend the process failure path with a `FailureClass`: `gpu-oom`, `host-oom`
(exit 137 / cgroup kill), `unsupported-arch`, `missing-shard`,
`invalid-flag`, `backend-error`, `crash-other`. Sources: exit code + signal,
and matching against a small table of engine allocation-failure patterns
(ROCm/CUDA/Metal/host) from the tail already captured by
`lastOutputLines`. Classification is best-effort; unknown stays
`crash-other`. Exposed in `last_error` / model status.

### 2. Bounded retry ladder (policy-gated)

On a *placement-attributable* failure (`gpu-oom`, `host-oom`) of a model
whose placement was auto-planned, retry with progressively safer plans, all
within existing budgets:

1. widen the GPU reserve (larger `--fit-target`) / move more experts to CPU,
2. reduce batch/ubatch,
3. reduce context — only down to `placement.minimumContext`,
4. full CPU-MoE,
5. stop: mark failed with the recorded ladder history.

Bounded (default ≤ 3 attempts, configurable), never loops, cleans up each
failed process, respects the crash-loop breaker (ladder attempts count
toward its window), and never retries non-memory failure classes. Each
attempt and its outcome is recorded and visible via the API.

### 3. Learned placement profiles

- Persist a per-model **placement profile** on success (JSON store beside the
  existing `ProfileStore` precedent): applied flags, measured peak VRAM and
  host RSS, load time, context, engine version, plus the inputs' identity
  (model path + size + mtime/hash, VRAM total, effective host limit).
- Next launch: a valid profile is the first candidate plan (skipping the
  estimate), invalidated when any keyed input changes.
- A run that succeeded on the last ladder rung with margins below reserve
  targets MUST NOT be learned as a profile — barely-fit is not known-good.
- Peaks come from the existing perf monitor samples during load/warm-up; no
  new sampling infrastructure.

## Non-goals

- No tok/s benchmarking or quality scoring (perf classification stays
  qualitative in the planner).
- No cross-host profile sharing (skein-level concern).
- No retry for models with hand-pinned placement flags (`custom` mode) — the
  operator owns those.

## Risks

- **Retry storms on shared hosts**: mitigated by attempt caps, crash-loop
  integration, and only retrying memory-class failures.
- **Log-pattern brittleness**: patterns are a hint layered over exit-code
  truth; unmatched output degrades to `crash-other` (no retry), never a
  wrong retry.
- **Stale learned profiles**: strict identity keying + fail-open re-planning
  on any mismatch.
