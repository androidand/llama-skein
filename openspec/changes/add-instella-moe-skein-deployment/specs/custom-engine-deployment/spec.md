# Spec delta: custom-engine-deployment

## ADDED Requirements

### Requirement: A model may run on a custom engine build without endangering the shared one

A model entry SHALL be able to point at a locally-built inference binary that coexists with
the managed prebuilt engine, such that the runtime upgrade path cannot overwrite the custom
build, its shared libraries, or terminate its process.

#### Scenario: Upgrade runs while the custom engine is resident

- **WHEN** the llama-server upgrade endpoint is invoked while the custom-engine model is
  loaded
- **THEN** the managed prebuilt is installed to its own location
- **AND** the custom build's binary and shared libraries are unchanged
- **AND** the custom build's process is not killed

#### Scenario: Managed engine remains healthy

- **WHEN** the custom engine has been installed and a model using it has been loaded and
  unloaded
- **THEN** models using the managed prebuilt still load and generate normally
- **AND** the managed prebuilt's files match their pre-installation checksums

#### Scenario: Provenance is recorded

- **WHEN** a custom engine build is installed
- **THEN** its fork commit and upstream base commit are recorded on disk alongside the
  binary

### Requirement: A research-licensed model is not exposed to agent runners

A model whose license forbids production use SHALL be reachable for evaluation but absent
from the advertised model list, excluded from groups, and never the default.

#### Scenario: Model listing

- **WHEN** a client requests the available model list
- **THEN** the research-licensed model does not appear

#### Scenario: License is visible where the model is described

- **WHEN** an operator reads the model's configuration entry
- **THEN** its name and description state the license restriction

#### Scenario: Promotion requires a decision

- **WHEN** the deployment change is merged
- **THEN** nothing in it makes the model routable to agent work, and promoting it requires a
  separate explicit decision

### Requirement: Configured context is not silently rewritten for a fully-resident model

The configured context size SHALL be honoured for a model whose weights and cache fit
within VRAM with all experts resident.

#### Scenario: Fit check on a resident mixture-of-experts model

- **WHEN** the fit report is generated for the deployed model with all layers on the GPU
- **THEN** the configured context size is unchanged
- **AND** the fit level is not a refusal

#### Scenario: A rewrite is surfaced, not absorbed

- **WHEN** the fit guard does rewrite the configured context size
- **THEN** the discrepancy is recorded as a defect to investigate rather than accepted as
  normal

### Requirement: Comparative benchmarks require a measured baseline

A performance comparison against an existing fleet model SHALL be published only when both
sides have been measured on the same host.

#### Scenario: Baseline is measured first

- **WHEN** benchmark results for the new model are recorded
- **THEN** the incumbent model has been measured on the same host under the same prompt and
  generation lengths
- **AND** no throughput figure is reported that was not measured
