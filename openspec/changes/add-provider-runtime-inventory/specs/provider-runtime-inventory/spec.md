# Provider Runtime Inventory

## ADDED Requirements

### Requirement: Every installed engine is enumerated

`/api/system/version` SHALL report an entry for each supported inference engine
(llama.cpp, MLX, vLLM), whether or not it is installed and whether or not it is
currently serving. Each entry SHALL carry: `name`, `installed`, `version`, `path`,
and `active` (whether any running model uses it).

An engine that is not installed SHALL be reported with `installed: false`, not
omitted — absence must be distinguishable from a gap in reporting.

#### Scenario: Host with several engines

- **WHEN** a provider has llama.cpp and MLX installed and vLLM absent
- **THEN** three entries are reported: llama.cpp and MLX with `installed: true`, vLLM with
  `installed: false`

#### Scenario: Engine installed but idle

- **WHEN** MLX is installed and no MLX model is loaded
- **THEN** its entry has `installed: true` and `active: false`

### Requirement: Engine version identifies the build

The reported llama.cpp version SHALL be the build identifier the engine reports for
itself, including the commit — for example `b1-dd1ea52`. A bare major version such as
`1` SHALL NOT be reported when a fuller identifier is obtainable.

The value SHALL be taken from `/props.build_info` when a model is loaded, and otherwise
from the complete `--version` output. When neither yields an identifier, the version
SHALL be `null`, never a placeholder.

#### Scenario: Version from a loaded engine

- **WHEN** a llama.cpp model is loaded and `/props.build_info` is `b1-dd1ea52`
- **THEN** the engine version is `b1-dd1ea52`

#### Scenario: Version with nothing loaded

- **WHEN** no model is loaded and `llama-server --version` prints `version: 1 (dd1ea52)`
- **THEN** the reported version retains the commit, not `1` alone

#### Scenario: Version undeterminable

- **WHEN** the engine binary cannot be executed
- **THEN** the version is `null` and `installed` reflects whether the binary exists

### Requirement: The accelerator runtime is reported

`/api/system/version` SHALL report an accelerator block describing the compute stack:
`vendor` (amd/nvidia/apple/cpu), `runtime_version` (ROCm, CUDA, or macOS version),
`driver_version` where the platform exposes one, and `gpu_architecture` (for example
`gfx1100`, `sm_89`).

`gpu_architecture` is required because it determines which prebuilt engine bundle is
correct for the host.

#### Scenario: AMD ROCm host

- **WHEN** the provider runs an AMD GPU under ROCm
- **THEN** `vendor` is `amd`, `runtime_version` is the ROCm version, and `gpu_architecture`
  is the gfx string

#### Scenario: Apple silicon host

- **WHEN** the provider is Apple silicon
- **THEN** `vendor` is `apple` and the block reports the macOS version; GPU architecture
  is the chip family

#### Scenario: Detection unavailable

- **WHEN** no accelerator tooling is present
- **THEN** `vendor` is `cpu` and version fields are `null` — the endpoint still succeeds

### Requirement: Bundled math libraries report kernel-data integrity

On a ROCm provider, the inventory SHALL report, for each bundled math library that
requires kernel data (rocBLAS, hipBLASLt), whether that data is present alongside the
library. Each entry SHALL carry `library`, `present`, `kernel_data_present`, and the
`kernel_data_path` checked.

A library installed without its kernel data SHALL be reported as
`kernel_data_present: false`. This state is not observable from the engine's own health
check: the engine starts, answers `/health`, and serves short prompts, failing only on
the first batched prefill.

#### Scenario: Complete bundle

- **WHEN** `librocblas.so.5` is installed with a populated `rocblas/library/`
- **THEN** the entry reports `present: true` and `kernel_data_present: true`

#### Scenario: The rocky failure

- **WHEN** `librocblas.so.5` is installed and `rocblas/library/` is missing or empty
- **THEN** the entry reports `kernel_data_present: false`

#### Scenario: Non-ROCm provider

- **WHEN** the provider has no ROCm math libraries
- **THEN** the math-library list is empty and the endpoint succeeds

### Requirement: Inventory degrades rather than fails

A detection step that errors or times out SHALL yield `null` for the affected fields and
SHALL NOT fail the request. `/api/system/version` SHALL remain available on a provider
whose accelerator tooling is broken — that is precisely when it is needed.

#### Scenario: Accelerator tool missing

- **WHEN** `rocm-smi` is absent on a host that otherwise looks like ROCm
- **THEN** the accelerator block reports what is known, `null` elsewhere, and returns 200
