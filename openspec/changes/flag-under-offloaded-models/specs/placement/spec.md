# Report the counterfactual for pinned placement

## ADDED Requirements

### Requirement: A pinned model never claims native-GPU performance it does not have

`perf_class` SHALL describe the placement the model actually runs under, and SHALL
NOT be reported as `native-gpu` merely because placement flags were pinned.

`Compute` currently returns `PerfClass: PerfNativeGPU` unconditionally for
`ModeCustom` (`placement.go:153`). On rocky this reported
`perf_class: "native-gpu"` for a model decoding 26 of 66 layers on the CPU at
1.2 tok/s.

This is not a cosmetic inaccuracy. opencode-skein's sole defense against routing
sub-agents onto host-paced models keys on exactly this field —
`isHostPaced()` (`packages/opencode/src/local/placement.ts:104-107`) tests
`perf_class` for `cpu-bound-hybrid` or `cpu-only` and applies a 200,000-point
`HOST_PACED_PENALTY`. Because every pinned model claims `native-gpu`, that penalty
never fires for the one configuration that most reliably produces CPU-bound
models. The guard is disabled by the thing it exists to catch.

When the counterfactual cannot be computed, `perf_class` SHALL be omitted or
reported as unknown rather than defaulting to an optimistic value.

#### Scenario: Pinned flags produce host-paced execution

- **WHEN** pinned flags leave a material share of layers on the CPU
- **THEN** `perf_class` is `cpu-bound-hybrid` or `cpu-only` as the split warrants,
  so downstream host-paced penalties engage

#### Scenario: Pinned flags produce full GPU residency

- **WHEN** pinned flags place every layer on the GPU
- **THEN** `perf_class` is `native-gpu`, as it is today

#### Scenario: Placement cannot be assessed

- **WHEN** the model shape or memory budget is unknown for a pinned model
- **THEN** `perf_class` is omitted or unknown rather than optimistically
  `native-gpu`

## MODIFIED Requirements

### Requirement: Pinned placement is planned for reporting, never applied

`placement.Compute` SHALL apply nothing when a command pins any
placement-affecting flag (`-ngl`, `--n-cpu-moe`, `--cpu-moe`,
`--override-tensor`, `--tensor-split`): it returns `Mode = ModeCustom` with
`FlagOps` empty and `Applies()` false, so no command is rewritten.

It SHALL nonetheless compute the plan it *would* have chosen and report it via
`Estimate` (`est_gpu_mb` / `est_host_mb`) and `PerfClass`.

`Reason` SHALL state how the pinned flags compare to the computed plan — better,
equivalent, or worse — and, when worse, how much GPU-resident weight the pinned
flags give up.

Previously `Compute` returned before planning, so a pinned model reported
`est_gpu_mb: 0, est_host_mb: 0` and a reason explaining only that automation had
stood down. Declining to apply a plan is correct; declining to compute one
discarded the only signal showing that the operator's pinned flags were worse
than what the host would have chosen.

Counterfactual planning SHALL be best-effort: when the model shape or the memory
budget is unknown, the estimate SHALL be omitted rather than guessed, and
`Reason` SHALL fall back to the previous text.

#### Scenario: Pinned flags are worse than the computed plan

- **WHEN** a model is launched with `--n-gpu-layers 40` on a host whose budget
  holds all 66 offloadable layers
- **THEN** the result is `ModeCustom` with `Applies()` false, `Estimate` describes
  full GPU residency, and `Reason` states the pinned flags leave ~7.2 GB of
  weights in host RAM that the computed plan would have kept on the GPU

#### Scenario: Pinned flags match what automation would choose

- **WHEN** the pinned flags produce the same placement the planner would have
- **THEN** the result is `ModeCustom` and `Reason` states the pinned flags are
  equivalent to the computed plan

#### Scenario: Operator pins are still honoured

- **WHEN** any placement-affecting flag is pinned
- **THEN** no flag operations are returned and the command runs exactly as
  configured, regardless of what the counterfactual shows

#### Scenario: Shape or budget unknown

- **WHEN** placement flags are pinned but the model shape cannot be parsed or the
  VRAM budget is unknown
- **THEN** the result is `ModeCustom` with no estimate, and `Reason` is the
  previous "automatic placement stays out" text

#### Scenario: perf_class reflects the pinned reality, not an assumption

- **WHEN** a model pinned at `--n-gpu-layers 40` runs 26 of 66 layers on the CPU
- **THEN** `perf_class` describes that host-paced placement, and is not reported as
  `native-gpu`

#### Scenario: Compute still never errors

- **WHEN** counterfactual planning cannot complete for any reason
- **THEN** `Compute` returns a plan rather than an error, and the model runs
  exactly as configured
