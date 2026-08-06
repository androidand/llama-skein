# Spec delta: hardware-api (add-container-memory-limits)

## ADDED

### Effective host memory limit

- The hardware inventory MUST report the process-visible effective memory
  limit: physical `MemTotal` clamped by an applicable cgroup (v2 `memory.max`,
  else v1 `memory.limit_in_bytes`). When no limit applies, effective figures
  MUST equal the raw figures and `limit_source` MUST be `none`.
- `GET /api/hardware` and `GET /api/resources` MUST expose
  `memory.effective_total_mb`, `memory.effective_available_mb`, and
  `memory.limit_source` (`none|cgroup-v2|cgroup-v1`).
- Effective available memory MUST NOT exceed the effective limit minus current
  cgroup usage.
- Swap MUST NOT be counted toward any capacity, budget, or fit figure. Swap
  totals remain reported for observability only.
- Every host-memory *budget* consumer in the server (fit engine host-RAM
  paths, memory guard, placement planning) MUST consume effective figures,
  not raw ones.
- Resolution MUST fail open: unreadable cgroup files leave behavior identical
  to today's meminfo-based reporting.
