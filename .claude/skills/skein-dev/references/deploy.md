# Deploying to providers

```bash
git push origin main                      # the script clones origin, not your tree
~/dev/docs-skein/deploy/fleet-deploy.sh   # runnable from any Mac in the fleet
```

It builds both architectures with `make` (Svelte UI + version ldflags) from a
clean `origin/main` checkout, then deploys every reachable provider with
backup, health check, commit verification, and rollback. A provider already
running the built commit is skipped.

Deploy IPs, container IDs, and service paths live in the **private**
`~/dev/docs-skein` — `deploy/llama-skein.md` and `topology.md`. Clone it if
missing: `git clone git@github.com:androidand/docs-skein.git ~/dev/docs-skein`.

## Never hand-build for a deploy

`go build ./...` omits the embedded Svelte dashboard, and the binary looks fine
until `/ui/` returns 404. Use `make linux-amd64` / `make mac`, or just let
`fleet-deploy.sh` do it.

## Addressing traps

**A container's service IP is not its host's.** Each Proxmox LXC serves on its
own address, distinct from the host you ssh to — using one for the other makes a
reachable provider look dead. Addresses, container IDs and ssh users are in the
private `docs-skein` (`topology.md`), never here.

**The two containers run differently-named binaries.** LXC 1016's unit runs
`/usr/local/bin/llama-swap`; LXC 102 runs `/usr/local/bin/llama-skein`.
Installing to the wrong name leaves the old binary serving while the deploy
reports success — this kept proxmox on a stale build for months.

**mDNS names may not resolve across subnets.** Find a provider by browsing the
service instead:

```bash
dns-sd -B _llamaswap._tcp        # each provider registers itself
```

## Rocky specifics

- The **user** unit is the operational one. The system unit at
  `/etc/systemd/system/llama-skein.service` must stay disabled — under it the
  proxy runs as SELinux `init_t` and cannot fork/exec `llama-server` from
  `~`, so every model spawn fails with permission denied.
- After replacing the binary: `chcon -t bin_t ~/.local/bin/llama-skein`.
  `fleet-deploy.sh` does this; a manual copy must too.

## ROCm engine bundles

A prebuilt ROCm bundle must be installed **with** its `rocblas/library` and
`hipblaslt/library` kernel trees. The `.so` files alone load fine, pass the
health check, and serve short prompts — then abort on the first batched
prefill, which is the only path that reaches a rocBLAS GEMM:

```
rocBLAS error: Cannot read .../rocblas/library/TensileLibrary.dat for GPU arch : gfx1100
```

`POST /api/system/upgrade` handles this now (`copyRuntimeDataDirs` installs the
trees, `verifyRuntimeDataDirs` fails and rolls back an incomplete bundle). Any
manual install must copy them too.

## Checking the fleet

```bash
skein providers            # status table
skein providers probe      # health across backends
```

Confirm a deploy landed by comparing commits, not by health alone — see
`verification.md`.
