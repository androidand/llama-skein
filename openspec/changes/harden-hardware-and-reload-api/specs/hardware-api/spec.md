# Spec delta: hardware-api (harden-hardware-and-reload-api)

## MODIFIED

### GET /api/hardware — loaded_model determinism

- `loaded_model` MUST identify the same model across consecutive calls while
  the set of running models is unchanged.
- Selection rule: among running models whose weight file resolves, the model
  with the largest `model_mb` is reported; ties break on lexicographically
  smallest model ID.
- `kv_estimate_mb` semantics unchanged (VRAM delta on GPU hosts, fit-engine
  GGUF estimate fallback when the delta is 0).

### Config reload trigger delivery

- A reload trigger (config PATCH/add/remove, `POST /api/config/reload`,
  file-watcher event, SIGHUP) that arrives while a reload is executing MUST
  NOT be lost: reloads repeat until a pass begins at-or-after the last
  trigger (loop-until-clean). Every config-file write made before a trigger
  returned is observed by some completed reload. Triggers arriving during a
  follow-up pass dirty it again.
- Overlapping reload executions remain forbidden (existing behavior).
- `POST /api/config/reload` continues to return 202 immediately.

### Fit and advertised context vs. server slots

- `max_safe_ctx` (and everything derived from it: `/v1/models`
  `context_length` for llama-skein backends) MUST reflect the *effective
  per-request* context: the configured (or trained-fallback) `n_ctx` divided
  by the model command's `--parallel`/`-np` slot count.
- Shipped default configs MUST set `--ctx-size` and `--parallel` explicitly;
  llama-server defaults are version-dependent and MUST NOT be relied upon.

## ADDED

### GGUF metadata caching (internal)

- The server MAY cache parsed GGUF metadata per (file path, mtime). A change
  to the file's mtime invalidates its entry. The cache MUST NOT survive a
  config hot reload.
