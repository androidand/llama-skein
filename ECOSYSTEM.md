# llama-skein — Ecosystem Context

> **Canonical:** `/Users/andreas/dev/skein/docs/ECOSYSTEM.md` — full ecosystem map,
> cross-repo dependencies, inspiration repos. This stub covers what agents need locally.

## What this repo is

**llama-skein** is a fork of [mostlygeek/llama-swap](https://github.com/mostlygeek/llama-swap).
It is the LLM inference proxy layer in the skein ecosystem.

Fork extensions over upstream:
- `GET /api/resources` — unified GPU/CPU/RAM snapshot
- `GET /api/storage` — model dir disk usage
- `POST /api/models/pull` — HuggingFace model download with streaming progress
- `DELETE /api/models/:id` — unload + delete weight file
- `PATCH /api/config/models/:id` — live model config patch (ctx-size, n_gpu_layers)
- `POST/DELETE /api/config/models` — add/remove models at runtime
- `GET /api/config/info` — config path + file existence
- mDNS registration (`_llamaswap._tcp.local.`) on startup
- Ollama-compat endpoints
- HTTP 413 on context size exceeded
- `context_length` / `max_output_tokens` in `/v1/models`
- `autoUnload` config for model groups
- Slot cancel on client disconnect (prevents zombie GPU allocations)
- ROCm build targets for AMD GPUs on inference hosts

## Upstream sync

**Upstream:** `https://github.com/mostlygeek/llama-swap` (remote alias: `upstream`, fetch-only)
**Our branch:** `feat/model-state-and-lifecycle-api`
**Current gap:** check with `git log --oneline HEAD..upstream/main | wc -l`

Prefer `git rebase upstream/main` (gap is usually <30 commits).

```bash
git fetch upstream
git log --oneline HEAD..upstream/main      # what upstream has that we don't
git rebase upstream/main
GOWORK=off go build ./... && make test-all
git push --force-with-lease origin feat/model-state-and-lifecycle-api
```

**Take:** GPU fixes, routing/process fixes, new model formats, ROCm improvements, llama.cpp parser fixes.

**Never take:** module name changes, anything conflicting with `/api/resources`, `/api/storage`,
`/api/config/*`, `/api/models/*` routes, or the slot-cancel logic in `proxy/process.go`.

**Conflict hotspot:** `proxy/process.go` — our slot-cancel + autoUnload vs upstream routing changes.

## Deploying

Full deploy instructions (host IPs, container IDs, service paths) are in the private companion repo:

```
~/dev/docs-skein/deploy/llama-skein.md
```

If not present: `git clone git@github.com:androidand/docs-skein.git ~/dev/docs-skein`

## Related repos

| Repo | Role |
|------|------|
| skein | Supervisor that drives agents; reads this proxy's health + model lists |
| opencode fork | Agent runner; discovers this proxy via mDNS + LAN scan |
| openclaw | Inspiration: multi-provider routing patterns |
| odysseus | Inspiration: llmfit VRAM-aware model selection, built on llama.cpp |
