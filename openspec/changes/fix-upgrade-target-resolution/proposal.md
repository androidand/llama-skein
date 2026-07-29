# Stop resolving the upgrade target with pgrep

## Context

`POST /api/system/upgrade` decides **where to install** a new `llama-server` by
running `pgrep -a llama-server` and taking the **first line's** binary path
(`internal/server/apiupgrade.go:680-701`):

```go
cmd := exec.Command("pgrep", "-a", "llama-server")
...
fields := strings.Fields(lines[0])
// pgrep -a output: <PID> <binary> [args...]
if len(fields) >= 2 { return fields[1], nil }
```

Nothing consults the config. The install destination is therefore whichever
`llama-server`-ish process the kernel happens to list first.

Three failure modes follow from that one line:

1. **Wrong install target.** `pgrep -a llama-server` matches by pattern, so a
   differently-named engine such as `llama-server-instella` matches too. Its path
   becomes `serverPath` and `safeReplaceBinary` (`:497`) overwrites it with a build
   for a different configuration.
2. **Shared libraries clobbered — worse than the binary.** `libDir :=
   filepath.Dir(serverPath)` (`:485`, `:636`) then copies every `.so` from the
   downloaded archive into that directory. A self-contained engine directory
   (bundled ROCm runtime, hardware-tuned `hipblaslt` kernel DB) gets foreign
   libraries written over it. The `.bak` copy at `:423` only protects the binary, so
   even a "successful" rollback leaves a restored binary running against replaced
   libraries.
3. **Unrelated engines killed.** `restartLlamaServer()` (`:839-860`) `proc.Kill()`s
   **every** `pgrep -a llama-server` match, matching on process name, not path.

This is not hypothetical. It is why the Instella engine had to be installed as
`/opt/llamacpp-instella/llama-server-instella` — renaming the binary was the only
available mitigation, and it works only by accident of the pattern not matching.

## Why

The upgrade path is destructive and currently targets a directory nobody declared.
On a host running more than one engine build — a tailored vendor build plus a
locally-compiled one, which is exactly the supported RDNA3 situation — an
`/api/system/upgrade` call can silently destroy the build it did not mean to touch.
Renaming binaries to dodge a `pgrep` pattern is not a safety mechanism.

The config already knows every engine path: it is in each model's `cmd`. The fix is
to stop guessing.

## What changes

1. A top-level **`enginePath`** config key naming the `llama-server` binary that
   `/api/system/upgrade` manages. When set it is used verbatim and `pgrep` is never
   consulted.
2. When `enginePath` is unset, discovery becomes **conservative instead of
   arbitrary**: only processes whose binary *basename* is exactly `llama-server`
   are candidates, and if candidates disagree on path the request **fails with a
   clear error** naming them rather than picking one.
3. `restartLlamaServer()` uses the same basename filter, so an engine that is not
   the managed one is never killed.

## Non-goals

- Managing more than one engine through the upgrade API. One managed path is
  enough; other builds are simply left alone.
- Changing the `~/.local/lib/llama-cpp/llama-server` fallback, which stays as the
  last resort when nothing is running and nothing is configured.
- Deriving the path from model `cmd` strings automatically. That would reintroduce
  ambiguity (several models, several engines) — an explicit key is the point.

## Risks

- A host that today relies on pgrep discovery and runs its engine under a
  non-`llama-server` basename would newly fail the upgrade with an error instead of
  silently installing somewhere. That is the intended behaviour change: a loud
  refusal beats a wrong write. The error text names `enginePath` as the fix.
