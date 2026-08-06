# Spec delta: placement-recovery (add-placement-retry-learning)

## ADDED

### Failure classification

- Model startup/crash failures MUST carry a failure class distinguishing at
  minimum: GPU out-of-memory, host out-of-memory (including cgroup kills),
  unsupported architecture, missing GGUF shard, invalid command-line flag,
  backend error, and unclassified crash. Classification derives from exit
  code and signal first, engine output patterns second; an unmatched failure
  MUST be reported as unclassified, never guessed into a memory class.

### Bounded adaptive retry

- Only memory-class failures of models with automatically planned placement
  are retried, with progressively more conservative plans (wider GPU reserve
  / more experts on CPU → smaller batches → smaller context, never below the
  configured minimum → full CPU-MoE).
- Retries MUST be bounded by a configurable attempt cap, count toward the
  crash-loop breaker, terminate failed processes before relaunching, and
  record every attempt's plan and failure reason retrievably via the API.
- Context reduction and any KV-cache change remain subject to placement
  policy. Models with operator-pinned placement flags MUST never be retried
  with altered flags.

### Learned placement profiles

- A healthy successful launch of an auto-placed model MUST persist a
  placement profile: applied flags, measured peak VRAM and host memory, load
  time, keyed by model identity, engine version, total VRAM, effective host
  memory limit, and context.
- A subsequent launch MUST use a matching profile as its first candidate and
  MUST invalidate it when any key input changes, falling back to fresh
  planning.
- A run that only survived on the most conservative ladder rung, or whose
  measured margins breach the configured reserves, MUST NOT be recorded as a
  known-good profile.
