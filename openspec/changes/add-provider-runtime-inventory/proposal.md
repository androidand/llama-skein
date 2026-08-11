# Provider Runtime Inventory

## Why

You cannot tell, from the API, what a provider is actually running. `/api/system/version`
on rocky returns:

```json
{"llamacpp": {"detail": "llama-server", "version": "1"},
 "runtime": {"go_arch": "amd64", "go_os": "linux"}}
```

`"1"` is what `llama-server --version` prints as its major version; the part that
identifies the build — `dd1ea52`, which the engine itself reports on `/props` as
`b1-dd1ea52` — is discarded. There is no MLX entry, no vLLM entry, and nothing about
the accelerator runtime. `/api/hardware` names the GPU `"AMD GPU [0]"` — no `gfx1100`,
no driver or ROCm version.

The cost of that opacity is not hypothetical. A prebuilt upgrade on 2026-08-11 installed
`librocblas.so.5.7` into rocky's bundle without its `rocblas/library/` Tensile kernels.
Every model on the host then loaded normally, passed health checks, served short prompts,
and aborted on the first batched prefill. Nothing in any API surface would have shown the
ROCm math libraries were incomplete, or that the bundle's rocBLAS (5.7) differed from the
host's (`/opt/rocm`, 5.2.70203). Diagnosis required SSH and running the engine by hand.

A host may have all three engines installed at once. Which one a given model uses, and
what version each is, should be answerable over HTTP.

## What Changes

- **Correct llama.cpp version reporting.** Report the build identifier the engine reports
  for itself (`b1-dd1ea52`), not a truncated major version. Take it from `/props.build_info`
  when a model is loaded, falling back to parsing `--version` in full.
- **All installed engines, not just the active one.** Report every engine present on the
  host with its version and path, so a provider with llama.cpp *and* MLX *and* vLLM shows
  three entries. An engine that is absent is reported as absent, not omitted.
- **Accelerator runtime block.** Report the compute stack under the engine: ROCm version
  and GPU architecture on AMD, CUDA and driver version on NVIDIA, macOS and Metal family
  on Apple silicon. Include the GPU architecture string (`gfx1100`) — it is what decides
  which prebuilt bundle is correct.
- **Math-library integrity.** On ROCm, report whether each bundled math library has its
  kernel data present. This is the specific check that would have caught the rocBLAS
  failure before it reached a user.

## Capabilities

### New Capabilities

- `provider-runtime-inventory`: engine enumeration, accelerator runtime reporting, and
  math-library integrity checks.

## Modified Capabilities

- `backend-runtime`: `add-backend-runtime-management` specs *managing* engines (install,
  upgrade, health). This change specs *observing* them. The version/health surface that
  change describes for `/api/system/version` is satisfied by the inventory defined here;
  the two must land with one shared schema, not two overlapping ones.

## Non-Goals

- **Not** engine installation or upgrade — that is `add-backend-runtime-management`.
- **Not** remediation. The inventory reports that rocBLAS is missing its kernels; fixing
  it is the upgrade path's job (already guarded by `verifyRuntimeDataDirs`).
- **Not** a fleet aggregator. Each provider reports itself; assembling a fleet view is
  skein's job.

## Impact

- `contracts/llama-skein.openapi.json` — `EngineInfo`, `AcceleratorRuntime`,
  `MathLibraryStatus`; `/api/system/version` response extended. Spec first.
- `internal/server/apisystem.go` — engine enumeration and version detection.
- `internal/hardware/` (or equivalent) — accelerator runtime detection per platform.
- skein — `providers probe` surfaces engine and accelerator versions.
