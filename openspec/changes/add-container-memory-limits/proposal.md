# Proposal: Container-aware effective host memory limits

## Context

llama-skein's host-memory picture comes from gopsutil (`/proc/meminfo` via
`internal/perf/monitor_unix.go`). Nothing anywhere reads cgroup limits
(`/sys/fs/cgroup/memory.max` v2, `memory.limit_in_bytes` v1). Fleet reality:

- z4's provider is **LXC 102, capped at 48 GiB** (`pct config 102 → memory: 49152`,
  swap 512 MiB) on a host with 128 GB. lxcfs happens to virtualize `/proc/meminfo`
  inside Proxmox LXC, so today's numbers are right there *by accident of the
  deployment mechanism* — a Docker deployment of the same image sees the full
  host RAM and over-reports.
- Upcoming hybrid GPU+RAM placement (`add-auto-hybrid-placement`) budgets model
  weights against **host RAM**. An over-reported limit means planning a 70 GB
  host-resident tenancy into a 48 GiB cgroup — the OOM killer, not llama.cpp,
  decides the outcome.
- The memory guard (`internal/server/memguard.go`) and the fit engine's
  CPU-only path (`hostVRAM` no-GPU branch) consume the same over-reported
  numbers.

## Why

Every memory consumer must budget against the **process-visible effective
limit**: `min(MemTotal, cgroup memory.max)`. Swap must never be counted as
capacity (z4's container has 512 MiB swap; inference from swap is unusable).

## What

- A single resolver in `internal/perf` that computes the effective memory
  limit: gopsutil totals clamped by the cgroup v2/v1 limit when one applies to
  this process. Unlimited (`max`) or absent cgroup ⇒ meminfo unchanged.
- `SysStat` carries both raw and effective figures; `available` is likewise
  clamped (available cannot exceed limit minus current cgroup usage).
- `/api/hardware` (and `/api/resources`) expose `memory.effective_total_mb`,
  `memory.effective_available_mb`, and `memory.limit_source`
  (`none|cgroup-v2|cgroup-v1`) — OpenAPI spec first, then generated types.
- Fit engine host-RAM paths (`internal/server/apifit.go hostVRAM` no-GPU and
  unified branches) and the memory guard consume effective figures.
- Swap is reported but never added to any capacity or budget figure.

## Non-goals

- No NUMA topology, no memory-bandwidth discovery (later, if the placement
  planner's performance classification needs it).
- No change to VRAM accounting.

## Risks

- Low. Reading two well-known cgroup files with fail-open fallback to current
  behavior. Where lxcfs already virtualizes `/proc/meminfo` the clamp is a
  no-op (min of two equal numbers).
