# Model Config Gallery

## ADDED Requirements

### Requirement: A gallery entry pins a configuration to its conditions

A gallery entry SHALL record the conditions under which a configuration was observed,
because a configuration is only meaningful against the hardware and engine that ran it.
An entry SHALL carry:

- **model**: base model id, repository, filename, quantisation, file size, and file hash
- **hardware**: GPU architecture, VRAM total, GPU count, host RAM
- **engine**: name and build identifier (for example `llamacpp` / `b1-dd1ea52`)
- **arguments**: the complete argument set used
- **measurements**: prefill tok/s, decode tok/s, peak VRAM, context size tested, and the
  sample count behind them
- **provenance**: when it was captured and whether measured or suggested

An entry without measurements SHALL be marked `measured: false`.

#### Scenario: A measured entry

- **WHEN** an entry is captured from a real run
- **THEN** it records the engine build, GPU architecture, arguments, and observed
  throughput, and is marked `measured: true`

#### Scenario: A suggested entry

- **WHEN** an entry is derived from the fit engine without any run
- **THEN** it is marked `measured: false` and carries no throughput figures

### Requirement: Entries are captured from real runs

llama-skein SHALL capture entries from models it actually served, using the arguments it
launched, the hardware it detected, and throughput the engine reported. Capture SHALL NOT
require the operator to transcribe anything by hand.

Capture SHALL be limited to runs that produced enough traffic to be meaningful; a single
short completion SHALL NOT establish a throughput claim.

#### Scenario: Model served under real traffic

- **WHEN** a model has served enough completions to meet the sampling threshold
- **THEN** an entry is recorded with the observed throughput and its sample count

#### Scenario: Model loaded but barely used

- **WHEN** a model is loaded and serves a single short completion
- **THEN** no measured entry is recorded

#### Scenario: Arguments changed mid-life

- **WHEN** a model's arguments change
- **THEN** measurements accumulated under the previous arguments are not attributed to the
  new ones

### Requirement: Recommendations state how well they match

`GET /api/gallery/{model}` SHALL return the best-known configuration for this host along
with a `match` describing how it was chosen: `exact` (same model, GPU architecture, VRAM
class, and engine build), `near` (differs in stated ways), or `computed` (no entry; from
the fit engine).

A `near` match SHALL enumerate the differing dimensions. A recommendation SHALL never
present another host's numbers as if measured locally.

#### Scenario: Exact match

- **WHEN** an entry exists for this model, GPU architecture, VRAM class, and engine build
- **THEN** `match` is `exact` and the measured throughput is returned

#### Scenario: Different engine build

- **WHEN** the only entry was measured on a different engine build
- **THEN** `match` is `near` and the differences list includes the engine build

#### Scenario: No entry

- **WHEN** no entry exists for the model
- **THEN** `match` is `computed` and the fit engine's starting configuration is returned
  with no throughput figures

#### Scenario: Incompatible hardware is not offered

- **WHEN** the only entries are from a different GPU vendor
- **THEN** they are not returned as `near`; the response is `computed`

### Requirement: Viability listing answers what runs here

`GET /api/gallery/viable` SHALL list models known to run on hardware comparable to this
provider, each with expected throughput, VRAM requirement, and the match quality that
produced the estimate. The list SHALL include models not present on this host, so it can
inform a download decision.

#### Scenario: Listing for a 24 GB AMD host

- **WHEN** the provider has 24 GB of VRAM on gfx1100
- **THEN** the listing includes models with entries from comparable hardware, with expected
  throughput and VRAM

#### Scenario: A model that cannot fit

- **WHEN** a model's known configurations all require more VRAM than the host has
- **THEN** it is either excluded or marked as not viable, never listed as runnable

### Requirement: Recommendations are never applied automatically

A recommendation SHALL NOT change a model's running configuration. Applying one SHALL be
an explicit, separate action.

#### Scenario: Recommendation differs from the running config

- **WHEN** the gallery recommends different arguments than a model is running
- **THEN** the running configuration is unchanged and the difference is reported

### Requirement: Nothing is shared without an explicit decision

Entries SHALL remain local to the provider. No entry SHALL be transmitted anywhere
without an explicit, separately-specified opt-in. Entries contain host paths, hardware
detail, and model inventories.

#### Scenario: Default operation

- **WHEN** entries are captured with no sharing configured
- **THEN** they are stored locally and nothing is transmitted
