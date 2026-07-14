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
- [x] 6. Deploy to z4 + rocky (+ proxmox); verify two parallel requests no
       longer wedge, and a forced stuck request returns an error + restarts.
- [x] 7. Regression: recovery trigger was gated on `inflight<=1` for BOTH the
       client-disconnect path and the new hard-timeout path, so under
       `--parallel 1` with requests piled up (retries/concurrent sessions —
       the actual real-world trigger), `inflight` never dropped to <=1 and
       recovery never fired. Split the two triggers (`shouldRecoverWedge`):
       our own timeout expiring is authoritative and always recovers;
       disconnect keeps the conservative guard. Test: `TestShouldRecoverWedge`.
- [x] 8. Second regression: `killProcess`'s post-SIGKILL wait for `cmdDone` had
       NO timeout. SIGKILL cannot interrupt a process blocked in an
       uninterruptible kernel-level wait (a GPU-driver ioctl behind a
       livelocked compute kernel); since this wait runs inside the model's
       single in-order control loop, an unbounded hang there freezes EVERY
       future operation on that model — there was no bounded worst case for
       recovery at all. Added `defaultPostKillGrace` (overridable per-process
       as `postKillGrace`, matching the `waitDelay`/`probeInterval` pattern):
       give up after the grace period, log loudly, and return — the process
       is abandoned but `reclaimStalePort` (already used before every start)
       force-kills whatever still holds its port on the next start attempt,
       independent of the abandoned wait. Test:
       `TestKillProcess_BoundedWaitOnUnreapableProcess`.
- [ ] 9. Follow-up: GPU-memory-activity watchdog to catch an in-flight wedge
       without waiting for the wall-clock cap; re-home remaining dead-`proxy`
       safeguards; set a global `maxRequestTimeSecs` default on the hosts;
       investigate whether two different models' processes can genuinely
       contend for the same physical GPU during a swap (the user's own
       hypothesis) — not confirmed this incident (process state was `ready`,
       not `stopping`, when diagnosed) but worth a dedicated look.
