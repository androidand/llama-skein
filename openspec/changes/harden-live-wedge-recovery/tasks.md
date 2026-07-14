# Tasks: harden-live-wedge-recovery

- [x] 1. Generalize the per-process serialization slot (`internal/process`,
       was `mlxSlot`) to `serializeSlot`: MLX → cap 1; llamacpp → explicit
       `--parallel`/`-np` value; llamacpp without it → nil (unbounded). Add
       `parallelFromCmd`.
- [x] 2. `ServeHTTP`: enforce `maxRequestTimeSecs` — bound the upstream request
       with a deadline so a wedged request returns an error to the client
       instead of hanging.
- [x] 3. Trigger `cancelBusySlots` on the timeout path (not just disconnect);
       enhance it to verify the slot released after cancel and `Stop()` the
       backend when it stays wedged or the control plane hangs (adds
       `hasProcessingSlot`).
- [x] 4. Tests: `parallelFromCmd`, `serializeSlot` capacity per backend.
- [x] 5. `go build ./...`, `go test -short ./proxy/... ./internal/...` (984 ok).
- [ ] 6. Deploy to z4 + rocky (+ proxmox); verify two parallel requests no
       longer wedge, and a forced stuck request returns an error + restarts.
- [ ] 7. Follow-up: GPU-memory-activity watchdog to catch an in-flight wedge
       without waiting for the wall-clock cap; re-home remaining dead-`proxy`
       safeguards; set a global `maxRequestTimeSecs` default on the hosts.
