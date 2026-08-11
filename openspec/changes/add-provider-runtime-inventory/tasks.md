# Tasks: Provider Runtime Inventory

## 1. Reconcile with add-backend-runtime-management

- [ ] 1.1 That change specs `/api/system/version` reporting per-engine version/health.
      Agree one schema across both changes before writing contract entries — two
      overlapping shapes for the same endpoint is the failure mode to avoid.
      Verify: cross-link recorded in both proposals.

## 2. Contract

- [ ] 2.1 Add `EngineInfo` (`name`, `installed`, `version`, `path`, `active`),
      `AcceleratorRuntime` (`vendor`, `runtime_version`, `driver_version`,
      `gpu_architecture`), `MathLibraryStatus` (`library`, `present`,
      `kernel_data_present`, `kernel_data_path`) to the OpenAPI spec.
- [ ] 2.2 Extend `/api/system/version` with `engines[]`, `accelerator`,
      `math_libraries[]`. Verify: `make check-codegen` clean.

## 3. Engine detection

- [ ] 3.1 Fix llama.cpp version detection: prefer `/props.build_info`; else parse the full
      `--version` output preserving the commit. Verify: unit test over both forms asserts
      `b1-dd1ea52`, never `1`.
- [ ] 3.2 Detect MLX (venv `mlx_lm` presence + version) and vLLM (venv `vllm --version`).
      Absent engines report `installed: false`. Verify: table test with fake trees.
- [ ] 3.3 Mark `active: true` for engines backing a currently-running model.
      Verify: test with a loaded llama.cpp model asserts only that engine is active.

## 4. Accelerator detection

- [ ] 4.1 AMD: ROCm version and gfx architecture. Verify on rocky (gfx1100) and z4.
- [ ] 4.2 NVIDIA: CUDA and driver version, compute capability, via `nvidia-smi`.
      Verify on proxmox LXC 1016.
- [ ] 4.3 Apple: macOS version and chip family. Verify on m3 and m5.
- [ ] 4.4 No accelerator tooling → `vendor: cpu`, `null` versions, still 200.
      Verify: test with all detection commands absent.

## 5. Math-library integrity

- [ ] 5.1 For each of rocBLAS and hipBLASLt present in the engine's lib dir, report whether
      its `library/` kernel tree exists and is non-empty. Reuse the layout rule already
      implemented by `verifyRuntimeDataDirs` in `internal/server/apiupgrade.go` rather than
      duplicating it. Verify: test over complete, missing, and empty-dir trees.
- [ ] 5.2 Report the bundled library version alongside the host's, so a bundle/host
      mismatch (rocky: bundle 5.7 vs `/opt/rocm` 5.2.70203) is visible.
      Verify: rocky reports both.

## 6. Verification

- [ ] 6.1 `go build ./... && go vet ./... && go test -short ./internal/...` clean.
- [ ] 6.2 Per-provider capture of `/api/system/version` from rocky, z4, proxmox, m3, m5.
      Record output here. **Blocked** until each provider is reachable — as of this change
      only rocky and z4 answer from the dev machine.
- [ ] 6.3 Confirm the rocky rocBLAS state is detected: temporarily rename
      `~/.local/lib/llama-cpp/rocblas`, confirm `kernel_data_present: false`, restore.
      Do this only when the host is idle.
