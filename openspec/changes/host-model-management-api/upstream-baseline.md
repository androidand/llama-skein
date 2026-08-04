# Upstream baseline (task 1.1)

## Merge base

- Our merge base with `upstream` (`mostlygeek/llama-swap`) is **`v223`**
  (`git describe --tags $(git merge-base HEAD upstream/main)` → `v223-2-gccfba0d`).
- Current upstream tip is **`v247`** — **66 commits ahead** of our merge base
  (`git log --oneline HEAD..upstream/main | wc -l`).
- The "v223-era" framing in `openspec/changes/model-gallery-ui/source-analysis.md`
  (opencode-skein) is accurate as of when it was written but the gap has grown;
  worth re-checking before that change's own task 1.4 verification.

## Notable upstream commits not yet in this fork (informational, not required by this task)

Two look directly relevant to this change's own subject matter and are worth a
deliberate look before or during section 5 (Inventory and lifecycle), not
silently skipped:

- `298848d` `internal/hw: detect inference host hardware (#978)` — upstream now
  has its own hardware-detection package. Overlaps in purpose with this
  ecosystem's `/api/hardware` and the hypothetical-fit work
  (`fit-hypothetical-models`); worth comparing before assuming ours is still
  the better source of truth.
- `8d61908` `internal/perf: fix rocm-smi GPU memory utilization (#973)` — a
  ROCm memory-reading fix. Our ROCm hosts (rocky, z4, proxmox) use `rocm-smi`
  for GPU monitoring (confirmed live on rocky during the `fit-hypothetical-models`
  1.7 smoke test); if our reading has the same bug, this is a candidate pull.

No rebase performed. Per `ECOSYSTEM.md`'s own discipline ("never merge",
`proxy/process.go` as the conflict hotspot with our slot-cancel/autoUnload
logic), a 66-commit rebase is a separate, deliberate, conflict-prone action —
out of scope for a record-and-verify task, not something to do opportunistically
while unblocking section 2.

## Behavior verification

The specific behaviors this change assumes and builds on — model state,
load/unload, routing, loading-stream, and performance — were verified passing
at the current merge base (2026-08-04, commit `4976be9`):

```
GOWORK=off go test ./internal/router/... ./internal/process/... ./internal/perf/... \
  ./internal/runtime/... ./proxy/... -count=1
GOWORK=off go test ./internal/server/... -run 'Loading|Stream|ModelState|Ps' -count=1
```

All packages pass. Specifically confirmed: `TestServer_CloseStreams`,
`TestMemGuard_DoesNotUnloadLoadingModel`, `TestServer_ProcessStreamingResponse`
(+ `_NoData`), `TestServer_LogStream_ModelID` (+ `_UnknownID_Returns400`), plus
the full `internal/process`, `internal/router`, `internal/perf`, `internal/runtime`,
and `proxy` suites.

**Conclusion**: the current merge base provides everything this change assumes.
No upstream sync is required before starting section 2. The two hardware/ROCm
commits above are worth a deliberate look later, not a blocker now.
