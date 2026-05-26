@AGENTS.md

## Upstream Sync (cherry-pick process)

llama-skein tracks upstream `ggml-org/llama-swap` (now `mostlygeek/llama-swap`) for
community improvements. Use this process to integrate upstream changes:

### Fetching upstream

```bash
git fetch upstream
git log upstream/main --oneline --since="1 week ago"
```

### Cherry-picking

```bash
# Pick individual commits:
git cherry-pick <hash>

# Or pick a range:
git cherry-pick <old-hash>^..<new-hash>
```

### What to take from upstream
- GPU detection and VRAM calculation fixes
- llama.cpp tool-call parser improvements  
- New model format support (Qwen, Gemma, etc.)
- Performance optimizations (Flash Attention, ROCm fixes)
- Bug fixes in routing, process management, and metrics

### What to NEVER take from upstream
- Any changes to module name or import paths
- PRs that conflict with companion extensions (`/api/skein/*` routes)
- Changes to the `pkg/` public API surface
- UI changes that conflict with our svelte modifications
- Anything that breaks `go test` or `make test-all`

### Cadence
- Weekly scan of upstream `main` for relevant commits
- Cherry-pick within 48h when a fix is relevant to our hardware or model family
- Always run `go test` and `make test-all` after cherry-picking before pushing

### After cherry-picking
```bash
make test-all
git push origin feat/model-state-and-lifecycle-api
```

## git remotes

- **origin** — `https://github.com/androidand/llama-skein` (our fork, push/pull)
- **upstream** — `https://github.com/mostlygeek/llama-swap` (original fork, fetch only)
