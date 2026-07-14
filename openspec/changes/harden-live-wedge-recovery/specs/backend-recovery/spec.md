# Spec delta: backend-recovery (harden-live-wedge-recovery)

## ADDED

### Serialize concurrent requests to the backend slot count

- Each local process MUST bound concurrent inference to its backend's slot
  count: MLX to 1, and llama.cpp to its explicit `--parallel`/`-np` value.
  With no explicit `--parallel`, llama.cpp is unbounded (its implicit slot
  count is version-dependent, so none is assumed). Requests beyond the bound
  QUEUE (honoring client disconnect), they are not rejected — so two requests
  can never race into a `--parallel 1` GPU slot and deadlock it.

### Enforce the request-time cap on the live path

- When a model's `maxRequestTimeSecs` (own or inherited global) is set, the
  server MUST bound the upstream request with that deadline. On expiry the
  upstream round-trip is cancelled and the client receives an error rather than
  an unbounded hang.

### Restart a wedged backend

- When a request ends with its context cancelled, recovery MUST fire on two
  distinct triggers with different safety margins:
  - The server's own `maxRequestTimeSecs` deadline expiring is authoritative
    proof that request exceeded its budget, so recovery MUST run regardless of
    how many other requests are queued behind it. Gating this on "no other
    request in flight" would silently defeat the timeout under the load
    pattern most likely to cause a wedge (`--parallel 1` with requests piled
    up from retries or concurrent clients).
  - A client disconnecting with no timeout involved keeps the conservative
    "no other request in flight" guard, since another client's slot may be
    legitimately busy.
- On either trigger, the server MUST attempt to cancel the backend's
  processing llama.cpp slots and then verify the slot released. If the slot is
  still processing after a short grace (the cancel was ignored — the
  GPU-kernel-wedge signature), or the backend's control plane stops answering,
  the server MUST restart the process so the next request reloads a fresh
  backend instead of hanging on the wedged one.

### Bounded process teardown

- Terminating a process (via SIGKILL, after a graceful signal did not stop it
  in time) MUST NOT wait indefinitely for the OS to reap it. SIGKILL cannot
  interrupt a process blocked in an uninterruptible kernel-level wait (e.g. a
  GPU driver call behind a livelocked compute kernel); since this teardown
  runs on the model's single in-order control path, an unbounded wait here
  would freeze every future operation on that model (start, stop, health
  checks) with no bounded worst case for recovery. After a bounded grace
  period the server MUST give up, log the condition, and continue — leaving
  the OS process to be reaped whenever it eventually exits, or forcibly
  reclaimed by its listening port the next time that model is started.
