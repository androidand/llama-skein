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

- When a request ends with its context cancelled (client disconnect OR the
  request-time cap) and no other request is in flight, the server MUST attempt
  to cancel the backend's processing llama.cpp slots and then VERIFY the slot
  released. If the slot is still processing after a short grace (the cancel was
  ignored — the GPU-kernel-wedge signature), or the backend's control plane
  stops answering, the server MUST restart the process so the next request
  reloads a fresh backend instead of hanging on the wedged one.
